//go:build !windows

package ui

import (
	"time"

	"golang.org/x/sys/unix"
)

// pollFdReady reports whether the file descriptor has input data available
// within the given timeout. Used by csiuReader to detect whether a lone ESC
// byte is the start of an escape sequence (more bytes imminent) or a
// standalone Escape keypress (no bytes follow).
func pollFdReady(fd int, timeout time.Duration) bool {
	ms := int(timeout.Milliseconds())
	if ms < 1 {
		ms = 1
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, ms)
		if err == unix.EINTR {
			continue // retry after signal interruption
		}
		return err == nil && n > 0
	}
}
