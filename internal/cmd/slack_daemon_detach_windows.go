//go:build windows

package cmd

import "os/exec"

// detachFromTerminal is a no-op on Windows. The daemon lifecycle (start/stop/
// status) is Unix-oriented (POSIX signals via syscall.SIGTERM); the Windows
// build exists so the package compiles, not so the daemon actually
// daemonizes there.
func detachFromTerminal(c *exec.Cmd) {}
