package statedb

import (
	"path/filepath"
	"testing"
	"time"
)

// Issue #2986 item 2: a full-row save (SaveInstance / UpsertInstances /
// SaveInstances) is an INSERT OR REPLACE whose column list has no
// acknowledged / acknowledged_at, so it used to reset both to their
// defaults. The seen mark is written only by targeted paths (SetAcknowledged,
// the running-write reset in WriteStatus & co.), so a full save from a
// process whose snapshot predates another process's ack must carry the stored
// mark over, not wipe it.

func openSharedPair(t *testing.T) (*StateDB, *StateDB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	open := func() *StateDB {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := db.Migrate(); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	return open(), open()
}

func ackTestRow(status string) *InstanceRow {
	now := time.Now()
	return &InstanceRow{
		ID: "s1", Title: "one", ProjectPath: "/tmp/p", GroupPath: "g",
		Tool: "claude", Status: status, TmuxSession: "agentdeck_s1",
		CreatedAt: now, LastAccessed: now,
	}
}

func readAck(t *testing.T, db *StateDB) StatusRow {
	t.Helper()
	all, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	row, ok := all["s1"]
	if !ok {
		t.Fatalf("row s1 missing")
	}
	return row
}

func TestFullSave_FromOtherProcess_KeepsSeenMark(t *testing.T) {
	saves := map[string]func(db *StateDB, row *InstanceRow) error{
		"SaveInstance":    func(db *StateDB, row *InstanceRow) error { return db.SaveInstance(row) },
		"UpsertInstances": func(db *StateDB, row *InstanceRow) error { return db.UpsertInstances([]*InstanceRow{row}) },
		"SaveInstances":   func(db *StateDB, row *InstanceRow) error { return db.SaveInstances([]*InstanceRow{row}) },
	}
	for name, save := range saves {
		t.Run(name, func(t *testing.T) {
			a, b := openSharedPair(t)
			if err := a.SaveInstance(ackTestRow("waiting")); err != nil {
				t.Fatalf("seed: %v", err)
			}
			// Process A: the user opens the session; A writes the seen mark.
			if err := a.SetAcknowledged("s1", true); err != nil {
				t.Fatalf("SetAcknowledged: %v", err)
			}
			before := readAck(t, a)
			if !before.Acknowledged || before.AcknowledgedAt.IsZero() {
				t.Fatalf("precondition: ack not stored: %+v", before)
			}

			// Process B: holds a snapshot that never saw the ack and does a
			// full save of the row (e.g. a title edit).
			stale := ackTestRow("waiting")
			stale.Title = "renamed by B"
			if err := save(b, stale); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			after := readAck(t, a)
			if !after.Acknowledged {
				t.Fatalf("%s from another process reset acknowledged (before %+v, after %+v)", name, before, after)
			}
			if !after.AcknowledgedAt.Equal(before.AcknowledgedAt) {
				t.Fatalf("%s from another process changed acknowledged_at: before %v, after %v", name, before.AcknowledgedAt, after.AcknowledgedAt)
			}
		})
	}
}

// The saving process's own targeted ack write still wins: clearing the mark
// ("mark unread") and then saving must not resurrect it, and setting it then
// saving keeps it.
func TestFullSave_OwnAckChangeWins(t *testing.T) {
	a, b := openSharedPair(t)
	if err := a.SaveInstance(ackTestRow("waiting")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := a.SetAcknowledged("s1", true); err != nil {
		t.Fatalf("SetAcknowledged: %v", err)
	}
	// B marks it unread through the dedicated path, then does a full save.
	if err := b.SetAcknowledged("s1", false); err != nil {
		t.Fatalf("SetAcknowledged(false): %v", err)
	}
	if err := b.UpsertInstances([]*InstanceRow{ackTestRow("waiting")}); err != nil {
		t.Fatalf("UpsertInstances: %v", err)
	}
	if got := readAck(t, a); got.Acknowledged || !got.AcknowledgedAt.IsZero() {
		t.Fatalf("own unread write lost after full save: %+v", got)
	}
	// And re-acking then saving keeps the new mark.
	if err := b.SetAcknowledged("s1", true); err != nil {
		t.Fatalf("SetAcknowledged: %v", err)
	}
	if err := b.SaveInstance(ackTestRow("idle")); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
	if got := readAck(t, a); !got.Acknowledged || got.AcknowledgedAt.IsZero() {
		t.Fatalf("own ack lost after full save: %+v", got)
	}
}

// A full save that writes status "running" is a running write: like
// WriteStatus it clears the seen mark and its visit time together.
func TestFullSave_RunningStatusClearsSeenMark(t *testing.T) {
	for _, upsert := range []bool{false, true} {
		a, b := openSharedPair(t)
		if err := a.SaveInstance(ackTestRow("waiting")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := a.SetAcknowledged("s1", true); err != nil {
			t.Fatalf("SetAcknowledged: %v", err)
		}
		var err error
		if upsert {
			err = b.UpsertInstances([]*InstanceRow{ackTestRow("running")})
		} else {
			err = b.SaveInstance(ackTestRow("running"))
		}
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if got := readAck(t, a); got.Acknowledged || !got.AcknowledgedAt.IsZero() {
			t.Fatalf("upsert=%v: running full save kept the seen mark: %+v", upsert, got)
		}
	}
}
