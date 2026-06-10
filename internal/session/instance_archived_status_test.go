package session

import (
	"testing"
	"time"
)

// Archived sessions were stopped deliberately; a missing tmux session is their
// expected end state. UpdateStatus must surface that as stopped, not error —
// otherwise sessions archived while their tmux was already dead (or whose
// stop-status persist was skipped) stay red in the archive list forever.
func TestUpdateStatus_ArchivedNilTmuxIsStoppedNotError(t *testing.T) {
	for _, start := range []Status{StatusError, StatusRunning, StatusWaiting} {
		inst := &Instance{
			ID:         "arch-" + string(start),
			Title:      "arch",
			Tool:       "claude",
			Status:     start,
			ArchivedAt: time.Now(),
			CreatedAt:  time.Now().Add(-time.Hour),
		}
		if err := inst.UpdateStatus(); err != nil {
			t.Fatalf("UpdateStatus(%s): %v", start, err)
		}
		if got := inst.Status; got != StatusStopped {
			t.Errorf("archived session with no tmux, starting %s: got status %s, want %s", start, got, StatusStopped)
		}
	}
}

// Non-archived sessions keep the existing behavior: a vanished tmux session is
// an error the user should see.
func TestUpdateStatus_NonArchivedNilTmuxStaysError(t *testing.T) {
	inst := &Instance{
		ID:        "live",
		Title:     "live",
		Tool:      "claude",
		Status:    StatusRunning,
		CreatedAt: time.Now().Add(-time.Hour),
	}
	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if got := inst.Status; got != StatusError {
		t.Errorf("non-archived session with no tmux: got status %s, want %s", got, StatusError)
	}
}
