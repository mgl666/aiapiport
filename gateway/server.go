package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"aiapiport/config"
	"aiapiport/provider"
	"aiapiport/router"
)

// Server assembles the config, router, and exposes an http.Handler.
type Server struct {
	mu     sync.RWMutex
	cfg    *config.Config
	router *router.Router
	regs   *provider.Registry
}

func New(cfg *config.Config) *Server {
	regs := provider.NewDefaultRegistry()
	return &Server{
		cfg:    cfg,
		router: router.New(cfg, regs),
		regs:   regs,
	}
}

// Reload replaces the request configuration atomically. Existing requests keep
// their original routing snapshot; new requests use the new configuration.
// server.listen is intentionally not reloaded because the listening socket is
// created only once at startup.
func (s *Server) Reload(cfg *config.Config) (listenChanged bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	listenChanged = cfg.Server.Listen != s.cfg.Server.Listen
	if listenChanged {
		cfg.Server.Listen = s.cfg.Server.Listen
	}
	s.cfg = cfg
	s.router = router.New(cfg, s.regs)
	return listenChanged
}

func (s *Server) snapshot() (*config.Config, *router.Router) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg, s.router
}

// TestResult summarizes one admin-panel "test" run.
type TestResult struct {
	OK       bool   `json:"ok"`
	Provider string `json:"provider,omitempty"`
	KeyIndex int    `json:"key_index,omitempty"`
	Status   int    `json:"status,omitempty"`
	Attempts int    `json:"attempts"`
	Reply    string `json:"reply,omitempty"`
	Error    string `json:"error,omitempty"`
}

// TestChat sends a tiny non-streaming chat request through the live routing for
// model (or directly to providerName when non-empty) and reports the outcome.
// Used by the admin panel. Up to 3 upstream attempts to avoid burning quota on
// an intentionally triggered test.
func (s *Server) TestChat(ctx context.Context, model, providerName string) TestResult {
	body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":8,"stream":false}`, model))
	cfg, rt := s.snapshot()

	var providers []config.Provider
	if providerName != "" {
		p, ok := cfg.FindProvider(providerName)
		if !ok {
			return TestResult{Error: fmt.Sprintf("provider %q not found", providerName)}
		}
		providers = []config.Provider{p}
	} else {
		pnames, err := rt.ProviderNames(model)
		if err != nil {
			return TestResult{Error: err.Error()}
		}
		for _, pname := range pnames {
			if p, ok := cfg.FindProvider(pname); ok {
				providers = append(providers, p)
			}
		}
	}

	const maxAttempts = 3
	attempts := 0
	var lastErr error
	for _, p := range providers {
		for ki := range p.Keys {
			if attempts >= maxAttempts {
				break
			}
			attempts++
			res, err := rt.AttemptProvider(ctx, p, body, model, false, ki)
			if err != nil {
				lastErr = err
				continue
			}
			if res.Err != nil {
				lastErr = res.Err
				continue
			}
			snippet, _ := io.ReadAll(io.LimitReader(res.Resp.Body, 512))
			_ = res.Resp.Body.Close()
			detail := strings.TrimSpace(string(snippet))
			if res.Retryable {
				lastErr = fmt.Errorf("%s returned HTTP %d: %s", p.Name, res.Resp.StatusCode, detail)
				continue
			}
			if res.Resp.StatusCode >= 200 && res.Resp.StatusCode < 300 {
				return TestResult{OK: true, Provider: p.Name, KeyIndex: ki, Status: res.Resp.StatusCode, Attempts: attempts, Reply: detail}
			}
			lastErr = fmt.Errorf("%s returned HTTP %d: %s", p.Name, res.Resp.StatusCode, detail)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all attempts failed")
	}
	return TestResult{Attempts: attempts, Error: lastErr.Error()}
}

// Handler returns the HTTP handler for the gateway.
//
// The endpoint set is intentionally wider than chat completions because clients
// differ in which OpenAI endpoint they call: modern SDKs use
// /v1/chat/completions, older "text completion" front-ends use /v1/completions,
// newer tools use /v1/responses, and RAG front-ends use /v1/embeddings.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/chat/completions", s.auth(http.MethodPost, s.handleChat))
	mux.HandleFunc("/v1/completions", s.auth(http.MethodPost, s.handleCompletions))
	mux.HandleFunc("/v1/responses", s.auth(http.MethodPost, s.handleResponses))
	mux.HandleFunc("/v1/embeddings", s.auth(http.MethodPost, s.handleEmbeddings))
	mux.HandleFunc("/v1/models", s.auth(http.MethodGet, s.handleModels))
	mux.HandleFunc("/v1/models/", s.auth(http.MethodGet, s.handleModelByID))
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/", handleUnknownEndpoint)

	return s.logMiddleware(withCORS(mux))
}

