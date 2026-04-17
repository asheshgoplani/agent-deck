//go:build darwin

package tmux

import "golang.org/x/sys/unix"

// FREAD from <sys/fcntl.h>; not re-exported by golang.org/x/sys/unix
// on darwin. Stable value across BSD-derived systems.
const darwinFREAD = 0x1

// Darwin's ioctl for flushing tty queues is TIOCFLUSH; the second
// argument selects which queue. FREAD flushes input — equivalent to
// Linux's TCFLSH + TCIFLUSH.
func flushDetachInput(fd int) error {
	return unix.IoctlSetInt(fd, unix.TIOCFLUSH, darwinFREAD)
}
