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
//     Retryable == true, Resp may be nil
//   - If the result should be returned to the client as-is:
//     Retryable == false (Resp may still be an error status)
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
	return r.AttemptProvider(ctx, p, reqBody, model, isStream, keyIndex)
}

// AttemptProvider sends one request to a specific provider using the key at
// keyIndex, bypassing route lookup. Used by the admin panel for direct tests.
func (r *Router) AttemptProvider(ctx context.Context, p config.Provider, reqBody []byte, model string, isStream bool, keyIndex int) (Result, error) {
	return r.AttemptProviderPath(ctx, p, provider.ChatPath, reqBody, model, isStream, keyIndex)
}

// AttemptProviderPath is AttemptProvider for an arbitrary upstream path
// (e.g. "/embeddings"). The chat path works with every adapter; any other path
// requires an adapter that implements provider.PathAdapter.
func (r *Router) AttemptProviderPath(ctx context.Context, p config.Provider, path string, reqBody []byte, model string, isStream bool, keyIndex int) (Result, error) {
	if keyIndex >= len(p.Keys) {
		return Result{Retryable: false}, fmt.Errorf("keyIndex out of range")
	}
	adapter, ok := r.regs.Get(p.Type)
	if !ok {
		return Result{Retryable: false}, fmt.Errorf("no adapter for type %q (provider %q)", p.Type, p.Name)
	}

	var (
		resp *http.Response
		err  error
	)
	if path == provider.ChatPath {
		resp, err = adapter.Do(ctx, p.BaseURL, p.Keys[keyIndex], reqBody, model, isStream)
	} else {
		pa, ok := adapter.(provider.PathAdapter)
		if !ok {
			return Result{Retryable: false}, fmt.Errorf("provider %q (type %s) cannot serve %s", p.Name, p.Type, path)
		}
		resp, err = pa.DoPath(ctx, p.BaseURL, p.Keys[keyIndex], path, reqBody, model, isStream)
	}
	if err != nil {
		return Result{Retryable: true, Err: err}, nil
	}
	if retryableStatus(resp.StatusCode) {
		return Result{Resp: resp, Retryable: true}, nil
	}
	return Result{Resp: resp, Retryable: false}, nil
}

// SupportsPath reports whether the provider's adapter can serve path, so the
// gateway can skip e.g. a Claude-direct provider on /v1/embeddings instead of
// burning a key attempt on a request it cannot translate.
func (r *Router) SupportsPath(p config.Provider, path string) bool {
	if path == provider.ChatPath {
		return true
	}
	a, ok := r.regs.Get(p.Type)
	if !ok {
		return false
	}
	_, ok = a.(provider.PathAdapter)
	return ok
}

// retryableStatus reports the upstream statuses that trigger key/provider
// fallback: exhausted quota, rate limits, auth failures and server errors.
func retryableStatus(code int) bool {
	return code == 402 || code == 429 || code >= 500 || code == 401 || code == 403
}

// ProviderNames returns the ordered list of provider names for model.
func (r *Router) ProviderNames(model string) ([]string, error) {
	pnames, ok := r.cfg.Routes[model]
	if !ok {
		return nil, fmt.Errorf("no route for model %q", model)
	}
	return pnames, nil
}
