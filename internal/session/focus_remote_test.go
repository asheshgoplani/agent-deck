package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// #2704: a local request must keep its exact pre-remote bytes, so a TUI of
// any age reads it the same way.
func TestFocusRequest_LocalBytesUnchanged(t *testing.T) {
	const now = int64(1_000_000_000_000)
	cases := []struct {
		attach bool
		want   string
	}{
		{false, `{"id":"abc","ts":1000000000000}`},
		{true, `{"id":"abc","ts":1000000000000,"attach":true}`},
	}
	for _, tc := range cases {
		old, err := EncodeFocusRequestAttach("abc", now, tc.attach)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		full, err := EncodeFocusRequestFull(FocusRequest{ID: "abc", TS: now, Attach: tc.attach})
		if err != nil {
			t.Fatalf("encode full: %v", err)
		}
		if old != tc.want || full != tc.want {
			t.Fatalf("attach=%v: encoded %q / %q, want %q", tc.attach, old, full, tc.want)
		}
	}
}

func TestFocusRequest_RemoteRoundTrip(t *testing.T) {
	const now = int64(1_000_000_000_000)
	in := FocusRequest{ID: "r-1", TS: now, Attach: true, Remote: "box"}
	val, err := EncodeFocusRequestFull(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if want := `{"id":"r-1","ts":1000000000000,"attach":true,"remote":"box"}`; val != want {
		t.Fatalf("encoded %q, want %q", val, want)
	}
	got, ok, fresh := ParseFocusRequest(val, now, FocusRequestTTL)
	if !ok || !fresh || got != in {
		t.Fatalf("parse = (%+v, ok=%v, fresh=%v), want (%+v, true, true)", got, ok, fresh, in)
	}
	if _, _, fresh := ParseFocusRequest(val, now+int64(11*time.Second), FocusRequestTTL); fresh {
		t.Fatal("a request older than the TTL must not be fresh")
	}
	// The old decoder still reads a remote request (and ignores the remote).
	id, attach, fresh := DecodeFocusRequestAttach(val, now, FocusRequestTTL)
	if id != "r-1" || !attach || !fresh {
		t.Fatalf("old decoder = (%q, %v, %v)", id, attach, fresh)
	}
}

func TestParseFocusRequest_Invalid(t *testing.T) {
	for _, val := range []string{"", "{not json", `{"id":"","ts":1,"remote":"box"}`} {
		if _, ok, _ := ParseFocusRequest(val, 1, FocusRequestTTL); ok {
			t.Fatalf("ParseFocusRequest(%q) ok=true, want false", val)
		}
	}
}

func TestTUIAttachRecord(t *testing.T) {
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadTUIAttach(db); ok {
		t.Fatal("no record expected on a fresh db")
	}
	if err := WriteTUIAttach(db, TUIAttach{Socket: "agent-deck", ClientPID: 42}); err != nil {
		t.Fatal(err)
	}
	if a, ok := ReadTUIAttach(db); !ok || a != (TUIAttach{Socket: "agent-deck", ClientPID: 42}) {
		t.Fatalf("read = %+v ok=%v", a, ok)
	}
	// Another client's clear leaves the record alone.
	_ = ClearTUIAttach(db, 7)
	if _, ok := ReadTUIAttach(db); !ok {
		t.Fatal("a clear for another pid removed the record")
	}
	_ = ClearTUIAttach(db, 42)
	if _, ok := ReadTUIAttach(db); ok {
		t.Fatal("record not cleared")
	}
}
