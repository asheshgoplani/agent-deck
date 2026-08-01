//go:build !linux && !windows
// +build !linux,!windows

package tmux

import "golang.org/x/sys/unix"

// getTermiosReq is the ioctl that reads a tty's termios on darwin and the
// BSDs, where the Linux TCGETS request does not exist.
const getTermiosReq = unix.TIOCGETA