// handleHealth stays unauthenticated so monitors and the deploy scripts can
// probe it without a key.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		writeErrorCode(w, http.StatusMethodNotAllowed, "only GET is allowed on /health", "method_not_allowed", "")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleUnknownEndpoint answers every unmatched path with an OpenAI-style JSON
// error: a bare "404 page not found" makes most clients report a useless
// "unknown error".
func handleUnknownEndpoint(w http.ResponseWriter, r *http.Request) {
	writeErrorCode(w, http.StatusNotFound,
		fmt.Sprintf("unknown endpoint %s %s — available: POST /v1/chat/completions, POST /v1/completions, POST /v1/responses, POST /v1/embeddings, GET /v1/models, GET /health", r.Method, r.URL.Path),
		"unknown_endpoint", "")
}

// withCORS answers cross-origin preflight requests and tags every response with
// Access-Control-Allow-* headers. Browser-based chat front-ends (NextChat,
// LobeChat, Open WebUI…) call the gateway from another origin, so without this
// the preflight gets a 405 and the browser blocks the request even though the
// key is correct.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if origin := r.Header.Get("Origin"); origin != "" {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
		} else {
			h.Set("Access-Control-Allow-Origin", "*")
		}
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		allowed := r.Header.Get("Access-Control-Request-Headers")
		if allowed == "" {
			allowed = "Authorization, Content-Type, x-api-key, api-key, anthropic-version, anthropic-beta, x-stainless-os, x-stainless-lang"
		}
		h.Set("Access-Control-Allow-Headers", allowed)
		h.Set("Access-Control-Expose-Headers", "Content-Type, x-request-id")
		h.Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth validates the gateway's fixed auth_key on every request. The method is
// checked first so a wrong-verb request gets a 405 instead of a misleading 401.
func (s *Server) auth(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if method != "" && r.Method != method && !(method == http.MethodGet && r.Method == http.MethodHead) {
			w.Header().Set("Allow", method)
			writeErrorCode(w, http.StatusMethodNotAllowed,
				fmt.Sprintf("%s is not allowed on %s — use %s", r.Method, r.URL.Path, method),
				"method_not_allowed", "")
			return
		}
		cfg, _ := s.snapshot()
		key := clientKey(r)
		if key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(cfg.Server.AuthKey)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="aiapiport"`)
			writeErrorCode(w, http.StatusUnauthorized,
				"invalid api key — send it as 'Authorization: Bearer <key>', 'x-api-key: <key>' or '?key=<key>'",
				"invalid_api_key", "")
			return
		}
		next(w, r)
	}
}

// clientKey extracts the gateway key from whichever header or query parameter the
// client uses. Clients are not consistent: OpenAI SDKs send
// "Authorization: Bearer …", Anthropic-style tools send x-api-key, and several
// proxies put the key in ?key=.
func clientKey(r *http.Request) string {
	if h := strings.TrimSpace(r.Header.Get("Authorization")); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			return strings.TrimSpace(h[7:])
		}
		return h
	}
	for _, name := range []string{"x-api-key", "api-key"} {
		if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
			return v
		}
	}
	q := r.URL.Query()
	for _, name := range []string{"key", "api_key", "apikey"} {
		if v := strings.TrimSpace(q.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// handleModels returns the list of models defined in routes, in OpenAI format.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.snapshot()
	models := make([]string, 0, len(cfg.Routes))
	for m := range cfg.Routes {
		models = append(models, m)
	}
	sort.Strings(models)

	now := time.Now().Unix()
	data := make([]map[string]any, len(models))
	for i, m := range models {
		data[i] = map[string]any{
			"id":       m,
			"object":   "model",
			"created":  now,
			"owned_by": "aiapiport",
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", status, "bytes", recorder.bytes, "dur", time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *statusRecorder) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
