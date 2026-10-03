package comms

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

func TestULIDIsSortableDecodableAndUniqueWithinAMillisecond(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	ids := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		ids = append(ids, NewID(now))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if len(id) != 26 {
			t.Fatalf("ulid length %d: %q", len(id), id)
		}
		if seen[id] {
			t.Fatalf("duplicate ulid %s", id)
		}
		seen[id] = true
		if got := IDTime(id); !got.Equal(now) {
			t.Fatalf("IDTime(%s) = %v, want %v", id, got, now)
		}
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("ids minted in one millisecond are not ordered")
	}
	later := NewID(now.Add(time.Millisecond))
	if later <= ids[len(ids)-1] {
		t.Fatalf("later id %s does not sort after %s", later, ids[len(ids)-1])
	}
	if !IDTime("not-a-ulid").IsZero() {
		t.Fatal("malformed id decoded a time")
	}
}

func TestStampFillsIdentityHashBytesAndLatency(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_500)
	r := Record{Kind: KindTurn, From: "c", Text: "hello", TSignal: now.UnixMilli() - 35}
	r.Stamp(now)
	if r.ID == "" || r.TRecord != now.UnixMilli() || r.TH != TextHash("hello") || r.Bytes != 5 || r.LatencyMS != 35 {
		t.Fatalf("stamped %+v", r)
	}
	id := r.ID
	r.Stamp(now.Add(time.Hour))
	if r.ID != id || r.TRecord != now.UnixMilli() || r.LatencyMS != 35 {
		t.Fatalf("second stamp changed the record: %+v", r)
	}
}

func TestKeyIsStableAndDistinguishesParts(t *testing.T) {
	a := Key(KindTurn, "child", "uuid-1")
	if a != Key(KindTurn, "child", "uuid-1") {
		t.Fatal("key not stable")
	}
	if a == Key(KindTurn, "child", "uuid-2") || a == Key(KindTurn, "other", "uuid-1") || a == Key(KindSend, "child", "uuid-1") {
		t.Fatal("key collides across parts")
	}
	if !strings.HasPrefix(a, "turn:child:") || len(a) != len("turn:child:")+16 {
		t.Fatalf("key shape %q", a)
	}
}

func TestCapTextClipsOnRuneBoundaryWithinCeiling(t *testing.T) {
	long := strings.Repeat("é", 2000) // 4000 bytes
	got := CapText(long, 0)
	if len(got) > DefaultTextBytes || !strings.HasSuffix(got, "…") {
		t.Fatalf("default cap: %d bytes", len(got))
	}
	if got := CapText(long, 10_000); len(got) > MaxTextBytes {
		t.Fatalf("ceiling not applied: %d bytes", len(got))
	}
	if got := CapText("short", 3); got != "…" {
		t.Fatalf("tiny cap: %q", got)
	}
	if got := CapText("fits", 100); got != "fits" {
		t.Fatalf("no clip expected: %q", got)
	}
}

