package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"aiapiport/admin"
	"aiapiport/config"
	"aiapiport/gateway"
)

const version = "0.1.2"

func usage() {
	fmt.Fprintf(os.Stderr, `aiapiport - lightweight LLM gateway v%s

Usage:
  aiapiport start   [-config FILE] [-port PORT]  # start in background
  aiapiport stop                                  # stop background process
  aiapiport status                                # check status
  aiapiport logs    [-n N] [-f]                   # view/follow logs
  aiapiport serve   [-config FILE] [-port PORT]   # run in foreground (no daemonize)

Flags:
  -config FILE   config file path (default config.yaml)
  -port   PORT   override listen port from config.yaml
  -n      N      show last N log lines (default 50)
  -f             follow log (tail -f)

`, version)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "start":
		cmdStart(args)
	case "stop":
		cmdStop()
	case "status":
		cmdStatus()
	case "logs":
		cmdLogs(args)
	case "serve":
		cmdServe(args)
	case "version", "-v", "--version":
		fmt.Println("aiapiport", version)
	default:
		usage()
	}
}

// ---- logs ----

func cmdLogs(args []string) {
	fs := newFlagSet("logs")
	n := fs.Int("n", 50, "last N lines")
	follow := fs.Bool("f", false, "follow")
	_ = fs.Parse(args)

	lf := logFile()
	if _, err := os.Stat(lf); err != nil {
		fmt.Println("no log file yet")
		return
	}
	tailArgs := []string{"-n", strconv.Itoa(*n)}
	if *follow {
		tailArgs = append(tailArgs, "-f")
	}
	tailArgs = append(tailArgs, lf)
	cmd := exec.Command("tail", tailArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// ---- serve (foreground) ----

func cmdServe(args []string) {
	fs := newFlagSet("serve")
	cfgPath := fs.String("config", "config.yaml", "config file")
	port := fs.String("port", "", "override listen port")
	_ = fs.Parse(args)

	// When running as a daemon (stdout is not a terminal), wrap stderr with a
	// rotating writer so the log file never exceeds 20 MB.
	logOut := os.Stderr
	var rw *rotatingWriter
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		if rw, err = newRotatingWriter(logFile()); err == nil {
			logOut = os.NewFile(0, "") // placeholder — slog uses the writer directly
			_ = logOut
			slog.SetDefault(slog.New(slog.NewTextHandler(rw, &slog.HandlerOptions{Level: slog.LevelInfo})))
		}
	}
	if rw == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal("load config: %v", err)
	}
	if *port != "" {
		cfg.Server.Listen = ":" + strings.TrimPrefix(*port, ":")
	}

	srv := gateway.New(cfg)
	go watchConfig(*cfgPath, srv)

	// Optional web admin panel on its own port (config: admin.listen).
	if cfg.Admin != nil && cfg.Admin.Listen != "" {
		adm := admin.New(cfg, *cfgPath, srv)
		admServer := &http.Server{Addr: cfg.Admin.Listen, Handler: adm.Handler()}
		go func() {
			slog.Info("admin panel listening", "addr", cfg.Admin.Listen)
			if err := admServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("admin panel server", "err", err)
			}
		}()
	}

	httpServer := &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: srv.Handler(),
	}

	slog.Info("listening", "addr", cfg.Server.Listen)
	if err := httpServer.ListenAndServe(); err != nil {
		fatal("server: %v", err)
	}
}

// watchConfig polls the config file so deployments can update routes and keys
// without restarting the gateway. A failed reload always leaves the last valid
// configuration active.
func watchConfig(path string, srv *gateway.Server) {
	const interval = time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastMod time.Time
	var lastSize int64 = -1
	for range ticker.C {
		info, err := os.Stat(path)
		if err != nil {
			slog.Warn("config reload check failed", "path", path, "err", err)
			continue
		}
		if info.ModTime().Equal(lastMod) && info.Size() == lastSize {
			continue
		}
		lastMod, lastSize = info.ModTime(), info.Size()

		cfg, err := config.Load(path)
		if err != nil {
			slog.Error("config reload rejected; keeping last valid configuration", "path", path, "err", err)
			continue
		}
		listenChanged := srv.Reload(cfg)
		slog.Info("config reloaded", "path", path, "providers", len(cfg.Providers), "routes", len(cfg.Routes), "listen_change_ignored", listenChanged)
	}
}

// ---- helpers ----

func logsContain(substr string) bool {
	data, err := os.ReadFile(logFile())
	if err != nil {
		return false
	}
	return strings.Contains(string(data), substr)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

// keep compiler happy — time and exec are used by platform files via same package
var _ = time.Sleep
