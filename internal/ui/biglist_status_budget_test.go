package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestBigListBackgroundStatusWorkIsBounded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := &Home{}
	for i := 0; i < 100; i++ {
		h.instances = append(h.instances, &session.Instance{
			ID: fmt.Sprintf("s%d", i), Tool: "shell", Status: session.StatusRunning,
		})
	}
	countUpdated := func() int {
		count := 0
		for _, inst := range h.instances {
			if inst.GetStatusThreadSafe() != session.StatusRunning {
				count++
			}
		}
		return count
	}

	h.backgroundStatusUpdate()
	first := countUpdated()
	if first == 0 || first > 32 {
		t.Fatalf("first tick updated %d sessions, want 1..32", first)
	}
	for i := 0; i < 3; i++ {
		h.backgroundStatusUpdate()
	}
	if got := countUpdated(); got != 100 {
		t.Fatalf("four ticks updated %d sessions, want 100", got)
	}
}
