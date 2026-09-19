//go:build unix

package procowner

import (
	"errors"
	"syscall"
)

// signalZero is kill(pid, 0): nil or EPERM when the pid exists.
func signalZero(pid int) error { return syscall.Kill(pid, 0) }

func isPermissionDenied(err error) bool { return errors.Is(err, syscall.EPERM) }
