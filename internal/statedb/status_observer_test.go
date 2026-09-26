package statedb

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestWriteStatusRacingWritersPublishOneEdge: the TUI and the notify daemon
// both read "waiting" and both write "running" through their own
// connections. Only the write that changed the row may publish the edge
// (review of the macapp core surface: both published, so a client saw the
// same session.status transition twice).
func TestWriteStatusRacingWritersPublishOneEdge(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "profiles", "p", "state.db")
	open := func() *StateDB {
		db, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
		return db
	}
	tui, daemon := open(), open()
	now := time.Now()
	if err := tui.SaveInstance(&InstanceRow{ID: "s1", Title: "t", ProjectPath: "/p", GroupPath: "g", Tool: "claude", Status: "waiting", TmuxSession: "agentdeck_t", CreatedAt: now, LastAccessed: now}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var edges []StatusChange
	SetStatusChangeObserver(func(c StatusChange) {
		mu.Lock()
		edges = append(edges, c)
		mu.Unlock()
	})
	t.Cleanup(func() { SetStatusChangeObserver(nil) })

	// The daemon's whole write lands between the TUI's read and its update.
	interleaved := false
	afterStatusRead = func() {
		if interleaved {
			return
		}
		interleaved = true
		if err := daemon.WriteStatus("s1", "running", "claude"); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterStatusRead = nil })
	if err := tui.WriteStatus("s1", "running", "claude"); err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].From != "waiting" || edges[0].To != "running" {
		t.Fatalf("one waiting->running edge published %d times: %+v", len(edges), edges)
	}
	var status string
	if err := tui.db.QueryRow(`SELECT status FROM instances WHERE id = 's1'`).Scan(&status); err != nil || status != "running" {
		t.Fatalf("status = %q, %v", status, err)
	}
}

// TestWriteStatusWithoutObserverIsUnchanged: with no observer (the default)
// a write still updates status and tool.
func TestWriteStatusWithoutObserverIsUnchanged(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	if err := db.SaveInstance(&InstanceRow{ID: "s2", Title: "t", ProjectPath: "/p", GroupPath: "g", Tool: "claude", Status: "idle", CreatedAt: now, LastAccessed: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteStatus("s2", "waiting", "codex"); err != nil {
		t.Fatal(err)
	}
	var status, tool string
	if err := db.db.QueryRow(`SELECT status, tool FROM instances WHERE id = 's2'`).Scan(&status, &tool); err != nil || status != "waiting" || tool != "codex" {
		t.Fatalf("status/tool = %q/%q, %v", status, tool, err)
	}
}
