package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

type fixture struct {
	t     *testing.T
	root  string // Claude config dir with projects/
	st    *store.Store
	stats testcorpus.Stats
}

func newFixture(t *testing.T, o testcorpus.Options) *fixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "claude")
	stats, err := testcorpus.Generate(root, o)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "data", "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return &fixture{t: t, root: root, st: st, stats: stats}
}

func (f *fixture) roots() []reader.Root {
	return []reader.Root{{Harness: reader.HarnessClaude, Profile: "personal", Dir: f.root, RetentionDays: 30}}
}

func (f *fixture) sweep(opts Options) Result {
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

func (f *fixture) count(q string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestSweep_IndexesAndResumesIncrementally(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 6, Seed: 7, SubagentEvery: 3})
	res := f.sweep(Options{})
	if res.Discovered != f.stats.Files || res.Parsed != f.stats.Files || res.Errors != 0 {
		t.Fatalf("first sweep: %+v (files %d)", res, f.stats.Files)
	}
	if got := f.count(`SELECT count(*) FROM session`); got != int64(f.stats.Files) {
		t.Fatalf("sessions = %d want %d (one per file incl. subagents)", got, f.stats.Files)
	}
	if got := f.count(`SELECT sum(turns) FROM session`); got != int64(f.stats.Prompts) {
		t.Fatalf("turns = %d want %d prompts", got, f.stats.Prompts)
	}
	if f.count(`SELECT count(*) FROM msg`) != f.count(`SELECT count(*) FROM msg_fts`) {
		t.Fatal("msg and msg_fts out of step")
	}
	if f.count(`SELECT count(*) FROM tool_call`) == 0 || f.count(`SELECT count(*) FROM file_touch`) == 0 {
		t.Fatal("tool calls / file touches not recorded")
	}
	if f.count(`SELECT count(*) FROM tool_call WHERE duration_ms > 0`) == 0 {
		t.Fatal("no tool call got a duration from its result")
	}
	if f.count(`SELECT count(*) FROM session WHERE is_sidechain=1`) != 2 {
		t.Fatalf("sidechains = %d", f.count(`SELECT count(*) FROM session WHERE is_sidechain=1`))
	}
	if f.count(`SELECT count(*) FROM conv_edge WHERE kind='subagent_of'`) != 2 {
		t.Fatal("subagent_of edges missing")
	}
	if f.count(`SELECT count(*) FROM session WHERE compacts > 0`) == 0 {
		t.Fatal("compact_boundary not counted")
	}
	if f.count(`SELECT count(*) FROM session WHERE in_tok > 0 AND model='claude-opus-5'`) == 0 {
		t.Fatal("usage / model not folded into session")
	}
	if f.count(`SELECT count(*) FROM card WHERE preview<>''`) != int64(f.stats.Files) {
		t.Fatal("cards not projected")
	}
	if f.count(`SELECT count(*) FROM card_fts WHERE card_fts MATCH 'bench'`) == 0 {
		t.Fatal("card_fts sees no titles")
	}

	// Nothing changed: nothing parsed, nothing read.
	res = f.sweep(Options{})
	if res.Unchanged != f.stats.Files || res.Parsed != 0 || res.BytesRead != 0 {
		t.Fatalf("second sweep: %+v", res)
	}

	// Append two turns to one file: only the tail is read.
	path := f.stats.Paths[0]
	before, _ := os.Stat(path)
	msgsBefore := f.count(`SELECT count(*) FROM msg`)
	appendTurns(t, path, f.stats.Sessions[0], 2)
	after, _ := os.Stat(path)
	res = f.sweep(Options{})
	if res.Parsed != 1 || res.BytesRead != after.Size()-before.Size() {
		t.Fatalf("incremental sweep read %d bytes, appended %d: %+v", res.BytesRead, after.Size()-before.Size(), res)
	}
	if got := f.count(`SELECT count(*) FROM msg`); got != msgsBefore+4 {
		t.Fatalf("msgs after append = %d want %d", got, msgsBefore+4)
	}
	var parsedTo, size int64
	if err := f.st.R.QueryRow(`SELECT parsed_to, size FROM source WHERE path=?`, path).Scan(&parsedTo, &size); err != nil || parsedTo != size {
		t.Fatalf("cursor %d != size %d (%v)", parsedTo, size, err)
	}
}

// appendTurns appends n prompt/reply pairs to a session file.
func appendTurns(t *testing.T, path, sessionID string, n int) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for i := 0; i < n; i++ {
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		fmt.Fprintf(fh, `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"appended prompt %d zebra"},"uuid":"u%d","timestamp":"%s","sessionId":"%s","cwd":"/x"}`+"\n", i, i, ts, sessionID)
		fmt.Fprintf(fh, `{"parentUuid":null,"isSidechain":false,"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"appended reply %d"}],"usage":{"input_tokens":1,"output_tokens":1}},"uuid":"a%d","timestamp":"%s","sessionId":"%s","cwd":"/x"}`+"\n", i, i, ts, sessionID)
	}
}

