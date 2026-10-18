package session

import (
	"testing"
	"time"
)

// Issue #2665: after a pi child is revived, the tracked poll status is error,
// and a short turn answered between two polls never shows running. The
// snapshot edge (error -> waiting) is not notifiable, so the pi turn_end hook
// is the only signal that the turn finished. It must become a hook
// transition candidate and reach the parent inbox exactly once.
func TestPiTurnEndHookTransitionWakesParentAfterRevive(t *testing.T) {
	f := newTurnTestFixture(t)
	f.child.Tool = "pi"

	hs := &HookStatus{Status: "waiting", Event: "turn_end", UpdatedAt: time.Now()}
	candidate, ok := terminalHookTransitionCandidate(f.child.Tool, hs)
	if !ok {
		t.Fatalf("pi turn_end hook is not a transition candidate")
	}

	prev := map[string]string{f.child.ID: "error", f.parent.ID: "waiting"}
	current := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	candidates := map[string]hookTransitionCandidate{f.child.ID: candidate}

	f.d.emitHookTransitionCandidates("default", f.byID, prev, current, candidates)
	got := f.inboxRecords(t)
	if len(got) != 1 {
		t.Fatalf("want one parent inbox record after the revived turn, got %d: %+v", len(got), got)
	}
	if got[0].FromStatus != "running" || got[0].ToStatus != "waiting" {
		t.Fatalf("record = %s -> %s, want running -> waiting", got[0].FromStatus, got[0].ToStatus)
	}

	// The same hook file is re-read on the next poll; it must not emit again.
	f.d.emitHookTransitionCandidates("default", f.byID, current, current, candidates)
	if got := f.inboxRecords(t); len(got) != 1 {
		t.Fatalf("re-read of the same turn_end hook emitted again: %d records: %+v", len(got), got)
	}
}
