package statedb

import (
	"encoding/json"
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
