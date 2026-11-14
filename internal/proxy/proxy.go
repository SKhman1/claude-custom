package proxy

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"claude-custom/internal/admission"
	"claude-custom/internal/catalog"
	"claude-custom/internal/config"
)

type Target struct {
	Name      string
	BaseURL   string
	APIKey    string
	Model     string
	MaxOutput int
	Headers   map[string]string
}

type Server struct {
	cfg       *config.Config
	profile   config.Profile
	provider  config.Provider
	name      string
	model     string
	apiKey    string
	token     string
	fallbacks []Target
	limiter   *admission.Limiter
	client    *http.Client
	logger    *log.Logger

	breakerMu sync.Mutex
	breakers  map[string]*circuitBreaker

	gatedMu sync.Mutex
	gated   map[string]bool

	maxOutMu sync.Mutex
	maxOut   map[string]int

	modelsMu        sync.Mutex
	modelsCache     []catalog.Model
	modelsFetchedAt time.Time
	modelsFailedAt  time.Time

	httpSrv  *http.Server
	listener net.Listener
	stopOnce sync.Once
	onStop   func()

	startedAt   time.Time
	requests    atomic.Int64
	streams     atomic.Int64
	retries     atomic.Int64
	recoveries  atomic.Int64
	fallbackUse atomic.Int64
	rateLimits  atomic.Int64
	payloadErrs atomic.Int64
	serverErrs  atomic.Int64
	lastError   atomic.Value
}

type Options struct {
	ProfileName string
	APIKey      string
	Token       string
	Fallbacks   []Target
	Logger      *log.Logger
	OnStop      func()
}

func New(cfg *config.Config, opts Options) (*Server, error) {
	profile, provider, err := cfg.Resolve(opts.ProfileName)
	if err != nil {
		return nil, err
	}
	provider.Headers = catalog.AttributionHeaders(profile.BaseURL, provider.Headers)
	limits := profile.Limits
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	s := &Server{
		cfg:       cfg,
		profile:   profile,
		provider:  provider,
		name:      opts.ProfileName,
		model:     profile.Model,
		apiKey:    opts.APIKey,
		token:     opts.Token,
		fallbacks: opts.Fallbacks,
		limiter:   admission.New(limits.RequestsPerWindow, time.Duration(limits.WindowSeconds)*time.Second, limits.MaxConcurrency),
		logger:    logger,
		onStop:    opts.OnStop,
		startedAt: time.Now(),
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:          16,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 120 * time.Second,
				ForceAttemptHTTP2:     true,
			},
			Timeout: 0,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.route(s.handleMessages))
	mux.HandleFunc("/v1/messages/count_tokens", s.route(s.handleCountTokens))
	mux.HandleFunc("/v1/models", s.route(s.handleModels))
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/stop", s.handleStop)
	mux.HandleFunc("/", s.handleHealth)
	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}
	return s, nil
}

func (s *Server) route(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing claude-custom token")
			return
		}
		next(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	provided := r.Header.Get("x-api-key")
	if provided == "" {
		auth := r.Header.Get("Authorization")
		if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
			provided = strings.TrimSpace(auth[7:])
		}
	}
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1
}

func (s *Server) Start() (string, error) {
	return s.StartPort(s.cfg.Server.Port)
}

func (s *Server) StartPort(port int) (string, error) {
	bind := s.cfg.Server.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", bind, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.listener = ln
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Printf("proxy stopped: %v", err)
		}
	}()
	return ln.Addr().String(), nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	last := ""
	if v := s.lastError.Load(); v != nil {
		last, _ = v.(string)
	}
	status := "ok"
	if s.breakerFor(s.primaryTarget()).isOpen() {
		status = "degraded"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         status,
		"provider":       s.profile.Provider,
		"profile":        s.profileName(),
		"model":          s.model,
		"uptime_seconds": int64(time.Since(s.startedAt).Seconds()),
		"counters": map[string]int64{
			"requests":          s.requests.Load(),
			"streaming":         s.streams.Load(),
			"retries":           s.retries.Load(),
			"recoveries":        s.recoveries.Load(),
			"fallbacks":         s.fallbackUse.Load(),
			"rate_limited":      s.rateLimits.Load(),
			"payload_too_large": s.payloadErrs.Load(),
			"server_errors":     s.serverErrs.Load(),
		},
		"last_error": last,
	})
}

