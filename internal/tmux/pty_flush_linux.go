//go:build linux
// +build linux

package tmux

import "golang.org/x/sys/unix"

// FlushInput drains pending input from the terminal fd so that any
// bytes typed after the detach key (e.g. Ctrl+Q) do not bleed into the
// TUI's stdin after the attach returns. Linux implementation uses
// TCFLSH; darwin/bsd live in pty_flush_other.go.
func FlushInput(fd int) error {
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}
