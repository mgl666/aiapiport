package provider

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
)

// OpenAIAdapter handles OpenAI-compatible upstreams (OpenAI, DeepSeek, Moonshot,
// SiliconFlow, local vLLM/OneAPI, etc.). Forwards the request body as-is, only
// replacing the Authorization header.
type OpenAIAdapter struct {
	NonStreamClient *http.Client // for non-streaming requests
	StreamClient    *http.Client // for streaming (timeout via request context)
}

func (a *OpenAIAdapter) Do(ctx context.Context, baseURL, key string, req []byte, model string, isStream bool) (*http.Response, error) {
	url := baseURL + "/chat/completions"
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
