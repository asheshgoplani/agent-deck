package tmux

import (
	"testing"
	"time"
)

// A hook event from the current pane generation ends the startup phase, so a
// session that lived on the hook fast path is not expired on its first
// fallthrough to GetStatus.
func TestIssue2361_HookEvidenceClearsStartupClock(t *testing.T) {
	s := NewSession("issue2361-hook-clears", t.TempDir())
	s.startupAt = time.Now().Add(-startupStateWindow - time.Second)

	s.MarkInteractiveAt(s.startupAt.Add(time.Second))

	if !s.startupAt.IsZero() {
		t.Fatal("hook evidence did not clear the startup clock")
	}
	if s.expireStartupHandover() {
		t.Fatal("startup timeout fired after hook evidence of an interactive agent")
	}
}

// Hook timestamps have one-second resolution: a SessionStart stamped in the
// same second the pane started still counts.
func TestIssue2361_SameSecondHookCounts(t *testing.T) {
	s := NewSession("issue2361-same-second", t.TempDir())
	s.startupAt = time.Date(2026, 9, 22, 12, 47, 53, 900_000_000, time.Local)

	s.MarkInteractiveAt(time.Date(2026, 9, 22, 12, 47, 53, 0, time.Local))

	if !s.startupAt.IsZero() {
		t.Fatal("same-second hook evidence was rejected")
	}
}

// A late hook from the previous pane generation must not vouch for a pane
// that was respawned after it.
func TestIssue2361_StaleGenerationHookIgnored(t *testing.T) {
	s := NewSession("issue2361-stale-hook", t.TempDir())
	s.startupAt = time.Now()

	s.MarkInteractiveAt(s.startupAt.Add(-5 * time.Second))

	if s.startupAt.IsZero() {
		t.Fatal("hook from an earlier pane generation cleared the new startup clock")
	}
}