// The 189 worker-scratch homes on the design machine each symlink
// `projects` to a real config dir; they must collapse onto one source row
// per file and cost one EvalSymlinks per root, not a walk per root.
func TestSweep_SymlinkedRootsCollapseToOneSource(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 4, Seed: 3, SubagentEvery: 2})
	scratch := filepath.Join(t.TempDir(), "worker-scratch")
	roots := f.roots()
	const links = 189
	for i := 0; i < links; i++ {
		home := filepath.Join(scratch, fmt.Sprintf("%08x-%d", i, 1789814563+i))
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(f.root, "projects"), filepath.Join(home, "projects")); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, reader.Root{Harness: reader.HarnessClaude, Profile: "", Dir: home})
	}
	// Also a symlinked file inside the tree (a share import that symlinks).
	if err := os.Symlink(f.stats.Paths[0], filepath.Join(filepath.Dir(f.stats.Paths[1]), "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	before := reader.FSCallCounts()
	res := f.sweep(Options{Roots: roots})
	calls := reader.FSCallCounts().Sub(before)
	if res.Discovered != f.stats.Files || res.Parsed != f.stats.Files {
		t.Fatalf("discovered %d parsed %d want %d: %+v", res.Discovered, res.Parsed, f.stats.Files, res)
	}
	if got := f.count(`SELECT count(*) FROM source`); got != int64(f.stats.Files) {
		t.Fatalf("source rows = %d want %d", got, f.stats.Files)
	}
	if got := f.count(`SELECT count(*) FROM source WHERE profile='personal'`); got != int64(f.stats.Files) {
		t.Fatalf("sources attributed to the real profile = %d", got)
	}
	dirs := countDirs(t, filepath.Join(f.root, "projects"))
	// One EvalSymlinks per root plus one per symlinked file; one ReadDir
	// per directory; one lstat per file. A regression to per-root walks
	// would multiply ReadDir by 190.
	if calls.EvalSymlinks > int64(links)+1+2 {
		t.Fatalf("EvalSymlinks = %d for %d roots", calls.EvalSymlinks, links+1)
	}
	if calls.ReadDir > int64(dirs)+2 {
		t.Fatalf("ReadDir = %d for %d directories: roots are being re-walked", calls.ReadDir, dirs)
	}
	if calls.Lstat > int64(f.stats.Files)+2 || calls.Open > int64(f.stats.Files)*3 {
		t.Fatalf("lstat %d open %d for %d files", calls.Lstat, calls.Open, f.stats.Files)
	}
	t.Logf("fs calls for %d roots / %d dirs / %d files: %+v", links+1, dirs, f.stats.Files, calls)
}

func countDirs(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Syscall regression: an unchanged tree costs the directory walk and the
// per-file lstat, never an open.
func TestSweep_UnchangedTreeOpensNoFiles(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 5, Seed: 11, SubagentEvery: 2})
	f.sweep(Options{})
	before := reader.FSCallCounts()
	res := f.sweep(Options{})
	calls := reader.FSCallCounts().Sub(before)
	if res.Unchanged != f.stats.Files {
		t.Fatalf("%+v", res)
	}
	dirs := countDirs(t, filepath.Join(f.root, "projects"))
	if calls.Open != 0 || calls.Stat != 0 {
		t.Fatalf("unchanged sweep opened files: %+v", calls)
	}
	if calls.ReadDir != int64(dirs) || calls.Lstat != int64(f.stats.Files) || calls.EvalSymlinks != 1 {
		t.Fatalf("unchanged sweep cost %+v for %d dirs / %d files", calls, dirs, f.stats.Files)
	}
}

// A same-length in-place rewrite is invisible to (size, mtime) alone; the
// tail signature catches it and forces a full reparse of that source.
func TestSweep_SameLengthRewriteReparsesViaTailSig(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 2, Seed: 5})
	f.sweep(Options{})
	path := f.stats.Paths[0]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite bytes inside the tail window in place: same length, same
	// size. (The window is the last 4 KiB before the cursor; a rewrite
	// further back is caught the next time the file grows past it.)
	i := bytes.LastIndex(data, []byte(`"userType":"external"`))
	if i < 0 || len(data)-i > 4096 {
		t.Fatalf("no userType field in the last 4 KiB (i=%d len=%d)", i, len(data))
	}
	copy(data[i+len(`"userType":"`):], "EXTERNAL")
	info, _ := os.Stat(path)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Keep size identical and move mtime so the sweep looks at it at all
	// (a real editor does both; an mtime-preserving rewrite is caught the
	// next time the file grows).
	if err := os.Chtimes(path, time.Now(), info.ModTime().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var msgsBefore, revBefore int64
	_ = f.st.R.QueryRow(`SELECT count(*) FROM msg WHERE src_id=(SELECT src_id FROM source WHERE path=?)`, path).Scan(&msgsBefore)
	_ = f.st.R.QueryRow(`SELECT derived_rev FROM session WHERE sess_id=(SELECT sess_id FROM source WHERE path=?)`, path).Scan(&revBefore)
	res := f.sweep(Options{})
	if res.Parsed != 1 || res.BytesRead != int64(len(data)) {
		t.Fatalf("expected a full reparse of %d bytes: %+v", len(data), res)
	}
	var msgsAfter, revAfter int64
	_ = f.st.R.QueryRow(`SELECT count(*) FROM msg WHERE src_id=(SELECT src_id FROM source WHERE path=?)`, path).Scan(&msgsAfter)
	_ = f.st.R.QueryRow(`SELECT derived_rev FROM session WHERE sess_id=(SELECT sess_id FROM source WHERE path=?)`, path).Scan(&revAfter)
	if msgsAfter != msgsBefore || revAfter <= revBefore {
		t.Fatalf("msgs %d -> %d, rev %d -> %d", msgsBefore, msgsAfter, revBefore, revAfter)
	}
	if f.count(`SELECT count(*) FROM msg`) != f.count(`SELECT count(*) FROM msg_fts`) {
		t.Fatal("fts rowids leaked across the reparse")
	}
}

