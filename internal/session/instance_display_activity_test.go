package session

import (
	"testing"
	"time"
)

// DisplayLastActivityTime is the UI-facing "last active" timestamp. Unlike
// GetLastActivityTime (which feeds OpenCode rotation windows and returns the
// tmux tracker's raw lastChangeTime), it must never leak the tracker's
// initialization timestamp (~ TUI load time) for a session that has no
// CONFIRMED activity. Error/idle/stopped sessions with a live-but-inactive
// tmux pane fall into exactly that case: the web shows the persisted
// last-accessed time, and the TUI must match it rather than jumping to the
// most recent TUI load. See the last-used regression.

func TestDisplayLastActivityTime_NoConfirmedActivity_UsesLastAccessed(t *testing.T) {
	accessed := time.Now().Add(-24 * time.Hour)
	created := time.Now().Add(-72 * time.Hour)
	// nil tmuxSession => LastObservedActivity() reports (zero,false), i.e. no
	// confirmed activity — the error-session case.
	inst := &Instance{ID: "err-sess", CreatedAt: created, LastAccessedAt: accessed}

	got := inst.DisplayLastActivityTime()
	if !got.Equal(accessed) {
		t.Errorf("with no confirmed activity, want LastAccessedAt %v, got %v", accessed, got)
	}
	// Must NOT report ~now (the load-time leak this fix targets).
	if time.Since(got) < time.Hour {
		t.Errorf("display time %v is suspiciously recent — the load-time leak is back", got)
	}
}

func TestDisplayLastActivityTime_NoAccess_FallsBackToCreated(t *testing.T) {
	created := time.Now().Add(-72 * time.Hour)
	inst := &Instance{ID: "err-sess-2", CreatedAt: created} // LastAccessedAt zero

	got := inst.DisplayLastActivityTime()
	if !got.Equal(created) {
		t.Errorf("with no access and no activity, want CreatedAt %v, got %v", created, got)
	}
}

// The zero-data tail: very old rows persist last_accessed AND created_at as the
// Go zero sentinel (-62135596800 unix). Both fall through to a zero time, which
// the UI renders as "unknown" — an honest blank, never the ~load-time leak.
func TestDisplayLastActivityTime_AllZero_ReturnsZeroNotLoadTime(t *testing.T) {
	inst := &Instance{ID: "err-sess-3"} // CreatedAt and LastAccessedAt both zero

	got := inst.DisplayLastActivityTime()
	if !got.IsZero() {
		t.Errorf("with no access, activity, or creation time, want zero time, got %v", got)
	}
}
