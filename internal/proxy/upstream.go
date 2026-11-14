package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"claude-custom/internal/translate"
)

const (
	maxAttempts       = 10
	baseBackoff       = 250 * time.Millisecond
	maxBackoff        = 8 * time.Second
	maxRetryAfterWait = 15 * time.Second
	earlyHoldback     = 750 * time.Millisecond
	earlyHoldbackMax  = 64 << 10
)

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	var req translate.MessagesRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}
	s.requests.Add(1)
	if req.Stream {
		s.streams.Add(1)
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "messages must not be empty")
		return
	}

	release, err := s.limiter.Acquire(r.Context())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writeError(w, http.StatusServiceUnavailable, "overloaded_error", "too many concurrent requests")
		return
	}
	defer release()

	chatModel := s.resolveModel(req.Model)
	chatReq, err := translate.BuildChatRequest(req, chatModel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if s.profile.MaxOutputTokens > 0 && chatReq.MaxTokens > s.profile.MaxOutputTokens {
		chatReq.MaxTokens = s.profile.MaxOutputTokens
	}
	chatReq.Stream = req.Stream
	if chatReq.Stream {
		chatReq.StreamOptions = &translate.StreamOptions{IncludeUsage: true}
	}

	if req.Stream {
		s.streamMessages(w, r, chatReq, chatModel, req.Model)
		return
	}
	s.jsonMessages(w, r, chatReq, chatModel, req.Model)
}

// resolveModel maps the requested Anthropic model to the model id sent
// to the primary upstream. The proxy aliases ("claude-custom",
// "claude-sonnet-4-5") resolve to the configured profile model; a
// discovered live model id passes through so model pickers work.
func (s *Server) resolveModel(requested string) string {
	if isClaudeAlias(requested) {
		return s.model
	}
	return requested
}

// isClaudeAlias reports whether an Anthropic-style model name from the
// client should be replaced by the configured provider model. Claude Code
// sends names like "claude-opus-5-5" for any /model choice; forwarding
// those verbatim makes the provider answer 404 "model does not exist".
func isClaudeAlias(requested string) bool {
	if requested == "" || requested == "claude-custom" {
		return true
	}
	return strings.HasPrefix(requested, "claude-")
}

// targetsFor picks the upstream targets for a request. An explicit
// fallback alias ("name/model") routes straight to that fallback; a
// known live model id overrides the primary model; anything else uses
// the default primary + fallback chain.
func (s *Server) targetsFor(requested string) []Target {
	if isClaudeAlias(requested) {
		return s.messageTargets()
	}
	for _, fallback := range s.fallbacks {
		if requested == fallback.Name+"/"+fallback.Model {
			return []Target{fallback}
		}
	}
	if s.knownLiveModel(requested) {
		target := s.primaryTarget()
		target.Model = requested
		return []Target{target}
	}
	return s.messageTargets()
}

func (s *Server) primaryTarget() Target {
	return Target{
		Name:      s.profile.Provider,
		BaseURL:   s.profile.BaseURL,
		APIKey:    s.apiKey,
		Model:     s.model,
		MaxOutput: s.profile.MaxOutputTokens,
		Headers:   s.provider.Headers,
	}
}

func (s *Server) messageTargets() []Target {
	targets := []Target{s.primaryTarget()}
	return append(targets, s.fallbacks...)
}

func (s *Server) breakerFor(target Target) *circuitBreaker {
	key := target.Name + "|" + target.BaseURL + "|" + target.Model
	s.breakerMu.Lock()
	defer s.breakerMu.Unlock()
	if s.breakers == nil {
		s.breakers = make(map[string]*circuitBreaker)
	}
	breaker, ok := s.breakers[key]
	if !ok {
		breaker = newCircuitBreaker()
		s.breakers[key] = breaker
	}
	return breaker
}

func (s *Server) useFallback(target Target) {
	s.fallbackUse.Add(1)
	s.logger.Printf("falling back to %s/%s", target.Name, target.Model)
}

func gatedKey(target Target) string {
	return target.BaseURL + "|" + target.Model
}