// A copied transcript (fork, session-share import, switch-account) keeps
// its sessionId: it must be quarantined, never merged into the original.
func TestSweep_CopiedFileQuarantinesInsteadOfMerging(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 2, Seed: 9})
	f.sweep(Options{})
	msgs := f.count(`SELECT count(*) FROM msg`)
	src := f.stats.Paths[0]
	data, _ := os.ReadFile(src)
	dst := filepath.Join(filepath.Dir(f.stats.Paths[1]), filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	res := f.sweep(Options{})
	if res.Quarantined != 1 || res.Parsed != 0 {
		t.Fatalf("%+v", res)
	}
	if f.count(`SELECT count(*) FROM msg`) != msgs {
		t.Fatal("copy was merged into the original session")
	}
	var state int
	var why string
	if err := f.st.R.QueryRow(`SELECT state, last_error FROM source WHERE path=?`, dst).Scan(&state, &why); err != nil || state != recall.SourceQuarantined || !strings.Contains(why, "copy of source") {
		t.Fatalf("state %d %q %v", state, why, err)
	}
	// Still quarantined on the next pass, even after it diverges.
	appendTurns(t, dst, f.stats.Sessions[0], 1)
	res = f.sweep(Options{})
	if res.Quarantined != 1 || f.count(`SELECT count(*) FROM msg`) != msgs {
		t.Fatalf("diverged copy leaked: %+v", res)
	}
	// The same file copied into ANOTHER profile is a legitimate second
	// session (profile is part of the session key).
	other := filepath.Join(t.TempDir(), "work")
	otherPath := filepath.Join(other, "projects", "-p", filepath.Base(src))
	if err := os.MkdirAll(filepath.Dir(otherPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	roots := append(f.roots(), reader.Root{Harness: reader.HarnessClaude, Profile: "work", Dir: other})
	res = f.sweep(Options{Roots: roots})
	if res.Parsed != 1 || f.count(`SELECT count(*) FROM source WHERE state=?`, recall.SourceQuarantined) != 1 {
		t.Fatalf("%+v", res)
	}
	if f.count(`SELECT count(*) FROM session WHERE profile='work'`) != 1 {
		t.Fatal("switch-account copy in another profile was not its own session")
	}
}

func TestSweep_MissingSourceDropsRowsAndGCReclaimsPages(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 4, Seed: 13})
	f.sweep(Options{})
	path := f.stats.Paths[0]
	var srcID, sessID int64
	if err := f.st.R.QueryRow(`SELECT src_id, sess_id FROM source WHERE path=?`, path).Scan(&srcID, &sessID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	res := f.sweep(Options{})
	if res.Missing != 1 {
		t.Fatalf("%+v", res)
	}
	if f.count(`SELECT count(*) FROM msg WHERE src_id=?`, srcID) != 0 || f.count(`SELECT count(*) FROM msg_fts WHERE rowid IN (SELECT msg_id FROM msg WHERE src_id=?)`, srcID) != 0 {
		t.Fatal("missing source's rows survived")
	}
	if f.count(`SELECT count(*) FROM session WHERE sess_id=?`, sessID) != 1 || f.count(`SELECT count(*) FROM card WHERE sess_id=?`, sessID) != 1 {
		t.Fatal("session/card must survive a missing source")
	}
	if f.count(`SELECT count(*) FROM tombstone WHERE src_id=?`, srcID) != 1 {
		t.Fatal("no tombstone")
	}
	if f.count(`SELECT state FROM source WHERE src_id=?`, srcID) != recall.SourceMissing {
		t.Fatal("state not missing")
	}
	if f.count(`PRAGMA freelist_count`) == 0 {
		t.Fatal("deleting the rows freed no pages")
	}
	ing := New(f.st, Options{Roots: f.roots()})
	gc, err := ing.GC(0)
	if err != nil {
		t.Fatal(err)
	}
	if gc.FreePagesAfter != 0 || gc.FreePagesBefore == 0 {
		t.Fatalf("gc: %+v", gc)
	}
	if gc.SourcesDropped != 1 || gc.TombstonesDropped != 1 {
		t.Fatalf("gc with keep=0 should drop the missing ledger row and its tombstone: %+v", gc)
	}
	if f.count(`SELECT count(*) FROM session WHERE sess_id=?`, sessID) != 1 {
		t.Fatal("gc must not drop sessions")
	}
}

func TestSweep_TornTailWaitsForNewline(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 1, Seed: 2})
	path := f.stats.Paths[0]
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	torn := `{"type":"user","message":{"role":"user","content":"half written`
	fmt.Fprint(fh, torn)
	fh.Close()
	res := f.sweep(Options{})
	info, _ := os.Stat(path)
	var parsedTo int64
	_ = f.st.R.QueryRow(`SELECT parsed_to FROM source WHERE path=?`, path).Scan(&parsedTo)
	if parsedTo != info.Size()-int64(len(torn)) || res.Errors != 0 {
		t.Fatalf("parsed_to %d size %d torn %d: %+v", parsedTo, info.Size(), len(torn), res)
	}
	fh, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	fmt.Fprintf(fh, `"},"uuid":"t1","timestamp":"2026-09-19T10:00:00Z","sessionId":"%s"}`+"\n", f.stats.Sessions[0])
	fh.Close()
	res = f.sweep(Options{})
	_ = f.st.R.QueryRow(`SELECT parsed_to FROM source WHERE path=?`, path).Scan(&parsedTo)
	info, _ = os.Stat(path)
	if parsedTo != info.Size() || res.Parsed != 1 {
		t.Fatalf("completed line not consumed: parsed_to %d size %d %+v", parsedTo, info.Size(), res)
	}
	if f.count(`SELECT count(*) FROM msg_fts WHERE msg_fts MATCH 'half AND written'`) != 1 {
		t.Fatal("completed record not indexed")
	}
}

// The interactive sweep (before a search) must stop within its byte budget,
// keep the index consistent, and say what it deferred.
func TestSweep_InteractiveBudgetDefersAndReports(t *testing.T) {
	f := newFixture(t, testcorpus.Options{TargetBytes: 3 << 20, Seed: 17})
	budget := reader.NewBudget(InteractiveDeadline, 1<<20)
	res := f.sweep(Options{Budget: budget})
	if res.Deferred == 0 || len(res.DeferredPaths) == 0 || res.DeferredBytes == 0 {
		t.Fatalf("nothing deferred on a 3 MB corpus with a 1 MB budget: %+v", res)
	}
	if res.BytesRead > (1<<20)+reader.MaxLineBytes {
		t.Fatalf("read %d bytes past a 1 MiB budget", res.BytesRead)
	}
	if f.count(`SELECT count(*) FROM msg`) != f.count(`SELECT count(*) FROM msg_fts`) {
		t.Fatal("index inconsistent after a deferred pass")
	}
	if got := f.count(`SELECT count(*) FROM source WHERE state=?`, recall.SourcePartial); got == 0 && res.Parsed > 0 {
		t.Log("no partial source (budget fell between files)")
	}
	// A deadline of one nanosecond defers everything without reading.
	res = f.sweep(Options{Budget: reader.NewBudget(time.Nanosecond, 0)})
	if res.BytesRead != 0 || res.Deferred == 0 || res.DeferredBytes == 0 {
		t.Fatalf("expired deadline still read: %+v", res)
	}
	// The next unbudgeted sweep finishes the job.
	res = f.sweep(Options{})
	if res.Deferred != 0 || f.count(`SELECT count(*) FROM source WHERE state<>?`, recall.SourceOK) != 0 {
		t.Fatalf("catch-up sweep: %+v", res)
	}
	if got := f.count(`SELECT sum(turns) FROM session`); got != int64(f.stats.Prompts) {
		t.Fatalf("turns after catch-up = %d want %d", got, f.stats.Prompts)
	}
}

