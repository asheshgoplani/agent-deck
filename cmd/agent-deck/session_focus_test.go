package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func newTempStateDB(t *testing.T) *statedb.StateDB {
	t.Helper()
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestResolveAndWriteFocus_ValidID(t *testing.T) {
	db := newTempStateDB(t)
	inst := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")
	now := time.Now().UnixNano()

	if err := resolveAndWriteFocus(db, []*session.Instance{inst}, inst.ID, now); err != nil {
		t.Fatalf("resolveAndWriteFocus valid id: %v", err)
	}

	raw, err := session.ReadFocusRequest(db)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	id, fresh := session.DecodeFocusRequest(raw, now, session.FocusRequestTTL)
	if id != inst.ID || !fresh {
		t.Fatalf("stored request = (%q, %v), want (%q, true)", id, fresh, inst.ID)
	}
}

func TestResolveAndWriteFocus_UnknownID(t *testing.T) {
	db := newTempStateDB(t)
	inst := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")

	err := resolveAndWriteFocus(db, []*session.Instance{inst}, "does-not-exist", time.Now().UnixNano())
	if !errors.Is(err, errFocusNotFound) {
		t.Fatalf("unknown id err = %v, want errFocusNotFound", err)
	}
	// No row should have been written.
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("unknown id wrote a row: %q", raw)
	}
}

func TestResolveAndWriteFocus_EmptyID(t *testing.T) {
	db := newTempStateDB(t)
	if err := resolveAndWriteFocus(db, nil, "", time.Now().UnixNano()); err == nil {
		t.Fatal("empty id: want error, got nil")
	}
}
