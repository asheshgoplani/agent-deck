package telemetry

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"syscall"
)

// ErrKindOf maps an error to a published error kind. Only the returned
// enum is recorded; the error text never leaves this function.
func ErrKindOf(err error) ErrKind {
	switch {
	case err == nil:
		return KindOther
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return KindTimeout
	case errors.Is(err, fs.ErrPermission):
		return KindPermission
	case errors.Is(err, syscall.ENOSPC):
		return KindDiskFull
	case errors.Is(err, exec.ErrNotFound):
		return KindToolNotFound
	}
	return KindOther
}