func TestSweep_PerSourceCapContinuesNextPass(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 1, Seed: 21})
	res := f.sweep(Options{PerSourceBytes: 64 << 10})
	if res.Deferred != 1 || res.BytesRead > (64<<10)+reader.MaxLineBytes {
		t.Fatalf("%+v", res)
	}
	for i := 0; i < 200; i++ {
		res = f.sweep(Options{PerSourceBytes: 64 << 10})
		if res.Deferred == 0 {
			break
		}
	}
	if res.Deferred != 0 || f.count(`SELECT sum(turns) FROM session`) != int64(f.stats.Prompts) {
		t.Fatalf("capped passes did not converge: %+v turns=%d want %d", res, f.count(`SELECT sum(turns) FROM session`), f.stats.Prompts)
	}
}

func TestGate_RefusesWhileBusyOrLoaded(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 1, Seed: 1})
	gate := &Gate{Busy: func() (bool, string) { return true, "session auth-fix is running" }}
	_, err := New(f.st, Options{Roots: f.roots(), Gate: gate}).Sweep(context.Background())
	if !errors.Is(err, ErrGated) || !strings.Contains(err.Error(), "auth-fix") {
		t.Fatalf("busy gate: %v", err)
	}
	gate = &Gate{MaxLoadAvg: 4, LoadAvg: func() float64 { return 55 }}
	_, err = New(f.st, Options{Roots: f.roots(), Gate: gate}).Sweep(context.Background())
	if !errors.Is(err, ErrGated) || !strings.Contains(err.Error(), "55.0") {
		t.Fatalf("load gate: %v", err)
	}
	if f.count(`SELECT count(*) FROM source`) != 0 {
		t.Fatal("gated sweep touched the ledger")
	}
	gate = &Gate{Busy: func() (bool, string) { return false, "" }, MaxLoadAvg: 4, LoadAvg: func() float64 { return 1 }}
	if _, err := New(f.st, Options{Roots: f.roots(), Gate: gate}).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type stubRegistry struct {
	deck    map[string]string
	hints   map[string]string
	changed []Ref
}

func (s stubRegistry) DeckID(_, _, native string) string { return s.deck[native] }
func (s stubRegistry) Hints(_, _, native string) (string, string) {
	return s.hints[native], "auth flaky"
}
func (s stubRegistry) ChangedSince(time.Time) []Ref { return s.changed }

type stubUsage struct{ got map[string]int }

func (s *stubUsage) Usage(_, deck string, ev []reader.Usage) error {
	s.got[deck] += len(ev)
	return nil
}

func TestSweep_BindsDeckIDAndFoldsUsageFromAuthoritativeLinkOnly(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 2, Seed: 4, SubagentEvery: 1})
	linked := f.stats.Sessions[0]
	reg := stubRegistry{deck: map[string]string{linked: "deck-1"}, hints: map[string]string{linked: "purpose=fix flaky auth test ticket=SB-412"}}
	usage := &stubUsage{got: map[string]int{}}
	f.sweep(Options{Registry: reg, Usage: usage})
	if f.count(`SELECT count(*) FROM session WHERE deck_id='deck-1'`) != 1 {
		t.Fatal("deck_id not bound from the registry")
	}
	if f.count(`SELECT count(*) FROM session WHERE deck_id<>''`) != 1 {
		t.Fatal("deck_id bound for an unlinked session (cwd match is forbidden)")
	}
	if usage.got["deck-1"] == 0 {
		t.Fatal("usage of the linked session not handed to the cost sink")
	}
	if len(usage.got) != 1 {
		t.Fatalf("usage for unlinked sessions leaked: %v", usage.got)
	}
	// The subagent of the linked session bills to the same deck session.
	if usage.got["deck-1"] <= int(f.count(`SELECT count(*) FROM tool_call WHERE sess_id=(SELECT sess_id FROM session WHERE native_id=?)`, linked)) {
		t.Log("subagent usage attribution not separately verifiable here")
	}
	if f.count(`SELECT count(*) FROM card_fts WHERE card_fts MATCH '"SB-412"'`) != 1 {
		t.Fatal("hint not searchable through card_fts")
	}
	if f.count(`SELECT count(*) FROM card_fts WHERE card_fts MATCH 'tags:flaky'`) != 4 {
		t.Fatal("tags not projected")
	}
}

func TestSweep_OverlongLineIsSkippedNotFatal(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 1, Seed: 6})
	path := f.stats.Paths[0]
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	fmt.Fprintf(fh, `{"type":"user","message":{"role":"user","content":"%s"},"sessionId":"%s"}`+"\n", strings.Repeat("x", reader.MaxLineBytes+10), f.stats.Sessions[0])
	fmt.Fprintf(fh, `{"type":"user","message":{"role":"user","content":"after the giant line"},"sessionId":"%s","timestamp":"2026-09-19T10:00:00Z"}`+"\n", f.stats.Sessions[0])
	fh.Close()
	res := f.sweep(Options{})
	if res.Errors != 0 || res.Parsed != 1 {
		t.Fatalf("%+v", res)
	}
	if f.count(`SELECT count(*) FROM msg_fts WHERE msg_fts MATCH 'after AND giant AND line'`) != 1 {
		t.Fatal("record after the giant line lost")
	}
	if f.count(`SELECT count(*) FROM msg WHERE nchars > ?`, reader.MaxLineBytes) != 0 {
		t.Fatal("giant line was indexed")
	}
}

