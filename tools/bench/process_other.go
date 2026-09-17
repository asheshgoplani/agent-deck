//go:build !linux

package main

import "os/exec"

// Other platforms still use explicit deferred child reaping and private tmux
// teardown. The supported automated execution environment is Linux Docker.
func bindChildLifetime(c *exec.Cmd) {}
