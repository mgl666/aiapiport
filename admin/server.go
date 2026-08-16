// Package admin implements the optional web admin panel. It reads and writes
// the same config file the gateway hot-reloads, so changes saved through the
// panel take effect within about one second, without a restart.
package admin

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"aiapiport/config"
	"aiapiport/gateway"
)

//go:embed ui/index.html
var uiFS embed.FS

// Server is the web admin panel.
type Server struct {
	mu      sync.RWMutex
	cfg     *config.Config
	cfgPath string
	gw      *gateway.Server
}

// New creates an admin panel bound to the given config file. gw is the running
// gateway, used by the live connection tests.
func New(cfg *config.Config, cfgPath string, gw *gateway.Server) *Server {
	return &Server{cfg: cfg, cfgPath: cfgPath, gw: gw}
}

// refresh reloads the config from disk so the panel always edits the live file.
// A failed reload keeps the last known-good config (same policy as the gateway).
func (s *Server) refresh() {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		slog.Warn("admin: config reload failed, keeping last known", "path", s.cfgPath, "err", err)
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

func (s *Server) current() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Handler returns the HTTP handler for the admin panel.
func (s *Server) Handler() http.Handler {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("GET /api/health", s.apiHealth)
	mux.HandleFunc("GET /api/config", s.auth(s.apiGetConfig))
	mux.HandleFunc("PUT /api/config", s.auth(s.apiPutConfig))
	mux.HandleFunc("POST /api/test", s.auth(s.apiTest))
	return mux
}

// auth requires X-Admin-Key to match the gateway's server.auth_key.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Admin-Key")
		if key == "" || key != s.current().Server.AuthKey {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (s *Server) apiHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiGetConfig returns the raw YAML plus the parsed config.
func (s *Server) apiGetConfig(w http.ResponseWriter, r *http.Request) {
	s.refresh()
	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "read config: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"raw":    string(raw),
		"config": s.current(),
		"path":   s.cfgPath,
	})
}

// apiPutConfig accepts either {"raw": "<yaml text>"} or a structured
// {"config": {server, providers, routes}} payload. It validates, then writes
// atomically; the gateway's hot-reload picks the change up within ~1s.
func (s *Server) apiPutConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}
	var req struct {
		Raw    string         `json:"raw"`
		Config *config.Config `json:"config"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body: " + err.Error()})
		return
	}

	var cfg *config.Config
	switch {
	case req.Raw != "":
		cfg, err = config.Parse([]byte(req.Raw))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	case req.Config != nil:
		cfg = req.Config
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": `provide either "raw" (YAML text) or "config" (structured JSON)`})
		return
	}
	if err := cfg.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if err := cfg.Save(s.cfgPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.refresh()
	slog.Info("admin: config saved", "path", s.cfgPath, "providers", len(cfg.Providers), "routes", len(cfg.Routes))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已保存，网关将在 1 秒内热重载生效", "path": s.cfgPath})
}

// apiTest runs a live test through the gateway. Body: {"model": "...", "provider": "..."(optional)}.
func (s *Server) apiTest(w http.ResponseWriter, r *http.Request) {
	s.refresh()
	var req struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.gw.TestChat(ctx, req.Model, req.Provider))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
