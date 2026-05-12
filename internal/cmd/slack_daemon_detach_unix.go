//go:build !windows

package cmd

import (
	"os/exec"
	"syscall"
)

// detachFromTerminal puts the child process in its own session so the parent
// can exit without taking the daemon with it (and so signals delivered to the
// parent's pgrp don't reach the daemon).
func detachFromTerminal(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
