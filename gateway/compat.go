package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aiapiport/config"
	"aiapiport/provider"
	"aiapiport/router"
)

// This file implements the endpoints that clients other than modern OpenAI SDKs
// call, so a single gateway URL works in as many front-ends as possible:
//
//	POST /v1/completions   legacy text completions — converted to a chat request
//	                       upstream (chat-only upstreams have no /completions)
//	                       and converted back to the legacy response shape,
//	                       streaming included.
//	POST /v1/responses     OpenAI Responses API — passed through to the upstream.
//	POST /v1/embeddings    passed through to the upstream.
//	GET  /v1/models/{id}   single model lookup.
//
// All of them honour the same model→provider route and key fallback order as
// /v1/chat/completions.

// legacySamplingFields are the request fields shared by the legacy completions
// and chat completions APIs. Anything else in a legacy request (echo, suffix,
// best_of, logprobs…) has no chat equivalent and is dropped.
var legacySamplingFields = []string{
	"max_tokens", "temperature", "top_p", "stop", "presence_penalty",
	"frequency_penalty", "seed", "user", "n", "logit_bias", "stream_options",
}

// ---- POST /v1/completions ----

// handleCompletions serves the legacy text-completion API by converting the
// request to a chat request and converting the reply back.
func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	defer r.Body.Close()

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "parse request body: "+err.Error())
		return
	}
	model, _ := req["model"].(string)
	if model == "" {
		writeErrorCode(w, http.StatusBadRequest, `field "model" is required`, "", "model")
		return
	}
	prompt, err := legacyPromptText(req["prompt"])
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, err.Error(), "", "prompt")
		return
	}
	isStream, _ := req["stream"].(bool)

	chatReq := map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "user", "content": prompt}},
		"stream":   isStream,
	}
	for _, f := range legacySamplingFields {
		if v, ok := req[f]; ok {
			chatReq[f] = v
		}
	}
	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build chat request: "+err.Error())
		return
	}

	cfg, rt := s.snapshot()
	pnames, err := rt.ProviderNames(model)
	if err != nil {
		writeErrorCode(w, http.StatusNotFound, err.Error()+"; see GET /v1/models for the configured model names", "model_not_found", "model")
		return
	}

	slog.Info("legacy completions converted to chat", "model", model, "stream", isStream)
	forwardPath(r.Context(), w, model, chatBody, isStream, provider.ChatPath, pnames, cfg, rt,
		func(resp *http.Response, p config.Provider) {
			if isStream {
				s.deliverLegacyStream(w, resp, p, model)
				return
			}
			s.deliverLegacyNonStream(w, resp, p, model)
		})
}

// legacyPromptText flattens a legacy prompt (string or array of strings) into
// the single string a chat message needs.
func legacyPromptText(raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		if v == "" {
			return "", fmt.Errorf(`field "prompt" is empty`)
		}
		return v, nil
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return "", fmt.Errorf(`unsupported "prompt" element: only strings are supported`)
			}
			parts = append(parts, s)
		}
		if len(parts) == 0 {
			return "", fmt.Errorf(`field "prompt" is required`)
		}
		return strings.Join(parts, "\n"), nil
	case nil:
		return "", fmt.Errorf(`field "prompt" is required`)
	default:
		return "", fmt.Errorf(`unsupported "prompt" type: use a string`)
	}
}

// deliverLegacyNonStream converts a chat reply (OpenAI or Anthropic upstream)
// into a legacy text-completion body.
func (s *Server) deliverLegacyNonStream(w http.ResponseWriter, resp *http.Response, p config.Provider, requestedModel string) {
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "read upstream: "+err.Error())
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeUpstreamError(w, resp.StatusCode, raw)
		return
	}

	chatJSON := raw
	if p.Type == "anthropic" {
		converted, err := provider.AnthropicToOpenAIResponse(raw, requestedModel)
		if err != nil {
			writeError(w, http.StatusBadGateway, "convert anthropic response: "+err.Error())
			return
		}
		chatJSON = converted
	}
	out, err := chatToLegacyCompletion(chatJSON, requestedModel)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// deliverLegacyStream converts an upstream chat SSE stream into legacy
