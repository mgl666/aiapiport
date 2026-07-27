package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AnthropicAdapter handles the Claude API. Performs bidirectional OpenAI <-> Anthropic conversion.
//
// Anthropic /v1/messages request body shape:
//
//	{ "model": "...", "max_tokens": N, "system": "..." (optional),
//	  "messages": [{"role":"user"/"assistant","content":"..."|[{...}]}],
//	  "temperature": ..., "stream": bool }
//
// Key differences from OpenAI:
//   - system is a top-level field, not a message
//   - max_tokens is required
//   - tool_choice/tools format differs (tool conversion not implemented; pass-through only)
//
// Response conversion is in anthropic_response.go.
type AnthropicAdapter struct {
	NonStreamClient *http.Client
	StreamClient    *http.Client
}

func (a *AnthropicAdapter) Do(ctx context.Context, baseURL, key string, req []byte, model string, isStream bool) (*http.Response, error) {
	body, err := openaiToAnthropicRequest(req, model, isStream)
	if err != nil {
		return nil, fmt.Errorf("anthropic: convert request: %w", err)
	}
	url := baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if isStream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	c := a.NonStreamClient
	if isStream {
		c = a.StreamClient
	}
	resp, err := c.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: do: %w", err)
	}
	return resp, nil
}

// openaiToAnthropicRequest converts an OpenAI chat/completions request body to
// an Anthropic /v1/messages request body.
func openaiToAnthropicRequest(oaiBody []byte, model string, isStream bool) ([]byte, error) {
	var oai struct {
		Model       string          `json:"model"`
		Messages    []oaiMessage    `json:"messages"`
		MaxTokens   int             `json:"max_tokens"`
		Temperature *float64        `json:"temperature"`
		TopP        *float64        `json:"top_p"`
		Stream      bool            `json:"stream"`
		Stop        json.RawMessage `json:"stop"`
	}
	if err := json.Unmarshal(oaiBody, &oai); err != nil {
		return nil, fmt.Errorf("parse openai request: %w", err)
	}

	// Collect all system messages into the top-level system field (Anthropic
	// accepts only one). OpenAI allows system messages mid-conversation; we
	// concatenate all of them in order and hoist them to the top.
	var systemParts []string
	var restMsgs []oaiMessage
	for _, m := range oai.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.TextContent())
		} else {
			restMsgs = append(restMsgs, m)
		}
	}

	// Anthropic requires messages to be non-empty; pass roles through as-is.
	anthMsgs := make([]anthMessage, 0, len(restMsgs))
	for _, m := range restMsgs {
		anthMsgs = append(anthMsgs, m.ToAnthropic())
	}
	if len(anthMsgs) == 0 {
		// If all messages were system, add a placeholder user message to avoid 400.
		anthMsgs = append(anthMsgs, anthMessage{Role: "user", Content: "."})
	}

	maxTokens := oai.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024 // Anthropic requires this field; use a conservative default
	}

	out := map[string]any{
		"model":      oaiModelName(model),
		"max_tokens": maxTokens,
		"messages":   anthMsgs,
	}
	if len(systemParts) > 0 {
		system := ""
		for i, s := range systemParts {
			if i > 0 {
				system += "\n\n"
			}
			system += s
		}
		out["system"] = system
	}
	if oai.Temperature != nil {
		out["temperature"] = *oai.Temperature
	}
	if oai.TopP != nil {
		out["top_p"] = *oai.TopP
	}
	if isStream {
		out["stream"] = true
	}
	// stop -> stop_sequences
	if len(oai.Stop) > 0 && string(oai.Stop) != "null" {
		var ss []string
		if err := json.Unmarshal(oai.Stop, &ss); err == nil && len(ss) > 0 {
			out["stop_sequences"] = ss
		} else {
			var single string
			if err := json.Unmarshal(oai.Stop, &single); err == nil && single != "" {
				out["stop_sequences"] = []string{single}
			}
		}
	}

	return json.Marshal(out)
}

// oaiModelName passes the model name through as-is. If the user configures
// the real Anthropic model ID (e.g. claude-sonnet-4-5-20250929) in routes,
// no transformation is needed.
func oaiModelName(m string) string { return m }

// ---- common message types ----

type oaiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// TextContent returns the message content as a plain string. Handles both
// string content and OpenAI multi-part content arrays; non-text parts are dropped.
func (m oaiMessage) TextContent() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		var out string
		for _, p := range parts {
			if p.Type == "text" || p.Type == "input_text" {
				out += p.Text
			}
		}
		return out
	}
	return ""
}

// ToAnthropic converts an OpenAI message to an Anthropic message using plain
// string content (sufficient for text-only use cases).
func (m oaiMessage) ToAnthropic() anthMessage {
	return anthMessage{Role: m.Role, Content: m.TextContent()}
}

type anthMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
