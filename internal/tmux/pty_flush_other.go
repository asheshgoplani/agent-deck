//go:build !linux && !windows
// +build !linux,!windows

package tmux

import "golang.org/x/sys/unix"

// FlushInput on darwin/bsd uses TIOCFLUSH with the FREAD direction
// bit. Equivalent to Linux's TCIFLUSH on TCFLSH — drains pending input
// from the terminal fd after detach.
func FlushInput(fd int) error {
	// FREAD == 1; flushing the read queue matches TCIFLUSH semantics.
	return unix.IoctlSetInt(fd, unix.TIOCFLUSH, 1)
}
