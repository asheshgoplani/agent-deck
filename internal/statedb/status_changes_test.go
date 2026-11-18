package statedb

import (
	"testing"
	"time"
)

// status_changes records when each session's status last changed, from
// every write path (needs-me "waiting since", PR 3170 re-review F1).

func readStamp(t *testing.T, db *StateDB, id string) (StatusStamp, bool) {
	t.Helper()
	stamps, err := db.ReadStatusChanges()
	if err != nil {
		t.Fatalf("ReadStatusChanges: %v", err)
	}
	s, ok := stamps[id]
	return s, ok
}

func within(t *testing.T, got, lo, hi time.Time) {
	t.Helper()
	if got.Before(lo.Add(-time.Second)) || got.After(hi.Add(time.Second)) {
		t.Fatalf("stamp %v not within [%v, %v]", got, lo, hi)
	}
}

func TestStatusChangesEveryWritePathStampsAChange(t *testing.T) {
	writers := map[string]func(db *StateDB, id string) error{
		"WriteStatus": func(db *StateDB, id string) error { return db.WriteStatus(id, "waiting", "claude") },
		"WriteStatusIfCurrent": func(db *StateDB, id string) error {
			_, err := db.WriteStatusIfCurrent(id, "idle", "waiting", "claude")
			return err
		},
		"PersistInstanceStatusesTx": func(db *StateDB, id string) error {
			return db.PersistInstanceStatusesTx([]InstanceStatusUpdate{{ID: id, Status: "waiting"}})
		},
		"SaveInstances": func(db *StateDB, id string) error {
			r := ackRow(id)
			r.Status = "waiting"
			return db.SaveInstances([]*InstanceRow{r})
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			if err := db.SaveInstances([]*InstanceRow{ackRow("s1")}); err != nil {
				t.Fatal(err)
			}
			before := time.Now()
			if err := write(db, "s1"); err != nil {
				t.Fatal(err)
			}
			after := time.Now()
			s, ok := readStamp(t, db, "s1")
			if !ok || s.Status != "waiting" {
				t.Fatalf("stamp = %+v %v, want status waiting", s, ok)
			}
			within(t, s.At, before, after)
		})
	}
}

func TestStatusChangesSameStatusKeepsTheStamp(t *testing.T) {
	db := newTestDB(t)
	r := ackRow("s1")
	r.Status = "waiting"
	if err := db.SaveInstances([]*InstanceRow{r}); err != nil {
		t.Fatal(err)
	}
	first, ok := readStamp(t, db, "s1")
	if !ok {
		t.Fatal("no stamp for a new waiting row")
	}
	time.Sleep(20 * time.Millisecond)
	// Same status through every writer, including the full-row
	// INSERT OR REPLACE save: the stamp must not move.
	if err := db.WriteStatus("s1", "waiting", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := db.PersistInstanceStatusesTx([]InstanceStatusUpdate{{ID: "s1", Status: "waiting"}}); err != nil {
		t.Fatal(err)
	}
	r.Title = "renamed"
	if err := db.SaveInstances([]*InstanceRow{r}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveInstance(r); err != nil {
		t.Fatal(err)
	}
	got, _ := readStamp(t, db, "s1")
	if !got.At.Equal(first.At) || got.Status != "waiting" {
		t.Fatalf("stamp moved on a same-status write: %+v -> %+v", first, got)
	}
}

func TestStatusChangesDeleteDropsTheStamp(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{ackRow("s1")}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteInstance("s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := readStamp(t, db, "s1"); ok {
		t.Fatal("stamp kept after the session was deleted")
	}
}

func TestStatusChangesMigrateIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := db.Migrate(); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := db.SaveInstances([]*InstanceRow{ackRow("s1")}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteStatus("s1", "waiting", "claude"); err != nil {
		t.Fatal(err)
	}
	if s, ok := readStamp(t, db, "s1"); !ok || s.Status != "waiting" {
		t.Fatalf("stamp after a second Migrate = %+v %v", s, ok)
	}
}
