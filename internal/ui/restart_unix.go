//go:build !windows

package ui

import "syscall"

// execSelf replaces the running process image. On success it never returns.
func execSelf(exe string, args, env []string) error {
	return syscall.Exec(exe, args, env)
}
