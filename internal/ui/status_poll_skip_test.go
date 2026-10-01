package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Perf: the per-tick background status loop walks every loaded instance and
// runs UpdateStatus() (a tmux subprocess). Archived sessions have had their
// tmux pane torn down and their row status is display-frozen (rowStatusGlyph
// forces the stopped glyph regardless of Status), so polling them can never
// change anything the UI shows — it only burns a serialized tmux call. With a
// large archive backlog (observed: 723 archived of 742 total) this dominated
// the loop and pushed it to multi-second spikes. shouldPollStatusInLoop pins
// the contract that archived sessions are skipped and active ones are not.
func TestShouldPollStatusInLoop_SkipsArchived(t *testing.T) {
	active := &session.Instance{ID: "a", Title: "active"}
	archived := &session.Instance{ID: "b", Title: "archived", ArchivedAt: time.Now().UTC()}

	if !shouldPollStatusInLoop(active) {
		t.Fatalf("active session must be polled")
	}
	if shouldPollStatusInLoop(archived) {
		t.Fatalf("archived session must be skipped (no live pane, frozen status)")
	}
	if shouldPollStatusInLoop(nil) {
		t.Fatalf("nil instance must not be polled")
	}
}

// An archived session whose tmux was already gone when it was archived never
// went through Kill() (the CLI archive path only kills a live session), so it
// keeps its last stored live status. Skipping it forever froze that status,
// and the header pill counted 28 long-dead archived sessions as running.
// Archived rows still claiming a live status are polled until they settle on
// error/stopped; settled ones stay skipped.
func TestShouldPollStatusInLoop_ArchivedWithStaleLiveStatus(t *testing.T) {
	archivedAt := time.Now().UTC()
	for _, tc := range []struct {
		status session.Status
		want   bool
	}{
		{session.StatusRunning, true},
		{session.StatusWaiting, true},
		{session.StatusIdle, true},
		{session.StatusStarting, true},
		{session.StatusError, false},
		{session.StatusStopped, false},
	} {
		inst := &session.Instance{ID: "x", Status: tc.status, ArchivedAt: archivedAt}
		if got := shouldPollStatusInLoop(inst); got != tc.want {
			t.Errorf("archived %s: shouldPollStatusInLoop = %v, want %v", tc.status, got, tc.want)
		}
	}
}
