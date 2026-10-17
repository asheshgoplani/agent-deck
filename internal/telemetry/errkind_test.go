package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"syscall"
	"testing"
)

// ErrKindOf maps an error to a published kind from its type only; the error
// text never leaves the machine.
func TestErrKindOfClassifiesWithoutText(t *testing.T) {
	cases := []struct {
		err  error
		want ErrKind
	}{
		{fmt.Errorf("wrap: %w", context.DeadlineExceeded), KindTimeout},
		{&fs.PathError{Op: "open", Path: "/x", Err: fs.ErrPermission}, KindPermission},
		{fmt.Errorf("write: %w", syscall.ENOSPC), KindDiskFull},
		{&exec.Error{Name: "claude", Err: exec.ErrNotFound}, KindToolNotFound},
		{errors.New("something else"), KindOther},
	}
	for _, c := range cases {
		if got := ErrKindOf(c.err); got != c.want {
			t.Errorf("ErrKindOf(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
