package comms

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Consumer side (P2): a consumer reads only what is addressed to it, its
// acknowledgements survive concurrent readers, a new consumer starts at its
// first record, and every loss (epoch change, compaction) is explicit.

func openPair(t *testing.T) (*Ledger, *Reader, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	r, err := OpenReaderAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return l, r, dir
}

func mustCommit(t *testing.T, l *Ledger, r Record) events.Cursor {
	t.Helper()
	_, c, err := l.Commit(r)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// pass runs one Do for consumer acknowledging nothing and returns the
// pending cursors.
func pass(t *testing.T, r *Reader, consumer string, ack func(Pass) []events.Cursor) ([]events.Cursor, ConsumerFile) {
	t.Helper()
	var got []events.Cursor
	f, err := r.Do(consumer, func(p Pass) ([]events.Cursor, error) {
		for _, e := range p.Pending {
			got = append(got, e.Cursor)
		}
		if ack == nil {
			return nil, nil
		}
		return ack(p), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got, f
}

func all(p Pass) []events.Cursor {
	var out []events.Cursor
	for _, e := range p.Pending {
		out = append(out, e.Cursor)
	}
	return out
}

func TestConsumerReadsOnlyItsNewsAndAckMovesTheWatermark(t *testing.T) {
	l, r, dir := openPair(t)
	mine := mustCommit(t, l, Record{Kind: KindTurn, From: "c1", To: []string{"P"}, Tier: TierInfo, Text: "one"})
	mustCommit(t, l, Record{Kind: KindTurn, From: "c2", To: []string{"Q"}, Tier: TierUrgent, Text: "not mine"})
	mustCommit(t, l, Record{Kind: KindTurn, From: "c1", To: []string{"P"}, Tier: TierNoise, Text: "one"})
	mustCommit(t, l, Record{Kind: KindWake, From: "agent-deck", To: []string{"P"}, Text: "[INBOX] ..."})
	mustCommit(t, l, Record{Kind: KindCall, From: "P", State: CallSessionOutput})
	sendSeen := mustCommit(t, l, Record{Kind: KindSend, From: "c1", To: []string{"c2", "P"}, Tier: TierInfo, Text: "hi sibling"})
	// The send's target reads it from its pane, never again from the ledger.
	if Deliverable(Record{Kind: KindSend, From: "c1", To: []string{"c2", "P"}}, "c2") {
		t.Fatal("a send must not be delivered to its own target")
	}

	got, f := pass(t, r, "P", nil)
	if len(got) != 2 || got[0] != mine || got[1] != sendSeen {
		t.Fatalf("pending for P = %v, want [%d %d] (noise, wake, call and Q's record are not P's news)", got, mine, sendSeen)
	}
	if f.Watermark != mine-1 || f.Pending != 2 {
		t.Fatalf("peek moved the watermark past pending news: %+v", f)
	}
	if HasNothingPending(dir, "P") {
		t.Fatal("fast path says nothing pending while two records wait")
	}
	_, f = pass(t, r, "P", all)
	if f.Watermark != l.Cursor() || f.Pending != 0 || f.Acked != nil {
		t.Fatalf("after acking everything: %+v (cursor %d)", f, l.Cursor())
	}
	if !HasNothingPending(dir, "P") {
		t.Fatal("fast path must skip a consumer with nothing new")
	}
	// A record for someone else does not wake P's fast path; one for P does.
	mustCommit(t, l, Record{Kind: KindTurn, From: "c2", To: []string{"Q"}, Tier: TierUrgent, Text: "q again"})
	if !HasNothingPending(dir, "P") {
		t.Fatal("Q's record raised P's flag")
	}
	next := mustCommit(t, l, Record{Kind: KindTurn, From: "c1", To: []string{"P"}, Tier: TierUrgent, Text: "done", Done: "ok"})
	if HasNothingPending(dir, "P") {
		t.Fatal("a new record for P must defeat the fast path")
	}
	if got, _ := pass(t, r, "P", nil); len(got) != 1 || got[0] != next {
		t.Fatalf("pending after a new record: %v", got)
	}
}

func TestNewConsumerStartsAtItsFirstRecordElseAtTheEnd(t *testing.T) {
	l, r, _ := openPair(t)
	mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"Q"}, Tier: TierInfo, Text: "before P existed"})
	first := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierInfo, Text: "first for P"})
	mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierInfo, Text: "second for P"})
	got, f := pass(t, r, "P", nil)
	if len(got) != 2 || got[0] != first || f.Generation != 1 || f.Store != l.Store().ID {
		t.Fatalf("new consumer P: pending %v state %+v", got, f)
	}
	// A consumer nobody ever addressed starts at the end: no history flood.
	got, f = pass(t, r, "nobody", nil)
	if len(got) != 0 || f.Watermark != l.Cursor() {
		t.Fatalf("unaddressed consumer: pending %v watermark %d (cursor %d)", got, f.Watermark, l.Cursor())
	}
	later := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"nobody"}, Tier: TierInfo, Text: "now you"})
	if got, _ := pass(t, r, "nobody", nil); len(got) != 1 || got[0] != later {
		t.Fatalf("record after creation: %v", got)
	}
}

