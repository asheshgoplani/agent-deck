package statedb

import (
	"path/filepath"
	"testing"
	"time"
)

// The generation bounds the autonomous fork chain, so it must survive a TUI
// restart — an in-memory counter would reset and let the chain run forever.
func TestHandoffGeneration_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	row := &InstanceRow{
		ID:          "sess-1",
		Title:       "worker",
		ProjectPath: "/tmp/p",
		GroupPath:   "my-sessions",
		Tool:        "claude",
		Status:      "running",
		CreatedAt:   time.Unix(1000, 0),
	}
	if err := db.SaveInstances([]*InstanceRow{row}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	// A human-started session is generation 0.
	got, err := db.ReadHandoffGeneration("sess-1")
	if err != nil {
		t.Fatalf("ReadHandoffGeneration(unset): %v", err)
	}
	if got != 0 {
		t.Errorf("unset generation = %d, want 0", got)
	}

	if err := db.WriteHandoffGeneration("sess-1", 2); err != nil {
		t.Fatalf("WriteHandoffGeneration: %v", err)
	}
	got, err = db.ReadHandoffGeneration("sess-1")
	if err != nil {
		t.Fatalf("ReadHandoffGeneration: %v", err)
	}
	if got != 2 {
		t.Errorf("generation = %d, want 2", got)
	}
}

// The generation shares the tool_data blob with handoff state; writing one must
// not clobber the other, or a restart would lose the chain bound.
func TestHandoffGeneration_DoesNotClobberHandoffState(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db.SaveInstances([]*InstanceRow{{
		ID: "sess-1", Title: "w", ProjectPath: "/tmp/p", GroupPath: "my-sessions",
		Tool: "claude", Status: "running", CreatedAt: time.Unix(1000, 0),
	}}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	trig := time.Unix(1700000000, 0)
	if err := db.WriteHandoffState("sess-1", "wait_handoff", trig); err != nil {
		t.Fatalf("WriteHandoffState: %v", err)
	}
	if err := db.WriteHandoffGeneration("sess-1", 3); err != nil {
		t.Fatalf("WriteHandoffGeneration: %v", err)
	}

	state, at, err := db.ReadHandoffState("sess-1")
	if err != nil {
		t.Fatalf("ReadHandoffState: %v", err)
	}
	if state != "wait_handoff" || !at.Equal(trig) {
		t.Errorf("handoff state clobbered: got (%q,%v), want (wait_handoff,%v)", state, at, trig)
	}
	gen, err := db.ReadHandoffGeneration("sess-1")
	if err != nil {
		t.Fatalf("ReadHandoffGeneration: %v", err)
	}
	if gen != 3 {
		t.Errorf("generation = %d, want 3", gen)
	}
}
