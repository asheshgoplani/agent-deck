package statedb

import (
	"testing"
	"time"
)

// The `u` key (mark unread) clears the acknowledged flag with a targeted write
// and then saves the accompanying status. The TUI is both writer and reader of
// this database, so that write must be identifiable as its own: otherwise the
// save reads the bump as another process's change, aborts, and reloads --
// rebuilding acknowledged from the stale stored status and undoing the mark.

func ackRow(id string) *InstanceRow {
	return &InstanceRow{
		ID:          id,
		Title:       "target",
		ProjectPath: "/tmp/proj",
		GroupPath:   "Ungrouped",
		Command:     "claude",
		Tool:        "claude",
		Status:      "idle",
		TmuxSession: "agentdeck_target_deadbeef",
		CreatedAt:   time.Now(),
	}
}

func TestSetAcknowledgedStamped_StampsIdentifyOurOwnBump(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{ackRow("ack-stamped")}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	loadedAt, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}

	stamps, err := db.SetAcknowledgedStamped("ack-stamped", false)
	if err != nil {
		t.Fatalf("SetAcknowledgedStamped: %v", err)
	}
	current, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified after write: %v", err)
	}

	if !stamps.SoleWriterSince(loadedAt, current) {
		t.Errorf("stamps do not identify our own bump: before=%d after=%d loadedAt=%d current=%d "+
			"(the TUI reads its own acknowledged write as an external change, aborts the save "+
			"that would persist the new status, and reloads the stale one)",
			stamps.Before, stamps.After, loadedAt, current)
	}
}

// A write by somebody else between our load and our write must NOT be adopted:
// the database really has moved on and the abort is the correct outcome.
func TestSetAcknowledgedStamped_ForeignWriteIsNotAdopted(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{ackRow("ack-foreign")}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	loadedAt, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}

	// Another process writes after we loaded.
	if err := db.Touch(); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	stamps, err := db.SetAcknowledgedStamped("ack-foreign", false)
	if err != nil {
		t.Fatalf("SetAcknowledgedStamped: %v", err)
	}
	current, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified after write: %v", err)
	}

	if stamps.SoleWriterSince(loadedAt, current) {
		t.Error("adopted a bump that followed another process's write: the TUI would then " +
			"overwrite whatever that process changed")
	}
}

// SetAcknowledged keeps its existing signature and behavior for the callers
// that do not need the stamps.
func TestSetAcknowledged_StillPersistsAndTouches(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{ackRow("ack-plain")}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	before, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}

	if err := db.SetAcknowledged("ack-plain", true); err != nil {
		t.Fatalf("SetAcknowledged: %v", err)
	}

	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	if !statuses["ack-plain"].Acknowledged {
		t.Error("acknowledged flag was not persisted")
	}
	after, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified after write: %v", err)
	}
	if after <= before {
		t.Errorf("last_modified did not advance (%d -> %d): peers poll it to notice the change",
			before, after)
	}
}
