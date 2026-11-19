package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// needsForceToRemove keys on a live process, not on the tmux session merely
// existing. The probe is stubbed so this holds whether or not tmux is there.
func TestNeedsForceToRemove_UsesLiveProcessProbe(t *testing.T) {
	orig := sessionHasLiveProcess
	t.Cleanup(func() { sessionHasLiveProcess = orig })

	for _, tc := range []struct {
		status session.Status
		live   bool
		want   bool
	}{
		{session.StatusRunning, true, true},
		{session.StatusWaiting, true, true},
		{session.StatusStarting, true, true},
		{session.StatusIdle, true, true},
		// remain-on-exit kept the tmux session but all its panes are dead.
		{session.StatusRunning, false, false},
		{session.StatusWaiting, false, false},
		{session.StatusStarting, false, false},
		{session.StatusStopped, true, false},
		{session.StatusError, true, false},
	} {
		live := tc.live
		sessionHasLiveProcess = func(*session.Instance) bool { return live }
		inst := &session.Instance{Title: "probe", Status: tc.status}
		if got := needsForceToRemove(inst); got != tc.want {
			t.Errorf("needsForceToRemove(status=%s, live=%v) = %v, want %v", tc.status, tc.live, got, tc.want)
		}
	}
}

// A row with no tmux session at all has no live process.
func TestSessionHasLiveProcess_NoTmuxSession(t *testing.T) {
	if sessionHasLiveProcess(&session.Instance{Title: "none"}) {
		t.Fatal("an instance with no tmux session reported a live process")
	}
}
