package session

import (
	"fmt"
	"path/filepath"
	"testing"
)

func remoteSyncTestRows(n int) []RemoteSyncRow {
	rows := make([]RemoteSyncRow, n)
	for i := range rows {
		rows[i] = RemoteSyncRow{RemoteSessionInfo: RemoteSessionInfo{ID: fmt.Sprintf("id-%05d", i), Title: "t", Status: "stopped"}, Order: i}
	}
	return rows
}

// More deletions than the tombstone cap keep the newest RemoteSyncTombstoneCap
// and raise the floor, so a cursor taken before them resyncs in full while a
// cursor taken after the dropped ones still gets a delta.
func TestRemoteSyncAnswer_TombstoneCapRaisesFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), RemoteSyncJournalName)
	n := RemoteSyncTombstoneCap + 10
	first, err := RemoteSyncAnswer(path, remoteSyncTestRows(n), RemoteSyncRequest{Scope: RemoteSyncScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Full || len(first.Sessions) != n {
		t.Fatalf("first pass: full=%v rows=%d", first.Full, len(first.Sessions))
	}
	gone, err := RemoteSyncAnswer(path, nil, RemoteSyncRequest{Scope: RemoteSyncScopeAll, Cursor: first.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if !gone.Full || len(gone.Sessions) != 0 {
		t.Fatalf("cursor below the raised floor: want an empty full resync, got full=%v rows=%d removed=%d", gone.Full, len(gone.Sessions), len(gone.Removed))
	}
	j := loadRemoteSyncJournal(path)
	if len(j.Gone) != RemoteSyncTombstoneCap || j.Floor != uint64(n+10) {
		t.Fatalf("journal keeps %d tombstones with floor %d, want %d and %d", len(j.Gone), j.Floor, RemoteSyncTombstoneCap, n+10)
	}
	atFloor := formatRemoteSyncCursor(j.Epoch, j.Floor)
	d, err := RemoteSyncAnswer(path, nil, RemoteSyncRequest{Scope: RemoteSyncScopeAll, Cursor: atFloor})
	if err != nil {
		t.Fatal(err)
	}
	if d.Full || len(d.Removed) != RemoteSyncTombstoneCap {
		t.Fatalf("cursor at the floor: full=%v removed=%d", d.Full, len(d.Removed))
	}
}

// A row that comes back after being deleted loses its tombstone and is an
// upsert again.
func TestRemoteSyncAnswer_RecreatedRowDropsTombstone(t *testing.T) {
	path := filepath.Join(t.TempDir(), RemoteSyncJournalName)
	rows := remoteSyncTestRows(2)
	env, err := RemoteSyncAnswer(path, rows, RemoteSyncRequest{})
	if err != nil {
		t.Fatal(err)
	}
	env, err = RemoteSyncAnswer(path, rows[:1], RemoteSyncRequest{Cursor: env.Cursor})
	if err != nil || len(env.Removed) != 1 {
		t.Fatalf("delete: %+v %v", env, err)
	}
	env, err = RemoteSyncAnswer(path, rows, RemoteSyncRequest{Cursor: env.Cursor})
	if err != nil || len(env.Removed) != 0 || len(env.Sessions) != 1 || env.Sessions[0].ID != rows[1].ID {
		t.Fatalf("recreate: %+v %v", env, err)
	}
	if j := loadRemoteSyncJournal(path); len(j.Gone) != 0 {
		t.Fatalf("tombstone kept after recreate: %v", j.Gone)
	}
}

func TestParseRemoteSyncToken(t *testing.T) {
	for _, tc := range []struct {
		tok  string
		want int
		ok   bool
	}{
		{"ab.12", 1, true},
		{"ab.12.3", 2, true},
		{"ab.12", 2, false},
		{".12", 1, false},
		{"ab.x", 1, false},
		{"ab.-1", 1, false},
		{"", 1, false},
	} {
		if _, _, ok := parseRemoteSyncToken(tc.tok, tc.want); ok != tc.ok {
			t.Errorf("parseRemoteSyncToken(%q, %d) ok=%v, want %v", tc.tok, tc.want, ok, tc.ok)
		}
	}
}
