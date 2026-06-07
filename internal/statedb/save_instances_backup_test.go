package statedb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// S2 data-loss safeguard (2026-06-04 incident).
//
// S1 refuses the fully-EMPTY sweep. S2 covers the large-but-NOT-empty replace
// that S1 cannot catch: before saveInstancesOnce DELETEs a meaningful number of
// on-disk rows, it snapshots the DB file to "<path>.bak" so the destructive
// replace stays recoverable. These tests pin:
//
//	(a) a save that drops >= backupRowDropThreshold rows creates state.db.bak;
//	(b) a save that drops fewer than the threshold does NOT create the backup
//	    (routine session churn must not thrash the disk);
//	(c) the .bak is a usable snapshot of the PRE-sweep state (still contains the
//	    rows the sweep removed).

// newTestDBAtPath opens a StateDB at a known file path (not t.TempDir's hidden
// path) so the test can assert on the sibling state.db.bak file.
func newTestDBAtPath(t *testing.T) (*StateDB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dbPath
}

// (a) A destructive replace dropping >= threshold rows backs up first.
func TestSaveInstances_LargeDrop_CreatesBackup(t *testing.T) {
	db, dbPath := newTestDBAtPath(t)
	seedInstances(t, db, "a", "b", "c", "d", "e")

	bakPath := dbPath + ".bak"
	if _, err := os.Stat(bakPath); err == nil {
		t.Fatalf("precondition: %s should not exist yet", bakPath)
	}

	// Replace the set with just "a" — this DELETEs b,c,d,e (4 rows >= threshold 3).
	now := time.Now()
	rows := []*InstanceRow{
		{ID: "a", Title: "a", ProjectPath: "/a", GroupPath: "grp", Order: 0, Tool: "claude", Status: "idle", CreatedAt: now, ToolData: json.RawMessage("{}")},
	}
	if err := db.SaveInstances(rows); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	if _, err := os.Stat(bakPath); err != nil {
		t.Fatalf("expected %s to be created before the large sweep, stat err: %v", bakPath, err)
	}
}

// (b) A small drop (1 row) below the threshold does NOT create the backup.
func TestSaveInstances_SmallDrop_NoBackup(t *testing.T) {
	db, dbPath := newTestDBAtPath(t)
	seedInstances(t, db, "a", "b", "c")

	bakPath := dbPath + ".bak"

	// Drop just "c" (1 row < threshold 3).
	now := time.Now()
	rows := []*InstanceRow{
		{ID: "a", Title: "a", ProjectPath: "/a", GroupPath: "grp", Order: 0, Tool: "claude", Status: "idle", CreatedAt: now, ToolData: json.RawMessage("{}")},
		{ID: "b", Title: "b", ProjectPath: "/b", GroupPath: "grp", Order: 1, Tool: "claude", Status: "idle", CreatedAt: now, ToolData: json.RawMessage("{}")},
	}
	if err := db.SaveInstances(rows); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	if _, err := os.Stat(bakPath); err == nil {
		t.Fatalf("expected NO backup for a sub-threshold drop, but %s exists", bakPath)
	}
}

// (c) The .bak captures PRE-sweep state: it must still contain the swept rows.
func TestSaveInstances_Backup_CapturesPreSweepState(t *testing.T) {
	db, dbPath := newTestDBAtPath(t)
	seedInstances(t, db, "a", "b", "c", "d")

	now := time.Now()
	rows := []*InstanceRow{
		{ID: "a", Title: "a", ProjectPath: "/a", GroupPath: "grp", Order: 0, Tool: "claude", Status: "idle", CreatedAt: now, ToolData: json.RawMessage("{}")},
	}
	if err := db.SaveInstances(rows); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	// Open the backup as a separate DB and confirm it still holds all 4 rows.
	bakPath := dbPath + ".bak"
	bak, err := Open(bakPath)
	if err != nil {
		t.Fatalf("Open backup: %v", err)
	}
	defer bak.Close()

	loaded, err := bak.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances from backup: %v", err)
	}
	if len(loaded) != 4 {
		t.Fatalf("expected backup to hold the 4 pre-sweep rows, got %d", len(loaded))
	}
}
