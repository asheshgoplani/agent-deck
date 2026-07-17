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
