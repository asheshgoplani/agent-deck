//go:build linux

package tmux

import "golang.org/x/sys/unix"

func flushDetachInput(fd int) error {
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}
