package session

import (
	"strings"
	"testing"
	"time"
)

// TestLegacyDedup_ReproducesUnforkableSymptom characterizes the CURRENT
// (shipped) behavior that this prototype targets: UpdateClaudeSessionsWithDedup
// blanks the newer collider's ID and zeroes ClaudeDetectedAt, leaving it
// un-forkable. This is the exact mechanism behind "f does nothing on a
// same-cwd session". Pinning it here makes the contrast with
// RepairDuplicateClaudeSessions explicit and guards against silent
// behavior drift.
func TestLegacyDedup_ReproducesUnforkableSymptom(t *testing.T) {
	const shared = "8bb987b1-7d26-4c85-a606-c6a8a80f8645"

	older := NewInstance("make beads", "/home/src/tbd")
	older.Tool = "claude"
	older.Status = StatusRunning
	older.ClaudeSessionID = shared
	older.ClaudeDetectedAt = time.Now()
	older.CreatedAt = time.Now().Add(-2 * time.Hour)

	newer := NewInstance("r/55 show knowledge", "/home/src/tbd")
	newer.Tool = "claude"
	newer.Status = StatusRunning
	newer.ClaudeSessionID = shared
	newer.ClaudeDetectedAt = time.Now()
	newer.CreatedAt = time.Now().Add(-1 * time.Hour)

	// Both are forkable before dedup runs.
	if !newer.CanFork() {
		t.Fatalf("precondition: newer should be forkable before dedup")
	}

	UpdateClaudeSessionsWithDedup([]*Instance{older, newer})

	// Symptom: newer is blanked + zeroed → CanFork() false → no `f`.
	if newer.ClaudeSessionID != "" {
		t.Errorf("expected legacy dedup to blank newer's ID, got %q", newer.ClaudeSessionID)
	}
	if !newer.ClaudeDetectedAt.IsZero() {
		t.Errorf("expected legacy dedup to zero ClaudeDetectedAt")
	}
	if newer.CanFork() {
		t.Errorf("symptom not reproduced: newer is still forkable after legacy dedup")
	}
	// Older keeps the ID and stays forkable.
	if older.ClaudeSessionID != shared || !older.CanFork() {
		t.Errorf("older should retain ID and remain forkable")
	}
}

// TestRepairDuplicateClaudeSessions_RekeysNewerCollider is the prototype
// regression for the "can't fork a same-cwd session" symptom.
//
// Repro: two Claude sessions in the same project dir end up sharing one
// ClaudeSessionID (both resumed the same conversation). The legacy
// UpdateClaudeSessionsWithDedup blanks the newer one's ID and zeroes
// ClaudeDetectedAt, so CanFork() returns false and the TUI never renders
// the `f` key — and because the tmux env still asserts the shared ID, it
// flip-flops forever.
//
// RepairDuplicateClaudeSessions instead re-keys the newer collider to its
// OWN fresh forked session ID, keeping it forkable and making the two
// sessions genuinely distinct.
func TestRepairDuplicateClaudeSessions_RekeysNewerCollider(t *testing.T) {
	const shared = "8bb987b1-7d26-4c85-a606-c6a8a80f8645"

	older := NewInstance("make beads", "/home/src/tbd")
	older.Tool = "claude"
	older.Status = StatusRunning
	older.ClaudeSessionID = shared
	older.ClaudeDetectedAt = time.Now()
	older.CreatedAt = time.Now().Add(-2 * time.Hour)

	newer := NewInstance("r/55 show knowledge", "/home/src/tbd")
	newer.Tool = "claude"
	newer.Status = StatusRunning
	newer.ClaudeSessionID = shared
	newer.ClaudeDetectedAt = time.Now()
	newer.CreatedAt = time.Now().Add(-1 * time.Hour) // newer than `older`

	// Precondition: both currently collide and the symptom is present
	// (newer is forkable only because the IDs haven't been resolved yet).
	if !older.CanFork() {
		t.Fatalf("precondition: older should be forkable")
	}

	rekeyed := RepairDuplicateClaudeSessions([]*Instance{older, newer})

	// Owner (older) keeps the shared ID.
	if older.ClaudeSessionID != shared {
		t.Errorf("older lost its ID: got %q want %q", older.ClaudeSessionID, shared)
	}

	// Newer is re-keyed to a distinct, non-empty ID (NOT blanked).
	if newer.ClaudeSessionID == "" {
		t.Fatalf("newer was blanked — legacy destructive behavior, symptom NOT fixed")
	}
	if newer.ClaudeSessionID == shared {
		t.Errorf("newer still shares the ID: %q", newer.ClaudeSessionID)
	}

	// ClaudeDetectedAt stays fresh so CanFork() is true (the actual fix).
	if newer.ClaudeDetectedAt.IsZero() {
		t.Errorf("newer.ClaudeDetectedAt was zeroed — CanFork() would fail")
	}
	if !newer.CanFork() {
		t.Errorf("newer is not forkable after repair — symptom not fixed")
	}

	// Newer is set up to materialize as an independent fork on next start.
	if !newer.IsForkAwaitingStart {
		t.Errorf("newer.IsForkAwaitingStart not set")
	}
	if !strings.Contains(newer.Command, "--fork-session") ||
		!strings.Contains(newer.Command, "--resume "+shared) {
		t.Errorf("newer.Command is not a fork-from-shared command: %q", newer.Command)
	}

	// The re-keyed instance is reported so the caller can Restart() it.
	if len(rekeyed) != 1 || rekeyed[0] != newer {
		t.Errorf("expected exactly the newer instance returned for restart, got %v", rekeyed)
	}
}

// TestRepairDuplicateClaudeSessions_FallsBackToBlankWhenOwnerNotForkable
// covers the degenerate case: if the owner can't be forked (no fresh
// detection / no conversation to inherit), there's nothing to fork from,
// so the newer collider is blanked as before.
func TestRepairDuplicateClaudeSessions_FallsBackToBlankWhenOwnerNotForkable(t *testing.T) {
	const shared = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	older := NewInstance("owner", "/home/src/tbd")
	older.Tool = "claude"
	older.ClaudeSessionID = shared
	older.ClaudeDetectedAt = time.Now().Add(-10 * time.Minute) // stale → CanFork() false
	older.CreatedAt = time.Now().Add(-2 * time.Hour)

	newer := NewInstance("dup", "/home/src/tbd")
	newer.Tool = "claude"
	newer.ClaudeSessionID = shared
	newer.ClaudeDetectedAt = time.Now()
	newer.CreatedAt = time.Now().Add(-1 * time.Hour)

	if older.CanFork() {
		t.Fatalf("precondition: older should NOT be forkable (stale)")
	}

	rekeyed := RepairDuplicateClaudeSessions([]*Instance{older, newer})

	if newer.ClaudeSessionID != "" {
		t.Errorf("expected legacy blank when owner not forkable, got %q", newer.ClaudeSessionID)
	}
	if len(rekeyed) != 0 {
		t.Errorf("expected no re-keyed instances, got %d", len(rekeyed))
	}
}