func (s *Server) profileName() string {
	if s.name != "" {
		return s.name
	}
	if s.cfg.DefaultProfile != "" {
		return s.cfg.DefaultProfile
	}
	return s.profile.Provider
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing claude-custom token")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "stopping"})
	cb := s.onStop
	if cb != nil {
		go cb()
	} else {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Shutdown(ctx)
		}()
	}
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use GET")
		return
	}
	display := s.profile.Provider + "/" + s.model
	items := []map[string]any{
		{
			"id":           "claude-custom",
			"type":         "model",
			"display_name": display,
			"created_at":   "2026-01-01T00:00:00Z",
		},
		{
			"id":           "claude-sonnet-4-5",
			"type":         "model",
			"display_name": display + " (alias)",
			"created_at":   "2026-01-01T00:00:00Z",
		},
	}
	for _, fallback := range s.fallbacks {
		id := fallback.Name + "/" + fallback.Model
		items = append(items, map[string]any{
			"id":           id,
			"type":         "model",
			"display_name": id + " (fallback)",
			"created_at":   "2026-01-01T00:00:00Z",
		})
	}
	seen := map[string]bool{"claude-custom": true, "claude-sonnet-4-5": true}
	for _, fallback := range s.fallbacks {
		seen[fallback.Name+"/"+fallback.Model] = true
	}
	for _, model := range catalog.FilterChat(s.liveModels(r.Context())) {
		if seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		items = append(items, map[string]any{
			"id":           model.ID,
			"type":         "model",
			"display_name": model.Name,
			"created_at":   "2026-01-01T00:00:00Z",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":     items,
		"has_more": false,
		"first_id": items[0]["id"],
		"last_id":  items[len(items)-1]["id"],
	})
}

const (
	modelsTTL     = 5 * time.Minute
	modelsFailTTL = 30 * time.Second
	modelsTimeout = 10 * time.Second
)

// liveModels returns the provider's live model inventory, cached for
// modelsTTL. On fetch failure it serves nothing new for modelsFailTTL
// so /v1/models never hammers a broken upstream; stale cache (when
// present) is still used for model-override routing.
func (s *Server) liveModels(ctx context.Context) []catalog.Model {
	s.modelsMu.Lock()
	now := time.Now()
	if len(s.modelsCache) > 0 && now.Sub(s.modelsFetchedAt) < modelsTTL {
		models := s.modelsCache
		s.modelsMu.Unlock()
		return models
	}
	if now.Sub(s.modelsFailedAt) < modelsFailTTL {
		s.modelsMu.Unlock()
		return nil
	}
	s.modelsMu.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	models, err := catalog.FetchModelsWithHeaders(fetchCtx, s.profile.BaseURL, s.apiKey, s.provider.Headers)
	s.modelsMu.Lock()
	defer s.modelsMu.Unlock()
	if err != nil {
		s.modelsFailedAt = time.Now()
		s.logger.Printf("live model fetch failed: %v", err)
		return nil
	}
	if len(models) == 0 {
		s.modelsFailedAt = time.Now()
		return nil
	}
	s.modelsCache = models
	s.modelsFetchedAt = time.Now()
	s.modelsFailedAt = time.Time{}
	return models
}

func (s *Server) knownLiveModel(id string) bool {
	s.modelsMu.Lock()
	defer s.modelsMu.Unlock()
	for _, model := range s.modelsCache {
		if model.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	var req struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}
	var total int
	total += len(req.System)
	for _, m := range req.Messages {
		total += len(m.Content)
	}
	for _, t := range req.Tools {
		total += len(t.Name) + len(t.Description) + len(t.InputSchema)
	}
	tokens := total / 4
	if tokens < 1 {
		tokens = 1
	}
	writeJSON(w, http.StatusOK, map[string]int{"input_tokens": tokens})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    kind,
			"message": message,
		},
	})
}

func (s *Server) setLastError(message string) {
	s.lastError.Store(message)
}
