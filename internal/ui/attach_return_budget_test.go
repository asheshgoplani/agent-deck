package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestAttachReturnRefreshNotChargedToStatusBudget(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	prior := tmux.DefaultSocketName()
	tmux.SetDefaultSocketName("attach-return-budget")
	t.Cleanup(func() { tmux.SetDefaultSocketName(prior) })

	h := &Home{}
	totalBefore := tmux.SubprocessStarts()
	before := tmux.SnapshotStarts()
	cmd := h.attachReturnRefreshCmd()
	if cmd == nil {
		t.Fatal("attach return refresh cmd is nil")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("attach return refresh returned no message")
	}
	if tmux.SubprocessStarts() == totalBefore {
		t.Fatal("attach return refresh did not start tmux")
	}
	if got := before.Charged(); got != 0 {
		t.Fatalf("Ctrl+Q cache refresh charged %d tmux calls to the status budget", got)
	}
}
