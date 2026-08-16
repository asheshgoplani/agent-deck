package statedb

import (
	"errors"
	"testing"
	"time"
)

// A restart mints a new tmux session name and that name is the only handle
// anything has on the live process. WriteRestartOutcome is the targeted write
// that records it, together with the status the restart ended in (#1870).

func restartOutcomeRow(id string) *InstanceRow {
	return &InstanceRow{
		ID:          id,
		Title:       "target",
		ProjectPath: "/tmp/proj",
		GroupPath:   "Ungrouped",
		Command:     "claude",
		Tool:        "claude",
		Status:      "error",
		TmuxSession: "agentdeck_target_deadbeef",
		CreatedAt:   time.Now(),
	}
}

func TestWriteRestartOutcome_UpdatesOnlyItsOwnColumns(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{restartOutcomeRow("restart-target")}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	if _, err := db.WriteRestartOutcome("restart-target", "agentdeck_target_f00dcafe", "waiting", "claude"); err != nil {
		t.Fatalf("WriteRestartOutcome: %v", err)
	}

	rows, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("LoadInstances returned %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.TmuxSession != "agentdeck_target_f00dcafe" {
		t.Errorf("tmux_session = %q, want the restart's new name: the stored name still points at "+
			"the session the restart killed, so agent-deck reports a live session as errored", got.TmuxSession)
	}
	if got.Status != "waiting" {
		t.Errorf("status = %q, want waiting: a row naming a live pane but still marked error "+
			"misreports the session just as badly as one naming a dead pane", got.Status)
	}
	// The point of a targeted write is that it leaves everything else alone. A
	// whole-row save from a stale snapshot is what this exists to avoid.
	if got.Title != "target" || got.ProjectPath != "/tmp/proj" || got.GroupPath != "Ungrouped" {
		t.Errorf("unrelated columns changed: title=%q path=%q group=%q",
			got.Title, got.ProjectPath, got.GroupPath)
	}
}

// TestWriteRestartOutcome_StampsIdentifyOurOwnBump pins the mechanism the TUI
// depends on. The write moves last_modified, and a TUI that cannot tell that
// bump apart from another process's change reads its own write as external and
// abandons the save that would have persisted the rest of the restart.
func TestWriteRestartOutcome_StampsIdentifyOurOwnBump(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{restartOutcomeRow("stamped")}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	loadedAt, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}

	stamps, err := db.WriteRestartOutcome("stamped", "agentdeck_target_f00dcafe", "waiting", "claude")
	if err != nil {
		t.Fatalf("WriteRestartOutcome: %v", err)
	}
	current, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}
	if !stamps.SoleWriterSince(loadedAt, current) {
		t.Fatalf("SoleWriterSince(%d, %d) = false for stamps %+v: nothing else wrote, so this "+
			"write must be recognisable as our own", loadedAt, current, stamps)
	}

	// Somebody else writes afterwards: our stamp is no longer what the database
	// reads, so the change IS external and the caller must not claim otherwise.
	if err := db.WriteStatus("stamped", "running", "claude"); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	if err := db.Touch(); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	after, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}
	if stamps.SoleWriterSince(loadedAt, after) {
		t.Error("SoleWriterSince = true after another writer bumped last_modified")
	}
}

// TestWriteStamps_SoleWriterSince_RejectsAPriorExternalChange is the half that
// protects against the lost update. A change that landed BEFORE our write is
// still a change we have not seen, and treating our own bump as proof of
// freshness would let a stale snapshot save proceed and revert it.
func TestWriteStamps_SoleWriterSince_RejectsAPriorExternalChange(t *testing.T) {
	const loadedAt = 1000
	// Something wrote at 1500, then we wrote at 2000.
	stamps := WriteStamps{Before: 1500, After: 2000}

	if stamps.SoleWriterSince(loadedAt, 2000) {
		t.Error("SoleWriterSince = true though another process wrote between the load and our write: " +
			"the caller would treat its stale snapshot as current and overwrite that process's change")
	}
	if !(WriteStamps{Before: 1000, After: 2000}).SoleWriterSince(loadedAt, 2000) {
		t.Error("SoleWriterSince = false when our write was the only change since the load")
	}
	if (WriteStamps{}).SoleWriterSince(loadedAt, 0) {
		t.Error("a zero WriteStamps claims sole authorship; it recorded nothing")
	}
}

func TestWriteRestartOutcome_UnknownInstanceIsNotSilentlyDropped(t *testing.T) {
	db := newTestDB(t)

	_, err := db.WriteRestartOutcome("never-stored", "agentdeck_ghost_f00dcafe", "waiting", "claude")
	if err == nil {
		t.Fatal("WriteRestartOutcome succeeded for an instance with no row: SQLite reports a " +
			"zero-row UPDATE as success, so the caller would announce a durable write that " +
			"never happened")
	}
	if !errors.Is(err, ErrInstanceNotStored) {
		t.Errorf("error = %v, want ErrInstanceNotStored so callers can tell it apart from an I/O failure", err)
	}
}
