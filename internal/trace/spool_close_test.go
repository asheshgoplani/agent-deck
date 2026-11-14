package trace

import (
	"errors"
	"os"
	"testing"
)

// A failed Close on the writable spool handle can lose the appended line,
// so AppendSpool must report it instead of dropping it.
func TestAppendSpoolReturnsCloseError(t *testing.T) {
	want := errors.New("close failed")
	orig := spoolClose
	spoolClose = func(f *os.File) error { _ = f.Close(); return want }
	t.Cleanup(func() { spoolClose = orig })

	err := AppendSpool(t.TempDir(), SpoolRecord{})
	if !errors.Is(err, want) {
		t.Fatalf("AppendSpool error = %v, want the close error", err)
	}
}
