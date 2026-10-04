package comms

import (
	"errors"
	"os"
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
	if _, _, err := l.Commit(Record{Kind: KindTurn, From: "child", Key: Key(KindTurn, "child", "u1"), Text: "one", Tool: "codex"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate key accepted: %v", err)
	}
	if stored, _, err := l.Commit(Record{Kind: KindTurn, From: "child", Key: Key(KindTurn, "child", "u1"), Text: "one again"}); !errors.Is(err, ErrConflict) || stored.ID != r1.ID {
		t.Fatalf("same key, different content must be a conflict with the stored record: %+v %v", stored, err)
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
	if _, _, err := l2.Commit(Record{Kind: KindTurn, From: "child", Key: Key(KindTurn, "child", "u2"), Text: "two"}); !errors.Is(err, ErrDuplicate) {
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
	exported, _, err := Export(rbus, 1, 0)
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
	// The same remote record pulled through another alias is the same
	// record: dedup is namespaced by the origin STORE id, not the alias.
	if done, err := local.Import("g14", exported[:1]); err != nil || done != 2 {
		t.Fatalf("Import from g14: done=%d err=%v", done, err)
	}
	lbus, err := OpenReaderDir(localDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lbus.Close()
	recs, _, err := ReadAfter(lbus, 0, 0)
	if err != nil || len(recs) != 3 {
		t.Fatalf("local ledger: %d records err %v", len(recs), err)
	}
	imported := recs[1]
	if imported.Origin != "agentbox" || imported.SrcCursor != 2 || imported.Text != "two" || imported.Seq != 2 || imported.Host == "" {
		t.Fatalf("imported record: %+v", imported)
	}
	if imported.ID != exported[0].Record.ID || imported.TRecord != exported[0].Record.TRecord {
		t.Fatal("import must keep the origin's id and commit time")
	}
	if imported.DedupKey() == recs[0].DedupKey() || imported.Store == recs[0].Store {
		t.Fatalf("store namespacing: local %s vs imported %s", recs[0].DedupKey(), imported.DedupKey())
	}
	if got := Line(imported, nil); !strings.HasPrefix(got, "[info] agentbox:child-r #") {
		t.Fatalf("remote record line: %q", got)
	}
	if _, err := local.Import("", exported); err == nil {
		t.Fatal("import without origin accepted")
	}
}

func r2time() time.Time { return time.UnixMilli(1_700_000_000_000) }

func TestSendRetryWithTheSameRequestIDReturnsTheStoredReceipt(t *testing.T) {
	l, err := OpenDir("p", t.TempDir()+"/ledger")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	send := Record{Kind: KindSend, From: "conductor", To: []string{"child"}, Req: "req-42", Key: SendKey("conductor", "req-42"), Text: "do the thing", State: StateQueued}
	first, cursor, err := l.Commit(send)
	if err != nil || cursor != 1 || first.V != SchemaVersion {
		t.Fatalf("first send: %+v %d %v", first, cursor, err)
	}
	// The receipt exists before any action is taken: a retry returns it.
	again, c2, err := l.Commit(Record{Kind: KindSend, From: "conductor", To: []string{"child"}, Req: "req-42", Key: SendKey("conductor", "req-42"), Text: "do the thing", State: StateQueued})
	if !errors.Is(err, ErrDuplicate) || c2 != 0 || again.ID != first.ID || again.TRecord != first.TRecord {
		t.Fatalf("retry: %+v %d %v", again, c2, err)
	}
	// The same request id with different content is a conflict, not a
	// second delivery and not a silent duplicate.
	if _, _, err := l.Commit(Record{Kind: KindSend, From: "conductor", To: []string{"child"}, Req: "req-42", Key: SendKey("conductor", "req-42"), Text: "do another thing", State: StateQueued}); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry with different content: %v", err)
	}
	if got, ok := l.Lookup(SendKey("conductor", "req-42")); !ok || got.ID != first.ID {
		t.Fatalf("Lookup: %+v %v", got, ok)
	}
	if _, ok := l.Lookup(SendKey("conductor", "req-43")); ok {
		t.Fatal("unknown request id found")
	}
	bus, _ := OpenReaderDir(l.bus.Stats().Dir)
	defer bus.Close()
	if recs, _, _ := ReadAfter(bus, 0, 0); len(recs) != 1 {
		t.Fatalf("a retried send must not add a record: %d", len(recs))
	}
}

func TestRestoreFromOlderCopyBumpsTheEpoch(t *testing.T) {
	dir := t.TempDir() + "/ledger"
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := l.Commit(Record{Kind: KindTurn, From: "c", Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	store := l.Store()
	_ = l.Close() // persists hwm=3
	// "Restore from backup": the log holds only the first line again.
	data, _ := os.ReadFile(dir + "/active.ndjson")
	first := strings.SplitAfter(string(data), "\n")[0]
	if err := os.WriteFile(dir+"/active.ndjson", []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.Store().ID != store.ID || l2.Store().Epoch != store.Epoch+1 {
		t.Fatalf("restore not detected: %+v -> %+v", store, l2.Store())
	}
	if err := (ConsumerState{Store: store.ID, Epoch: store.Epoch}).Check(l2.Store()); err != ErrEpoch {
		t.Fatalf("a pre-restore consumer state must be rejected: %v", err)
	}
}

func TestOpenFailsVisiblyWhenTheTailIsUnreadable(t *testing.T) {
	dir := t.TempDir() + "/ledger"
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := l.Commit(Record{Kind: KindTurn, From: "c", Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = l.Close()
	// cursor.state says 5 but the log only reaches 2: a reader can never
	// reach the writer's cursor. Open must return promptly with an error,
	// not hang the daemon.
	if err := os.WriteFile(dir+"/cursor.state", []byte("5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = OpenDir("p", dir)
	if err == nil {
		t.Fatal("open with an unreachable tail reported success")
	}
	if time.Since(start) > 2*warmTimeout {
		t.Fatalf("open took %v", time.Since(start))
	}
}

func TestQuotaIsAnExplicitOutcome(t *testing.T) {
	dir := t.TempDir() + "/ledger"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bus, err := events.OpenAt(dir, events.Options{Private: true, MaxBytes: 300})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	var got error
	n := 0
	for i := 0; i < 50 && got == nil; i++ {
		_, got = bus.Commit("turn", "c", map[string]string{"text": "0123456789"})
		if got == nil {
			n++
		}
	}
	if !errors.Is(got, events.ErrQuota) || n == 0 || n >= 50 {
		t.Fatalf("quota: committed %d, err %v", n, got)
	}
	if st := bus.Stats(); st.Cursor != events.Cursor(n) {
		t.Fatalf("a refused frame must not advance the cursor: %d vs %d", st.Cursor, n)
	}
}

func TestImportDropsOurOwnEchoAndKeepsOriginStore(t *testing.T) {
	dir := t.TempDir() + "/ledger"
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	mine, _, err := l.Commit(Record{Kind: KindTurn, From: "c", Key: "k1", Text: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	echo := mine // our record coming back from another host
	echo.Origin = "box"
	done, err := l.Import("box", []Exported{{Cursor: 7, Record: echo}})
	if err != nil || done != 7 {
		t.Fatalf("echo import: %d %v", done, err)
	}
	bus, _ := OpenReaderDir(dir)
	defer bus.Close()
	if recs, _, _ := ReadAfter(bus, 0, 0); len(recs) != 1 {
		t.Fatalf("echo was re-imported: %d records", len(recs))
	}
}
