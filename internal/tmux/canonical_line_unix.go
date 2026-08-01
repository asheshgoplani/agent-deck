//go:build !windows
// +build !windows

package tmux

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// ttyLineDiscipline opens the pane's tty read-only and asks the kernel
// whether its reader is in canonical mode, and how much it will buffer.
//
// O_NONBLOCK|O_NOCTTY is load-bearing: without O_NONBLOCK the open blocks
// until a peer opens the other side, and without O_NOCTTY this process could
// acquire the pane's tty as its controlling terminal. Neither the fd nor the
// call disturbs the pane — tcgetattr is a pure read.
func ttyLineDiscipline(tty string) (lineDiscipline, error) {
	fd, err := unix.Open(tty, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		return lineDiscipline{}, fmt.Errorf("open pane tty %s: %w", tty, err)
	}
	defer func() { _ = unix.Close(fd) }()

	termios, err := unix.IoctlGetTermios(fd, getTermiosReq)
	if err != nil {
		return lineDiscipline{}, fmt.Errorf("read termios of %s: %w", tty, err)
	}
	return lineDiscipline{
		Canonical: termios.Lflag&unix.ICANON != 0,
		MaxLine:   canonicalBufferBytes(),
	}, nil
}

// canonicalBufferBytes returns the kernel's canonical line buffer for this
// platform, terminator included. See canonMinLinux for why fpathconf's
// _PC_MAX_CANON is not used: Linux reports the POSIX floor of 255 there while
// n_tty really buffers 4096, and rejecting sends at 255 bytes would break
// ordinary traffic. Unknown platforms fall back to the smallest figure we
// have measured, which errs toward checking more payloads rather than fewer.
func canonicalBufferBytes() int {
	switch runtime.GOOS {
	case "linux":
		return canonMinLinux
	case "darwin":
		return canonMinDarwin
	default:
		return canonMinDarwin
	}
}
