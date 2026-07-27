package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"aiapiport/config"
	"aiapiport/provider"
)

// handleChat handles POST /v1/chat/completions.
//
// Fallback semantics:
//   - Multi-provider: each model can map to multiple providers, tried in order.
//   - Key-level: within each provider, keys are tried in order on retryable
//     statuses (402/429/5xx/401/403/network error).
//   - Non-streaming: try each provider, then each key; first non-retryable or
//     successful response is returned directly.
//   - Streaming: same provider+key-order attempt, but fallback only happens
//     *before* the first byte is flushed to the client — once SSE streaming
//     begins, no further switching occurs.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	defer r.Body.Close()

	model, isStream, err := peekModelAndStream(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if model == "" {
		writeError(w, http.StatusBadRequest, `field "model" is required`)
		return
	}

	cfg, rt := s.snapshot()
	pnames, err := rt.ProviderNames(model)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if isStream {
		s.handleStream(r.Context(), w, body, model, pnames, cfg, rt)
		return
	}
	s.handleNonStream(r.Context(), w, body, model, pnames, cfg, rt)
}

// ---- non-streaming ----

func (s *Server) handleNonStream(ctx context.Context, w http.ResponseWriter, body []byte, model string, pnames []string, cfg *config.Config, rt *router.Router) {
	var lastErr error
	slog.Info("route selected", "model", model, "stream", false, "providers", pnames)
	for pi, pname := range pnames {
		p, _ := cfg.FindProvider(pname)
		keys := p.Keys
		for ki := range keys {
			slog.Info("upstream attempt", "model", model, "stream", false, "route_index", pi, "provider", pname, "base_url", p.BaseURL, "key_index", ki, "key_count", len(keys))
			res, err := rt.Attempt(ctx, model, body, false, pi, ki)
			if err != nil {
				writeError(w, http.StatusBadGateway, err.Error())
				return
			}
			if res.Err != nil {
				lastErr = res.Err
				slog.Warn("upstream error, trying next key", "model", model, "provider", pname, "base_url", p.BaseURL, "route_index", pi, "key_index", ki, "err", res.Err)
				continue
			}
			resp := res.Resp
			if res.Retryable {
				slog.Warn("upstream retryable status, trying next key", "model", model, "provider", pname, "base_url", p.BaseURL, "route_index", pi, "key_index", ki, "status", resp.StatusCode)
				errBody := drainBody(resp)
				_ = resp.Body.Close()
				lastErr = fmt.Errorf("upstream %s key#%d returned status %d: %s", pname, ki, resp.StatusCode, errBody)
				continue
			}
			slog.Info("upstream response", "model", model, "stream", false, "provider", pname, "base_url", p.BaseURL, "route_index", pi, "key_index", ki, "status", resp.StatusCode)
			s.deliverNonStream(ctx, w, resp, p, model)
			return
		}
	}
	slog.Error("all upstreams exhausted", "model", model, "stream", false, "providers", pnames, "err", lastErr)
	msg := "all providers and keys exhausted"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	writeError(w, http.StatusBadGateway, msg)
}

// deliverNonStream converts the upstream 2xx response to OpenAI format and writes it.
//   - openai type: pass through body as-is
//   - anthropic type: convert then return
func (s *Server) deliverNonStream(ctx context.Context, w http.ResponseWriter, resp *http.Response, p config.Provider, requestedModel string) {
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "read upstream: "+err.Error())
		return
	}

	var payload []byte
	switch p.Type {
	case "anthropic":
		out, err := provider.AnthropicToOpenAIResponse(respBody, requestedModel)
		if err != nil {
			writeError(w, http.StatusBadGateway, "convert anthropic response: "+err.Error())
			return
		}
		payload = out
	default: // openai
		payload = respBody
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// ---- streaming ----

func (s *Server) handleStream(ctx context.Context, w http.ResponseWriter, body []byte, model string, pnames []string, cfg *config.Config, rt *router.Router) {
	var lastErr error
	slog.Info("route selected", "model", model, "stream", true, "providers", pnames)
	for pi, pname := range pnames {
		p, _ := cfg.FindProvider(pname)
		keys := p.Keys
		for ki := range keys {
			slog.Info("upstream attempt", "model", model, "stream", true, "route_index", pi, "provider", pname, "base_url", p.BaseURL, "key_index", ki, "key_count", len(keys))
			res, err := rt.Attempt(ctx, model, body, true, pi, ki)
			if err != nil {
				writeError(w, http.StatusBadGateway, err.Error())
				return
			}
			if res.Err != nil {
				lastErr = res.Err
				slog.Warn("upstream stream error, trying next key", "model", model, "provider", pname, "base_url", p.BaseURL, "route_index", pi, "key_index", ki, "err", res.Err)
				continue
			}
			resp := res.Resp
			if res.Retryable {
				slog.Warn("upstream stream retryable status before flush, trying next key", "model", model, "provider", pname, "base_url", p.BaseURL, "route_index", pi, "key_index", ki, "status", resp.StatusCode)
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				lastErr = fmt.Errorf("upstream %s key#%d stream status %d", pname, ki, resp.StatusCode)
				continue
			}
			slog.Info("upstream response", "model", model, "stream", true, "provider", pname, "base_url", p.BaseURL, "route_index", pi, "key_index", ki, "status", resp.StatusCode)
			s.deliverStream(ctx, w, resp, p, model)
			return
		}
	}
	slog.Error("all upstreams exhausted", "model", model, "stream", true, "providers", pnames, "err", lastErr)
	msg := "all providers and keys exhausted"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	writeError(w, http.StatusBadGateway, msg)
}

// deliverStream pipes the upstream streaming response to the client.
//   - openai type: pass through SSE as-is (io.Copy + per-chunk flush)
//   - anthropic type: convert Anthropic SSE events to OpenAI chat.completion.chunk
func (s *Server) deliverStream(ctx context.Context, w http.ResponseWriter, resp *http.Response, p config.Provider, requestedModel string) {
	defer resp.Body.Close()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	switch p.Type {
	case "anthropic":
		completionID := "chatcmpl-stream"
		if err := provider.ConvertAnthropicSSEStream(resp.Body, w, requestedModel, completionID); err != nil {
			slog.Warn("anthropic stream convert ended", "err", err)
		}
	default: // openai: pass through
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				if err != io.EOF {
					slog.Warn("upstream stream read ended", "err", err)
				}
				break
			}
		}
	}
}

// ---- helpers ----

// peekModelAndStream parses only the top-level model/stream fields to avoid full deserialization.
func peekModelAndStream(body []byte) (model string, stream bool, err error) {
	var top struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return "", false, fmt.Errorf("parse request body: %w", err)
	}
	return top.Model, top.Stream, nil
}

// drainBody reads up to 1 KB of the response body for use as an error message string.
func drainBody(resp *http.Response) string {
	limited := io.LimitReader(resp.Body, 1024)
	b, _ := io.ReadAll(limited)
	return string(b)
}

// writeError returns an OpenAI-style JSON error response.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "gateway_error",
		},
	})
}