func TestUrgentAckedAheadNeverHidesEarlierInfo(t *testing.T) {
	l, r, _ := openPair(t)
	info := mustCommit(t, l, Record{Kind: KindTurn, From: "a", To: []string{"P"}, Tier: TierInfo, Text: "progress"})
	urgent := mustCommit(t, l, Record{Kind: KindTurn, From: "b", To: []string{"P"}, Tier: TierUrgent, Text: "blocked?", Q: true})
	_, f := pass(t, r, "P", func(p Pass) []events.Cursor { return []events.Cursor{urgent} })
	if f.Watermark != info-1 || len(f.Acked) != 1 || f.Acked[0] != urgent || f.Pending != 1 {
		t.Fatalf("urgent acked ahead: %+v", f)
	}
	got, _ := pass(t, r, "P", nil)
	if len(got) != 1 || got[0] != info {
		t.Fatalf("the info record must stay pending: %v", got)
	}
	_, f = pass(t, r, "P", all)
	if f.Watermark != urgent || f.Acked != nil {
		t.Fatalf("contiguous acks fold into the watermark: %+v", f)
	}
}

func TestConcurrentReadersNeverLoseAnAcknowledgement(t *testing.T) {
	l, r, dir := openPair(t)
	var cursors []events.Cursor
	for i := 0; i < 40; i++ {
		cursors = append(cursors, mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierInfo, Text: strings.Repeat("x", i+1)}))
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(cursors))
	for _, c := range cursors {
		wg.Add(1)
		go func(c events.Cursor) {
			defer wg.Done()
			rd, err := OpenReaderAt(dir)
			if err != nil {
				errs <- err
				return
			}
			defer rd.Close()
			_, err = rd.Do("P", func(Pass) ([]events.Cursor, error) { return []events.Cursor{c}, nil })
			errs <- err
		}(c)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, f := pass(t, r, "P", nil)
	if len(got) != 0 || f.Watermark != cursors[len(cursors)-1] {
		t.Fatalf("lost acknowledgements: still pending %v, state %+v", got, f)
	}
}

