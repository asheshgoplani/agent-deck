package statedb

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// Targeted single-row mutators must advertise their write via Touch(), the
// metadata timestamp StorageWatcher polls to detect out-of-process changes.
// A mutator that writes instances but leaves last_modified frozen is invisible
// to a running TUI, which then force-saves its stale in-memory snapshot over
// the change (SaveInstances is a DELETE-NOT-IN + INSERT OR REPLACE sweep).
// That is how `agent-deck session archive` silently reverted: the archive
// landed, the TUI never reloaded, and the next forced save undid every row.

func seedChangeSignalInstance(t *testing.T, db *StateDB, id string) {
	t.Helper()
	if err := db.SaveInstance(&InstanceRow{
		ID:          id,
		Title:       id,
		ProjectPath: "/tmp/project",
		GroupPath:   "grp",
		Tool:        "claude",
		Status:      "stopped",
		CreatedAt:   time.Now(),
		ToolData:    json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("seed SaveInstance: %v", err)
	}
}

// baselineLastModified seeds last_modified and returns it, so a mutator that
// never touches the key is distinguishable from one that advances it.
func baselineLastModified(t *testing.T, db *StateDB) int64 {
	t.Helper()
	if err := db.Touch(); err != nil {
		t.Fatalf("Touch (baseline): %v", err)
	}
	ts, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified (baseline): %v", err)
	}
	if ts == 0 {
		t.Fatalf("baseline last_modified is 0, want a seeded timestamp")
	}
	return ts
}

func TestSetArchivedBumpsLastModified(t *testing.T) {
	db := newTestDB(t)
	seedChangeSignalInstance(t, db, "arch-signal")
	before := baselineLastModified(t, db)

	if err := db.SetArchived("arch-signal", time.Unix(1783589599, 0).UTC()); err != nil {
		t.Fatalf("SetArchived: %v", err)
	}

	after, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}
	if after <= before {
		t.Errorf("SetArchived did not bump last_modified: before=%d after=%d "+
			"(a running TUI cannot see this write and will clobber it)", before, after)
	}
}

func TestSetArchivedUnarchiveBumpsLastModified(t *testing.T) {
	db := newTestDB(t)
	seedChangeSignalInstance(t, db, "unarch-signal")
	if err := db.SetArchived("unarch-signal", time.Unix(1783589599, 0).UTC()); err != nil {
		t.Fatalf("SetArchived(archive): %v", err)
	}
	before := baselineLastModified(t, db)

	// Unarchive is the zero-time write; it must announce itself too.
	if err := db.SetArchived("unarch-signal", time.Time{}); err != nil {
		t.Fatalf("SetArchived(unarchive): %v", err)
	}

	after, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}
	if after <= before {
		t.Errorf("SetArchived(unarchive) did not bump last_modified: before=%d after=%d",
			before, after)
	}
}

// TestCLIArchiveIsVisibleToAnotherProcessBeforeItSaves reproduces the original
// clobber end-to-end with two connections to one DB file: `tui` holds a snapshot
// loaded before the archive, `cli` archives a row.
//
// The clobber is not that SaveInstances sweeps — it legitimately does — but that
// the TUI had no way to learn it was holding a stale snapshot. This asserts the
// signal it polls actually advances, and then demonstrates the data loss that
// follows when a stale snapshot is written blind.
func TestCLIArchiveIsVisibleToAnotherProcessBeforeItSaves(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")

	tui, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(tui): %v", err)
	}
	defer tui.Close()
	if err := tui.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	cli, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(cli): %v", err)
	}
	defer cli.Close()

	seedChangeSignalInstance(t, tui, "clobber-1")

	// The TUI loads its in-memory snapshot and records the change signal it saw.
	staleSnapshot, err := tui.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	lastSeen, err := tui.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}

	// A separate process archives the row.
	if err := cli.SetArchived("clobber-1", time.Unix(1783589599, 0).UTC()); err != nil {
		t.Fatalf("SetArchived: %v", err)
	}

	// The TUI's watcher must be able to observe that write. If it cannot, it
	// keeps the stale snapshot and the next forced save silently reverts.
	now, err := tui.LastModified()
	if err != nil {
		t.Fatalf("LastModified (after): %v", err)
	}
	if now <= lastSeen {
		t.Fatalf("TUI cannot observe the CLI archive (last_modified %d -> %d); "+
			"it will save its stale snapshot and revert the archive", lastSeen, now)
	}

	// Demonstrate the loss the signal exists to prevent: writing the pre-archive
	// snapshot blind resets archived_at. This is why detection must work.
	if err := tui.SaveInstances(staleSnapshot); err != nil {
		t.Fatalf("SaveInstances(stale): %v", err)
	}
	rows, err := tui.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances (post-sweep): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if !rows[0].ArchivedAt.IsZero() {
		t.Fatal("expected the stale full-table sweep to clobber archived_at; " +
			"if it no longer does, this test's premise is stale")
	}
}

func TestSetAcknowledgedBumpsLastModified(t *testing.T) {
	db := newTestDB(t)
	seedChangeSignalInstance(t, db, "ack-signal")
	before := baselineLastModified(t, db)

	if err := db.SetAcknowledged("ack-signal", true); err != nil {
		t.Fatalf("SetAcknowledged: %v", err)
	}

	after, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}
	if after <= before {
		t.Errorf("SetAcknowledged did not bump last_modified: before=%d after=%d",
			before, after)
	}
}
