package provider

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ChatPath is the upstream path for chat completions. Every adapter supports it.
const ChatPath = "/chat/completions"

// OpenAIAdapter handles OpenAI-compatible upstreams (OpenAI, DeepSeek, Moonshot,
// SiliconFlow, local vLLM/OneAPI, etc.). Forwards the request body as-is, only
// replacing the Authorization header.
type OpenAIAdapter struct {
	NonStreamClient *http.Client // for non-streaming requests
	StreamClient    *http.Client // for streaming (timeout via request context)
}

// Do implements Adapter for the chat completions path.
func (a *OpenAIAdapter) Do(ctx context.Context, baseURL, key string, req []byte, model string, isStream bool) (*http.Response, error) {
	return a.DoPath(ctx, baseURL, key, ChatPath, req, model, isStream)
}

// DoPath sends req to baseURL+path. It lets the gateway expose endpoints other
// than chat completions (/-completions, /embeddings, /responses) by reusing the
// plain OpenAI passthrough instead of an adapter per endpoint.
func (a *OpenAIAdapter) DoPath(ctx context.Context, baseURL, key, path string, req []byte, model string, isStream bool) (*http.Response, error) {
	url := strings.TrimSuffix(baseURL, "/") + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	if isStream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	c := a.NonStreamClient
	if isStream {
		c = a.StreamClient
	}
	resp, err := c.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: do: %w", err)
	}
	return resp, nil
}
