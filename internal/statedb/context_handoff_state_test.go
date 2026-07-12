package statedb

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHandoffState_RoundTrip(t *testing.T) {
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

	// Unset: empty state, zero time.
	gotState, gotAt, err := db.ReadHandoffState("sess-1")
	if err != nil {
		t.Fatalf("ReadHandoffState(unset): %v", err)
	}
	if gotState != "" || !gotAt.IsZero() {
		t.Errorf("unset = (%q,%v), want empty/zero", gotState, gotAt)
	}

	trig := time.Unix(1700000000, 0)
	if err := db.WriteHandoffState("sess-1", "wait_handoff", trig); err != nil {
		t.Fatalf("WriteHandoffState: %v", err)
	}
	gotState, gotAt, err = db.ReadHandoffState("sess-1")
	if err != nil {
		t.Fatalf("ReadHandoffState: %v", err)
	}
	if gotState != "wait_handoff" {
		t.Errorf("state = %q, want wait_handoff", gotState)
	}
	if gotAt.Unix() != trig.Unix() {
		t.Errorf("triggeredAt = %v, want %v", gotAt, trig)
	}
}

func TestHandoffState_SurvivesFullSave(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	row := &InstanceRow{ID: "s", Title: "t", ProjectPath: "/p", GroupPath: "g", Tool: "claude", Status: "running", CreatedAt: time.Unix(1, 0)}
	if err := db.SaveInstances([]*InstanceRow{row}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	if err := db.WriteHandoffState("s", "wrap_requested", time.Unix(50, 0)); err != nil {
		t.Fatalf("WriteHandoffState: %v", err)
	}
	// A subsequent full-table save (e.g. status change) must not clobber the key.
	row.Status = "waiting"
	if err := db.SaveInstances([]*InstanceRow{row}); err != nil {
		t.Fatalf("SaveInstances#2: %v", err)
	}
	gotState, _, err := db.ReadHandoffState("s")
	if err != nil {
		t.Fatalf("ReadHandoffState: %v", err)
	}
	if gotState != "wrap_requested" {
		t.Errorf("state after full save = %q, want wrap_requested", gotState)
	}
}