func TestEpochChangeRebuildsTheConsumerWithAnExplicitGap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := l.Commit(Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierInfo, Text: strings.Repeat("y", i+1)}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := OpenReaderAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	// P read (peeked) but acknowledged nothing: it has unread records when
	// the ledger is restored, so the reset is a gap it is told about.
	if _, err := r.Do("P", func(p Pass) ([]events.Cursor, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	// C read and acknowledged everything: nothing was lost, no gap.
	if _, err := r.Do("C", func(p Pass) ([]events.Cursor, error) { return all(p), nil }); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	oldEpoch := l.Store().Epoch
	_ = l.Close()
	// Restore from an older copy: only the first line survives.
	data, _ := os.ReadFile(filepath.Join(dir, "active.ndjson"))
	if err := os.WriteFile(filepath.Join(dir, "active.ndjson"), []byte(strings.SplitAfter(string(data), "\n")[0]), 0o600); err != nil {
		t.Fatal(err)
	}
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.Store().Epoch != oldEpoch+1 {
		t.Fatalf("restore not detected: epoch %d", l2.Store().Epoch)
	}
	fresh := mustCommit(t, l2, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierUrgent, Text: "after restore", Done: "ok"})
	r2, err := OpenReaderAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	var gap *GapNote
	f, err := r2.Do("P", func(p Pass) ([]events.Cursor, error) {
		gap = p.Gap
		if len(p.Pending) != 1 || p.Pending[0].Cursor != fresh {
			t.Errorf("after the epoch change P must see the new record once: %+v", p.Pending)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gap == nil || gap.Reason != "epoch" || gap.From > gap.To || f.Epoch != l2.Store().Epoch || f.Generation != 2 || len(f.Gaps) != 1 {
		t.Fatalf("epoch change must rebuild with an explicit gap: gap %+v state %+v", gap, f)
	}
	var cGap *GapNote
	if _, err := r2.Do("C", func(p Pass) ([]events.Cursor, error) { cGap = p.Gap; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if cGap != nil {
		t.Fatalf("a caught-up consumer lost nothing; no gap: %+v", cGap)
	}
}

func TestCompactionKeepsPendingRecordsAndALaggardGetsAGap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	bus, err := events.OpenAt(dir, events.Options{Private: true, KeepCorrupt: true, MaxSegmentBytes: 600, RetainSegments: 1,
		RetainFrom: func() events.Cursor { return ConsumersRetainFrom(dir, time.Now()) }})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if err := writeStoreIdentity(dir, StoreIdentity{ID: "S", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	commit := func(to, text string) events.Cursor {
		f, err := bus.Commit(KindTurn, "c", Record{V: 1, ID: NewID(time.Now()), Kind: KindTurn, From: "c", To: []string{to}, Tier: TierInfo, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return f.Cursor
	}
	commit("Q", "lost to Q "+strings.Repeat("q", 700))
	firstForP := commit("P", "pending for P "+strings.Repeat("p", 700))
	// The raw bus has no Ledger: create P's state at its record, as the
	// daemon does before a record for a new recipient becomes visible.
	if err := EnsureConsumer(dir, "P", StoreIdentity{ID: "S", Epoch: 1}, firstForP-1); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReaderAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Q's state exists but it has not read for longer than the audit
	// retention, so it holds nothing; P reads (a peek) and holds its record.
	if err := writeConsumer(dir, ConsumerFile{ConsumerState: ConsumerState{Consumer: "Q", Store: "S", Epoch: 1, Generation: 1},
		Updated: time.Now().Add(-2 * activeConsumerFor).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if got, _ := pass(t, r, "P", nil); len(got) != 1 || got[0] != firstForP {
		t.Fatalf("P pending %v", got)
	}
	for i := 0; i < 12; i++ {
		commit("Q", strings.Repeat("q", 700))
	}
	oldest, err := bus.Oldest()
	if err != nil {
		t.Fatal(err)
	}
	if oldest > firstForP || oldest <= 1 {
		t.Fatalf("compaction must drop Q's unheld record and stop at P's pending one: oldest %d, P's record %d", oldest, firstForP)
	}
	if got, _ := pass(t, r, "P", nil); len(got) != 1 || got[0] != firstForP {
		t.Fatalf("compaction dropped P's pending record: %v", got)
	}
	// Q is told what it lost instead of silently starting later.
	var gap *GapNote
	if _, err := r.Do("Q", func(p Pass) ([]events.Cursor, error) { gap = p.Gap; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if gap == nil || gap.Reason != "compacted" || gap.From != 1 || gap.To != oldest-1 || gap.Resumed != oldest-1 {
		t.Fatalf("laggard gap %+v (oldest %d)", gap, oldest)
	}
}

func TestPendingFlagsAreRebuiltAtOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	c := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierUrgent, Text: "lost flag"})
	_ = l.Close()
	if err := os.RemoveAll(filepath.Join(dir, pendingDirName)); err != nil {
		t.Fatal(err)
	}
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	flag, ok := ReadFlag(dir, "P")
	if !ok || flag.Last != c || flag.Epoch != l2.Store().Epoch {
		t.Fatalf("flag not rebuilt at open: %+v ok=%v", flag, ok)
	}
}

func TestConsumerNamesAreOnePathElement(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../x", "a/b", ".hidden", strings.Repeat("a", 201)} {
		if ValidConsumer(bad) == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	for _, good := range []string{"8f3c2a1e-0000", "human:conductor-ops", "sess_1.2@host"} {
		if err := ValidConsumer(good); err != nil {
			t.Fatalf("rejected %q: %v", good, err)
		}
	}
	_, r, _ := openPair(t)
	if _, err := r.Do("../escape", func(Pass) ([]events.Cursor, error) { return nil, nil }); err == nil {
		t.Fatal("Do accepted a traversal consumer name")
	}
}

func TestADecideErrorAcknowledgesNothing(t *testing.T) {
	l, r, _ := openPair(t)
	mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierUrgent, Text: "show me"})
	boom := errors.New("stdout closed")
	if _, err := r.Do("P", func(p Pass) ([]events.Cursor, error) { return all(p), boom }); !errors.Is(err, boom) {
		t.Fatalf("decide error not returned: %v", err)
	}
	if got, _ := pass(t, r, "P", nil); len(got) != 1 {
		t.Fatalf("a failed print must leave the record pending: %v", got)
	}
}

// Verifier round 1 (#1): a flag is only a hint. A consumer whose flag is
// missing (lost to a crash, or never written by an older daemon) still
// gets every record addressed to it.
func TestAMissingFlagNeverSkipsARecord(t *testing.T) {
	l, r, dir := openPair(t)
	mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"Q"}, Tier: TierInfo, Text: "for Q"})
	mine := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierUrgent, Text: "for X"})
	if err := os.Remove(FlagPath(dir, "X")); err != nil {
		t.Fatal(err)
	}
	got, _ := pass(t, r, "X", nil)
	if len(got) != 1 || got[0] != mine {
		t.Fatalf("a missing flag skipped the record: pending %v", got)
	}
}

// Verifier round 1 (#1): the flag is raised (and a new recipient's state
// created) before the frame is visible, so a reader that sees the record
// also sees both.
func TestTheFlagIsRaisedBeforeTheRecordIsVisible(t *testing.T) {
	l, _, dir := openPair(t)
	seen := make(chan bool, 1)
	c := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierUrgent, Text: "x"})
	if st, found, _ := ReadConsumer(dir, "X"); !found || st.Watermark != c-1 {
		t.Fatalf("the daemon creates a new recipient's state just before its first record: %+v found=%v", st, found)
	}
	flag, ok := ReadFlag(dir, "X")
	seen <- ok
	if !<-seen || flag.Last < c {
		t.Fatalf("flag %+v ok=%v for cursor %d", flag, ok, c)
	}
}