// text_completion SSE events.
func (s *Server) deliverLegacyStream(w http.ResponseWriter, resp *http.Response, p config.Provider, requestedModel string) {
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		writeUpstreamError(w, resp.StatusCode, raw)
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	id := "cmpl-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	created := time.Now().Unix()
	model := requestedModel
	finish := "stop"
	var usage json.RawMessage

	scanner := bufio.NewScanner(chatSSEReader(resp, p, requestedModel))
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		payload, ok := sseData(scanner.Text())
		if !ok {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // keepalive comments and unknown event shapes
		}
		if chunk.ID != "" {
			id = legacyCompletionID(chunk.ID)
		}
		if chunk.Created != 0 {
			created = chunk.Created
		}
		if model == "" && chunk.Model != "" {
			model = chunk.Model
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage = chunk.Usage
		}

		var text strings.Builder
		for _, c := range chunk.Choices {
			text.WriteString(c.Delta.Content)
			if c.FinishReason != nil && *c.FinishReason != "" {
				finish = *c.FinishReason
			}
		}
		if text.Len() == 0 {
			continue
		}
		if err := writeSSE(w, flusher, legacyCompletionChunk(id, created, model, text.String(), "", nil)); err != nil {
			return
		}
	}

	_ = writeSSE(w, flusher, legacyCompletionChunk(id, created, model, "", finish, usage))
	_ = writeSSE(w, flusher, []byte("[DONE]"))
}

// chatToLegacyCompletion rewrites an OpenAI chat-completion response into the
// legacy text-completion shape.
func chatToLegacyCompletion(raw []byte, requestedModel string) ([]byte, error) {
	var chat struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, fmt.Errorf("parse chat response: %w", err)
	}

	model := requestedModel
	if model == "" {
		model = chat.Model
	}
	created := chat.Created
	if created == 0 {
		created = time.Now().Unix()
	}

	choices := make([]map[string]any, 0, len(chat.Choices))
	for i, c := range chat.Choices {
		index := c.Index
		if i > 0 && index == 0 {
			index = i
		}
		choices = append(choices, map[string]any{
			"text":          c.Message.Content,
			"index":         index,
			"logprobs":      nil,
			"finish_reason": c.FinishReason,
		})
	}

	out := map[string]any{
		"id":      legacyCompletionID(chat.ID),
		"object":  "text_completion",
		"created": created,
		"model":   model,
		"choices": choices,
	}
	if len(chat.Usage) > 0 && string(chat.Usage) != "null" {
		out["usage"] = chat.Usage
	}
	return json.Marshal(out)
}

// legacyCompletionID turns "chatcmpl-…" into "cmpl-…": several legacy clients
// branch on the id prefix.
func legacyCompletionID(id string) string {
	if id == "" {
		return "cmpl-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return strings.Replace(id, "chatcmpl", "cmpl", 1)
}

// legacyCompletionChunk builds one legacy streaming chunk. usage is only set on
// the final chunk, and only when the upstream reported it.
func legacyCompletionChunk(id string, created int64, model, text, finishReason string, usage json.RawMessage) []byte {
	choice := map[string]any{
		"text":          text,
		"index":         0,
		"logprobs":      nil,
		"finish_reason": nil,
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	chunk := map[string]any{
		"id":      id,
		"object":  "text_completion",
		"created": created,
		"model":   model,
		"choices": []any{choice},
	}
	if len(usage) > 0 && string(usage) != "null" {
		chunk["usage"] = usage
	}
	b, _ := json.Marshal(chunk)
	return b
}

// ---- POST /v1/responses, POST /v1/embeddings ----

// handleResponses forwards the OpenAI Responses API to whichever upstream the
// model routes to. The gateway does not translate between the Responses and chat
// formats, so the upstream has to implement /responses itself.
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	s.forwardOpenAIEndpoint(w, r, "/responses")
}

// handleEmbeddings forwards embedding requests to whichever upstream the model
// routes to.
func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	s.forwardOpenAIEndpoint(w, r, "/embeddings")
}

// forwardOpenAIEndpoint reads an OpenAI-format request and relays it unchanged to
// the given upstream path, streaming the reply back when the client asked for it.
func (s *Server) forwardOpenAIEndpoint(w http.ResponseWriter, r *http.Request, path string) {
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
		writeErrorCode(w, http.StatusBadRequest, `field "model" is required`, "", "model")
		return
	}

	cfg, rt := s.snapshot()
	pnames, err := rt.ProviderNames(model)
	if err != nil {
		writeErrorCode(w, http.StatusNotFound, err.Error()+"; see GET /v1/models for the configured model names", "model_not_found", "model")
		return
	}

	forwardPath(r.Context(), w, model, body, isStream, path, pnames, cfg, rt,
		func(resp *http.Response, p config.Provider) {
			deliverPassthrough(w, resp, isStream)
		})
}

