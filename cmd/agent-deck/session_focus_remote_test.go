package main

import (
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func fakeRemotes(names ...string) func(string) (bool, error) {
	return func(n string) (bool, error) {
		for _, name := range names {
			if name == n {
				return true, nil
			}
		}
		return false, nil
	}
}

// #2704: an unknown remote is rejected before anything is written or detached.
func TestRouteRemoteFocus_UnknownRemote(t *testing.T) {
	db := newTempStateDB(t)
	det := &fakeDetacher{}
	err := routeRemoteFocus(db, nil, "", "nope", "r-1", time.Now().UnixNano(), true, fakeRemotes("box"), det)
	if !errors.Is(err, errFocusRemoteUnknown) {
		t.Fatalf("err = %v, want errFocusRemoteUnknown", err)
	}
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("unknown remote wrote a row: %q", raw)
	}
	if det.calls != 0 {
		t.Fatalf("unknown remote detached %d times", det.calls)
	}
}

func TestRouteRemoteFocus_EmptyID(t *testing.T) {
	db := newTempStateDB(t)
	if err := routeRemoteFocus(db, nil, "", "box", "", time.Now().UnixNano(), false, fakeRemotes("box"), nil); err == nil {
		t.Fatal("empty id: want error")
	}
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("empty id wrote a row: %q", raw)
	}
}

// Select-only writes the remote request and never detaches anybody. The id is
// not checked locally: it belongs to the remote.
func TestRouteRemoteFocus_SelectOnlyNeverDetaches(t *testing.T) {
	db := newTempStateDB(t)
	det := &fakeDetacher{}
	now := time.Now().UnixNano()
	if err := routeRemoteFocus(db, []*session.Instance{instOnSocket("a", "agent-deck")}, "agent-deck", "box", "r-1", now, false, fakeRemotes("box"), det); err != nil {
		t.Fatalf("routeRemoteFocus: %v", err)
	}
	if det.calls != 0 {
		t.Fatalf("select-only detached %d times", det.calls)
	}
	raw, _ := session.ReadFocusRequest(db)
	fr, ok, fresh := session.ParseFocusRequest(raw, now, session.FocusRequestTTL)
	if !ok || !fresh || fr != (session.FocusRequest{ID: "r-1", TS: now, Remote: "box"}) {
		t.Fatalf("row = %+v ok=%v fresh=%v", fr, ok, fresh)
	}
}

// detachRecorder checks the request was written before the detach ran.
type detachRecorder struct {
	fakeDetacher
	sawRow string
	read   func() string
}

func (d *detachRecorder) detachClientsOn(sockets []string) (bool, error) {
	d.sawRow = d.read()
	return d.fakeDetacher.detachClientsOn(sockets)
}

// --attach writes first, then detaches every local socket once, the default
// server included.
func TestRouteRemoteFocus_AttachWritesThenDetachesAllSockets(t *testing.T) {
	db := newTempStateDB(t)
	det := &detachRecorder{read: func() string { raw, _ := session.ReadFocusRequest(db); return raw }}
	insts := []*session.Instance{instOnSocket("a", "agent-deck"), instOnSocket("b", "other"), instOnSocket("c", ""), instOnSocket("d", "other")}
	if err := routeRemoteFocus(db, insts, "agent-deck", "box", "r-1", time.Now().UnixNano(), true, fakeRemotes("box"), det); err != nil {
		t.Fatalf("routeRemoteFocus: %v", err)
	}
	if det.calls != 1 {
		t.Fatalf("detach calls = %d, want 1", det.calls)
	}
	if det.sawRow == "" {
		t.Fatal("the request must be written before the detach")
	}
	want := []string{"", "agent-deck", "other"}
	if len(det.gotSockets) != len(want) {
		t.Fatalf("sockets = %q, want %q", det.gotSockets, want)
	}
	for i := range want {
		if det.gotSockets[i] != want[i] {
			t.Fatalf("sockets = %q, want %q", det.gotSockets, want)
		}
	}
}

// A local focus request keeps today's exact bytes.
func TestRouteFocus_LocalRequestBytesUnchanged(t *testing.T) {
	db := newTempStateDB(t)
	inst := session.NewInstanceWithTool("a1", "/tmp/a1", "claude")
	const now = int64(1_000_000_000_000)
	if err := routeFocus(db, []*session.Instance{inst}, inst.ID, now, true, nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := session.ReadFocusRequest(db)
	if want := `{"id":"` + inst.ID + `","ts":1000000000000,"attach":true}`; raw != want {
		t.Fatalf("stored %q, want %q", raw, want)
	}
}
