package sendqueue

import (
	"encoding/json"
	"errors"
	"os"
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
	if first, second := NewID(base), NewID(base); first == second {
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

// TestLockReleaseReportsCloseError: Release surfaces a failed close of the
// lock file, and a second Release is a no-op.
func TestLockReleaseReportsCloseError(t *testing.T) {
	dir := t.TempDir()
	l, ok, err := TryLock(dir, "sess-close")
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("clean release: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}

	l, ok, err = TryLock(dir, "sess-close")
	if err != nil || !ok {
		t.Fatalf("relock: %v %v", ok, err)
	}
	// Close the descriptor behind the lock's back so its own close fails.
	if err := l.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("release after external close = %v, want os.ErrClosed", err)
	}
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatalf("nil release: %v", err)
	}
}

// TestNextIDIsMonotonicWithinAMillisecond: callers in the same millisecond,
// or behind a clock that stepped back, still get ids in call order.
func TestNextIDIsMonotonicWithinAMillisecond(t *testing.T) {
	dir := t.TempDir()
	now := time.UnixMilli(1790150376000)
	var ids []string
	for i := 0; i < 200; i++ {
		at := now
		if i == 100 {
			at = now.Add(-time.Second) // clock stepped back
		}
		id, err := NextID(dir, at)
		if err != nil {
			t.Fatal(err)
		}
		if !validID(id) {
			t.Fatalf("invalid id %q", id)
		}
		if len(ids) > 0 && id <= ids[len(ids)-1] {
			t.Fatalf("id %d %s does not sort after %s", i, id, ids[len(ids)-1])
		}
		ids = append(ids, id)
	}
}

func TestPendingTargetsAndPrune(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	recent := time.Now().UTC().Format(time.RFC3339Nano)
	mk := func(sess, state string, settled bool, updated string) string {
		id, _ := NextID(dir, time.Now())
		r := &Record{SendID: id, State: state, SessionID: sess, Settled: settled, UpdatedAt: updated}
		if err := Save(dir, r); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldLanded := mk("a", StateLanded, false, old)
	mk("a", StateLanded, false, recent)
	mk("b", StateTyping, false, old)
	mk("c", StateQueued, false, recent)
	oldSettled := mk("d", StateTyped, true, old)
	if got := PendingTargets(dir); len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("pending targets = %v", got)
	}
	Prune(dir, time.Now().Add(-RetainFinished))
	recs, _ := List(dir, "")
	if len(recs) != 3 {
		t.Fatalf("after prune %d records", len(recs))
	}
	for _, r := range recs {
		if r.SendID == oldLanded || r.SendID == oldSettled {
			t.Fatalf("old finished record kept: %+v", r)
		}
	}
}

// TestRecordSenderIsAdditive: a record written before the sender field
// existed still parses; a new record keeps its sender (issue #2481).
func TestRecordSenderIsAdditive(t *testing.T) {
	var old Record
	if err := json.Unmarshal([]byte(`{"send_id":"x","state":"queued","attempts":2}`), &old); err != nil || old.Sender != "" || old.Attempts != 2 {
		t.Fatalf("old record: %+v %v", old, err)
	}
	b, _ := json.Marshal(Record{SendID: "y", Sender: "cli"})
	var cur Record
	if err := json.Unmarshal(b, &cur); err != nil || cur.Sender != "cli" {
		t.Fatalf("new record round trip: %+v %v", cur, err)
	}
}

// TestRetryDelayDoublesToCap: the wait after a refusal doubles from the base
// and stops at the cap.
func TestRetryDelayDoublesToCap(t *testing.T) {
	base, max := time.Second, time.Minute
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for i, w := range want {
		if got := RetryDelay(base, max, i+1); got != w {
			t.Errorf("RetryDelay(attempts=%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := RetryDelay(base, max, 0); got != base {
		t.Errorf("RetryDelay(0) = %v, want %v", got, base)
	}
	if got := RetryDelay(base, max, 1000); got != max {
		t.Errorf("RetryDelay(1000) = %v, want %v", got, max)
	}
}