// markGated remembers that an upstream refuses this model by policy (for
// example OpenRouter's "agentic harness" gate), so later requests skip it
// instead of paying a round trip that can only fail again.
func (s *Server) markGated(target Target) {
	s.gatedMu.Lock()
	defer s.gatedMu.Unlock()
	if s.gated == nil {
		s.gated = make(map[string]bool)
	}
	s.gated[gatedKey(target)] = true
	s.logger.Printf("%s/%s is gated upstream; skipping it for new requests", target.Name, target.Model)
}

func (s *Server) isGated(target Target) bool {
	s.gatedMu.Lock()
	defer s.gatedMu.Unlock()
	return s.gated[gatedKey(target)]
}

func (s *Server) nextUsable(targets []Target, after int) int {
	for j := after + 1; j < len(targets); j++ {
		if !s.isGated(targets[j]) {
			return j
		}
	}
	return -1
}

func (s *Server) jsonMessages(w http.ResponseWriter, r *http.Request, chatReq translate.ChatRequest, chatModel, requested string) {
	targets := s.targetsFor(requested)
	var (
		limitStatus int
		limitBody   []byte
		skipped     int
	)
	for i, target := range targets {
		if s.isGated(target) {
			skipped++
			s.logger.Printf("skipping gated %s/%s", target.Name, target.Model)
			continue
		}
		payload, err := s.payloadFor(chatReq, target)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api_error", "encode upstream request: "+err.Error())
			return
		}
		resp, body, retryable, err := s.doUpstreamTarget(r.Context(), target, payload, false, triesFor(i, len(targets)))
		if err != nil {
			if next := s.nextUsable(targets, i); retryable && next >= 0 {
				s.useFallback(targets[next])
				continue
			}
			if limitStatus != 0 {
				writeUpstreamError(w, limitStatus, limitBody)
				return
			}
			writeError(w, http.StatusBadGateway, "api_error", err.Error())
			return
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if resp.StatusCode == http.StatusTooManyRequests || isTokenRateLimit(resp.StatusCode, body) {
				limitStatus, limitBody = resp.StatusCode, body
			}
			if isModelUnavailable(resp.StatusCode) {
				s.markGated(target)
				s.logger.Printf("%s/%s is not available upstream; skipping it for new requests", target.Name, target.Model)
			}
			if next := s.nextUsable(targets, i); (retryable || isModelUnavailable(resp.StatusCode)) && next >= 0 {
				s.useFallback(targets[next])
				continue
			}
			if limitStatus != 0 && resp.StatusCode != limitStatus {
				// A rate limit says "try again later"; a policy gate on one
				// model says nothing about the provider. Report the rate limit.
				writeUpstreamError(w, limitStatus, limitBody)
				return
			}
			writeUpstreamError(w, resp.StatusCode, body)
			return
		}
		defer resp.Body.Close()
		var chat translate.ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&chat); err != nil {
			writeError(w, http.StatusBadGateway, "api_error", "decode upstream response: "+err.Error())
			return
		}
		if chat.Error != nil {
			writeError(w, http.StatusBadGateway, "api_error", chat.Error.Message)
			return
		}
		writeJSON(w, http.StatusOK, translate.BuildAnthropicResponse(chat, chatModel))
		return
	}
	if limitStatus != 0 {
		writeUpstreamError(w, limitStatus, limitBody)
		return
	}
	writeError(w, http.StatusBadGateway, "api_error", fmt.Sprintf("all providers failed (%d gated upstream by policy; pick another model or provider)", skipped))
}

func (s *Server) payloadFor(chatReq translate.ChatRequest, target Target) ([]byte, error) {
	next := chatReq
	next.Model = target.Model
	limit := s.learnedMaxOutput(target)
	if target.MaxOutput > 0 && (limit == 0 || target.MaxOutput < limit) {
		limit = target.MaxOutput
	}
	if limit > 0 && next.MaxTokens > limit {
		next.MaxTokens = limit
	}
	return json.Marshal(next)
}

