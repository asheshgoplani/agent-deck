package session

import (
	"fmt"
	"os/exec"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A sweep classifies every instance; probing has-session per instance forked
// ~500 tmux clients every 60s at 492 sessions. One listing per socket answers
// them all, and "no server running" is an authoritative empty listing.
func TestReviver_SweepListsEachSocketOnce(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := tmux.StaleSocketForTest(t)
	exists := sweepTmuxExists()

	before := tmux.SubprocessStarts()
	for i := 0; i < 50; i++ {
		if exists(fmt.Sprintf("agentdeck_gone_%d", i), socket) {
			t.Fatalf("session %d reported alive with no server", i)
		}
	}
	if n := tmux.SubprocessStarts() - before; n != 1 {
		t.Errorf("50 classifications spawned %d tmux subprocesses, want 1", n)
	}
}
