//go:build linux
// +build linux

package tmux

import "golang.org/x/sys/unix"

// getTermiosReq is the ioctl that reads a tty's termios on Linux.
const getTermiosReq = unix.TCGETS