// learnedMaxOutput reports a per-target max_tokens cap discovered from an
// upstream rejection, so later requests are clamped before they are sent.
func (s *Server) learnedMaxOutput(target Target) int {
	s.maxOutMu.Lock()
	defer s.maxOutMu.Unlock()
	return s.maxOut[gatedKey(target)]
}

func (s *Server) learnMaxOutput(target Target, limit int) {
	if limit <= 0 {
		return
	}
	s.maxOutMu.Lock()
	defer s.maxOutMu.Unlock()
	if s.maxOut == nil {
		s.maxOut = make(map[string]int)
	}
	key := gatedKey(target)
	if current, ok := s.maxOut[key]; ok && current <= limit {
		return
	}
	s.maxOut[key] = limit
	s.logger.Printf("%s/%s caps max_tokens at %d; clamping future requests", target.Name, target.Model, limit)
}

// withMaxTokens rewrites an encoded chat request with a smaller max_tokens.
func withMaxTokens(payload []byte, limit int) ([]byte, bool) {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return payload, false
	}
	current := 0
	switch v := body["max_tokens"].(type) {
	case float64:
		current = int(v)
	case nil:
	default:
		return payload, false
	}
	if current != 0 && current <= limit {
		return payload, false
	}
	body["max_tokens"] = limit
	adjusted, err := json.Marshal(body)
	if err != nil {
		return payload, false
	}
	return adjusted, true
}

var maxTokensLimitPattern = regexp.MustCompile(`(?i)max_tokens[^0-9]{0,80}?(\d{2,7})`)

// maxTokensLimit reports the upstream-advertised max_tokens ceiling from a
// 400 like "`max_tokens` must be less than or equal to `16384`".
func maxTokensLimit(status int, body []byte) int {
	if status != http.StatusBadRequest {
		return 0
	}
	if !bytes.Contains(bytes.ToLower(body), []byte("max_tokens")) {
		return 0
	}
	match := maxTokensLimitPattern.FindSubmatch(body)
	if match == nil {
		return 0
	}
	limit, err := strconv.Atoi(string(match[1]))
	if err != nil || limit <= 0 {
		return 0
	}
	return limit
}