// Verifier round 1 (#2): acknowledging every pending record of a consumer
// whose records are interleaved with other traffic folds into the
// watermark instead of overflowing the sparse set.
func TestAckAllOverInterleavedTrafficNeverOverflowsTheSparseSet(t *testing.T) {
	if testing.Short() {
		t.Skip("commits 8k records")
	}
	l, r, _ := openPair(t)
	mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierInfo, Text: "first"})
	for i := 0; i < MaxSparseAcks+10; i++ {
		mustCommit(t, l, Record{Kind: KindTurn, From: "q", To: []string{"Q"}, Tier: TierInfo, Text: "q" + strings.Repeat("x", i%7)})
		mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierInfo, Text: "p" + strings.Repeat("y", i%7)})
	}
	_, f := pass(t, r, "P", all)
	if f.Pending != 0 || f.Acked != nil || f.Watermark != l.Cursor() {
		t.Fatalf("ack all over interleaved traffic: %+v (cursor %d)", f.ConsumerState, l.Cursor())
	}
}

// Verifier round 2 (#12): a consumer's start never comes from a flag. A
// recipient's state is created at its first record, so flags lost and
// rebuilt from a partial window at the next open skip nothing.
func TestRebuiltFlagsFromAPartialWindowSkipNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("commits 4k records")
	}
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	first := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierInfo, Text: "first for X"})
	for i := 0; i < recentKeys+10; i++ {
		mustCommit(t, l, Record{Kind: KindTurn, From: "q", To: []string{"Q"}, Tier: TierInfo, Text: "q" + strings.Repeat("x", i%5)})
	}
	last := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierInfo, Text: "last for X"})
	_ = l.Close()
	if err := os.RemoveAll(filepath.Join(dir, pendingDirName)); err != nil {
		t.Fatal(err)
	}
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	r, err := OpenReaderAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := pass(t, r, "X", nil)
	if len(got) != 2 || got[0] != first || got[1] != last {
		t.Fatalf("X must see both records: %v (want %d, %d)", got, first, last)
	}
}

