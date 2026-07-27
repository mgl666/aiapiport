package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"aiapiport/config"
	"aiapiport/provider"
	"aiapiport/router"
)

// Server assembles the config, router, and exposes an http.Handler.
type Server struct {
	cfg    *config.Config
	router *router.Router
}

func New(cfg *config.Config, regs *provider.Registry) *Server {
	nonStream := &http.Client{Timeout: 120 * time.Second}
	stream := &http.Client{Timeout: 0} // streaming timeout is controlled by request context

	regs = provider.NewRegistry()
	regs.Register("openai", &provider.OpenAIAdapter{NonStreamClient: nonStream, StreamClient: stream})
	regs.Register("anthropic", &provider.AnthropicAdapter{NonStreamClient: nonStream, StreamClient: stream})

	return &Server{
		cfg:    cfg,
		router: router.New(cfg, regs),
	}
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
		key := bearerToken(r)
		if key == "" || key != s.cfg.Server.AuthKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleModels returns the list of models defined in routes, in OpenAI format.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	models := make([]string, 0, len(s.cfg.Routes))
	for m := range s.cfg.Routes {
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
		next.ServeHTTP(w, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
	})
}