func (s *Server) streamMessages(w http.ResponseWriter, r *http.Request, chatReq translate.ChatRequest, chatModel, requested string) {
	targets := s.targetsFor(requested)
	var (
		resp        *http.Response
		body        []byte
		target      Target
		limitStatus int
		limitBody   []byte
	)
	for i, candidate := range targets {
		if s.isGated(candidate) {
			s.logger.Printf("skipping gated %s/%s", candidate.Name, candidate.Model)
			continue
		}
		payload, err := s.payloadFor(chatReq, candidate)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api_error", "encode upstream request: "+err.Error())
			return
		}
		rsp, b, retryable, err := s.doUpstreamTarget(r.Context(), candidate, payload, true, triesFor(i, len(targets)))
		if err != nil {
			if next := s.nextUsable(targets, i); retryable && next >= 0 {
				s.useFallback(targets[next])
				continue
			}
			writeError(w, http.StatusBadGateway, "api_error", err.Error())
			return
		}
		if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
			if rsp.StatusCode == http.StatusTooManyRequests || isTokenRateLimit(rsp.StatusCode, b) {
				limitStatus, limitBody = rsp.StatusCode, b
			}
			if isModelUnavailable(rsp.StatusCode) {
				s.markGated(candidate)
				s.logger.Printf("%s/%s is not available upstream; skipping it for new requests", candidate.Name, candidate.Model)
			}
			if next := s.nextUsable(targets, i); (retryable || isModelUnavailable(rsp.StatusCode)) && next >= 0 {
				rsp.Body.Close()
				s.useFallback(targets[next])
				continue
			}
			resp, body, target = rsp, b, candidate
			break
		}
		resp, body, target = rsp, b, candidate
		break
	}
	if resp == nil {
		if limitStatus != 0 {
			writeUpstreamError(w, limitStatus, limitBody)
			return
		}
		writeError(w, http.StatusBadGateway, "api_error", "all providers failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if limitStatus != 0 && resp.StatusCode != limitStatus {
			writeUpstreamError(w, limitStatus, limitBody)
			return
		}
		writeUpstreamError(w, resp.StatusCode, body)
		return
	}

	streamReq := chatReq
	streamReq.Model = target.Model
	hold := newHoldWriter(w)
	conv := translate.NewStreamConverter(hold, chatModel)
	sawDone, err := consumeStream(resp.Body, conv.Handle)
	if err != nil && r.Context().Err() != nil {
		return
	}
	if err != nil || (!sawDone && !conv.Finished()) {
		if !hold.Sent() && conv.Text() != "" {
			_ = hold.Flush()
		}
		if !hold.Sent() || conv.HasTools() || conv.Text() == "" {
			reason := "upstream stream ended before completion"
			if err != nil {
				reason = err.Error()
			}
			s.failStream(w, hold, conv, reason)
			return
		}
		if recErr := s.recoverStream(r.Context(), streamReq, conv, target); recErr != nil {
			if r.Context().Err() != nil {
				return
			}
			s.failStream(w, hold, conv, recErr.Error())
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	if err := conv.Finish(); err != nil {
		return
	}
	_ = hold.Flush()
}

const (
	maxRecoveries    = 4
	maxRecoveredText = 2 << 20
)

func (s *Server) recoverStream(ctx context.Context, chatReq translate.ChatRequest, conv *translate.StreamConverter, target Target) error {
	for attempt := 0; attempt < maxRecoveries; attempt++ {
		if conv.HasTools() || conv.Finished() {
			return nil
		}
		prior := conv.Text()
		if prior == "" {
			return errors.New("upstream stream ended before any text was delivered")
		}
		if len(prior) >= maxRecoveredText {
			return errors.New("stream recovery limit reached")
		}

		s.recoveries.Add(1)
		next := chatReq
		next.Stream = true
		next.StreamOptions = &translate.StreamOptions{IncludeUsage: true}
		next.Messages = append([]translate.ChatMessage{}, chatReq.Messages...)
		next.Messages = append(next.Messages,
			translate.ChatMessage{Role: "assistant", Content: prior},
			translate.ChatMessage{Role: "user", Content: "Continue from exactly where the previous response stopped. Do not repeat any text that was already written."},
		)
		payload, err := json.Marshal(next)
		if err != nil {
			return err
		}

		resp, body, _, err := s.doUpstreamTarget(ctx, target, payload, true, maxAttempts)
		if err != nil {
			return fmt.Errorf("stream recovery failed: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return fmt.Errorf("stream recovery failed: %s", snippet(body))
		}

		var buffered []translate.ChatResponse
		var continuation strings.Builder
		sawDone, scanErr := consumeStream(resp.Body, func(chunk translate.ChatResponse) error {
			buffered = append(buffered, chunk)
			continuation.WriteString(chunkText(chunk))
			return nil
		})
		resp.Body.Close()
		if scanErr != nil && ctx.Err() != nil {
			return ctx.Err()
		}

		text := stripOverlap(prior, continuation.String())
		if text != "" {
			if err := conv.Handle(translate.ChatResponse{Choices: []translate.ChatChoice{{
				Delta: translate.ChatDelta{Content: text},
			}}}); err != nil {
				return err
			}
		}
		for _, chunk := range buffered {
			if len(chunk.Choices) > 0 {
				chunk.Choices[0].Delta.Content = ""
				chunk.Choices[0].Message.Content = nil
			}
			if err := conv.Handle(chunk); err != nil {
				return err
			}
		}
		if scanErr != nil {
			return fmt.Errorf("stream recovery interrupted: %w", scanErr)
		}
		if sawDone || conv.Finished() {
			return nil
		}
	}
	return errors.New("stream recovery limit reached")
}

func chunkText(chunk translate.ChatResponse) string {
	if len(chunk.Choices) == 0 {
		return ""
	}
	choice := chunk.Choices[0]
	if choice.Delta.Content != "" {
		return choice.Delta.Content
	}
	if text, ok := choice.Message.Content.(string); ok {
		return text
	}
	return ""
}

func stripOverlap(prior, continuation string) string {
	max := len(prior)
	if len(continuation) < max {
		max = len(continuation)
	}
	if max > 2000 {
		max = 2000
	}
	for n := max; n > 0; n-- {
		if strings.HasSuffix(prior, continuation[:n]) {
			return continuation[n:]
		}
	}
	return continuation
}

func consumeStream(body io.Reader, handle func(translate.ChatResponse) error) (bool, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		data := line
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			return true, nil
		}
		if !strings.HasPrefix(data, "{") {
			continue
		}
		var chunk translate.ChatResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return false, fmt.Errorf("invalid upstream stream data: %w", err)
		}
		if chunk.Error != nil {
			return false, errors.New(chunk.Error.Message)
		}
		if err := handle(chunk); err != nil {
			return false, err
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("upstream stream error: %w", err)
	}
	return false, nil
}

