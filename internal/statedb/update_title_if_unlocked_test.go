package statedb

import (
	"encoding/json"
	"testing"
	"time"
)

// UpdateTitleIfUnlocked is the fix for the "session name keeps getting
// overwritten" bug: the hook-triggered Claude title sync
// (cmd/agent-deck/hook_name_sync.go) runs as a separate process, decides
// whether to rename based on a possibly-stale snapshot, and used to persist
// via a full instance-list round-trip (UpsertInstances) with no freshness
// check — so a user rename landing in between could be silently reverted.
// This method closes that gap with a single conditional UPDATE instead.

func seedTitleRow(t *testing.T, db *StateDB, id, title string, locked bool) {
	t.Helper()
	row := &InstanceRow{
		ID: id, Title: title, ProjectPath: "/p", GroupPath: "",
		Order: 0, Tool: "claude", Status: "idle", CreatedAt: time.Now(),
		ToolData: json.RawMessage("{}"), TitleLocked: locked,
	}
	if err := db.UpsertInstances([]*InstanceRow{row}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// The core race: the write must be a no-op once the row is locked, even
// though the caller's own decision to write was made before the lock existed
// (that staleness is exactly the scenario — the guard is enforced at write
// time, not decision time).
func TestUpdateTitleIfUnlocked_NoopWhenLocked(t *testing.T) {
	db := newTestDB(t)
	seedTitleRow(t, db, "s1", "my-custom-name", true)

	applied, err := db.UpdateTitleIfUnlocked("s1", "claude-derived-name")
	if err != nil {
		t.Fatalf("UpdateTitleIfUnlocked: %v", err)
	}
	if applied {
		t.Fatalf("expected no-op against a locked row, got applied=true")
	}

	loaded, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if loaded[0].Title != "my-custom-name" {
		t.Errorf("locked title was overwritten: got %q, want %q", loaded[0].Title, "my-custom-name")
	}
	if !loaded[0].TitleLocked {
		t.Errorf("lock was cleared by a title-only update")
	}
}

// The normal case: an unlocked row's title updates freely (this is the
// legitimate Claude-name-sync path when no user rename has happened).
func TestUpdateTitleIfUnlocked_AppliesWhenUnlocked(t *testing.T) {
	db := newTestDB(t)
	seedTitleRow(t, db, "s1", "old-name", false)

	applied, err := db.UpdateTitleIfUnlocked("s1", "claude-derived-name")
	if err != nil {
		t.Fatalf("UpdateTitleIfUnlocked: %v", err)
	}
	if !applied {
		t.Fatalf("expected update to apply against an unlocked row")
	}

	loaded, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if loaded[0].Title != "claude-derived-name" {
		t.Errorf("title = %q, want %q", loaded[0].Title, "claude-derived-name")
	}
}

// Critical: session set-title-lock off (or any other writer that legitimately
// wants to unlock+retitle) must NOT go through this method's WHERE guard and
// must still be able to change a locked row. UpdateTitleIfUnlocked is only
// for the specific "sync while possibly stale" case; a deliberate unlock is a
// normal UpsertInstances/SaveInstance call with TitleLocked=false, which must
// still succeed unconditionally (regression check for the overly-broad
// merge-based approach this replaced, which blocked legitimate unlocks too).
func TestUpsertInstances_DeliberateUnlockStillApplies(t *testing.T) {
	db := newTestDB(t)
	seedTitleRow(t, db, "s1", "locked-name", true)

	unlock := &InstanceRow{
		ID: "s1", Title: "locked-name", ProjectPath: "/p", GroupPath: "",
		Order: 0, Tool: "claude", Status: "idle", CreatedAt: time.Now(),
		ToolData: json.RawMessage("{}"), TitleLocked: false,
	}
	if err := db.UpsertInstances([]*InstanceRow{unlock}); err != nil {
		t.Fatalf("UpsertInstances unlock: %v", err)
	}

	loaded, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if loaded[0].TitleLocked {
		t.Fatalf("deliberate unlock via UpsertInstances did not apply — title_locked still true")
	}
}

// Sequential legitimate renames (both explicitly locked, e.g. the user
// renames twice) must both apply — this is not the stale-writer scenario.
func TestUpsertInstances_SequentialLockedRenamesBothApply(t *testing.T) {
	db := newTestDB(t)
	seedTitleRow(t, db, "s1", "first-name", true)

	second := &InstanceRow{
		ID: "s1", Title: "second-name", ProjectPath: "/p", GroupPath: "",
		Order: 0, Tool: "claude", Status: "idle", CreatedAt: time.Now(),
		ToolData: json.RawMessage("{}"), TitleLocked: true,
	}
	if err := db.UpsertInstances([]*InstanceRow{second}); err != nil {
		t.Fatalf("UpsertInstances second rename: %v", err)
	}

	loaded, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if loaded[0].Title != "second-name" || !loaded[0].TitleLocked {
		t.Fatalf("expected second locked rename to apply, got %+v", loaded[0])
	}
}
