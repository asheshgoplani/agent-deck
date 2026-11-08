//go:build unix

package session

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The sandbox scope (hooks/sandbox/<id>/) is writable from the container, so a
// FIFO planted at a path readHookTurnSeq reads must not block it: it runs in
// noteFreshStart (Start) and in the daemon's judge. Each FIFO reads as unknown.
func TestIssue3012_ReadHookTurnSeqFIFODoesNotBlock(t *testing.T) {
	for _, c := range []struct {
		name string
		path func(hooks, id string) string
	}{
		{"scoped turnseq", func(h, id string) string { return filepath.Join(h, "sandbox", id, id+HookTurnSeqSuffix) }},
		{"flat turnseq", func(h, id string) string { return filepath.Join(h, id+HookTurnSeqSuffix) }},
		{"scoped hook file", func(h, id string) string { return filepath.Join(h, "sandbox", id, id+".json") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolatedHomeDir(t)
			const id = "seq-fifo"
			fifo := c.path(GetHooksDir(), id)
			if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan uint64, 1)
			go func() { done <- readHookTurnSeq(id) }()
			select {
			case got := <-done:
				if got != 0 {
					t.Fatalf("readHookTurnSeq with a FIFO = %d, want 0 (unknown)", got)
				}
			case <-time.After(time.Second):
				// Release the blocked reader so the test fails instead of hanging.
				if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = w.Close()
				}
				t.Fatal("readHookTurnSeq blocked on a FIFO for over 1s")
			}
		})
	}
}