func (s *Server) failStream(w http.ResponseWriter, hold *holdWriter, conv *translate.StreamConverter, message string) {
	s.setLastError(message)
	if !hold.Sent() {
		hold.Discard()
		writeError(w, http.StatusBadGateway, "api_error", message)
		return
	}
	_ = hold.Event("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": message},
	})
	_ = hold.Flush()
	_ = conv
}

func (s *Server) doUpstreamTarget(ctx context.Context, target Target, payload []byte, stream bool, maxTries int) (*http.Response, []byte, bool, error) {
	if maxTries < 1 {
		maxTries = 1
	}
	breaker := s.breakerFor(target)
	if !breaker.allow() {
		err := fmt.Errorf("%s/%s is cooling down after repeated failures (%ds left)", target.Name, targetModel(target), int(breaker.openRemaining().Seconds())+1)
		s.setLastError(err.Error())
		return nil, nil, true, err
	}
	success := false
	neutral := false
	defer func() {
		if neutral {
			breaker.recordNeutral()
			return
		}
		breaker.record(success)
	}()
	var lastErr error
	clampRetries := 0
	for attempt := 0; attempt < maxTries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.targetURL(target, "/chat/completions"), bytes.NewReader(payload))
		if err != nil {
			return nil, nil, false, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		if target.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+target.APIKey)
		}
		for k, v := range target.Headers {
			req.Header.Set(k, v)
		}

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, nil, false, ctx.Err()
			}
			if !retryableNetworkError(err) {
				return nil, nil, false, err
			}
			if attempt == maxTries-1 {
				return nil, nil, true, err
			}
			s.retries.Add(1)
			s.setLastError(err.Error())
			if err := sleepBackoff(ctx, attempt+1, ""); err != nil {
				return nil, nil, false, err
			}
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			success = true
			return resp, nil, false, nil
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		lastErr = fmt.Errorf("upstream returned %s: %s", resp.Status, snippet(body))
		s.logger.Printf("upstream %s %s: %s", target.Name, resp.Status, snippet(body))
		s.setLastError(lastErr.Error())
		tokenLimit := isTokenRateLimit(resp.StatusCode, body)
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || tokenLimit:
			s.rateLimits.Add(1)
		case resp.StatusCode == http.StatusRequestEntityTooLarge:
			s.payloadErrs.Add(1)
		}
		if resp.StatusCode >= 500 {
			s.serverErrs.Add(1)
		}
		if tokenLimit {
			// A provider that reports a per-minute token limit as 413 answers the
			// same way for the rest of the minute, so retrying here is pointless:
			// hand it straight to the fallback chain. The provider itself is
			// healthy, so this must not count against its circuit.
			neutral = true
			return resp, body, true, nil
		}
		if limit := maxTokensLimit(resp.StatusCode, body); limit > 0 && clampRetries < 2 {
			if adjusted, ok := withMaxTokens(payload, limit); ok {
				payload = adjusted
				s.learnMaxOutput(target, limit)
				clampRetries++
				if attempt > 0 {
					attempt--
				}
				continue
			}
		}
		if isHarnessGate(resp.StatusCode, body) {
			// OpenRouter gates some free endpoints to listed apps; the same
			// request succeeds on another model, so fall back at once. This is a
			// permission decision, not a provider failure.
			s.markGated(target)
			neutral = true
			return resp, body, true, nil
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			// A rate limit is a normal answer, not an outage: opening a circuit
			// here would block the provider exactly when it recovers. Retrying
			// inside the proxy only stacks waits (a daily quota can carry a
			// Retry-After of hours), so hand it to the fallback chain and let
			// Claude Code pace its own retry.
			neutral = true
			return resp, body, true, nil
		}
		if !retryableStatus(resp.StatusCode) {
			success = true
			return resp, body, false, nil
		}
		if attempt == maxTries-1 {
			return resp, body, true, nil
		}
		s.retries.Add(1)
		if err := sleepBackoff(ctx, attempt+1, resp.Header.Get("Retry-After")); err != nil {
			return nil, nil, false, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("upstream request failed")
	}
	return nil, nil, true, lastErr
}