// Verifier round 2 (#3/#5): a consumer nobody ever addressed starts at the
// end, so compaction before it existed is not a loss it is told about.
func TestANeverAddressedConsumerGetsNoFalseGap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	bus, err := events.OpenAt(dir, events.Options{Private: true, KeepCorrupt: true, MaxSegmentBytes: 600, RetainSegments: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if err := writeStoreIdentity(dir, StoreIdentity{ID: "S", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := bus.Commit(KindTurn, "c", Record{V: 1, ID: NewID(time.Now()), Kind: KindTurn, From: "c", To: []string{"Q"}, Tier: TierInfo, Text: strings.Repeat("q", 700)}); err != nil {
			t.Fatal(err)
		}
	}
	if oldest, _ := bus.Oldest(); oldest <= 1 {
		t.Fatalf("expected compaction, oldest %d", oldest)
	}
	r, err := OpenReaderAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var gap *GapNote
	f, err := r.Do("human:new", func(p Pass) ([]events.Cursor, error) { gap = p.Gap; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if gap != nil || len(f.Gaps) != 0 {
		t.Fatalf("a never-addressed consumer was told about a loss: %+v", gap)
	}
}

// A pass that changes nothing writes nothing and is not activity, so a
// daemon polling a consumer never keeps it holding compaction.
func TestANoOpPassNeitherWritesNorCountsAsActivity(t *testing.T) {
	l, r, dir := openPair(t)
	mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"P"}, Tier: TierInfo, Text: "x"})
	_, first := pass(t, r, "P", nil)
	info1, _ := os.Stat(ConsumerPath(dir, "P"))
	r.Now = func() time.Time { return time.Now().Add(time.Hour) }
	_, second := pass(t, r, "P", nil)
	info2, _ := os.Stat(ConsumerPath(dir, "P"))
	if second.Updated != first.Updated || !info2.ModTime().Equal(info1.ModTime()) {
		t.Fatalf("a no-op pass rewrote the state: %d -> %d", first.Updated, second.Updated)
	}
	if HasNothingPending(dir, "P") {
		t.Fatal("one record is still pending")
	}
	_, acked := pass(t, r, "P", all)
	if acked.Updated == first.Updated {
		t.Fatal("an acknowledgement is activity")
	}
}

// Verifier round 3 (#1): a consumer whose state file was lost after
// records were addressed to it is told so instead of silently starting at
// the end.
func TestALostStateFileIsAGap(t *testing.T) {
	l, r, dir := openPair(t)
	c := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierUrgent, Text: "x"})
	if err := os.Remove(ConsumerPath(dir, "X")); err != nil {
		t.Fatal(err)
	}
	var gap *GapNote
	if _, err := r.Do("X", func(p Pass) ([]events.Cursor, error) { gap = p.Gap; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if gap == nil || gap.Reason != "state_lost" || gap.To != c {
		t.Fatalf("lost state: %+v", gap)
	}
}

// Verifier round 3 (#2): a watermark past the end of a log restored from a
// copy older than its high-water mark resumes at the end with a gap, so
// new records (which reuse those cursors) are not skipped.
func TestAWatermarkPastTheEndResumesAtTheEnd(t *testing.T) {
	l, r, dir := openPair(t)
	first := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierInfo, Text: "x"})
	if err := writeConsumer(dir, ConsumerFile{ConsumerState: ConsumerState{Consumer: "X", Store: l.Store().ID, Epoch: l.Store().Epoch, Generation: 1, Watermark: 50},
		Through: 50, Updated: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	next := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierUrgent, Text: "after the restore"})
	var gap *GapNote
	var got []events.Cursor
	if _, err := r.Do("X", func(p Pass) ([]events.Cursor, error) {
		gap = p.Gap
		for _, e := range p.Pending {
			got = append(got, e.Cursor)
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if gap == nil || gap.Reason != "restored" || gap.To != 50 {
		t.Fatalf("restored: %+v", gap)
	}
	// Re-read from the last persisted high-water mark (0 here): the record
	// committed after the restore is delivered, the older one again (at
	// least once).
	if len(got) != 2 || got[0] != first || got[1] != next {
		t.Fatalf("records at or below the end after a restore must be delivered: %v", got)
	}
}

// Verifier round 4 (#5): profile names agent-deck itself accepts keep a
// ledger; listing debris does not.
func TestLedgerDirAcceptsRealProfileNamesOnly(t *testing.T) {
	for _, good := range []string{"default", "my work", "café", "work+acme", "a@b", "_test"} {
		if _, err := Dir(good); err != nil {
			t.Fatalf("Dir refused %q: %v", good, err)
		}
	}
	for _, bad := range []string{"*", "Total:", "_test*", "[x]", "a?b", "../x", "a/b", ".", "..", "tab\tname"} {
		if _, err := Dir(bad); err == nil {
			t.Fatalf("Dir accepted %q", bad)
		}
	}
}

// Verifier round 4 (#7): opening a ledger written before consumer states
// existed creates them at the end, so the first read reports no false loss.
func TestARecipientWithoutStateAtOpenStartsAtTheEndWithoutAFalseGap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierInfo, Text: "p1 era"})
	_ = l.Close()
	// As a P1 daemon left it: neither consumer states nor pending flags.
	for _, sub := range []string{cursorsDirName, pendingDirName} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			t.Fatal(err)
		}
	}
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	r, err := OpenReaderAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var gap *GapNote
	if _, err := r.Do("X", func(p Pass) ([]events.Cursor, error) { gap = p.Gap; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if gap != nil {
		t.Fatalf("a P1-era ledger is not a lost state: %+v", gap)
	}
	newer := mustCommit(t, l2, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierInfo, Text: "p2"})
	if got, _ := pass(t, r, "X", nil); len(got) != 1 || got[0] != newer {
		t.Fatalf("records after the open are pending: %v", got)
	}
}