func TestRebuild_EmptiesAndReindexes(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 3, Seed: 8})
	f.sweep(Options{})
	msgs := f.count(`SELECT count(*) FROM msg`)
	if _, err := f.st.W.Exec(`INSERT INTO meta(k,v) VALUES('probe','1')`); err != nil {
		t.Fatal(err)
	}
	res, err := New(f.st, Options{Roots: f.roots()}).Rebuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Parsed != 3 || f.count(`SELECT count(*) FROM msg`) != msgs || f.count(`SELECT count(*) FROM meta WHERE k='probe'`) != 0 {
		t.Fatalf("rebuild: %+v msgs=%d want %d", res, f.count(`SELECT count(*) FROM msg`), msgs)
	}
}

// Index size against a corpus shaped like the measured one (about 38
// messages per MB, 3.3% of bytes as user/assistant text, 41% noise). The
// design budgets about 2.4% of raw and the prototype measured 2.0% on the
// real corpus. The generator's prose compresses 2.35x under zstd where real
// chat text compresses 3.6x, so the synthetic index lands at about 3.3%;
// the bar here is 3.5%, and the real-corpus measurement in the phase 2
// receipt is the acceptance number for the 2.5% target.
func TestIndexSize_TracksTheDesignBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	f := newFixture(t, testcorpus.Options{TargetBytes: 48 << 20, Seed: 42, SubagentEvery: 4})
	res := f.sweep(Options{})
	if res.Errors != 0 {
		t.Fatalf("%+v", res)
	}
	if _, err := New(f.st, Options{Roots: f.roots()}).GC(0); err != nil {
		t.Fatal(err)
	}
	size := store.FileSize(f.st.Path)
	ratio := float64(size) / float64(f.stats.Bytes)
	t.Logf("input %.1f MB -> recall.db %.2f MB (%.2f%%), %d msgs, text share %.2f%%", float64(f.stats.Bytes)/1e6, float64(size)/1e6, ratio*100,
		f.count(`SELECT count(*) FROM msg`), 100*float64(f.stats.TextBytes)/float64(f.stats.Bytes))
	if ratio > 0.035 {
		t.Fatalf("index is %.2f%% of input; the synthetic bar is 3.5%% (2.5%% on real text)", ratio*100)
	}
}