func (s *Server) targetURL(target Target, suffix string) string {
	base := strings.TrimRight(strings.TrimSpace(target.BaseURL), "/")
	return base + suffix
}

func targetModel(target Target) string {
	if strings.TrimSpace(target.Model) == "" {
		return "(provider default)"
	}
	return target.Model
}

// triesFor bounds retries per target: with another target still available, a
// failing target is handed to the fallback chain at once instead of burning
// retries and backoff on a provider that cannot serve this request.
func triesFor(index, targetCount int) int {
	if index+1 < targetCount {
		return 1
	}
	return maxAttempts
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly,
		http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return status >= 520 && status <= 527
}

func retryableNetworkError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"connection reset", "connection refused", "broken pipe", "eof", "timeout", "temporarily unavailable", "no such host"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return true
}

func sleepBackoff(ctx context.Context, attempt int, retryAfter string) error {
	delay := baseBackoff << uint(attempt-1)
	if delay > maxBackoff || delay <= 0 {
		delay = maxBackoff
	}
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
			delay = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(retryAfter); err == nil {
			if d := time.Until(t); d > 0 {
				delay = d
			}
		}
	}
	if delay > maxRetryAfterWait {
		delay = maxRetryAfterWait
	}
	delay += time.Duration(rand.Int63n(int64(250 * time.Millisecond)))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func writeUpstreamError(w http.ResponseWriter, status int, body []byte) {
	tokenLimit := isTokenRateLimit(status, body)
	if status == http.StatusRequestEntityTooLarge && tokenLimit {
		// Claude Code maps a 413 to its own "Request too large (max 32MB)" and
		// starts a /compact loop (whose request hits the same limit); report it
		// as the rate limit it actually is.
		status = http.StatusTooManyRequests
	}
	kind := "api_error"
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = "invalid_request_error"
	case http.StatusRequestEntityTooLarge:
		kind = "invalid_request_error"
	case http.StatusUnauthorized:
		kind = "authentication_error"
	case http.StatusForbidden:
		kind = "permission_error"
	case http.StatusNotFound:
		kind = "not_found_error"
	case http.StatusTooManyRequests:
		kind = "rate_limit_error"
	case 529:
		kind = "overloaded_error"
	}
	if status == http.StatusTooManyRequests && isDailyQuota(body) {
		// A daily quota cannot recover by retrying now; a 429 makes Claude Code
		// retry for hours, so report it as a terminal request error instead.
		message := snippet(body)
		var envelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			message = envelope.Error.Message
		}
		message += " (daily free-model quota exhausted"
		if reset := quotaResetTime(body); reset != "" {
			message += "; resets " + reset
		}
		message += " — add credits or pick another provider with `claude-custom profile`)"
		writeError(w, http.StatusBadRequest, "invalid_request_error", message)
		return
	}
	message := snippet(body)
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		message = envelope.Error.Message
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if tokenLimit || status == http.StatusRequestEntityTooLarge {
		message += " (provider input-token-per-minute/ITPM limit — reduce context, trim MCP tools, or switch to a model/provider with a larger input limit)"
	}
	if status == http.StatusForbidden && bytes.Contains(body, []byte("agentic harness")) {
		message += " (this free model is reserved for apps listed on openrouter.ai/apps; pick another model with `claude-custom model list --refresh`)"
	}
	writeError(w, status, kind, message)
}

