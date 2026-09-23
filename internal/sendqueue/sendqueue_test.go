package sendqueue

import (
	"sort"
	"testing"
	"time"
)

func TestNewIDIsSortableULID(t *testing.T) {
	base := time.UnixMilli(1790150376000)
	var ids []string
	for i := 0; i < 50; i++ {
		id := NewID(base.Add(time.Duration(i) * time.Millisecond))
		if !validID(id) {
			t.Fatalf("invalid id %q", id)
		}
		ids = append(ids, id)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("ids do not sort by time: %v", ids)
	}
	if NewID(base) == NewID(base) {
		t.Fatal("ids collide within one millisecond")
	}
}

func TestSaveLoadListUpdate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	var ids []string
	for i, sess := range []string{"a", "b", "a"} {
		r := &Record{SendID: NewID(now.Add(time.Duration(i) * time.Millisecond)), State: StateQueued, SessionID: sess, Message: "m"}
		if err := Save(dir, r); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.SendID)
	}
	recs, err := List(dir, "a")
	if err != nil || len(recs) != 2 || recs[0].SendID != ids[0] || recs[1].SendID != ids[2] {
		t.Fatalf("List(a) = %v %v", recs, err)
	}
	r, err := Update(dir, ids[0], now, func(r *Record) { r.State, r.LandedRowID = StateLanded, "u1" })
	if err != nil || r.State != StateLanded || r.UpdatedAt == "" {
		t.Fatalf("Update: %+v %v", r, err)
	}
	if got, _ := Load(dir, ids[0]); !got.Final() || got.LandedRowID != "u1" {
		t.Fatalf("Load after update: %+v", got)
	}
	if _, err := Load(dir, "../../etc/passwd"); err != ErrUnknown {
		t.Fatalf("path-like id: %v", err)
	}
	if _, err := Load(dir, NewID(now.Add(time.Hour))); err != ErrUnknown {
		t.Fatalf("missing id: %v", err)
	}
	settled := &Record{State: StateSubmitted, Settled: true}
	if !settled.Final() || (&Record{State: StateTyped}).Final() {
		t.Fatal("Final semantics")
	}
}

func TestTryLockIsExclusivePerTarget(t *testing.T) {
	dir := t.TempDir()
	l1, ok, err := TryLock(dir, "sess-1")
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	if _, ok, err := TryLock(dir, "sess-1"); err != nil || ok {
		t.Fatalf("second lock on the same target: %v %v", ok, err)
	}
	l2, ok, err := TryLock(dir, "sess-2")
	if err != nil || !ok {
		t.Fatalf("other target: %v %v", ok, err)
	}
	l1.Release()
	l3, ok, err := TryLock(dir, "sess-1")
	if err != nil || !ok {
		t.Fatalf("relock after release: %v %v", ok, err)
	}
	l2.Release()
	l3.Release()
}
