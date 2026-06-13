package session

// Stale-row veto regression tests: under a live TUI heartbeat
// (AliveInstanceCount > 0) syncProfile reads session statuses from DB rows.
// A TUI that holds the heartbeat without refreshing its rows (orphaned tab,
// or sessions created after it loaded its list) leaves rows frozen at
// `running`, and emitHookTransitionCandidates let that stale row OVERRIDE a
// FRESH terminal hook status — the candidate was then dropped as
// non-terminal, so the child's completion vanished with no event and no log.
//
// Written test-first: TestSyncOnce_StaleRunningRowDoesNotVetoFreshTerminalHook
// must FAIL on pre-fix main. The other two pin the boundaries of the fix:
// a MORE-final row still wins, and the snapshot path still owns transitions
// the rows actually reflect (no double delivery).
//
// NOTE: rename to issueNNNN_stale_row_veto_test.go once the upstream issue
// is filed, per the regression-test naming convention.

import (
	"testing"
	"time"
)

// seedStaleRowFixture saves a parent + claude child (status running) into the
// profile's storage, registers a fresh TUI heartbeat on the profile DB (the
// orphaned-TUI condition: heartbeat alive, rows not refreshed), and writes the
// child's DB status row. Returns the parent and child.
func seedStaleRowFixture(t *testing.T, storage *Storage, childID, parentID, rowStatus string) (*Instance, *Instance) {
	t.Helper()
	now := time.Now()
	child := &Instance{
		ID:              childID,
		Title:           "worker",
		ProjectPath:     "/tmp/" + childID,
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parentID,
		Tool:            "claude",
		Status:          StatusRunning,
		CreatedAt:       now,
	}
	parent := &Instance{
		ID:          parentID,
		Title:       "orchestrator",
		ProjectPath: "/tmp/" + parentID,
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   now,
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	db := storage.GetDB()
	if db == nil {
		t.Fatal("storage has no state DB")
	}
	// The orphaned TUI: a fresh heartbeat keeps the daemon on the DB-row
	// status source for this profile…
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	// …while the child's row stays at whatever the TUI last wrote.
	if err := db.WriteStatus(child.ID, rowStatus, child.Tool); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	return parent, child
}

// TestSyncOnce_StaleRunningRowDoesNotVetoFreshTerminalHook is the PRIMARY
// regression test. The child's own Stop hook asserts a fresh terminal status
// (waiting) while the orphaned TUI's DB row still says running. The daemon
// must emit the transition to the parent's inbox — the stale non-terminal row
// must not veto the fresh hook status.
func TestSyncOnce_StaleRunningRowDoesNotVetoFreshTerminalHook(t *testing.T) {
	const profile = "_test_stalerow_veto"
	d, storage := bootstrapDaemonProfile(t, profile)
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(ResetInboxFingerprintCacheForTest)

	parent, child := seedStaleRowFixture(t, storage, "stalerow-child-1", "stalerow-parent-1", "running")

	// The child's own runtime asserts a FRESH terminal status via its Stop hook.
	seedHookStatusFile(t, child.ID, "Stop", "55555555-5555-5555-5555-555555555555", "waiting")

	d.syncProfile(profile)

	inbox, err := DrainInboxForParent(parent.ID)
	if err != nil {
		t.Fatalf("DrainInboxForParent: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("stale running row vetoed the fresh terminal hook status: parent inbox has %d events, want 1", len(inbox))
	}
	if inbox[0].ChildSessionID != child.ID || inbox[0].ToStatus != "waiting" {
		t.Fatalf("wrong event committed: %+v", inbox[0])
	}
}

// TestSyncOnce_MoreFinalRowStillWinsOverHookStatus guards the deliberately
// preserved half of the override: when the DB row is itself notify-terminal
// (and possibly MORE final than the hook status, e.g. error), the row still
// wins and the emitted event carries the row's status.
func TestSyncOnce_MoreFinalRowStillWinsOverHookStatus(t *testing.T) {
	const profile = "_test_stalerow_morefinal"
	d, storage := bootstrapDaemonProfile(t, profile)
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(ResetInboxFingerprintCacheForTest)

	parent, child := seedStaleRowFixture(t, storage, "stalerow-child-2", "stalerow-parent-2", "error")

	seedHookStatusFile(t, child.ID, "Stop", "66666666-6666-6666-6666-666666666666", "waiting")

	d.syncProfile(profile)

	inbox, err := DrainInboxForParent(parent.ID)
	if err != nil {
		t.Fatalf("DrainInboxForParent: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("parent inbox has %d events, want 1", len(inbox))
	}
	if inbox[0].ToStatus != "error" {
		t.Fatalf("more-final row must win over the hook status: got to_status %q, want %q", inbox[0].ToStatus, "error")
	}
}

// TestSyncOnce_SnapshotPathStillOwnsRefreshedRowTransition guards against the
// fix introducing double delivery: when the rows ARE being refreshed (healthy
// TUI) and both the row and the hook status report the same terminal
// transition, the snapshot path emits it and the hook-candidate path stands
// down — exactly one event reaches the parent.
func TestSyncOnce_SnapshotPathStillOwnsRefreshedRowTransition(t *testing.T) {
	const profile = "_test_stalerow_norefire"
	d, storage := bootstrapDaemonProfile(t, profile)
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(ResetInboxFingerprintCacheForTest)

	parent, child := seedStaleRowFixture(t, storage, "stalerow-child-3", "stalerow-parent-3", "running")

	// First poll observes the running snapshot (no hook file yet).
	d.syncProfile(profile)
	if events, err := DrainInboxForParent(parent.ID); err != nil || len(events) != 0 {
		t.Fatalf("no event expected on the running snapshot, got %d (err=%v)", len(events), err)
	}

	// The TUI refreshes the row AND the child's Stop hook fires: both sources
	// now agree on running→waiting.
	if err := storage.GetDB().WriteStatus(child.ID, "waiting", child.Tool); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	seedHookStatusFile(t, child.ID, "Stop", "77777777-7777-7777-7777-777777777777", "waiting")

	d.syncProfile(profile)

	inbox, err := DrainInboxForParent(parent.ID)
	if err != nil {
		t.Fatalf("DrainInboxForParent: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("refreshed-row transition must be delivered exactly once, got %d events", len(inbox))
	}
	if inbox[0].ChildSessionID != child.ID || inbox[0].ToStatus != "waiting" {
		t.Fatalf("wrong event committed: %+v", inbox[0])
	}
}
