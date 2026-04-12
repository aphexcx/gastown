package slack

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// PIDFilePath returns ~/gt/.runtime/slack.pid. Overridable in tests via the
// HOME env var.
func PIDFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, "gt", ".runtime", "slack.pid"), nil
}

// WritePIDFile writes the current process's PID to path. Creates the parent
// directory if missing. Mode 0600.
func WritePIDFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir pid dir: %w", err)
	}
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(path, []byte(pid+"\n"), 0o600); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	return nil
}

// ReadPIDFile reads and parses an existing PID file.
func ReadPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
	}
	return pid, nil
}

// RemovePIDFile deletes the PID file. Missing file is not an error.
func RemovePIDFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsProcessAlive returns true if pid refers to a live process the current
// user can signal. Uses signal 0 (test-only) — no effect on the target.
func IsProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
