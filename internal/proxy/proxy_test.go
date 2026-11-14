package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"time"

	"claude-custom/internal/config"
)

func testConfig(base string) *config.Config {
	cfg := config.Default()
	cfg.Providers["test"] = config.Provider{BaseURL: base}
	cfg.Profiles["test"] = config.Profile{
		Provider: "test",
		Model:    "upstream-model",
		BaseURL:  base,
		Limits:   config.Limits{MaxConcurrency: 4, RequestsPerWindow: 100, WindowSeconds: 1},
	}
	cfg.DefaultProfile = "test"
	return cfg
}

func startTestProxy(t *testing.T, upstreamURL string) (*Server, string) {
	t.Helper()
	srv, err := New(testConfig(upstreamURL), Options{ProfileName: "test", APIKey: "test-key", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.StartPort(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv, addr
}

func TestMessagesNonStreaming(t *testing.T) {
	var gotAuth, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer upstream.Close()

	_, addr := startTestProxy(t, upstream.URL+"/v1")
	reqBody := `{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, data)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Content) != 1 || out.Content[0].Text != "hello" {
		t.Fatalf("content = %+v", out.Content)
	}
	if out.StopReason != "end_turn" {
		t.Fatalf("stop = %q", out.StopReason)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotModel != "upstream-model" {
		t.Fatalf("model = %q", gotModel)
	}
}

func TestModelsDiscoversUpstreamAndRoutesSelection(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"live-model","display_name":"Live Model"}]}`)
		case "/v1/chat/completions":
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotModel = body.Model
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"x","model":"live-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
		}
	}))
	defer upstream.Close()

	_, addr := startTestProxy(t, upstream.URL+"/v1")

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/models", nil)
	req.Header.Set("x-api-key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(data), "live-model") {
		t.Fatalf("models: status=%d body=%s", resp.StatusCode, data)
	}

	reqBody := `{"model":"live-model","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req, _ = http.NewRequest(http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("messages status = %d, body = %s", resp.StatusCode, data)
	}
	if gotModel != "live-model" {
		t.Fatalf("upstream model = %q", gotModel)
	}
}

func TestMessagesFallsBackOnRetryableFailure(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"error":{"message":"busy"}}`, http.StatusTooManyRequests)
	}))
	defer primary.Close()

	var fallbackModel, fallbackAuth string
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		fallbackAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fallbackModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","model":"backup-model","choices":[{"index":0,"message":{"role":"assistant","content":"fallback hello"},"finish_reason":"stop"}]}`)
	}))
	defer fallback.Close()

	cfg := testConfig(primary.URL + "/v1")
	srv, err := New(cfg, Options{
		ProfileName: "test",
		APIKey:      "test-key",
		Token:       "secret",
		Fallbacks: []Target{{
			Name:    "backup",
			BaseURL: fallback.URL + "/v1",
			APIKey:  "backup-key",
			Model:   "backup-model",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.StartPort(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	reqBody := `{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, data)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Content) != 1 || out.Content[0].Text != "fallback hello" {
		t.Fatalf("content = %+v", out.Content)
	}
	if primaryCalls.Load() == 0 {
		t.Fatal("primary was never called")
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback calls = %d", fallbackCalls.Load())
	}
	if fallbackModel != "backup-model" {
		t.Fatalf("fallback model = %q", fallbackModel)
	}
	if fallbackAuth != "Bearer backup-key" {
		t.Fatalf("fallback auth = %q", fallbackAuth)
	}
	if srv.fallbackUse.Load() != 1 {
		t.Fatalf("fallback counter = %d", srv.fallbackUse.Load())
	}
}

func newTestProxy(t *testing.T, primaryURL string, fallbacks []Target) (*Server, string) {
	t.Helper()
	srv, err := New(testConfig(primaryURL), Options{
		ProfileName: "test",
		APIKey:      "test-key",
		Token:       "secret",
		Fallbacks:   fallbacks,
	})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.StartPort(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv, addr
}

func postRaw(t *testing.T, addr, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func postMessages(t *testing.T, addr string) (int, []byte) {
	t.Helper()
	reqBody := `{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

const groqTokenLimitBody = `{"error":{"message":"Request too large for model ` + "`qwen/qwen3.8-27b`" + ` on tokens per minute (TPM): Limit 7000, Requested 35437, please reduce your message size and try again.","type":"tokens","code":"rate_limit_exceeded"}}`

func TestMessagesTokenRateLimitBecomesRateLimitError(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		fmt.Fprint(w, groqTokenLimitBody)
	}))
	defer primary.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", nil)
	status, body := postMessages(t, srv.Addr())
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d (a 413 makes Claude Code compact in a loop), body = %s", status, body)
	}
	if !strings.Contains(string(body), "rate_limit_error") {
		t.Fatalf("body = %s", body)
	}
	if srv.rateLimits.Load() != 1 {
		t.Fatalf("rate limit counter = %d", srv.rateLimits.Load())
	}
}

func TestMessagesTokenRateLimitFallsBack(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		fmt.Fprint(w, groqTokenLimitBody)
	}))
	defer primary.Close()

	var fallbackCalls atomic.Int64
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","model":"backup-model","choices":[{"index":0,"message":{"role":"assistant","content":"fallback hello"},"finish_reason":"stop"}]}`)
	}))
	defer fallback.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", []Target{{
		Name:    "backup",
		BaseURL: fallback.URL + "/v1",
		APIKey:  "backup-key",
		Model:   "backup-model",
	}})
	status, body := postMessages(t, srv.Addr())
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback calls = %d", fallbackCalls.Load())
	}
	if !strings.Contains(string(body), "fallback hello") {
		t.Fatalf("body = %s", body)
	}
}

