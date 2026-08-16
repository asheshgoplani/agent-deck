package statedb

import (
	"errors"
	"testing"
	"time"
)

// A restart mints a new tmux session name and that name is the only handle
// anything has on the live process. WriteTmuxSession is the targeted write that
// records it (#1870); these pin the two properties the restart path depends on.

func TestWriteTmuxSession_UpdatesOnlyThatColumnAndTouches(t *testing.T) {
	db := newTestDB(t)

	row := &InstanceRow{
		ID:          "restart-target",
		Title:       "target",
		ProjectPath: "/tmp/proj",
		GroupPath:   "Ungrouped",
		Command:     "claude",
		Tool:        "claude",
		Status:      "running",
		TmuxSession: "agentdeck_target_deadbeef",
		CreatedAt:   time.Now(),
	}
	if err := db.SaveInstances([]*InstanceRow{row}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	before, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}

	if err := db.WriteTmuxSession("restart-target", "agentdeck_target_f00dcafe"); err != nil {
		t.Fatalf("WriteTmuxSession: %v", err)
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
	// The point of a targeted write is that it leaves everything else alone --
	// a whole-row save from a stale snapshot is what this replaces.
	if got.Title != "target" || got.Status != "running" || got.Tool != "claude" {
		t.Errorf("unrelated columns changed: title=%q status=%q tool=%q", got.Title, got.Status, got.Tool)
	}

	after, err := db.LastModified()
	if err != nil {
		t.Fatalf("LastModified: %v", err)
	}
	if after < before {
		t.Errorf("last_modified went backwards: %d -> %d", before, after)
	}
	if after == 0 {
		t.Error("last_modified is unset: peers poll it to notice the change, so without a touch " +
			"a running TUI keeps serving the dead name from its own snapshot")
	}
}

func TestWriteTmuxSession_UnknownInstanceIsNotSilentlyDropped(t *testing.T) {
	db := newTestDB(t)

	err := db.WriteTmuxSession("never-stored", "agentdeck_ghost_f00dcafe")
	if err == nil {
		t.Fatal("WriteTmuxSession succeeded for an instance with no row: SQLite reports a " +
			"zero-row UPDATE as success, so the caller would announce a durable write that " +
			"never happened")
	}
	if !errors.Is(err, ErrInstanceNotStored) {
		t.Errorf("error = %v, want ErrInstanceNotStored so callers can tell it apart from an I/O failure", err)
	}
}
