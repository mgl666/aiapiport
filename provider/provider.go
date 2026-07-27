package provider

import (
	"context"
	"net/http"
)

// Adapter forwards an OpenAI-format request to an upstream API.
// The caller decides whether to pass through, convert, or retry based on the returned *http.Response.
//
// The Stream field tells the adapter whether this is a streaming request so it
// can construct the correct upstream request body/headers; responses are always
// returned as *http.Response regardless.
type Adapter interface {
	// Do sends one upstream request. model is the model name from the request
	// (passed through as-is). req is the raw OpenAI-format request body; the
	// adapter decides how to transform it. isStream indicates stream:true.
	Do(ctx context.Context, baseURL, key string, req []byte, model string, isStream bool) (*http.Response, error)
}

// Registry maps type names to adapters.
type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

func (r *Registry) Register(typeName string, a Adapter) {
	r.adapters[typeName] = a
}

func (r *Registry) Get(typeName string) (Adapter, bool) {
	a, ok := r.adapters[typeName]
	return a, ok
}