const harnessGateBody = `{"error":{"message":"thinkingmachines/inkling-2-small:free is only available on agentic harnesses. Try plugging it into a coding agent or productivity app listed on https://openrouter.ai/apps","code":403,"metadata":{"failed_routing_step":"Gate Free Endpoints by Agentic Harness"}}}`

func TestMessagesHarnessGateFallsBack(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, harnessGateBody)
	}))
	defer primary.Close()

	var fallbackCalls atomic.Int64
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","model":"backup-model","choices":[{"index":0,"message":{"role":"assistant","content":"fallback hello"},"finish_reason":"stop"}]}`)
	}))
	defer fallback.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", []Target{{
		Name:    "openrouter",
		BaseURL: fallback.URL + "/v1",
		APIKey:  "backup-key",
		Model:   "backup-model",
	}})
	status, body := postMessages(t, srv.Addr())
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback calls = %d", fallbackCalls.Load())
	}
	if !strings.Contains(string(body), "fallback hello") {
		t.Fatalf("body = %s", body)
	}
	if srv.retries.Load() != 0 {
		t.Fatalf("retries = %d; a gated endpoint should go straight to the fallback", srv.retries.Load())
	}
}

func TestMessagesHarnessGateWithoutFallbackIsPermissionError(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, harnessGateBody)
	}))
	defer primary.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", nil)
	status, body := postMessages(t, srv.Addr())
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(string(body), "permission_error") {
		t.Fatalf("body = %s", body)
	}
	if !strings.Contains(string(body), "openrouter.ai/apps") {
		t.Fatalf("missing gate hint, body = %s", body)
	}
}

// A provider answering 403/413 for one model must not open its circuit: the
// next request has to reach that provider again, and the fallback must stay
// usable throughout.
func TestRoutingErrorsDoNotOpenProviderBreaker(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		gated  bool
	}{
		{"harness gate", http.StatusForbidden, harnessGateBody, true},
		{"token limit", http.StatusRequestEntityTooLarge, groqTokenLimitBody, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var primaryOK atomic.Bool
			var primaryCalls atomic.Int64
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				if primaryOK.Load() {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"p","model":"primary-model","choices":[{"index":0,"message":{"role":"assistant","content":"primary hello"},"finish_reason":"stop"}]}`)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer primary.Close()

			var fallbackCalls atomic.Int64
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"f","model":"backup-model","choices":[{"index":0,"message":{"role":"assistant","content":"fallback hello"},"finish_reason":"stop"}]}`)
			}))
			defer fallback.Close()

			srv, _ := newTestProxy(t, primary.URL+"/v1", []Target{{
				Name:    "backup",
				BaseURL: fallback.URL + "/v1",
				APIKey:  "backup-key",
				Model:   "backup-model",
			}})

			for i := 0; i < breakerThreshold+2; i++ {
				status, body := postMessages(t, srv.Addr())
				if status != http.StatusOK || !strings.Contains(string(body), "fallback hello") {
					t.Fatalf("request %d: status = %d, body = %s", i+1, status, body)
				}
			}
			if fallbackCalls.Load() != breakerThreshold+2 {
				t.Fatalf("fallback calls = %d, want %d", fallbackCalls.Load(), breakerThreshold+2)
			}
			if tc.gated {
				// A policy refusal is cached per model, so the primary is not
				// contacted again at all; that is not a circuit.
				if got := primaryCalls.Load(); got != 1 {
					t.Fatalf("gated primary called %d times, want 1", got)
				}
				return
			}

			primaryOK.Store(true)
			status, body := postMessages(t, srv.Addr())
			if status != http.StatusOK || !strings.Contains(string(body), "primary hello") {
				t.Fatalf("primary was skipped after %d routing errors: status = %d, body = %s", primaryCalls.Load(), status, body)
			}
			if fallbackCalls.Load() != breakerThreshold+2 {
				t.Fatalf("fallback used again after primary recovered (calls = %d)", fallbackCalls.Load())
			}
		})
	}
}

func TestMessagesStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	_, addr := startTestProxy(t, upstream.URL+"/v1")
	reqBody := `{"model":"claude-sonnet-4-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	body := string(data)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	for _, needle := range []string{"event: message_start", `"text":"Hel"`, `"text":"lo"`, "event: message_stop"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %q in %s", needle, body)
		}
	}
	if strings.Contains(body, `"type":"error"`) {
		t.Fatalf("stream contained error: %s", body)
	}
}

func TestAuthRequired(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	_, addr := startTestProxy(t, upstream.URL+"/v1")
	resp, err := http.Post("http://"+addr+"/v1/messages", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestModelsAndHealth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	_, addr := startTestProxy(t, upstream.URL+"/v1")

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/models", nil)
	req.Header.Set("x-api-key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(data), "claude-custom") {
		t.Fatalf("models: status=%d body=%s", resp.StatusCode, data)
	}

	resp, err = http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
}

func TestMessagesStreamRecovery(t *testing.T) {
	var calls atomic.Int32
	var continuation struct {
		Roles    []string
		Contents []string
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hello \"}}]}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(800 * time.Millisecond)
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, m := range body.Messages {
			continuation.Roles = append(continuation.Roles, m.Role)
			continuation.Contents = append(continuation.Contents, m.Content)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello world\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	_, addr := startTestProxy(t, upstream.URL+"/v1")
	reqBody := `{"model":"claude-sonnet-4-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	body := string(data)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if strings.Count(body, "event: message_start") != 1 {
		t.Fatalf("message_start count wrong: %s", body)
	}
	if strings.Count(body, "event: message_stop") != 1 {
		t.Fatalf("message_stop count wrong: %s", body)
	}
	for _, needle := range []string{`"text":"Hello "`, `"text":"world"`} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %q in %s", needle, body)
		}
	}
	if strings.Contains(body, `"type":"error"`) {
		t.Fatalf("stream contained error: %s", body)
	}
	if strings.Contains(body, `"text":"Hello world"`) {
		t.Fatalf("continuation duplicated text: %s", body)
	}
	if len(continuation.Roles) < 3 || continuation.Roles[len(continuation.Roles)-2] != "assistant" {
		t.Fatalf("continuation messages = %+v", continuation)
	}
	if continuation.Contents[len(continuation.Contents)-2] != "Hello " {
		t.Fatalf("continuation prefill = %q", continuation.Contents[len(continuation.Contents)-2])
	}
}

func TestGatedModelIsSkippedAfterFirstRefusal(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, harnessGateBody)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"f","model":"backup-model","choices":[{"index":0,"message":{"role":"assistant","content":"fallback hello"},"finish_reason":"stop"}]}`)
	}))
	defer fallback.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", []Target{{Name: "backup", BaseURL: fallback.URL + "/v1", APIKey: "k", Model: "backup-model"}})

	for i := 0; i < 3; i++ {
		status, body := postMessages(t, srv.Addr())
		if status != http.StatusOK || !strings.Contains(string(body), "fallback hello") {
			t.Fatalf("request %d: status = %d, body = %s", i+1, status, body)
		}
	}
	if primaryCalls.Load() != 1 {
		t.Fatalf("gated primary called %d times, want 1 (skipped once gated)", primaryCalls.Load())
	}
	if fallbackCalls.Load() != 3 {
		t.Fatalf("fallback calls = %d, want 3", fallbackCalls.Load())
	}
}

func TestRateLimitFailsOverWithoutInternalRetries(t *testing.T) {
	var primaryCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"Rate limit exceeded: free-models-per-day"}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"f","model":"backup-model","choices":[{"index":0,"message":{"role":"assistant","content":"fallback hello"},"finish_reason":"stop"}]}`)
	}))
	defer fallback.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", []Target{{Name: "backup", BaseURL: fallback.URL + "/v1", APIKey: "k", Model: "backup-model"}})

	start := time.Now()
	status, body := postMessages(t, srv.Addr())
	if status != http.StatusOK || !strings.Contains(string(body), "fallback hello") {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("request took %s: a long Retry-After was honored instead of failing over", elapsed)
	}
	if primaryCalls.Load() != 1 {
		t.Fatalf("rate-limited primary called %d times, want 1", primaryCalls.Load())
	}
}

