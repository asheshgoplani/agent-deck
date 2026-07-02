package statedb

import (
	"encoding/json"
	"testing"
	"time"
)

// #1550: UpsertInstances is the sweep-free write path used by every routine save
// (Storage.SaveWithGroups). These tests pin the property the fix depends on:
// a row absent from the payload is NEVER deleted, so a writer holding a stale
// snapshot cannot destroy sessions another process created after that
// snapshot was loaded. SaveInstances (the DELETE-NOT-IN sweep, kept for
// whole-table replaces like the JSON->SQLite migration) retains its S1/S2
// guards and tests unchanged.

// A payload missing existing rows must leave them untouched (no sweep).
func TestUpsertInstances_PreservesRowsAbsentFromPayload(t *testing.T) {
	db := newTestDB(t)
	seedInstances(t, db, "a", "b", "c")

	// Upsert a payload that only knows about "a" (stale snapshot) plus a new
	// row "d" — b and c must survive, unlike SaveInstances' sweep.
	now := time.Now()
	rows := []*InstanceRow{
		{ID: "a", Title: "a-renamed", ProjectPath: "/a", GroupPath: "grp", Order: 0, Tool: "claude", Status: "idle", CreatedAt: now, ToolData: json.RawMessage("{}")},
		{ID: "d", Title: "d", ProjectPath: "/d", GroupPath: "grp", Order: 3, Tool: "claude", Status: "idle", CreatedAt: now, ToolData: json.RawMessage("{}")},
	}
	if err := db.UpsertInstances(rows); err != nil {
		t.Fatalf("UpsertInstances: %v", err)
	}

	loaded, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	byID := map[string]*InstanceRow{}
	for _, r := range loaded {
		byID[r.ID] = r
	}
	if len(loaded) != 4 {
		t.Fatalf("expected 4 rows (a,b,c,d) after upsert, got %d", len(loaded))
	}
	for _, id := range []string{"b", "c"} {
		if byID[id] == nil {
			t.Fatalf("row %q absent from the upsert payload must survive, but was deleted", id)
		}
	}
	if byID["a"] == nil || byID["a"].Title != "a-renamed" {
		t.Fatalf("expected row a updated to title %q, got %+v", "a-renamed", byID["a"])
	}
	if byID["d"] == nil {
		t.Fatalf("expected new row d inserted")
	}
}

// An empty upsert payload is a benign no-op on a populated table — no error,
// no rows touched (contrast S1: SaveInstances([]) refuses with
// ErrRefusingEmptySweep because its sweep WOULD wipe the table).
func TestUpsertInstances_EmptyPayload_NoOp(t *testing.T) {
	db := newTestDB(t)
	seedInstances(t, db, "a", "b")

	if err := db.UpsertInstances(nil); err != nil {
		t.Fatalf("expected empty upsert to be a no-op, got %v", err)
	}
	if n := countInstances(t, db); n != 2 {
		t.Fatalf("expected 2 rows preserved after empty upsert, got %d", n)
	}
}
