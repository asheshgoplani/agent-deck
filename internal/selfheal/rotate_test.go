package selfheal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock hands out a strictly increasing second per call, so rolled-segment
// stamps are unique and ordered without depending on wall-clock timing.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func newTestSink(t *testing.T, maxBytes int64, retain int) *NDJSONSink {
	t.Helper()
	dir := t.TempDir()
	sink, err := NewNDJSONSink(filepath.Join(dir, "selfheal-audit.ndjson"))
	if err != nil {
		t.Fatalf("NewNDJSONSink: %v", err)
	}
	sink.maxSegmentBytes = maxBytes
	sink.retainedSegments = retain
	clk := &fakeClock{t: time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)}
	sink.now = clk.now
	return sink
}

// appendSeq writes n events whose SessionID is a zero-padded sequence number, so
// a reader can prove ordering and completeness across rolls.
func appendSeq(t *testing.T, s *NDJSONSink, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ev := Event{
			TS:        formatTS(time.Unix(int64(1780000000+i), 0)),
			SessionID: fmt.Sprintf("s%05d", i),
			Title:     "rotation-fixture-session-title",
			Stage:     ModeObserve,
			Decision:  DecisionSkipBusy,
			Reads:     []ReadSig{{T: "t", Sig: "0123456789abcdef"}},
		}
		if err := s.Append(ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
}

func readAll(t *testing.T, live string) []Event {
	t.Helper()
	var out []Event
	if err := ForEachAuditEvent(live, func(e Event) error {
		out = append(out, e)
		return nil
	}); err != nil {
		t.Fatalf("ForEachAuditEvent: %v", err)
	}
	return out
}

func rolledSegments(t *testing.T, live string) []string {
	t.Helper()
	paths, err := AuditSegmentPaths(live)
	if err != nil {
		t.Fatalf("AuditSegmentPaths: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("AuditSegmentPaths returned nothing")
	}
	if paths[len(paths)-1] != live {
		t.Fatalf("live segment must sort last, got %v", paths)
	}
	return paths[:len(paths)-1]
}

func TestNDJSONSink_RotatesAtSizeThreshold(t *testing.T) {
	sink := newTestSink(t, 4096, 20)
	appendSeq(t, sink, 200)
	sink.rotations.Wait()

	fi, err := os.Stat(sink.Path())
	if err != nil {
		t.Fatalf("stat live: %v", err)
	}
	if fi.Size() > 4096 {
		t.Fatalf("live segment %d bytes exceeds the 4096-byte threshold — no rotation happened", fi.Size())
	}
	rolled := rolledSegments(t, sink.Path())
	if len(rolled) == 0 {
		t.Fatal("no rolled segment produced")
	}
	for _, p := range rolled {
		if !strings.HasSuffix(p, ".gz") {
			t.Fatalf("rolled segment %q was not compressed", p)
		}
	}
}

func TestNDJSONSink_RetentionEvictsOldest(t *testing.T) {
	const retain = 2
	sink := newTestSink(t, 2048, retain)
	appendSeq(t, sink, 400)
	sink.rotations.Wait()

	rolled := rolledSegments(t, sink.Path())
	if len(rolled) != retain {
		t.Fatalf("retention: want %d rolled segments, got %d (%v)", retain, len(rolled), rolled)
	}
	// The surviving records must be the NEWEST ones: the earliest sequence
	// numbers were evicted with the oldest segments.
	events := readAll(t, sink.Path())
	if len(events) == 0 {
		t.Fatal("no events readable after eviction")
	}
	if events[0].SessionID == "s00000" {
		t.Fatal("retention evicted the newest segments, not the oldest")
	}
	if got := events[len(events)-1].SessionID; got != "s00399" {
		t.Fatalf("last retained record = %q, want the newest s00399", got)
	}
	// Still contiguous within the retained window: no holes in the middle.
	for i := 1; i < len(events); i++ {
		if events[i].SessionID <= events[i-1].SessionID {
			t.Fatalf("records out of order across segments at %d: %q then %q", i, events[i-1].SessionID, events[i].SessionID)
		}
	}
}

func TestNDJSONSink_NoRecordLostAcrossRoll(t *testing.T) {
	const n = 500
	sink := newTestSink(t, 2048, 1000) // retain far more than we produce
	appendSeq(t, sink, n)
	sink.rotations.Wait()

	events := readAll(t, sink.Path())
	if len(events) != n {
		t.Fatalf("records across rolled+live segments = %d, want %d", len(events), n)
	}
	for i, e := range events {
		if want := fmt.Sprintf("s%05d", i); e.SessionID != want {
			t.Fatalf("record %d = %q, want %q (order or content lost across a roll)", i, e.SessionID, want)
		}
	}
	if len(rolledSegments(t, sink.Path())) < 2 {
		t.Fatal("fixture did not actually roll more than once")
	}
}

func TestNDJSONSink_ConcurrentAppendsStayLineAtomicAcrossRotation(t *testing.T) {
	const n = 300
	sink := newTestSink(t, 1024, 1000)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := sink.Append(Event{
				SessionID: fmt.Sprintf("c%05d", i),
				Title:     "concurrent-rotation-fixture",
				Stage:     ModeObserve,
				Reads:     []ReadSig{{T: "t", Sig: "0123456789abcdef"}},
			}); err != nil {
				t.Errorf("Append %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	sink.rotations.Wait()

	seen := map[string]bool{}
	if err := ForEachAuditEvent(sink.Path(), func(e Event) error {
		if seen[e.SessionID] {
			return fmt.Errorf("duplicate record %q", e.SessionID)
		}
		seen[e.SessionID] = true
		return nil
	}); err != nil {
		t.Fatalf("reading back concurrent appends: %v", err)
	}
	if len(seen) != n {
		t.Fatalf("concurrent appends across rotation: %d distinct records, want %d", len(seen), n)
	}
}

// A crash between the rename and the gzip leaves an UNCOMPRESSED rolled segment.
// It is still part of the window and must be read, and it must not be mistaken
// for another profile's live file.
func TestAuditSegmentPaths_IncludesUncompressedRollAndIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "selfheal-audit.ndjson")
	if err := os.WriteFile(live, mustLine(t, Event{SessionID: "live"}), 0o600); err != nil {
		t.Fatal(err)
	}
	// An interrupted compression: plain rolled segment, plus its abandoned temp.
	if err := os.WriteFile(live+".20260817T090001Z", mustLine(t, Event{SessionID: "rolled"}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live+".20260817T090002Z.gz.tmp", []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Another profile's live audit in the same directory must never be claimed.
	foreign := filepath.Join(dir, "selfheal-audit-work.ndjson")
	if err := os.WriteFile(foreign, mustLine(t, Event{SessionID: "foreign"}), 0o600); err != nil {
		t.Fatal(err)
	}

	paths, err := AuditSegmentPaths(live)
	if err != nil {
		t.Fatalf("AuditSegmentPaths: %v", err)
	}
	want := []string{live + ".20260817T090001Z", live}
	if len(paths) != len(want) {
		t.Fatalf("segments = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("segments = %v, want %v", paths, want)
		}
	}
	events := readAll(t, live)
	if len(events) != 2 || events[0].SessionID != "rolled" || events[1].SessionID != "live" {
		t.Fatalf("read window = %+v, want rolled then live", events)
	}
}

// The shipped dials are the whole point of the change: a sink built the way the
// daemon builds it must be bounded, and bounded at the sizes docs/self-heal.md
// justifies from the measured ~480 MB/day.
func TestNewNDJSONSink_ShipsBoundedDials(t *testing.T) {
	sink, err := NewNDJSONSink(filepath.Join(t.TempDir(), "selfheal-audit.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if sink.maxSegmentBytes != 128<<20 {
		t.Errorf("segment threshold = %d, want 128 MiB", sink.maxSegmentBytes)
	}
	if sink.retainedSegments != 28 {
		t.Errorf("retained segments = %d, want 28 (≈7.5 days at the measured rate)", sink.retainedSegments)
	}
	if sink.now == nil {
		t.Error("segment stamp clock is nil")
	}
	// Sanity: the retained window must still cover a week at 480 MB/day.
	window := float64(sink.maxSegmentBytes) * float64(sink.retainedSegments) / (480 << 20)
	if window < 7 {
		t.Errorf("retained window = %.1f days, want ≥7 at the measured rate", window)
	}
}

// A sink built without the constructor has no dials; it must stay append-only
// rather than rolling on every single append.
func TestNDJSONSink_ZeroDialsDoNotRotate(t *testing.T) {
	sink := &NDJSONSink{path: filepath.Join(t.TempDir(), "a.ndjson")}
	appendSeq(t, sink, 20)
	sink.rotations.Wait()
	if segs := rolledSegments(t, sink.Path()); len(segs) != 0 {
		t.Fatalf("unconfigured sink rotated: %v", segs)
	}
	if got := readAll(t, sink.Path()); len(got) != 20 {
		t.Fatalf("records = %d, want 20", len(got))
	}
}

func mustLine(t *testing.T, e Event) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}