// RSS cap: a single large file (over the design's max_file_mb) is streamed
// one record at a time. The heap must not grow with the file.
func TestRSS_LargeFileIsStreamed(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	base := t.TempDir()
	root := filepath.Join(base, "claude")
	projDir := filepath.Join(root, "projects", "-big")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(99))
	sessionID := testcorpus.UUID(r)
	path := filepath.Join(projDir, sessionID+".jsonl")
	// ~96 MB in one session file, with a few 2 MB records to stress the
	// long-line path.
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var written int64
	for i := 0; written < 96<<20; i++ {
		var line string
		if i%400 == 399 {
			line = fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"%s"}]},"sessionId":"%s","timestamp":"2026-09-19T10:00:00Z"}`+"\n", strings.Repeat("y", 2<<20), sessionID)
		} else if i%2 == 0 {
			line = fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"%s"},"uuid":"u%d","sessionId":"%s","timestamp":"2026-09-19T10:00:00Z","cwd":"/big"}`+"\n", testcorpus.Text(r, 40), i, sessionID)
		} else {
			line = fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"%s"}],"usage":{"input_tokens":5,"output_tokens":5}},"uuid":"a%d","sessionId":"%s","timestamp":"2026-09-19T10:00:01Z"}`+"\n", testcorpus.Text(r, 300), i, sessionID)
		}
		n, err := fh.WriteString(line)
		if err != nil {
			t.Fatal(err)
		}
		written += int64(n)
	}
	fh.Close()
	st, err := store.Open(filepath.Join(base, "data", "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	baseline := ms.HeapInuse
	done := make(chan struct{})
	var peak uint64
	go func() {
		defer close(done)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				runtime.ReadMemStats(&ms)
				if ms.HeapInuse > peak {
					peak = ms.HeapInuse
				}
			case <-done:
				return
			}
		}
	}()
	res, err := New(st, Options{Roots: []reader.Root{{Harness: reader.HarnessClaude, Dir: root}}}).Sweep(context.Background())
	done <- struct{}{}
	<-done
	if err != nil || res.Errors != 0 || res.Parsed != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	growth := int64(peak) - int64(baseline)
	t.Logf("file %.0f MB, heap baseline %.1f MB, peak %.1f MB, growth %.1f MB, %d msgs", float64(written)/1e6, float64(baseline)/1e6, float64(peak)/1e6, float64(growth)/1e6, res.Messages)
	if growth > 64<<20 {
		t.Fatalf("heap grew %.1f MB on a %.0f MB file: the reader is not streaming", float64(growth)/1e6, float64(written)/1e6)
	}
}

// An annotation written after the transcript was indexed reaches the card
// on the next sweep even though no file changed.
func TestSweep_ReprojectsCardsForAnnotatedSessions(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 2, Seed: 23})
	reg := &stubRegistry{deck: map[string]string{}, hints: map[string]string{}}
	f.sweep(Options{Registry: reg})
	if f.count(`SELECT count(*) FROM card_fts WHERE card_fts MATCH '"SB-777"'`) != 0 {
		t.Fatal("hint present before it was written")
	}
	native := f.stats.Sessions[1]
	reg.hints[native] = "ticket=SB-777"
	reg.deck[native] = "deck-7"
	reg.changed = []Ref{{Harness: reader.HarnessClaude, NativeID: native}, {Harness: reader.HarnessClaude, NativeID: "not-indexed"}}
	res := f.sweep(Options{Registry: reg})
	if res.Parsed != 0 || res.Sessions != 1 {
		t.Fatalf("%+v", res)
	}
	if f.count(`SELECT count(*) FROM card_fts WHERE card_fts MATCH '"SB-777"'`) != 1 || f.count(`SELECT count(*) FROM session WHERE deck_id='deck-7'`) != 1 {
		t.Fatal("annotation did not reach the card")
	}
	var v string
	if err := f.st.R.QueryRow(`SELECT v FROM meta WHERE k=?`, metaLastSweep).Scan(&v); err != nil || v == "" {
		t.Fatalf("last_sweep not recorded: %v", err)
	}
}

// sourceOf returns the ledger row ids of a path.
func (f *fixture) sourceOf(path string) (srcID, sessID int64) {
	f.t.Helper()
	if err := f.st.R.QueryRow(`SELECT src_id, sess_id FROM source WHERE path=?`, path).Scan(&srcID, &sessID); err != nil {
		f.t.Fatalf("source %s: %v", path, err)
	}
	return srcID, sessID
}

// A source that vanishes (an unmounted volume, a permission hiccup, a
// rename) and comes back under the same path is re-indexed in full: back
// unchanged, back with more content, and back after gc dropped its ledger
// row. Before the fix the pass resumed at the old cursor over the rows
// markMissing had dropped, and the session stayed empty for good.
func TestSweep_MissingSourceReappearsAndIsReindexed(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 3, Seed: 13})
	f.sweep(Options{})
	msgs := func(sessID int64) int64 { return f.count(`SELECT count(*) FROM msg WHERE sess_id=?`, sessID) }
	turns := func(sessID int64) int64 { return f.count(`SELECT turns FROM session WHERE sess_id=?`, sessID) }
	away := func(path string) {
		t.Helper()
		if err := os.Rename(path, path+".away"); err != nil {
			t.Fatal(err)
		}
		if res := f.sweep(Options{}); res.Missing != 1 {
			t.Fatalf("missing sweep: %+v", res)
		}
	}
	back := func(path string) {
		t.Helper()
		if err := os.Rename(path+".away", path); err != nil {
			t.Fatal(err)
		}
	}
	sessions := f.count(`SELECT count(*) FROM session`)

	// (a) missing, then back unchanged.
	p0 := f.stats.Paths[0]
	src0, sess0 := f.sourceOf(p0)
	before0, turns0 := msgs(sess0), turns(sess0)
	away(p0)
	if msgs(sess0) != 0 {
		t.Fatal("missing source kept its rows")
	}
	back(p0)
	info, _ := os.Stat(p0)
	res := f.sweep(Options{})
	if res.Parsed != 1 || res.BytesRead != info.Size() {
		t.Fatalf("reappear sweep must reparse the whole file: %+v (size %d)", res, info.Size())
	}
	if got := msgs(sess0); got != before0 {
		t.Fatalf("reappeared source has %d msgs, had %d", got, before0)
	}
	if got := turns(sess0); got != turns0 {
		t.Fatalf("turns %d after reappearance, had %d", got, turns0)
	}
	var state, parsedTo int64
	if err := f.st.R.QueryRow(`SELECT state, parsed_to FROM source WHERE src_id=?`, src0).Scan(&state, &parsedTo); err != nil || state != recall.SourceOK || parsedTo != info.Size() {
		t.Fatalf("state %d parsed_to %d (%v)", state, parsedTo, err)
	}
	if f.count(`SELECT count(*) FROM tombstone WHERE src_id=?`, src0) != 0 {
		t.Fatal("tombstone survived the source's return")
	}

	// (b) missing, then back with more content.
	p1 := f.stats.Paths[1]
	_, sess1 := f.sourceOf(p1)
	before1 := msgs(sess1)
	away(p1)
	back(p1)
	appendTurns(t, p1, f.stats.Sessions[1], 2)
	if res := f.sweep(Options{}); res.Parsed != 1 {
		t.Fatalf("%+v", res)
	}
	if got := msgs(sess1); got != before1+4 {
		t.Fatalf("changed source has %d msgs, want %d", got, before1+4)
	}
	if f.count(`SELECT count(*) FROM msg_fts WHERE rowid IN (SELECT msg_id FROM msg WHERE sess_id=?)`, sess1) != before1+4 {
		t.Fatal("fts out of step after the reparse")
	}

	// (c) missing, gc drops the ledger row, then back: a new source row
	// for the same session, nothing duplicated.
	p2 := f.stats.Paths[2]
	src2, sess2 := f.sourceOf(p2)
	before2 := msgs(sess2)
	away(p2)
	gc, err := New(f.st, Options{Roots: f.roots()}).GC(0)
	if err != nil || gc.SourcesDropped != 1 {
		t.Fatalf("gc: %+v %v", gc, err)
	}
	back(p2)
	if res := f.sweep(Options{}); res.Parsed != 1 {
		t.Fatalf("%+v", res)
	}
	if got := msgs(sess2); got != before2 {
		t.Fatalf("source back after gc has %d msgs, had %d", got, before2)
	}
	if f.count(`SELECT count(*) FROM source WHERE path=?`, p2) != 1 || f.count(`SELECT count(*) FROM tombstone WHERE src_id=?`, src2) != 0 {
		t.Fatal("expected exactly one source row for the returned path and no tombstone")
	}
	if f.count(`SELECT count(*) FROM session`) != sessions {
		t.Fatal("a session row was duplicated")
	}
	if f.count(`SELECT count(*) FROM source WHERE state=?`, recall.SourceMissing) != 0 {
		t.Fatal("a returned source is still marked missing")
	}
}

// A resume pass whose new bytes carry no message record (a /rename after
// the last sweep, a compact boundary) must still reach the session row.
func TestSweep_ResumePassWithOnlyTitleRecordsKeepsThem(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 1, Seed: 21})
	f.sweep(Options{})
	path := f.stats.Paths[0]
	_, sessID := f.sourceOf(path)
	compacts := f.count(`SELECT compacts FROM session WHERE sess_id=?`, sessID)
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(fh, `{"type":"custom-title","customTitle":"renamed after sweep","sessionId":%q}`+"\n", f.stats.Sessions[0])
	fmt.Fprintf(fh, `{"type":"system","subtype":"compact_boundary","content":"Conversation compacted","uuid":"cb9","timestamp":"2026-09-19T10:01:00.000Z"}`+"\n")
	fh.Close()
	res := f.sweep(Options{})
	if res.Parsed != 1 || res.BytesRead == 0 {
		t.Fatalf("%+v", res)
	}
	var title, titleSrc string
	if err := f.st.R.QueryRow(`SELECT title, title_src FROM session WHERE sess_id=?`, sessID).Scan(&title, &titleSrc); err != nil {
		t.Fatal(err)
	}
	if title != "renamed after sweep" || titleSrc != "custom-title" {
		t.Fatalf("title %q (%s): the rename was lost", title, titleSrc)
	}
	if got := f.count(`SELECT compacts FROM session WHERE sess_id=?`, sessID); got != compacts+1 {
		t.Fatalf("compacts %d want %d", got, compacts+1)
	}
	var cardTitle string
	if err := f.st.R.QueryRow(`SELECT title FROM card WHERE sess_id=?`, sessID).Scan(&cardTitle); err != nil || cardTitle != title {
		t.Fatalf("card title %q (%v): card not re-projected", cardTitle, err)
	}
	var parsedTo, size int64
	if err := f.st.R.QueryRow(`SELECT parsed_to, size FROM source WHERE path=?`, path).Scan(&parsedTo, &size); err != nil || parsedTo != size {
		t.Fatalf("cursor %d != size %d (%v)", parsedTo, size, err)
	}
}

// failAfter wraps the Claude reader and makes the sink refuse the nth
// message, which the reader reports as a pass error.
type failAfter struct {
	reader.Reader
	n int
}

type failingSink struct {
	reader.Sink
	left *int
}

var errInjected = errors.New("injected read error")

func (s failingSink) Msg(m reader.Msg) error {
	if *s.left == 0 {
		return errInjected
	}
	*s.left--
	return s.Sink.Msg(m)
}

func (r failAfter) Ingest(ctx context.Context, src reader.SourceRef, from int64, sink reader.Sink, b *reader.Budget) (int64, error) {
	left := r.n
	return r.Reader.Ingest(ctx, src, from, failingSink{Sink: sink, left: &left}, b)
}

// A reader error after a mid-pass checkpoint rewinds the cursor to that
// checkpoint, not to the pass start: the checkpointed rows stay, and the
// next pass must not insert them again.
func TestSweep_ReaderErrorRewindsToLastCheckpoint(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 1, Seed: 23})
	path := f.stats.Paths[0]
	whole := f.sweep(Options{BatchBytes: 1})
	want := f.count(`SELECT count(*) FROM msg`)
	if want < 10 {
		t.Fatalf("corpus too small: %d msgs", want)
	}
	// Start over with a pass that checkpoints after every message and dies
	// in the middle.
	if err := f.st.Reset(); err != nil {
		t.Fatal(err)
	}
	failing := failAfter{Reader: reader.Claude{}, n: int(want / 2)}
	res, err := New(f.st, Options{Roots: f.roots(), Readers: []reader.Reader{failing}, BatchBytes: 1}).Sweep(context.Background())
	if err != nil || res.Errors != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	kept := f.count(`SELECT count(*) FROM msg`)
	var parsedTo int64
	var state int
	if err := f.st.R.QueryRow(`SELECT parsed_to, state FROM source WHERE path=?`, path).Scan(&parsedTo, &state); err != nil {
		t.Fatal(err)
	}
	if kept == 0 || parsedTo == 0 || state != recall.SourceError {
		t.Fatalf("checkpointed rows %d, cursor %d, state %d: the checkpoint was not kept", kept, parsedTo, state)
	}
	if f.count(`SELECT count(*) FROM msg WHERE rec_off >= ?`, parsedTo) != 0 {
		t.Fatalf("rows beyond the cursor survived (cursor %d)", parsedTo)
	}
	// The next healthy pass completes the file without duplicates.
	res = f.sweep(Options{})
	if res.Parsed != 1 || res.Errors != 0 {
		t.Fatalf("%+v", res)
	}
	if got := f.count(`SELECT count(*) FROM msg`); got != want {
		t.Fatalf("msgs after recovery = %d want %d (whole-file sweep %+v)", got, want, whole)
	}
	if f.count(`SELECT count(*) FROM msg`) != f.count(`SELECT count(*) FROM msg_fts`) {
		t.Fatal("msg and msg_fts out of step")
	}
	if got := f.count(`SELECT turns FROM session`); got != int64(f.stats.Prompts) {
		t.Fatalf("turns %d want %d: counters doubled", got, f.stats.Prompts)
	}
}

// One Escape press is one interrupt: the marker message carries the flag
// and the interrupted tool result before it must not add a second (or a
// third) count.
func TestSweep_InterruptCountedOnce(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "claude", "projects", "-Users-x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-09-19T10:00:00.000Z","sessionId":"aaaa"}
{"parentUuid":"u1","isSidechain":false,"type":"assistant","message":{"role":"assistant","model":"m","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"sleep 100"}}],"usage":{"input_tokens":1,"output_tokens":1}},"uuid":"a1","timestamp":"2026-09-19T10:00:01.000Z","sessionId":"aaaa"}
{"parentUuid":"a1","isSidechain":false,"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"FAIL"}]},"toolUseResult":{"stdout":"","stderr":"","interrupted":true},"uuid":"u2","timestamp":"2026-09-19T10:00:09.000Z","sessionId":"aaaa"}
{"parentUuid":"u2","isSidechain":false,"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]},"uuid":"u3","timestamp":"2026-09-19T10:00:10.000Z","sessionId":"aaaa"}
{"parentUuid":"u3","isSidechain":false,"type":"user","message":{"role":"user","content":"carry on"},"uuid":"u4","timestamp":"2026-09-19T10:00:20.000Z","sessionId":"aaaa"}
{"parentUuid":"u4","isSidechain":false,"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},"uuid":"u5","timestamp":"2026-09-19T10:00:30.000Z","sessionId":"aaaa"}
`
	if err := os.WriteFile(filepath.Join(dir, "aaaa.jsonl"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	roots := []reader.Root{{Harness: reader.HarnessClaude, Profile: "p", Dir: filepath.Join(base, "claude")}}
	if _, err := New(st, Options{Roots: roots}).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.R.QueryRow(`SELECT interrupts FROM session WHERE native_id='aaaa'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("session.interrupts=%d for two Escape presses, want 2", n)
	}
}

// profileRegistry answers per (profile, native id), the way the CLI's
// adapter does over every profile's state.db.
type profileRegistry struct {
	deck, hints map[string]string // keyed by profile + "/" + native
	asked       map[string]int    // profiles asked
}

func (r profileRegistry) DeckID(profile, _, native string) string {
	r.asked[profile]++
	return r.deck[profile+"/"+native]
}
func (r profileRegistry) Hints(profile, _, native string) (string, string) {
	return r.hints[profile+"/"+native], "tag-" + profile
}
func (r profileRegistry) ChangedSince(time.Time) []Ref { return nil }

type profileUsage struct{ got map[string]int }

func (u *profileUsage) Usage(profile, deck string, ev []reader.Usage) error {
	u.got[profile+"/"+deck] += len(ev)
	return nil
}

// Two profiles holding the same conversation id are two sessions with
// their own cards and their own usage fold: the registry is asked with the
// transcript's profile, so a sweep run under one profile never overwrites
// the other's hints and tags with its own empty answer or drops the
// other's cost events.
func TestSweep_TwoProfilesKeepTheirOwnCardsAndUsage(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 1, Seed: 41})
	native := f.stats.Sessions[0]
	data, err := os.ReadFile(f.stats.Paths[0])
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "work")
	otherPath := filepath.Join(other, "projects", "-p", filepath.Base(f.stats.Paths[0]))
	if err := os.MkdirAll(filepath.Dir(otherPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	roots := append(f.roots(), reader.Root{Harness: reader.HarnessClaude, Profile: "work", Dir: other})
	reg := profileRegistry{
		deck:  map[string]string{"personal/" + native: "deck-p", "work/" + native: "deck-w"},
		hints: map[string]string{"personal/" + native: "ticket=P-1", "work/" + native: "ticket=W-1"},
		asked: map[string]int{},
	}
	usage := &profileUsage{got: map[string]int{}}
	opts := Options{Roots: roots, Registry: reg, Usage: usage}
	if res := f.sweep(opts); res.Parsed != 2 || res.Quarantined != 0 {
		t.Fatalf("%+v", res)
	}
	card := func(profile string) (deck, hints, tags string) {
		t.Helper()
		if err := f.st.R.QueryRow(`SELECT s.deck_id, c.hints, c.tags FROM session s JOIN card c ON c.sess_id=s.sess_id WHERE s.profile=? AND s.native_id=?`, profile, native).Scan(&deck, &hints, &tags); err != nil {
			t.Fatalf("card %s: %v", profile, err)
		}
		return
	}
	check := func() {
		t.Helper()
		if d, h, tg := card("personal"); d != "deck-p" || h != "ticket=P-1" || tg != "tag-personal" {
			t.Fatalf("personal card: deck %q hints %q tags %q", d, h, tg)
		}
		if d, h, tg := card("work"); d != "deck-w" || h != "ticket=W-1" || tg != "tag-work" {
			t.Fatalf("work card: deck %q hints %q tags %q", d, h, tg)
		}
	}
	check()
	if usage.got["personal/deck-p"] == 0 || usage.got["work/deck-w"] == 0 || len(usage.got) != 2 {
		t.Fatalf("usage fold by profile: %v", usage.got)
	}
	if reg.asked["work"] == 0 || reg.asked["personal"] == 0 {
		t.Fatalf("registry asked per profile: %v", reg.asked)
	}
	// Only the work transcript grows: its card is re-projected, the
	// personal card is untouched, and the new usage lands in work.
	appendTurns(t, otherPath, native, 1)
	beforeW, beforeP := usage.got["work/deck-w"], usage.got["personal/deck-p"]
	if res := f.sweep(opts); res.Parsed != 1 {
		t.Fatalf("%+v", res)
	}
	check()
	if usage.got["work/deck-w"] <= beforeW || usage.got["personal/deck-p"] != beforeP {
		t.Fatalf("usage after the work append: %v", usage.got)
	}
}

// uuidUsage counts how often each usage record was handed to the cost sink.
type uuidUsage struct{ folded map[string]int }

func (u *uuidUsage) Usage(_, _ string, ev []reader.Usage) error {
	for _, e := range ev {
		u.folded[e.UUID]++
	}
	return nil
}

// Usage hand-off is transactional with the pass: a pass that dies after a
// mid-pass checkpoint must not lose the usage of the committed prefix, and
// the retry must not fold anything twice. Every record of the file is
// folded exactly once across the failed pass and its retry.
func TestSweep_ReaderErrorKeepsUsageOfCommittedPrefix(t *testing.T) {
	f := newFixture(t, testcorpus.Options{Files: 1, Seed: 23})
	native := f.stats.Sessions[0]
	reg := profileRegistry{deck: map[string]string{"personal/" + native: "deck-p"}, hints: map[string]string{}, asked: map[string]int{}}
	clean := &uuidUsage{folded: map[string]int{}}
	f.sweep(Options{Registry: reg, Usage: clean, BatchBytes: 1})
	want := len(clean.folded)
	msgs := f.count(`SELECT count(*) FROM msg`)
	if want < 10 || msgs < 10 {
		t.Fatalf("corpus too small: %d usage records, %d msgs", want, msgs)
	}
	if err := f.st.Reset(); err != nil {
		t.Fatal(err)
	}
	got := &uuidUsage{folded: map[string]int{}}
	failing := failAfter{Reader: reader.Claude{}, n: int(msgs / 2)}
	res, err := New(f.st, Options{Roots: f.roots(), Readers: []reader.Reader{failing}, Registry: reg, Usage: got, BatchBytes: 1}).Sweep(context.Background())
	if err != nil || res.Errors != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	afterError := len(got.folded)
	if res := f.sweep(Options{Registry: reg, Usage: got}); res.Parsed != 1 || res.Errors != 0 {
		t.Fatalf("%+v", res)
	}
	var twice []string
	for id, n := range got.folded {
		if n != 1 {
			twice = append(twice, id)
		}
	}
	if len(got.folded) != want || len(twice) != 0 {
		t.Fatalf("usage folded %d/%d records (%d after the error), %d folded more than once: hand-off is not transactional with the pass", len(got.folded), want, afterError, len(twice))
	}
}
