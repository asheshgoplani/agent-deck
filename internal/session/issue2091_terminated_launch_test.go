package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A probe begun for an old process must not classify the replacement process.
// The channel handoff drives the actual lock-dropping path without real tmux.
func TestIssue2091TerminatedProbeCannotOverwriteNewLaunch(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	inst := &Instance{
		Tool: "codex", Status: StatusRunning,
		LastStartedAt: time.Now().Add(-time.Minute),
		tmuxSession:   &tmux.Session{Name: "issue2091-test-only"},
		paneDeadExitStatusForTest: func() (int, bool) {
			close(entered)
			<-release
			return 7, true
		},
	}
	go func() {
		inst.mu.Lock()
		inst.applyTerminatedPaneStatus()
		inst.mu.Unlock()
		close(finished)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("exit probe did not start")
	}
	inst.mu.Lock()
	inst.LastStartedAt = time.Now()
	inst.Status = StatusStarting
	inst.mu.Unlock()
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("exit probe did not finish")
	}
	if got := inst.GetStatusThreadSafe(); got != StatusStarting {
		t.Fatalf("old exit probe overwrote new launch: got %s, want %s", got, StatusStarting)
	}
}
