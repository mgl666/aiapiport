package gateway

import (
	"context"
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
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.auth(s.handleChat))
	mux.HandleFunc("GET /v1/models", s.auth(s.handleModels))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return s.logMiddleware(mux)
}

// auth validates the gateway's fixed auth_key on every request.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, _ := s.snapshot()
		key := bearerToken(r)
		if key == "" || key != cfg.Server.AuthKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
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

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const pfx = "Bearer "
	if strings.HasPrefix(h, pfx) {
		return strings.TrimSpace(h[len(pfx):])
	}
	return ""
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
