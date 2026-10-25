package statedb

import (
	"path/filepath"
	"testing"
	"time"
)

// TestWatcherDeliveryRefusals_CountsPerWatcherAndConductor: each refused
// routed event adds one to its (watcher, conductor) row, which keeps the time
// of the first and the last refusal; other pairs are counted apart.
func TestWatcherDeliveryRefusals_CountsPerWatcherAndConductor(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_800_000_000, 0)
	for i, at := range []time.Time{t0, t0.Add(time.Minute), t0.Add(2 * time.Minute)} {
		n, err := db.RecordWatcherDeliveryRefusal("w-hook", "demo", at)
		if err != nil || n != i+1 {
			t.Fatalf("refusal %d: total %d, %v; want %d", i, n, err, i+1)
		}
	}
	if n, err := db.RecordWatcherDeliveryRefusal("w-hook", "ops", t0); err != nil || n != 1 {
		t.Fatalf("ops: total %d, %v; want 1", n, err)
	}
	if n, err := db.RecordWatcherDeliveryRefusal("w-gh", "demo", t0); err != nil || n != 1 {
		t.Fatalf("w-gh: total %d, %v; want 1", n, err)
	}

	rows, err := db.LoadWatcherDeliveryRefusals("w-hook")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Conductor != "demo" || rows[0].Count != 3 ||
		!rows[0].FirstRefusedAt.Equal(t0) || !rows[0].LastRefusedAt.Equal(t0.Add(2*time.Minute)) ||
		rows[1].Conductor != "ops" || rows[1].Count != 1 {
		t.Fatalf("w-hook rows = %+v", rows)
	}
	all, err := db.LoadWatcherDeliveryRefusals("")
	if err != nil || len(all) != 3 {
		t.Fatalf("all rows = %+v, %v; want 3", all, err)
	}
}

// TestWatcherDeliveryRefusals_MigrateAddsTheTableToAnOlderDB: a state.db made
// before the refusal count (schema 13 without the table) gains it on the next
// Migrate, keeps its watcher rows, and a second Migrate is a no-op.
func TestWatcherDeliveryRefusals_MigrateAddsTheTableToAnOlderDB(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	// What a build before this change leaves behind.
	if _, err := db.db.Exec(`DROP TABLE watcher_delivery_refusals`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.SaveWatcher(&WatcherRow{ID: "w-hook", Name: "hook", Type: "webhook", Status: "running", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadWatcherDeliveryRefusals(""); err == nil {
		t.Fatal("the older DB already has the table; the test does not model it")
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate on an older DB: %v", err)
	}
	if n, err := db.RecordWatcherDeliveryRefusal("w-hook", "demo", now); err != nil || n != 1 {
		t.Fatalf("record after migrate: %d, %v", n, err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if w, err := db.LoadWatcherByName("hook"); err != nil || w == nil {
		t.Fatalf("watcher row after migrate: %+v, %v", w, err)
	}
	if rows, err := db.LoadWatcherDeliveryRefusals("w-hook"); err != nil || len(rows) != 1 || rows[0].Count != 1 {
		t.Fatalf("rows after the second Migrate = %+v, %v", rows, err)
	}
}
