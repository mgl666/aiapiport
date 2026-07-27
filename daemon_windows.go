//go:build windows

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func setSysProcAttr(cmd *exec.Cmd) {}

func cmdStart(_ []string) {
	fmt.Fprintln(os.Stderr, "start/stop/status are not supported on Windows — use: aiapiport serve -config config.yaml")
	os.Exit(1)
}

func cmdStop() {
	fmt.Fprintln(os.Stderr, "start/stop/status are not supported on Windows — use: aiapiport serve -config config.yaml")
	os.Exit(1)
}

func cmdStatus() {
	fmt.Fprintln(os.Stderr, "start/stop/status are not supported on Windows — use: aiapiport serve -config config.yaml")
	os.Exit(1)
}

func readPID() int { return 0 }

func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ExitOnError)
}

func runDir() string {
	dir := os.Getenv("AIAPIPORT_RUN_DIR")
	if dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aiapiport")
}

func pidFile() string { return filepath.Join(runDir(), "aiapiport.pid") }
func logFile() string { return filepath.Join(runDir(), "aiapiport.log") }

func ensureRunDir() {
	if err := os.MkdirAll(runDir(), 0o755); err != nil {
		fatal("cannot create run dir: %v", err)
	}
}