// Verifier round 5: a P2 consumer whose state was lost is told so even
// after a daemon restart (only a P1-era recipient, with no flag of this
// epoch, gets its state created at open).
func TestALostStateSurvivesARestartAsAGap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	c := mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierUrgent, Text: "x"})
	_ = l.Close()
	if err := os.Remove(ConsumerPath(dir, "X")); err != nil {
		t.Fatal(err)
	}
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	r, err := OpenReaderAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var gap *GapNote
	if _, err := r.Do("X", func(p Pass) ([]events.Cursor, error) { gap = p.Gap; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if gap == nil || gap.Reason != "state_lost" || gap.To < c {
		t.Fatalf("a lost state after a restart must be a gap: %+v", gap)
	}
}

// Verifier round 6: a restore that bumps the epoch plus a lost state is
// still a gap (a flag of an older epoch marks a P2 recipient).
func TestALostStateAfterARestoreIsAGap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	mustCommit(t, l, Record{Kind: KindTurn, From: "c", To: []string{"X"}, Tier: TierUrgent, Text: "x"})
	store := l.Store()
	_ = l.Close()
	if err := os.Remove(ConsumerPath(dir, "X")); err != nil {
		t.Fatal(err)
	}
	store.HWM = 100 // the next open sees a shorter log: a restore, new epoch
	if err := writeStoreIdentity(dir, store); err != nil {
		t.Fatal(err)
	}
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.Store().Epoch != store.Epoch+1 {
		t.Fatalf("restore not detected")
	}
	r, err := OpenReaderAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var gap *GapNote
	if _, err := r.Do("X", func(p Pass) ([]events.Cursor, error) { gap = p.Gap; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if gap == nil || gap.Reason != "state_lost" {
		t.Fatalf("restore plus a lost state must be a gap: %+v", gap)
	}
}