// deliverPassthrough relays an upstream response verbatim, keeping its status
// code and content type so clients see upstream errors as the upstream sent them.
func deliverPassthrough(w http.ResponseWriter, resp *http.Response, isStream bool) {
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)

	if isStream && strings.HasPrefix(contentType, "text/event-stream") {
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		w.WriteHeader(resp.StatusCode)
		if flusher != nil {
			flusher.Flush()
		}
		buf := make([]byte, 8192)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// ---- GET /v1/models/{id} ----

// handleModelByID answers a single-model lookup. The bare /v1/models/ path is
// treated as a list request because some clients append a trailing slash.
func (s *Server) handleModelByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/models/")
	if id == "" {
		s.handleModels(w, r)
		return
	}
	cfg, _ := s.snapshot()
	if _, ok := cfg.Routes[id]; !ok {
		writeErrorCode(w, http.StatusNotFound, fmt.Sprintf("model %q not found", id), "model_not_found", "model")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":       id,
		"object":   "model",
		"created":  time.Now().Unix(),
		"owned_by": "aiapiport",
	})
}

// ---- shared request loop ----

// forwardPath walks the route's providers and their keys in fallback order,
// sending body to the given upstream path, and hands the first usable response to
// deliver. Providers whose adapter cannot serve path are skipped instead of
// consuming a key attempt.
func forwardPath(ctx context.Context, w http.ResponseWriter, model string, body []byte, isStream bool, path string, pnames []string, cfg *config.Config, rt *router.Router, deliver func(*http.Response, config.Provider)) {
	var (
		lastErr  error
		attempts int
		skipped  []string
	)

	for _, pname := range pnames {
		p, ok := cfg.FindProvider(pname)
		if !ok {
			lastErr = fmt.Errorf("route %q -> missing provider %q", model, pname)
			continue
		}
		if !rt.SupportsPath(p, path) {
			skipped = append(skipped, p.Name)
			slog.Warn("provider skipped, endpoint not supported by its type", "provider", p.Name, "type", p.Type, "path", path)
			continue
		}
		for ki := range p.Keys {
			attempts++
			res, err := rt.AttemptProviderPath(ctx, p, path, body, model, isStream, ki)
			if err != nil {
				lastErr = err
				slog.Warn("upstream error, trying next key", "path", path, "model", model, "provider", p.Name, "key_index", ki, "err", err)
				continue
			}
			if res.Err != nil {
				lastErr = res.Err
				slog.Warn("upstream request failed, trying next key", "path", path, "model", model, "provider", p.Name, "key_index", ki, "err", res.Err)
				continue
			}
			resp := res.Resp
			if res.Retryable {
				detail := drainBody(resp)
				_ = resp.Body.Close()
				lastErr = fmt.Errorf("upstream %s key#%d returned status %d: %s", p.Name, ki, resp.StatusCode, detail)
				slog.Warn("upstream retryable status, trying next key", "path", path, "model", model, "provider", p.Name, "key_index", ki, "status", resp.StatusCode)
				continue
			}
			slog.Info("upstream response", "path", path, "model", model, "provider", p.Name, "key_index", ki, "status", resp.StatusCode)
			deliver(resp, p)
			return
		}
	}

	slog.Error("all upstreams exhausted", "path", path, "model", model, "err", lastErr)

	if attempts == 0 && len(skipped) > 0 {
		writeErrorCode(w, http.StatusNotImplemented,
			fmt.Sprintf("%s is not available for model %q: provider(s) %s cannot serve it; use /v1/chat/completions instead", path, model, strings.Join(skipped, ", ")),
			"endpoint_not_supported", "")
		return
	}
	msg := "all providers and keys exhausted"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	writeErrorCode(w, http.StatusBadGateway, msg, "upstream_error", "")
}

// ---- helpers ----

// sseData extracts the payload of one "data:" line. ok is false for every other
// SSE line (comments, event names, blank separators).
func sseData(line string) (payload string, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" {
		return "", false
	}
	return payload, true
}

// writeSSE writes one SSE event and flushes it.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, payload []byte) error {
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// chatSSEReader returns a reader of OpenAI chat-completion SSE events for resp,
// converting the stream first when the upstream speaks Anthropic.
func chatSSEReader(resp *http.Response, p config.Provider, requestedModel string) io.Reader {
	if p.Type != "anthropic" {
		return resp.Body
	}
	pr, pw := io.Pipe()
	go func() {
		defer resp.Body.Close()
		_ = pw.CloseWithError(provider.ConvertAnthropicSSEStream(resp.Body, pw, requestedModel, "chatcmpl-stream"))
	}()
	return pr
}

// writeUpstreamError relays an upstream failure: an already OpenAI-shaped body is
// passed through with its status, anything else is wrapped in the gateway's error
// format so the client still gets parseable JSON.
func writeUpstreamError(w http.ResponseWriter, status int, raw []byte) {
	if status < 400 {
		status = http.StatusBadGateway
	}
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &probe) == nil && len(probe.Error) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return
	}
	msg := strings.TrimSpace(string(raw))
	if msg == "" {
		msg = http.StatusText(status)
	}
	writeError(w, status, msg)
}