// isDailyQuota reports whether a 429 is a hard daily allowance (as opposed to a
// per-minute limit that will clear on its own).
func isDailyQuota(body []byte) bool {
	lower := bytes.ToLower(body)
	return bytes.Contains(lower, []byte("per-day")) ||
		bytes.Contains(lower, []byte("per_day")) ||
		bytes.Contains(lower, []byte("daily")) ||
		bytes.Contains(lower, []byte("free_daily_limit"))
}

var quotaResetPattern = regexp.MustCompile(`X-RateLimit-Reset"\s*:\s*"?(\d{10,19})`)

// quotaResetTime renders the upstream-reported reset instant, or "" when the
// response does not carry one.
func quotaResetTime(body []byte) string {
	match := quotaResetPattern.FindSubmatch(body)
	if match == nil {
		return ""
	}
	raw := string(match[1])
	millis, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return ""
	}
	if len(raw) > 11 {
		millis /= 1000
	}
	return time.Unix(millis, 0).Local().Format("2006-01-02 15:04 MST")
}

// isTokenRateLimit reports whether an upstream error is really a token-based
// rate limit, which Groq and some others deliver as 413 rather than 429.
// isModelUnavailable reports an upstream 404, which every OpenAI-compatible
// gateway uses for "this model id does not exist or is not provisioned for
// your account". Falling back is right: another provider may serve it.
func isModelUnavailable(status int) bool {
	return status == http.StatusNotFound
}

func isTokenRateLimit(status int, body []byte) bool {
	if status != http.StatusRequestEntityTooLarge && status != http.StatusTooManyRequests {
		return false
	}
	return bytes.Contains(body, []byte("rate_limit_exceeded")) ||
		bytes.Contains(body, []byte(`"type":"tokens"`)) ||
		bytes.Contains(body, []byte(`"type": "tokens"`))
}

// isHarnessGate reports whether an upstream 403 is OpenRouter's
// gate that reserves a free endpoint for apps listed on openrouter.ai/apps.
func isHarnessGate(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	return bytes.Contains(body, []byte("agentic harness")) ||
		bytes.Contains(body, []byte("Gate Free Endpoints by Agentic Harness"))
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 800 {
		s = s[:800] + "..."
	}
	return s
}

type holdWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	start   time.Time
	buf     bytes.Buffer
	started bool
}

func newHoldWriter(w http.ResponseWriter) *holdWriter {
	h := &holdWriter{w: w, start: time.Now()}
	if f, ok := w.(http.Flusher); ok {
		h.flusher = f
	}
	return h
}

func (h *holdWriter) Event(name string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var line bytes.Buffer
	fmt.Fprintf(&line, "event: %s\ndata: %s\n\n", name, data)
	if !h.started {
		h.buf.Write(line.Bytes())
		if time.Since(h.start) >= earlyHoldback || h.buf.Len() >= earlyHoldbackMax {
			h.startStream()
			h.flush()
		}
		return nil
	}
	if _, err := h.w.Write(line.Bytes()); err != nil {
		return err
	}
	h.flush()
	return nil
}

func (h *holdWriter) startStream() {
	if h.started {
		return
	}
	h.started = true
	h.w.Header().Set("Content-Type", "text/event-stream")
	h.w.Header().Set("Cache-Control", "no-cache")
	h.w.Header().Set("Connection", "keep-alive")
	h.w.Header().Set("X-Accel-Buffering", "no")
	h.w.WriteHeader(http.StatusOK)
}

func (h *holdWriter) flush() {
	if h.buf.Len() > 0 {
		_, _ = h.w.Write(h.buf.Bytes())
		h.buf.Reset()
	}
	if h.flusher != nil {
		h.flusher.Flush()
	}
}

func (h *holdWriter) Flush() error {
	if !h.started {
		if h.buf.Len() == 0 {
			return nil
		}
		h.startStream()
	}
	h.flush()
	return nil
}

func (h *holdWriter) Sent() bool { return h.started }

func (h *holdWriter) Discard() {
	h.buf.Reset()
}
