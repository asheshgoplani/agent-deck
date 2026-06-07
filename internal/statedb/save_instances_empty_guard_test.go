package statedb

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// S1 data-loss safeguard (2026-06-04 incident, third of its class).
//
// Root cause: SaveInstances([]) ran an unconditional `DELETE FROM instances`
// inside its DELETE+re-insert sweep. A stray empty-payload save therefore wiped
// the live personal-profile index. These tests pin the guarded behavior:
//
//	(a) SaveInstances([]) on a POPULATED table refuses (returns
//	    ErrRefusingEmptySweep) and leaves every row intact.
//	(b) SaveInstances([]) on an ALREADY-EMPTY table is a no-op (no error).
//	(c) ClearAllInstances() is the explicit escape hatch and DOES empty a
//	    populated table.
//	(d) A normal non-empty SaveInstances still replaces the set correctly.

func seedInstances(t *testing.T, db *StateDB, ids ...string) {
	t.Helper()
	now := time.Now()
	rows := make([]*InstanceRow, 0, len(ids))
	for i, id := range ids {
		rows = append(rows, &InstanceRow{
			ID:          id,
			Title:       id,
			ProjectPath: "/" + id,
			GroupPath:   "grp",
			Order:       i,
			Tool:        "claude",
			Status:      "idle",
			CreatedAt:   now,
			ToolData:    json.RawMessage("{}"),
		})
	}
	if err := db.SaveInstances(rows); err != nil {
		t.Fatalf("seed SaveInstances: %v", err)
	}
}

func countInstances(t *testing.T, db *StateDB) int {
	t.Helper()
	loaded, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	return len(loaded)
}

// (a) Empty payload on a populated table must be refused, rows preserved.
func TestSaveInstances_EmptyPayloadOnPopulatedTable_Refused(t *testing.T) {
	db := newTestDB(t)
	seedInstances(t, db, "a", "b", "c")

	err := db.SaveInstances(nil)
	if err == nil {
		t.Fatalf("expected SaveInstances([]) on populated table to return an error, got nil (rows would be wiped)")
	}
	if !errors.Is(err, ErrRefusingEmptySweep) {
		t.Fatalf("expected ErrRefusingEmptySweep, got %v", err)
	}

	if n := countInstances(t, db); n != 3 {
		t.Fatalf("expected 3 rows preserved after refused sweep, got %d", n)
	}
}

// (b) Empty payload on an already-empty table is a benign no-op.
func TestSaveInstances_EmptyPayloadOnEmptyTable_NoOp(t *testing.T) {
	db := newTestDB(t)

	if n := countInstances(t, db); n != 0 {
		t.Fatalf("expected empty table to start, got %d rows", n)
	}
	if err := db.SaveInstances(nil); err != nil {
		t.Fatalf("expected no-op (nil error) on empty table, got %v", err)
	}
	if n := countInstances(t, db); n != 0 {
		t.Fatalf("expected table to remain empty, got %d rows", n)
	}
}

// (c) The explicit escape hatch DOES empty a populated table.
func TestClearAllInstances_EmptiesPopulatedTable(t *testing.T) {
	db := newTestDB(t)
	seedInstances(t, db, "a", "b")

	if err := db.ClearAllInstances(); err != nil {
		t.Fatalf("ClearAllInstances: %v", err)
	}
	if n := countInstances(t, db); n != 0 {
		t.Fatalf("expected table emptied by ClearAllInstances, got %d rows", n)
	}

	// And on an already-empty table it's a clean no-op.
	if err := db.ClearAllInstances(); err != nil {
		t.Fatalf("ClearAllInstances on empty table: %v", err)
	}
}

// (d) Normal non-empty saves still replace the set (delete-not-in semantics).
func TestSaveInstances_NonEmpty_StillReplaces(t *testing.T) {
	db := newTestDB(t)
	seedInstances(t, db, "a", "b", "c")

	// Save a set that drops "c" and keeps a, b — c should be swept.
	now := time.Now()
	rows := []*InstanceRow{
		{ID: "a", Title: "a", ProjectPath: "/a", GroupPath: "grp", Order: 0, Tool: "claude", Status: "idle", CreatedAt: now, ToolData: json.RawMessage("{}")},
		{ID: "b", Title: "b", ProjectPath: "/b", GroupPath: "grp", Order: 1, Tool: "claude", Status: "idle", CreatedAt: now, ToolData: json.RawMessage("{}")},
	}
	if err := db.SaveInstances(rows); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	loaded, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 rows after replace, got %d", len(loaded))
	}
	for _, r := range loaded {
		if r.ID == "c" {
			t.Fatalf("expected row c to be swept, but it survived")
		}
	}
}
