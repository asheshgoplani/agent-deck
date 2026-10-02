package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type stubCloser struct{ err error }

func (s stubCloser) Close() error { return s.err }

func TestCloseFile_ReportsCloseErrorWhenNoEarlierError(t *testing.T) {
	closeErr := errors.New("close failed")
	var err error
	closeFile(stubCloser{err: closeErr}, &err)
	if !errors.Is(err, closeErr) {
		t.Fatalf("err = %v, want %v", err, closeErr)
	}
}

func TestCloseFile_JoinsEarlierError(t *testing.T) {
	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")
	err := writeErr
	closeFile(stubCloser{err: closeErr}, &err)
	if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
		t.Fatalf("err = %v, want both write and close errors", err)
	}
}

func TestCloseFile_CleanCloseLeavesNil(t *testing.T) {
	var err error
	closeFile(stubCloser{}, &err)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// A real *os.File that is already closed fails its second Close; the helper
// must surface that instead of dropping it.
func TestCloseFile_SurfacesRealFileCloseFailure(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var got error
	closeFile(f, &got)
	if !errors.Is(got, os.ErrClosed) {
		t.Fatalf("err = %v, want os.ErrClosed", got)
	}
}

func TestAppendLogLine_AppendsNewlineTerminatedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.log")
	for _, line := range []string{`{"a":1}`, `{"b":2}`} {
		if err := appendLogLine(path, []byte(line)); err != nil {
			t.Fatalf("appendLogLine: %v", err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"a\":1}\n{\"b\":2}\n"; string(got) != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestAppendLogLine_ReportsOpenFailure(t *testing.T) {
	// A directory cannot be opened for appending.
	if err := appendLogLine(t.TempDir(), []byte("x")); err == nil {
		t.Fatal("appendLogLine on a directory returned nil error")
	}
}
