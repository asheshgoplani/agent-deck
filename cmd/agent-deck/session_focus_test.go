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

	if err := resolveAndWriteFocus(db, []*session.Instance{inst}, inst.ID, now, false); err != nil {
		t.Fatalf("resolveAndWriteFocus valid id: %v", err)
	}

	raw, err := session.ReadFocusRequest(db)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	id, attach, fresh := session.DecodeFocusRequestAttach(raw, now, session.FocusRequestTTL)
	if id != inst.ID || !fresh || attach {
		t.Fatalf("stored request = (%q, attach=%v, fresh=%v), want (%q, false, true)", id, attach, fresh, inst.ID)
	}
}

func TestResolveAndWriteFocus_Attach(t *testing.T) {
	db := newTempStateDB(t)
	inst := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")
	now := time.Now().UnixNano()

	if err := resolveAndWriteFocus(db, []*session.Instance{inst}, inst.ID, now, true); err != nil {
		t.Fatalf("resolveAndWriteFocus attach: %v", err)
	}

	raw, err := session.ReadFocusRequest(db)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	id, attach, fresh := session.DecodeFocusRequestAttach(raw, now, session.FocusRequestTTL)
	if id != inst.ID || !fresh || !attach {
		t.Fatalf("stored request = (%q, attach=%v, fresh=%v), want (%q, true, true)", id, attach, fresh, inst.ID)
	}
}

func TestResolveAndWriteFocus_UnknownID(t *testing.T) {
	db := newTempStateDB(t)
	inst := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")

	err := resolveAndWriteFocus(db, []*session.Instance{inst}, "does-not-exist", time.Now().UnixNano(), false)
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
	if err := resolveAndWriteFocus(db, nil, "", time.Now().UnixNano(), false); err == nil {
		t.Fatal("empty id: want error, got nil")
	}
}

// fakeSwitcher records calls and returns a canned switched result, so routeFocus's
// attached-vs-list routing is testable without a real tmux server.
type fakeSwitcher struct {
	switched bool
	calls    int
	gotInst  *session.Instance
}

func (f *fakeSwitcher) switchInto(inst *session.Instance) (bool, error) {
	f.calls++
	f.gotInst = inst
	return f.switched, nil
}

// When a client is attached to the target's tmux server, the live switch wins and
// no focus_request row is written (otherwise the row would re-attach on detach).
func TestRouteFocus_AttachLiveSwitch_NoFocusRow(t *testing.T) {
	db := newTempStateDB(t)
	inst := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")
	sw := &fakeSwitcher{switched: true}
	now := time.Now().UnixNano()

	if err := routeFocus(db, []*session.Instance{inst}, inst.ID, now, true, sw); err != nil {
		t.Fatalf("routeFocus: %v", err)
	}
	if sw.calls != 1 || sw.gotInst != inst {
		t.Fatalf("switcher calls=%d gotInst=%v, want 1 call with the target instance", sw.calls, sw.gotInst)
	}
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("live switch must not write a focus_request, got %q", raw)
	}
}

// When no client is attached on the target's server (switched=false), routeFocus
// falls back to the foreground focus_request --attach path.
func TestRouteFocus_AttachFallback_WritesFocusRow(t *testing.T) {
	db := newTempStateDB(t)
	inst := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")
	sw := &fakeSwitcher{switched: false}
	now := time.Now().UnixNano()

	if err := routeFocus(db, []*session.Instance{inst}, inst.ID, now, true, sw); err != nil {
		t.Fatalf("routeFocus: %v", err)
	}
	raw, err := session.ReadFocusRequest(db)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	id, attach, fresh := session.DecodeFocusRequestAttach(raw, now, session.FocusRequestTTL)
	if id != inst.ID || !attach || !fresh {
		t.Fatalf("fallback row = (%q, attach=%v, fresh=%v), want (%q, true, true)", id, attach, fresh, inst.ID)
	}
}

// Without --attach there is nothing to switch into (the list isn't on screen), so
// the live switcher must never be consulted.
func TestRouteFocus_NoAttach_SkipsSwitcher(t *testing.T) {
	db := newTempStateDB(t)
	inst := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")
	sw := &fakeSwitcher{switched: true}
	now := time.Now().UnixNano()

	if err := routeFocus(db, []*session.Instance{inst}, inst.ID, now, false, sw); err != nil {
		t.Fatalf("routeFocus: %v", err)
	}
	if sw.calls != 0 {
		t.Fatalf("switcher consulted %d times for a non-attach focus, want 0", sw.calls)
	}
	raw, _ := session.ReadFocusRequest(db)
	id, attach, _ := session.DecodeFocusRequestAttach(raw, now, session.FocusRequestTTL)
	if id != inst.ID || attach {
		t.Fatalf("row = (%q, attach=%v), want (%q, false)", id, attach, inst.ID)
	}
}

// An unknown id is rejected before any switch attempt or row write.
func TestRouteFocus_UnknownID(t *testing.T) {
	db := newTempStateDB(t)
	inst := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")
	sw := &fakeSwitcher{switched: true}

	err := routeFocus(db, []*session.Instance{inst}, "nope", time.Now().UnixNano(), true, sw)
	if !errors.Is(err, errFocusNotFound) {
		t.Fatalf("unknown id err = %v, want errFocusNotFound", err)
	}
	if sw.calls != 0 {
		t.Fatalf("switcher consulted on unknown id (%d calls)", sw.calls)
	}
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("unknown id wrote a row: %q", raw)
	}
}
