package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

// harnessFixture lays out every non-Claude harness's fixture under one
// base dir and returns the roots the ingester walks.
type harnessFixture struct {
	t                                   *testing.T
	st                                  *store.Store
	base                                string
	codex, pi, gemini, opencode, hermes string
	rollout, piFile, geminiFile         string
	queue                               string
}

func newHarnessFixture(t *testing.T) *harnessFixture {
	t.Helper()
	base := t.TempDir()
	f := &harnessFixture{t: t, base: base}
	f.codex = filepath.Join(base, ".codex")
	f.pi = filepath.Join(base, ".pi")
	f.gemini = filepath.Join(base, ".gemini")
	f.hermes = filepath.Join(base, ".hermes")
	var err error
	if f.rollout, err = testcorpus.CodexHome(f.codex, -1); err != nil {
		t.Fatal(err)
	}
	if f.piFile, err = testcorpus.PiHome(f.pi); err != nil {
		t.Fatal(err)
	}
	if f.geminiFile, err = testcorpus.GeminiHome(f.gemini); err != nil {
		t.Fatal(err)
	}
	if f.opencode, err = testcorpus.OpenCodeTree(filepath.Join(base, "share")); err != nil {
		t.Fatal(err)
	}
	if _, err = testcorpus.HermesHome(f.hermes); err != nil {
		t.Fatal(err)
	}
	if f.st, err = store.Open(filepath.Join(base, "data", "recall.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.st.Close)
	f.queue = filepath.Join(base, "data", "recall", "queue.jsonl")
	return f
}

func (f *harnessFixture) roots() []reader.Root {
	return []reader.Root{
		{Harness: reader.HarnessCodex, Profile: "personal", Dir: f.codex},
		{Harness: reader.HarnessPi, Dir: f.pi},
		{Harness: reader.HarnessGemini, Dir: f.gemini},
		{Harness: reader.HarnessOpenCode, Dir: f.opencode},
		{Harness: reader.HarnessHermes, Dir: f.hermes},
	}
}

func (f *harnessFixture) sweep(opts Options) Result {
	f.t.Helper()
	if opts.Roots == nil {
		opts.Roots = f.roots()
	}
	res, err := New(f.st, opts).Sweep(context.Background())
	if err != nil {
		f.t.Fatalf("sweep: %v", err)
	}
	return res
}

func (f *harnessFixture) count(q string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestSweep_EveryHarnessIndexesAndResumesByItsCursorKind(t *testing.T) {
	f := newHarnessFixture(t)
	res := f.sweep(Options{})
	// 1 Codex + 1 pi + 1 Gemini + 1 OpenCode + 2 Hermes sources.
	if res.Discovered != 6 || res.Parsed != 6 || res.Errors != 0 {
		t.Fatalf("first sweep: %+v", res)
	}
	want := map[string]int64{"codex": 1, "pi": 2, "gemini": 1, "opencode": 1, "hermes": 2}
	for harness, n := range want {
		// pi: the fork_of edge creates the parent's row; hermes: the two
		// sessions; the rest one each.
		if got := f.count(`SELECT count(*) FROM session WHERE harness=?`, harness); got != n {
			t.Fatalf("%s sessions = %d want %d", harness, got, n)
		}
	}
	msgs := map[string]int64{"codex": 5, "pi": 4, "gemini": 4, "opencode": 2, "hermes": 5}
	for harness, n := range msgs {
		if got := f.count(`SELECT count(*) FROM msg m JOIN session s ON s.sess_id=m.sess_id WHERE s.harness=?`, harness); got != n {
			t.Fatalf("%s msgs = %d want %d", harness, got, n)
		}
	}
	// Titles from each harness's own source.
	for harness, title := range map[string]string{"codex": "Fix flaky auth", "pi": "hermes-eval", "gemini": "Analyze the flaky auth test.", "opencode": "Greeting and quick check-in", "hermes": "Friendly greeting #2"} {
		var got string
		if err := f.st.R.QueryRow(`SELECT title FROM session WHERE harness=? AND title<>'' ORDER BY sess_id LIMIT 1`, harness).Scan(&got); err != nil || got != title {
			t.Fatalf("%s title %q (%v) want %q", harness, got, err, title)
		}
	}
	// Edges: pi fork_of, hermes fork_of (parent_session_id), codex
	// compacted_into (a self-edge counting compactions).
	if n := f.count(`SELECT count(*) FROM conv_edge WHERE kind='fork_of'`); n != 2 {
		t.Fatalf("fork_of edges = %d", n)
	}
	if n := f.count(`SELECT count(*) FROM conv_edge e JOIN session s ON s.sess_id=e.from_sess WHERE e.kind='compacted_into' AND s.harness='codex' AND e.from_sess=e.to_sess`); n != 1 {
		t.Fatalf("compacted_into edge = %d", n)
	}
	// Codex: the four messages before the compaction are superseded, the
	// summary and the follow-up are not, and replacement_history added no
	// rows (5 messages, not 7).
	if n := f.count(`SELECT count(*) FROM msg m JOIN session s ON s.sess_id=m.sess_id WHERE s.harness='codex' AND m.superseded=1`); n != 3 {
		t.Fatalf("superseded codex msgs = %d want 3", n)
	}
	if n := f.count(`SELECT count(*) FROM msg m JOIN session s ON s.sess_id=m.sess_id WHERE s.harness='codex' AND m.superseded=0`); n != 2 {
		t.Fatalf("live codex msgs = %d want 2", n)
	}
	if n := f.count(`SELECT compacts FROM session WHERE harness='codex'`); n != 1 {
		t.Fatalf("codex compacts = %d", n)
	}
	// Cursor kinds in the ledger: bytes for codex/pi, size for gemini and
	// opencode (CursorNone), the last message id for hermes (opaque).
	var geminiParsed, hermesParsed, hermesSig string
	if err := f.st.R.QueryRow(`SELECT parsed_to FROM source WHERE harness='gemini'`).Scan(&geminiParsed); err != nil || geminiParsed != strconv.FormatInt(fileSize(t, f.geminiFile), 10) {
		t.Fatalf("gemini parsed_to %q (%v)", geminiParsed, err)
	}
	if err := f.st.R.QueryRow(`SELECT parsed_to, prefix_sig||tail_sig FROM source WHERE harness='hermes' AND path LIKE '%#'||?`, testcorpus.HermesSession).Scan(&hermesParsed, &hermesSig); err != nil || hermesParsed != "4" || hermesSig != "" {
		t.Fatalf("hermes parsed_to %q sig %q (%v): want the last message id and no byte signatures", hermesParsed, hermesSig, err)
	}

	// Nothing changed: nothing parsed.
	if res := f.sweep(Options{}); res.Parsed != 0 || res.Unchanged != 6 {
		t.Fatalf("second sweep: %+v", res)
	}

	// Gemini: the document is rewritten with one more message; the change
	// is a full reparse and the index holds exactly the new document.
	doc := strings.Replace(testcorpus.GeminiShapes, `],"summary"`, `,{"id":"u9","timestamp":"2026-01-19T12:30:00.000Z","type":"user","content":"one more prompt"}],"summary"`, 1)
	if err := os.WriteFile(f.geminiFile, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, f.geminiFile)
	if res := f.sweep(Options{}); res.Parsed != 1 {
		t.Fatalf("gemini rewrite: %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM msg m JOIN session s ON s.sess_id=m.sess_id WHERE s.harness='gemini'`); n != 5 {
		t.Fatalf("gemini msgs after rewrite = %d want 5 (no duplicates)", n)
	}
	if n := f.count(`SELECT count(*) FROM msg_fts WHERE msg_fts MATCH '"one" "more" "prompt"'`); n != 1 {
		t.Fatalf("new gemini prompt not searchable: %d", n)
	}

	// Hermes: a new message on one session moves that source only, and the
	// pass reads from the row-id cursor.
	appendHermesMessage(t, filepath.Join(f.hermes, "state.db"), testcorpus.HermesSession, "user", "another hermes question", 1786695300)
	res = f.sweep(Options{})
	if res.Parsed != 1 || res.Messages != 1 {
		t.Fatalf("hermes append: %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM msg m JOIN session s ON s.sess_id=m.sess_id WHERE s.harness='hermes'`); n != 6 {
		t.Fatalf("hermes msgs = %d want 6", n)
	}

	// OpenCode: editing a part (a later mtime) reparses the session tree.
	part := filepath.Join(f.opencode, "part", "msg_b", "prt_4.json")
	if err := os.WriteFile(part, []byte(`{"id":"prt_4","sessionID":"`+testcorpus.OpenCodeSession+`","messageID":"msg_b","type":"text","text":"The root cause was clock skew, fixed now."}`), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, part)
	if res := f.sweep(Options{}); res.Parsed != 1 {
		t.Fatalf("opencode edit: %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM msg m JOIN session s ON s.sess_id=m.sess_id WHERE s.harness='opencode'`); n != 2 {
		t.Fatalf("opencode msgs = %d want 2 (reparsed, not appended)", n)
	}
	if n := f.count(`SELECT count(*) FROM msg_fts WHERE msg_fts MATCH '"fixed"'`); n != 1 {
		t.Fatalf("edited opencode part not searchable: %d", n)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// touch moves a file's mtime forward by a second so a same-second rewrite
// is seen by the size/mtime comparison.
func touch(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

func appendHermesMessage(t *testing.T, dbPath, sessID, role, content string, ts float64) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, timestamp) VALUES (?, ?, ?, ?)`, sessID, role, content, ts); err != nil {
		t.Fatal(err)
	}
}

// TestSweepFiles_IndexesOneFileAndRefusesOutsiders: the Stop hook path.
// TestSweep_CodexTailBeyondALaggingCursorIsIndexedNextSweep pins the
// phase-3 review's finding 2: a pass that stops at Codex's projection
// cursor below the file size must leave the source re-examinable, so the
// tail is indexed once Codex commits it even when the rollout is never
// written again (the last turn of a thread).
func TestSweep_CodexTailBeyondALaggingCursorIsIndexedNextSweep(t *testing.T) {
	base := t.TempDir()
	codex := filepath.Join(base, ".codex")
	lines := strings.SplitAfter(testcorpus.CodexShapes, "\n")
	cursor := int64(len(strings.Join(lines[:6], "")))
	size := int64(len(testcorpus.CodexShapes))
	if _, err := testcorpus.CodexHome(codex, cursor); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "data", "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	f := &harnessFixture{t: t, st: st, base: base, codex: codex}
	roots := []reader.Root{{Harness: reader.HarnessCodex, Profile: "personal", Dir: codex}}
	hits := func(term string) int64 {
		return f.count(`SELECT count(*) FROM msg_fts WHERE msg_fts MATCH ?`, term)
	}

	res := f.sweep(Options{Roots: roots})
	if res.Parsed != 1 || res.Deferred != 0 || res.Messages != 2 {
		t.Fatalf("sweep 1: %+v", res)
	}
	var parsedTo, ledgerSize, state int64
	if err := st.R.QueryRow(`SELECT parsed_to, size, state FROM source`).Scan(&parsedTo, &ledgerSize, &state); err != nil {
		t.Fatal(err)
	}
	if parsedTo != cursor || state != recall.SourceOK {
		t.Fatalf("after sweep 1: parsed_to %d want %d, state %d", parsedTo, cursor, state)
	}
	if ledgerSize != cursor {
		t.Fatalf("the ledger must record the size it parsed to (%d), not the file's (%d), or the tail is orphaned", cursor, ledgerSize)
	}
	if hits("regression") != 0 {
		t.Fatal("the tail past the cursor must not be indexed yet")
	}

	// Codex commits the rest of the thread; the rollout itself is untouched.
	if err := testcorpus.SetCodexCursor(codex, size); err != nil {
		t.Fatal(err)
	}
	res = f.sweep(Options{Roots: roots})
	if res.Unchanged != 0 || res.Parsed != 1 || res.Messages != 3 {
		t.Fatalf("sweep 2 must re-examine the source and index the tail: %+v", res)
	}
	if err := st.R.QueryRow(`SELECT parsed_to, size FROM source`).Scan(&parsedTo, &ledgerSize); err != nil {
		t.Fatal(err)
	}
	if parsedTo != size || ledgerSize != size {
		t.Fatalf("after sweep 2: parsed_to %d size %d want %d", parsedTo, ledgerSize, size)
	}
	if hits("regression") != 1 || f.count(`SELECT count(*) FROM msg`) != 5 {
		t.Fatalf("tail not indexed: regression hits %d, msgs %d", hits("regression"), f.count(`SELECT count(*) FROM msg`))
	}
	// Fully parsed and unchanged: steady state.
	if res = f.sweep(Options{Roots: roots}); res.Unchanged != 1 || res.Parsed != 0 {
		t.Fatalf("sweep 3: %+v", res)
	}
}

// TestSweep_CodexEmptyCompactionSupersedesWithoutARow: the ingest side of
// finding 7. An empty compaction marks the earlier messages superseded and
// writes the compacted_into edge, but inserts no zero-character row; a
// compaction-only rollout leaves no session behind.
func TestSweep_CodexEmptyCompactionSupersedesWithoutARow(t *testing.T) {
	base := t.TempDir()
	codex := filepath.Join(base, ".codex")
	rollout, err := testcorpus.CodexHome(codex, -1)
	if err != nil {
		t.Fatal(err)
	}
	withEmpty := strings.Replace(testcorpus.CodexShapes, `"message":"Summary so far: the auth test flakes on clock skew."`, `"message":""`, 1)
	if err := os.WriteFile(rollout, []byte(withEmpty), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second rollout of another thread holding only its header and a
	// compaction.
	var only strings.Builder
	for _, l := range strings.SplitAfter(withEmpty, "\n") {
		if strings.Contains(l, `"type":"session_meta"`) || strings.Contains(l, `"type":"compacted"`) {
			only.WriteString(strings.ReplaceAll(l, testcorpus.CodexThread, "0199aaaa-0000-7000-8000-000000000002"))
		}
	}
	compactOnly := filepath.Join(filepath.Dir(rollout), "rollout-2026-09-19T14-00-00-0199aaaa-0000-7000-8000-000000000002.jsonl")
	if err := os.WriteFile(compactOnly, []byte(only.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "data", "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	f := &harnessFixture{t: t, st: st, base: base, codex: codex}
	res := f.sweep(Options{Roots: []reader.Root{{Harness: reader.HarnessCodex, Profile: "personal", Dir: codex}}})
	if res.Parsed != 2 || res.Errors != 0 {
		t.Fatalf("sweep: %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM session`); n != 1 {
		t.Fatalf("sessions = %d: the compaction-only rollout must not add one", n)
	}
	if n := f.count(`SELECT count(*) FROM msg WHERE nchars=0`); n != 0 {
		t.Fatalf("%d empty rows stored for the compaction", n)
	}
	if n := f.count(`SELECT count(*) FROM msg`); n != 4 {
		t.Fatalf("msgs = %d want 4 (the fixture's five less the empty summary)", n)
	}
	if n := f.count(`SELECT count(*) FROM msg WHERE superseded=1`); n != 3 {
		t.Fatalf("superseded = %d want the three messages before the compaction", n)
	}
	if n := f.count(`SELECT count(*) FROM conv_edge WHERE kind='compacted_into'`); n != 1 {
		t.Fatalf("compacted_into edges = %d", n)
	}
	if n := f.count(`SELECT compacts FROM session`); n != 1 {
		t.Fatalf("compacts = %d", n)
	}

	// An index written by the earlier reader holds empty rows: the next
	// sweep removes them once and leaves every other row alone.
	sessID := f.count(`SELECT sess_id FROM session`)
	if _, err := st.W.Exec(`INSERT INTO msg(sess_id, src_id, seq, role, class, ts, rec_off, rec_len, nchars, tool_name, is_error, is_interrupt, body, superseded)
		VALUES (?, 1, 99, 1, 9, 0, 0, 0, 0, '', 0, 0, X'', 1)`, sessID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`DELETE FROM meta WHERE k='mig_empty_compact'`); err != nil {
		t.Fatal(err)
	}
	f.sweep(Options{Roots: []reader.Root{{Harness: reader.HarnessCodex, Profile: "personal", Dir: codex}}})
	if n := f.count(`SELECT count(*) FROM msg WHERE nchars=0`); n != 0 {
		t.Fatalf("migration left %d empty rows", n)
	}
	if n := f.count(`SELECT count(*) FROM msg`); n != 4 {
		t.Fatalf("migration touched other rows: %d", n)
	}
}

func TestSweepFiles_IndexesOneFileAndRefusesOutsiders(t *testing.T) {
	f := newHarnessFixture(t)
	in := New(f.st, Options{Roots: f.roots()})
	outside := filepath.Join(f.base, "elsewhere", "rollout-2026-09-19T13-04-34-"+testcorpus.CodexThread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(testcorpus.CodexShapes), 0o644); err != nil {
		t.Fatal(err)
	}
	opens := reader.FSCallCounts()
	res, err := in.SweepFiles(context.Background(), []string{f.rollout, f.piFile, outside, f.geminiFile})
	if err != nil {
		t.Fatal(err)
	}
	// Codex and pi are hook-addressable; the outsider and the Gemini file
	// (sweep-only harness) are refused without being opened.
	if res.Discovered != 2 || res.Parsed != 2 || res.Errors != 2 || res.Sessions != 2 {
		t.Fatalf("SweepFiles: %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM session WHERE harness IN ('codex','pi') AND turns>0`); n != 2 {
		t.Fatalf("sessions after SweepFiles = %d", n)
	}
	if n := f.count(`SELECT count(*) FROM source`); n != 2 {
		t.Fatalf("sources = %d: SweepFiles must not walk", n)
	}
	if n := f.count(`SELECT count(*) FROM card`); n != 2 {
		t.Fatalf("cards = %d: SweepFiles must project cards", n)
	}
	// The outsider was never opened (only the two transcripts plus the
	// signature reads of ingest were).
	if d := reader.FSCallCounts().Sub(opens); d.Open > 2 {
		t.Fatalf("opens = %d: the outside path was opened", d.Open)
	}
	// Unchanged on repeat.
	if res, _ := in.SweepFiles(context.Background(), []string{f.rollout}); res.Unchanged != 1 || res.Parsed != 0 {
		t.Fatalf("repeat: %+v", res)
	}
	// A full sweep afterwards finds the rest and nothing twice.
	if res := f.sweep(Options{}); res.Discovered != 6 || res.Parsed != 4 || res.Unchanged != 2 {
		t.Fatalf("full sweep after SweepFiles: %+v", res)
	}
}

// TestSweep_QueuedFilesGoFirst: under a budget that covers one file, the
// queued one is the one parsed, and the queue is consumed.
func TestSweep_QueuedFilesGoFirst(t *testing.T) {
	f := newHarnessFixture(t)
	if err := recall.Enqueue(f.queue, recall.QueueEntry{Harness: "pi", Path: f.piFile, Event: "Stop"}); err != nil {
		t.Fatal(err)
	}
	res := f.sweep(Options{QueuePath: f.queue, Budget: reader.NewBudget(0, int64(len(testcorpus.PiShapes)))})
	if res.Queued != 1 || res.Parsed < 1 || res.Deferred == 0 {
		t.Fatalf("queued sweep: %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM session WHERE harness='pi' AND turns>0`); n != 1 {
		t.Fatalf("the queued pi file was not parsed first: %d", n)
	}
	if _, err := os.Stat(f.queue); !os.IsNotExist(err) {
		t.Fatal("queue not drained")
	}
	if res := f.sweep(Options{QueuePath: f.queue}); res.Queued != 0 {
		t.Fatalf("second drain: %+v", res)
	}
}