func TestLedgerCommitStampsSequencesAndDedupsByKey(t *testing.T) {
	dir := t.TempDir()
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	r1, c1, err := l.Commit(Record{Kind: KindTurn, From: "child", Key: Key(KindTurn, "child", "u1"), Text: "one", Tool: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if c1 != 1 || r1.ID == "" || r1.Seq != 1 || r1.Profile != "p" || r1.Bytes != 3 || r1.TH == "" {
		t.Fatalf("first commit %+v cursor %d", r1, c1)
	}
	if _, _, err := l.Commit(Record{Kind: KindTurn, From: "child", Key: Key(KindTurn, "child", "u1"), Text: "one again"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate key accepted: %v", err)
	}
	r2, c2, err := l.Commit(Record{Kind: KindTurn, From: "child", Key: Key(KindTurn, "child", "u2"), Text: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if c2 != 2 || r2.Seq != 2 {
		t.Fatalf("second commit %+v cursor %d", r2, c2)
	}
	if _, _, err := l.Commit(Record{Kind: KindTurn}); err == nil {
		t.Fatal("record without from accepted")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// A restarted daemon must keep the key window and the sequence.
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if _, _, err := l2.Commit(Record{Kind: KindTurn, From: "child", Key: Key(KindTurn, "child", "u2"), Text: "two again"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("key window lost across reopen: %v", err)
	}
	r3, c3, err := l2.Commit(Record{Kind: KindSend, From: "child", Text: "three"})
	if err != nil {
		t.Fatal(err)
	}
	if c3 != 3 || r3.Seq != 3 {
		t.Fatalf("sequence lost across reopen: %+v cursor %d", r3, c3)
	}
	if r4, _, _ := l2.Commit(Record{Kind: KindTurn, From: "other", Text: "x"}); r4.Seq != 1 {
		t.Fatalf("sequence is per From, got %d", r4.Seq)
	}
}

func TestReaderSeesCommittedRecordsAndNeverWrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenReaderDir(dir + "/none"); !errors.Is(err, ErrNoLedger) {
		t.Fatalf("missing ledger: %v", err)
	}
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 5; i++ {
		if _, _, err := l.Commit(Record{Kind: KindTurn, From: "c", Text: strings.Repeat("x", i+1), Tier: TierInfo}); err != nil {
			t.Fatal(err)
		}
	}
	bus, err := OpenReaderDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	recs, last, err := ReadAfter(bus, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || last != 5 || recs[0].Bytes != 3 || recs[2].Bytes != 5 {
		t.Fatalf("ReadAfter(2): %d records, last %d: %+v", len(recs), last, recs)
	}
	recs, last, err = ReadAfter(bus, 0, 2)
	if err != nil || len(recs) != 2 || last != 2 {
		t.Fatalf("ReadAfter(0, limit 2): %d records, last %d, err %v", len(recs), last, err)
	}
	if recs, last, err := ReadAfter(bus, 5, 0); err != nil || len(recs) != 0 || last != 5 {
		t.Fatalf("ReadAfter at the end: %d records, last %d, err %v", len(recs), last, err)
	}
	if _, err := bus.Commit("turn", "c", nil); !errors.Is(err, events.ErrReadOnly) {
		t.Fatalf("reader could write: %v", err)
	}
}

func TestLineAndDigestRendering(t *testing.T) {
	names := Names{"c1": "worker-a"}
	turn := Record{ID: "01J0000000000000000ABCDEF", Kind: KindTurn, From: "c1", Tool: "codex", Tier: TierUrgent, Text: "line one\n  line two  ", Q: true}
	if got, want := Line(turn, names), "[urgent ?] worker-a (codex) #ABCDEF: line one line two"; got != want {
		t.Fatalf("Line turn:\n got %q\nwant %q", got, want)
	}
	done := Record{ID: "01J0000000000000000ABCDEG", Kind: KindTurn, From: "c2", Tool: "claude", Tier: TierUrgent, Done: "ok", Summary: "built it", Text: "long text"}
	if got, want := Line(done, names), "[done ok] c2 (claude) #ABCDEG: built it"; got != want {
		t.Fatalf("Line done: %q", got)
	}
	remote := Record{ID: "01J0000000000000000ABCDEH", Kind: KindStatus, From: "c3", Tool: "shell", Origin: "box", State: "waiting"}
	if got, want := Line(remote, nil), "[status] box:c3 (shell) #ABCDEH: waiting"; got != want {
		t.Fatalf("Line status: %q", got)
	}
	errRec := Record{ID: "01J0000000000000000ABCDEJ", Kind: KindError, From: "c4", Err: "boom"}
	if got := Line(errRec, nil); got != "[error] c4 #ABCDEJ: boom" {
		t.Fatalf("Line error: %q", got)
	}
	info := Record{ID: "01J0000000000000000ABCDEK", Kind: KindTurn, From: "c1", Tier: TierInfo, Text: "progress"}
	digest := Digest([]Record{turn, info}, names)
	lines := strings.Split(digest, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "[agent-deck msg] 2 pending (1 urgent)") || !strings.HasPrefix(lines[2], "[info] worker-a #ABCDEK: progress") {
		t.Fatalf("Digest:\n%s", digest)
	}
	if Digest(nil, nil) != "" {
		t.Fatal("empty digest must render nothing")
	}
	if got := Age(Record{TRecord: time.Now().Add(-90 * time.Second).UnixMilli()}, time.Now()); got != "1m" {
		t.Fatalf("Age: %q", got)
	}
}

func TestRemoteFirstExportImportIsIdempotentPerOrigin(t *testing.T) {
	remoteDir, localDir := t.TempDir(), t.TempDir()
	remote, err := OpenDir("default", remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	for i, text := range []string{"one", "two", "three"} {
		if _, _, err := remote.Commit(Record{Kind: KindTurn, From: "child-r", Key: Key(KindTurn, "child-r", text), Text: text, Tier: TierInfo, TSignal: int64(1000 + i)}); err != nil {
			t.Fatal(err)
		}
	}
	// The remote's records carry its host; the exported pairs carry the
	// remote cursor the puller advances to.
	rbus, err := OpenReaderDir(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	defer rbus.Close()
	exported, err := Export(rbus, 1, 0)
	if err != nil || len(exported) != 2 || exported[0].Cursor != 2 || exported[1].Record.Text != "three" {
		t.Fatalf("Export(after 1): %+v err %v", exported, err)
	}
	if exported[0].Record.Host == "" {
		t.Fatal("exported record has no host")
	}

	local, err := OpenDir("default", localDir)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	// A local child with the SAME key must not collide with the remote's.
	if _, _, err := local.Commit(Record{Kind: KindTurn, From: "child-r", Key: Key(KindTurn, "child-r", "two"), Text: "local two"}); err != nil {
		t.Fatal(err)
	}
	done, err := local.Import("agentbox", exported)
	if err != nil || done != 3 {
		t.Fatalf("Import: done=%d err=%v", done, err)
	}
	// A re-pull of the same window is idempotent and still reports the cursor.
	done, err = local.Import("agentbox", exported)
	if err != nil || done != 3 {
		t.Fatalf("re-Import: done=%d err=%v", done, err)
	}
	// A different origin with the same keys is a different record.
	if done, err := local.Import("g14", exported[:1]); err != nil || done != 2 {
		t.Fatalf("Import from g14: done=%d err=%v", done, err)
	}
	lbus, err := OpenReaderDir(localDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lbus.Close()
	recs, _, err := ReadAfter(lbus, 0, 0)
	if err != nil || len(recs) != 4 {
		t.Fatalf("local ledger: %d records err %v", len(recs), err)
	}
	imported := recs[1]
	if imported.Origin != "agentbox" || imported.SrcCursor != 2 || imported.Text != "two" || imported.Seq != 2 || imported.Host == "" {
		t.Fatalf("imported record: %+v", imported)
	}
	if imported.ID != exported[0].Record.ID || imported.TRecord != exported[0].Record.TRecord {
		t.Fatal("import must keep the origin's id and commit time")
	}
	if recs[3].Origin != "g14" || recs[3].DedupKey() == imported.DedupKey() {
		t.Fatalf("origin namespacing: %+v", recs[3])
	}
	if got := Line(imported, nil); !strings.HasPrefix(got, "[info] agentbox:child-r #") {
		t.Fatalf("remote record line: %q", got)
	}
	if _, err := local.Import("", exported); err == nil {
		t.Fatal("import without origin accepted")
	}
}
