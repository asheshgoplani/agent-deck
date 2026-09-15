//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// The benchmark uses a separate worker and PTY session. Ensure those direct
// children cannot survive their supervisor disappearing during cancellation.
func bindChildLifetime(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
