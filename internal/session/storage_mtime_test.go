package session

import (
	"testing"
	"time"
)

// GetFileMtime is the clock the TUI's save path uses to detect that another
// process wrote to the profile DB (home.go saveInstancesWithForce). It used to
// os.Stat(state.db) — but SQLite runs in WAL mode, so a committed write lands in
// state.db-wal and leaves state.db's mtime untouched until a checkpoint. The
// guard could therefore never fire, and the TUI would overwrite CLI writes.
//
// GetFileMtime must instead reflect the DB's own last_modified metadata, which
// is the same signal StorageWatcher polls.

func TestGetFileMtimeAdvancesAfterDBWrite(t *testing.T) {
	s := newTestStorage(t)

	before, err := s.GetFileMtime()
	if err != nil {
		t.Fatalf("GetFileMtime (before): %v", err)
	}

	// A committed write through the DB — exactly what a separate `agent-deck
	// session archive` process does. In WAL mode this does not touch state.db.
	if err := s.GetDB().Touch(); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	after, err := s.GetFileMtime()
	if err != nil {
		t.Fatalf("GetFileMtime (after): %v", err)
	}

	if !after.After(before) {
		t.Errorf("GetFileMtime did not advance after a committed DB write: "+
			"before=%v after=%v (external-change detection is blind; "+
			"the TUI will clobber out-of-process writes)", before, after)
	}
}

// A zero baseline would make `currentMtime.After(ourLoadMtime)` in the save path
// trivially false, so GetFileMtime must return a real timestamp for a written DB
// rather than the zero time.
func TestGetFileMtimeIsNonZeroAfterWrite(t *testing.T) {
	s := newTestStorage(t)
	if err := s.GetDB().Touch(); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, err := s.GetFileMtime()
	if err != nil {
		t.Fatalf("GetFileMtime: %v", err)
	}
	if got.IsZero() {
		t.Fatal("GetFileMtime returned zero time after a write; guard would never fire")
	}
	if time.Since(got) > time.Minute {
		t.Errorf("GetFileMtime returned a stale timestamp %v (now=%v)", got, time.Now())
	}
}
