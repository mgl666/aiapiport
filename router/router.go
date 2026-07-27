package router

import (
	"context"
	"fmt"
	"net/http"

	"aiapiport/config"
	"aiapiport/provider"
)

// Router holds the config and adapter registry, routing each request to the
// correct provider and attempting keys in primary/fallback order.
// Supports multi-provider fallback: each model can map to multiple providers,
// tried in order (provider-level fallback after key-level fallback exhausted).
type Router struct {
	cfg  *config.Config
	regs *provider.Registry
}

func New(cfg *config.Config, regs *provider.Registry) *Router {
	return &Router{cfg: cfg, regs: regs}
}

// Result is the outcome of one routing attempt.
//   - If fallback should be attempted:
//       Retryable == true, Resp may be nil
//   - If the result should be returned to the client as-is:
//       Retryable == false (Resp may still be an error status)
type Result struct {
	Resp      *http.Response
	Retryable bool  // true means the handler should try the next key
	Err       error // non-nil means a network-level error (no response received)
}

// Attempt sends one request using the key at keyIndex within the provider at
// providerIndex for model. reqBody is the raw OpenAI-format request; isStream
// indicates stream:true.
//
// The caller loops over provider indices and key indices; Attempt never loops
// internally, giving the handler control over the "no fallback after SSE flush
// has started" invariant.
func (r *Router) Attempt(ctx context.Context, model string, reqBody []byte, isStream bool, providerIndex int, keyIndex int) (Result, error) {
	pnames, ok := r.cfg.Routes[model]
	if !ok {
		return Result{Retryable: false}, fmt.Errorf("no route for model %q", model)
	}
	if providerIndex >= len(pnames) {
		return Result{Retryable: false}, fmt.Errorf("providerIndex %d out of range (max %d)", providerIndex, len(pnames)-1)
	}
	pname := pnames[providerIndex]
	p, ok := r.cfg.FindProvider(pname)
	if !ok {
		return Result{Retryable: false}, fmt.Errorf("route %q -> missing provider %q", model, pname)
	}
	if keyIndex >= len(p.Keys) {
		return Result{Retryable: false}, fmt.Errorf("keyIndex out of range")
	}
	adapter, ok := r.regs.Get(p.Type)
	if !ok {
		return Result{Retryable: false}, fmt.Errorf("no adapter for type %q (provider %q)", p.Type, p.Name)
	}
	resp, err := adapter.Do(ctx, p.BaseURL, p.Keys[keyIndex], reqBody, model, isStream)
	if err != nil {
		return Result{Retryable: true, Err: err}, nil
	}
	if resp.StatusCode == 402 || resp.StatusCode == 429 || resp.StatusCode >= 500 || resp.StatusCode == 401 || resp.StatusCode == 403 {
		return Result{Resp: resp, Retryable: true}, nil
	}
	return Result{Resp: resp, Retryable: false}, nil
}

// ProviderNames returns the ordered list of provider names for model.
func (r *Router) ProviderNames(model string) ([]string, error) {
	pnames, ok := r.cfg.Routes[model]
	if !ok {
		return nil, fmt.Errorf("no route for model %q", model)
	}
	return pnames, nil
}