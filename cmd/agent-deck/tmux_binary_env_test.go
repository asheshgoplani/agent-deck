package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// CLI probes that follow $TMUX must run the binary of the server $TMUX names.
func TestTmuxBinaryForEnv_FollowsTMUXSocket(t *testing.T) {
	tmux.SetSocketBinaries(map[string]string{"deck36": "/opt/tmux-3.6/bin/tmux"})
	t.Cleanup(func() { tmux.SetSocketBinaries(nil) })

	t.Setenv("TMUX", "/nonexistent/tmux-1000/deck36,123,0")
	if got := tmuxBinaryForEnv(); got != "/opt/tmux-3.6/bin/tmux" {
		t.Fatalf("deck36: got %q", got)
	}
	t.Setenv("TMUX", "/nonexistent/tmux-1000/other,123,0")
	if got := tmuxBinaryForEnv(); got != "tmux" {
		t.Fatalf("other: got %q", got)
	}
	t.Setenv("TMUX", "")
	if got := tmuxBinaryForEnv(); got != "tmux" {
		t.Fatalf("unset: got %q", got)
	}
}
