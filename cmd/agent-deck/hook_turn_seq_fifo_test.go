//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO at <id>.turnseq must not block the hook handler's read of the count.
func TestIssue3012_ReadHookTurnSeqFileFIFODoesNotBlock(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "inst.turnseq")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan uint64, 1)
	go func() { done <- readHookTurnSeqFile(fifo) }()
	select {
	case got := <-done:
		if got != 0 {
			t.Fatalf("readHookTurnSeqFile with a FIFO = %d, want 0", got)
		}
	case <-time.After(time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("readHookTurnSeqFile blocked on a FIFO for over 1s")
	}
}