func TestAllTargetsRateLimitedReturns429(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"Rate limit exceeded: tokens per minute"}}`)
	}))
	defer primary.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", nil)
	status, body := postMessages(t, srv.Addr())
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429, body = %s", status, body)
	}
	if !strings.Contains(string(body), "tokens per minute") {
		t.Fatalf("rate-limit message lost: %s", body)
	}
}

// A daily allowance cannot clear by retrying, so it must not be reported as a
// retryable 429; Claude Code would otherwise retry for hours.
func TestDailyQuotaIsTerminal(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":429,"message":"Rate limit exceeded: free-models-per-day","metadata":{"headers":{"X-RateLimit-Reset":"1794700800000"}}}}`)
	}))
	defer primary.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", nil)
	status, body := postMessages(t, srv.Addr())
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (terminal), body = %s", status, body)
	}
	for _, want := range []string{"daily free-model quota exhausted", "resets"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}
func TestFallbackTargetClampsToItsOwnMaxOutput(t *testing.T) {
	var mu sync.Mutex
	var seen []int
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	}))
	defer primary.Close()

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		payload, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(payload, &body)
		mu.Lock()
		seen = append(seen, body.MaxTokens)
		mu.Unlock()
		if body.MaxTokens > 16384 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"`+"`max_tokens`"+` must be less than or equal to `+"`16384`"+`","type":"invalid_request_error","param":"max_tokens"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"p","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer fallback.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", []Target{
		{Name: "other", BaseURL: fallback.URL + "/v1", APIKey: "k", Model: "m", MaxOutput: 16384},
	})
	reqBody := `{"model":"claude-sonnet-4-5","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`
	status, body := postRaw(t, srv.Addr(), reqBody)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != 16384 {
		t.Fatalf("fallback saw max_tokens %v, want [16384]", seen)
	}
}
func TestUnavailableModelFallsBackToNextTarget(t *testing.T) {
	var mu sync.Mutex
	primaryCalls, fallbackCalls := 0, 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		primaryCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"status":404,"title":"Not Found","detail":"Function 'x': Not found for account 'y'"}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fallbackCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"p","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer fallback.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", []Target{
		{Name: "other", BaseURL: fallback.URL + "/v1", APIKey: "k", Model: "m"},
	})
	reqBody := `{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	for i := 0; i < 2; i++ {
		if status, body := postRaw(t, srv.Addr(), reqBody); status != http.StatusOK {
			t.Fatalf("request %d: status = %d, body = %s", i+1, status, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if primaryCalls != 1 {
		t.Fatalf("primary calls = %d, want 1 (404 model skipped afterwards)", primaryCalls)
	}
	if fallbackCalls != 2 {
		t.Fatalf("fallback calls = %d, want 2", fallbackCalls)
	}
}
func TestAnthropicModelAliasRoutesToProfileModel(t *testing.T) {
	var mu sync.Mutex
	var models []string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		payload, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(payload, &body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"p","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer primary.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", nil)
	for _, name := range []string{"claude-opus-5-5", "claude-sonnet-4-5", "claude-custom"} {
		body := `{"model":"` + name + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
		if status, resp := postRaw(t, srv.Addr(), body); status != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", name, status, resp)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for i, got := range models {
		if got != "upstream-model" {
			t.Fatalf("request %d sent model %q, want upstream-model", i+1, got)
		}
	}
	if len(models) != 3 {
		t.Fatalf("upstream calls = %d, want 3", len(models))
	}
}
func TestMaxTokensLimitIsLearnedAndRetried(t *testing.T) {
	var mu sync.Mutex
	var seen []int
	refusals := 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		payload, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(payload, &body)
		mu.Lock()
		seen = append(seen, body.MaxTokens)
		mu.Unlock()
		if body.MaxTokens > 16384 {
			mu.Lock()
			refusals++
			mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"`+"`max_tokens`"+` must be less than or equal to `+"`16384`"+`, the maximum value for `+"`max_tokens`"+` is less than the `+"`context_window`"+` for this model","type":"invalid_request_error","param":"max_tokens"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"p","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer primary.Close()

	srv, _ := newTestProxy(t, primary.URL+"/v1", nil)
	reqBody := `{"model":"claude-sonnet-4-5","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`

	for i := 0; i < 2; i++ {
		status, body := postRaw(t, srv.Addr(), reqBody)
		if status != http.StatusOK {
			t.Fatalf("request %d: status = %d, body = %s", i+1, status, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if refusals != 1 {
		t.Fatalf("upstream refusals = %d, want 1 (limit learned once)", refusals)
	}
	if len(seen) < 2 || seen[0] != 32000 || seen[1] != 16384 || seen[len(seen)-1] != 16384 {
		t.Fatalf("max_tokens sent = %v, want [32000 16384 ... 16384]", seen)
	}
}
