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

// fakePIDDetacher records which client routeRemoteFocus asked to detach, and
// what the db held at that moment.
type fakePIDDetacher struct {
	calls  int
	socket string
	pid    int
	sawRow string
	read   func() string
}

func (f *fakePIDDetacher) detachClient(socket string, pid int) (bool, error) {
	f.calls++
	f.socket, f.pid = socket, pid
	if f.read != nil {
		f.sawRow = f.read()
	}
	return true, nil
}

// #2704: an unknown remote is rejected before anything is written or detached.
func TestRouteRemoteFocus_UnknownRemote(t *testing.T) {
	db := newTempStateDB(t)
	_ = session.WriteTUIAttach(db, session.TUIAttach{Socket: "agent-deck", ClientPID: 42})
	det := &fakePIDDetacher{}
	err := routeRemoteFocus(db, "nope", "r-1", time.Now().UnixNano(), true, fakeRemotes("box"), det)
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
	if err := routeRemoteFocus(db, "box", "", time.Now().UnixNano(), false, fakeRemotes("box"), nil); err == nil {
		t.Fatal("empty id: want error")
	}
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("empty id wrote a row: %q", raw)
	}
}

// Select-only writes the remote request and never detaches anybody, even with
// the TUI paused in an attach. The id is not checked locally: it belongs to
// the remote.
func TestRouteRemoteFocus_SelectOnlyNeverDetaches(t *testing.T) {
	db := newTempStateDB(t)
	_ = session.WriteTUIAttach(db, session.TUIAttach{Socket: "agent-deck", ClientPID: 42})
	det := &fakePIDDetacher{}
	now := time.Now().UnixNano()
	if err := routeRemoteFocus(db, "box", "r-1", now, false, fakeRemotes("box"), det); err != nil {
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

// --attach with the TUI paused in a local attach: the request is written
// first, then exactly the recorded client is detached, once.
func TestRouteRemoteFocus_AttachDetachesOnlyTUIClient(t *testing.T) {
	db := newTempStateDB(t)
	_ = session.WriteTUIAttach(db, session.TUIAttach{Socket: "agent-deck", ClientPID: 42})
	det := &fakePIDDetacher{read: func() string { raw, _ := session.ReadFocusRequest(db); return raw }}
	if err := routeRemoteFocus(db, "box", "r-1", time.Now().UnixNano(), true, fakeRemotes("box"), det); err != nil {
		t.Fatalf("routeRemoteFocus: %v", err)
	}
	if det.calls != 1 || det.socket != "agent-deck" || det.pid != 42 {
		t.Fatalf("detach calls=%d socket=%q pid=%d, want 1 call for agent-deck/42", det.calls, det.socket, det.pid)
	}
	if det.sawRow == "" {
		t.Fatal("the request must be written before the detach")
	}
}

// --attach with no record (TUI on the list, embedded layout, or an older TUI
// that ignores remote requests): nobody is detached.
func TestRouteRemoteFocus_AttachWithoutRecordDetachesNobody(t *testing.T) {
	db := newTempStateDB(t)
	det := &fakePIDDetacher{}
	if err := routeRemoteFocus(db, "box", "r-1", time.Now().UnixNano(), true, fakeRemotes("box"), det); err != nil {
		t.Fatalf("routeRemoteFocus: %v", err)
	}
	if det.calls != 0 {
		t.Fatalf("detached %d times without a TUI attach record", det.calls)
	}
	if raw, _ := session.ReadFocusRequest(db); raw == "" {
		t.Fatal("the request must still be written")
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
