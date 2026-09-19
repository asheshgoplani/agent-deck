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
