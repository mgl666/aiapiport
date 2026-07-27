//go:build !windows

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func cmdStart(args []string) {
	fs := newFlagSet("start")
	cfgPath := fs.String("config", "config.yaml", "config file")
	port := fs.String("port", "", "override listen port")
	_ = fs.Parse(args)

	if readPID() > 0 {
		fmt.Fprintln(os.Stderr, "aiapiport already running (use 'aiapiport status')")
		os.Exit(1)
	}
	ensureRunDir()

	self, err := os.Executable()
	if err != nil {
		fatal("cannot find self: %v", err)
	}
	selfArgs := []string{"serve", "-config", *cfgPath}
	if *port != "" {
		selfArgs = append(selfArgs, "-port", *port)
	}

	logF, err := os.OpenFile(logFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fatal("open log file: %v", err)
	}

	cmd := exec.Command(self, selfArgs...)
	cmd.Stdout = logF
	cmd.Stderr = logF
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		fatal("start daemon: %v", err)
	}
	_ = logF.Close()

	if err := os.WriteFile(pidFile(), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		fatal("write pid: %v", err)
	}

	for i := 0; i < 25; i++ {
		time.Sleep(200 * time.Millisecond)
		if logsContain("listening") {
			fmt.Printf("aiapiport started (pid %d)  →  logs: %s\n", cmd.Process.Pid, logFile())
			return
		}
		if readPID() == 0 {
			fmt.Fprintln(os.Stderr, "aiapiport exited immediately — check logs:")
			cmdLogs([]string{"-n", "20"})
			os.Exit(1)
		}
	}
	fmt.Printf("aiapiport starting... (pid %d)  →  logs: %s\n", cmd.Process.Pid, logFile())
}

func cmdStop() {
	pid := readPID()
	if pid == 0 {
		fmt.Println("aiapiport is not running")
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil || proc.Signal(syscall.Signal(0)) != nil {
		fmt.Println("aiapiport is not running (stale pid file)")
		_ = os.Remove(pidFile())
		return
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fatal("send SIGTERM: %v", err)
	}
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if proc.Signal(syscall.Signal(0)) != nil {
			_ = os.Remove(pidFile())
			fmt.Println("aiapiport stopped")
			return
		}
	}
	_ = proc.Signal(syscall.SIGKILL)
	_ = os.Remove(pidFile())
	fmt.Println("aiapiport killed (SIGKILL)")
}

func cmdStatus() {
	pid := readPID()
	if pid == 0 {
		fmt.Println("stopped")
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil || proc.Signal(syscall.Signal(0)) != nil {
		fmt.Printf("stopped (stale pid %d)\n", pid)
		_ = os.Remove(pidFile())
		return
	}
	fmt.Printf("running  pid=%d  log=%s\n", pid, logFile())
}

func readPID() int {
	data, err := os.ReadFile(pidFile())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0
	}
	if proc.Signal(syscall.Signal(0)) != nil {
		return 0
	}
	return pid
}

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

func pidFile() string  { return filepath.Join(runDir(), "aiapiport.pid") }
func logFile() string  { return filepath.Join(runDir(), "aiapiport.log") }

func ensureRunDir() {
	if err := os.MkdirAll(runDir(), 0o755); err != nil {
		fatal("cannot create run dir: %v", err)
	}
}
