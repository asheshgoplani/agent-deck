package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// recallHome builds an isolated HOME with [recall] enabled, one Claude
// profile dir holding a synthetic corpus, and a second config dir mapped
// to a "work" account. Returns the home and the corpus stats.
func recallHome(t *testing.T, files int) (string, testcorpus.Stats) {
	t.Helper()
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	stats, err := testcorpus.Generate(claude, testcorpus.Options{Files: files, Seed: 31, SubagentEvery: 3})
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, ".claude-work")
	if err := os.MkdirAll(filepath.Join(work, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[recall]\nenabled = true\nmax_loadavg = 0\n\n[profiles.personal.claude]\nconfig_dir = %q\n\n[profiles.work.claude]\nconfig_dir = %q\n", claude, work)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, stats
}

func mustJSON(t *testing.T, stdout string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(stdout), v); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
}

func TestRecall_OffByDefaultAndHelp(t *testing.T) {
	home := t.TempDir()
	stdout, _, code := runAgentDeck(t, home, "recall", "status", "--json")
	if code != 2 || !strings.Contains(stdout, "enabled = true") {
		t.Fatalf("recall must be off by default: exit %d\n%s", code, stdout)
	}
	// Hints do not depend on the switch.
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runAgentDeck(t, home, "add", "-t", "s1", "-c", "shell", proj, "--ticket", "SB-1"); code != 0 {
		t.Fatalf("add: %s", stderr)
	}
	if stdout, _, code := runAgentDeck(t, home, "session", "annotate", "s1", "--json"); code != 0 || !strings.Contains(stdout, "SB-1") {
		t.Fatalf("annotate with recall off: exit %d %s", code, stdout)
	}
	for _, sub := range []string{"search", "sessions", "show", "open", "status", "backfill", "sweep", "gc", "rebuild", "context", "enrich", "export", "import", "pull", "mcp"} {
		stdout, stderr, code := runAgentDeck(t, home, "recall", sub, "--help")
		if code != 0 || !strings.Contains(stdout+stderr, "Usage: agent-deck recall "+sub) {
			t.Fatalf("recall %s --help: exit %d\n%s%s", sub, code, stdout, stderr)
		}
	}
	if stdout, _, code := runAgentDeck(t, home, "recall", "--help"); code != 0 || !strings.Contains(stdout, "backfill") {
		t.Fatalf("recall --help: %d %s", code, stdout)
	}
	if _, stderr, code := runAgentDeck(t, home, "recall", "bogus"); code != 1 || !strings.Contains(stderr, "unknown recall command") {
		t.Fatalf("unknown: %d %s", code, stderr)
	}
}

type recallStatusJSON struct {
	Status struct {
		Sessions     int            `json:"sessions"`
		Messages     int            `json:"messages"`
		Sources      map[string]int `json:"sources"`
		Cards        int            `json:"cards"`
		CardFTSRows  int            `json:"card_fts_rows"`
		IndexedBytes int64          `json:"indexed_bytes"`
		ByProfile    map[string]int `json:"by_profile"`
	} `json:"status"`
	Roots []map[string]any `json:"roots"`
}

type recallSearchJSON struct {
	Result struct {
		Hits []struct {
			SessID   int64  `json:"sess_id"`
			NativeID string `json:"native_id"`
			DeckID   string `json:"deck_id"`
			CardHit  bool   `json:"card_hit"`
			BodyHits int    `json:"body_hits"`
			Snippet  string `json:"snippet"`
		} `json:"hits"`
		Candidates int `json:"candidates"`
		Verified   int `json:"verified"`
		Scanned    int `json:"scanned"`
	} `json:"result"`
	Index struct {
		Swept    bool `json:"swept"`
		Deferred int  `json:"deferred"`
	} `json:"index"`
}

// hitIDs reduces a search to what must be stable across a rebuild.
func hitIDs(r recallSearchJSON) []string {
	var out []string
	for _, h := range r.Result.Hits {
		out = append(out, fmt.Sprintf("%s card=%v body=%d deck=%s", h.NativeID, h.CardHit, h.BodyHits, h.DeckID))
	}
	return out
}

func TestRecall_EndToEnd_BackfillSearchShowAndDurableDisposable(t *testing.T) {
	home, stats := recallHome(t, 5)

	// Backfill (the gate is disabled by max_loadavg = 0 and no session is
	// running, so no --force).
	stdout, stderr, code := runAgentDeck(t, home, "recall", "backfill", "--json")
	if code != 0 {
		t.Fatalf("backfill exit %d\n%s\n%s", code, stdout, stderr)
	}
	var bf struct {
		Result struct {
			Parsed, Messages, Errors int
		} `json:"result"`
	}
	mustJSON(t, stdout, &bf)
	if bf.Result.Parsed != stats.Files || bf.Result.Errors != 0 || bf.Result.Messages == 0 {
		t.Fatalf("backfill: %+v (files %d)", bf.Result, stats.Files)
	}

	stdout, _, code = runAgentDeck(t, home, "recall", "status", "--json")
	if code != 0 {
		t.Fatalf("status: %s", stdout)
	}
	var st recallStatusJSON
	mustJSON(t, stdout, &st)
	if st.Status.Sessions != stats.Files || st.Status.Sources["ok"] != stats.Files || st.Status.Cards != st.Status.CardFTSRows ||
		st.Status.IndexedBytes != stats.Bytes || st.Status.ByProfile["personal"] != stats.Files || len(st.Roots) != 2 {
		t.Fatalf("status: %+v", st)
	}

	// Register a deck session on one conversation, annotate it, and see
	// the hint through search without any sweep.
	linked := stats.Sessions[0]
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runAgentDeck(t, home, "add", "-t", "auth-fix", "-c", "claude", proj, "--resume-session", linked, "--ticket", "SB-412", "--tag", "auth", "--json")
	if code != 0 {
		t.Fatalf("add: %s %s", stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	mustJSON(t, stdout, &added)
	stdout, _, code = runAgentDeck(t, home, "session", "annotate", "auth-fix", "--decision", "root cause was clock skew", "--json")
	if code != 0 {
		t.Fatalf("annotate: %s", stdout)
	}
	var ann struct {
		Links []struct {
			NativeID      string `json:"native_id"`
			Authoritative bool   `json:"authoritative"`
		} `json:"links"`
	}
	mustJSON(t, stdout, &ann)
	if len(ann.Links) != 1 || ann.Links[0].NativeID != linked || !ann.Links[0].Authoritative {
		t.Fatalf("add --resume-session must write an authoritative link: %+v", ann.Links)
	}

	search := func(args ...string) recallSearchJSON {
		t.Helper()
		stdout, stderr, code := runAgentDeck(t, home, append([]string{"recall", "search"}, args...)...)
		if code != 0 {
			t.Fatalf("search %v: exit %d\n%s\n%s", args, code, stdout, stderr)
		}
		var r recallSearchJSON
		mustJSON(t, stdout, &r)
		return r
	}
	byHint := search("test", "--hint", "ticket=SB-412", "--json")
	if len(byHint.Result.Hits) != 1 || byHint.Result.Hits[0].NativeID != linked {
		t.Fatalf("hint filter (live, no sweep): %+v", byHint.Result.Hits)
	}
	// The interactive sweep before a search ran and had nothing to defer;
	// after it the card carries the ticket and the deck id.
	if !byHint.Index.Swept || byHint.Index.Deferred != 0 {
		t.Fatalf("index note: %+v", byHint.Index)
	}
	byTicket := search("SB-412", "--json")
	if len(byTicket.Result.Hits) != 1 || !byTicket.Result.Hits[0].CardHit || byTicket.Result.Hits[0].DeckID != added.ID {
		t.Fatalf("ticket search after the interactive sweep: %+v", byTicket.Result.Hits)
	}
	plain := search("flaky auth test", "--limit", "5", "--phrase", "--json")
	if len(plain.Result.Hits) == 0 || plain.Result.Scanned == 0 || plain.Result.Hits[0].Snippet == "" {
		t.Fatalf("plain search: %+v", plain.Result)
	}
	human, _, code := runAgentDeck(t, home, "recall", "search", "flaky auth test", "--limit", "2")
	if code != 0 || !strings.Contains(human, "session(s) for") || !strings.Contains(human, "in body") {
		t.Fatalf("human search: %s", human)
	}

	// show: card tier has no messages; excerpt does.
	stdout, _, code = runAgentDeck(t, home, "recall", "show", linked, "--tier", "card", "--json")
	if code != 0 {
		t.Fatalf("show: %s", stdout)
	}
	var shown struct {
		Detail struct {
			Session  map[string]any   `json:"session"`
			Tools    []map[string]any `json:"tools"`
			Messages []map[string]any `json:"messages"`
		} `json:"detail"`
	}
	mustJSON(t, stdout, &shown)
	if len(shown.Detail.Messages) != 0 || len(shown.Detail.Tools) == 0 || shown.Detail.Session["deck_id"] != added.ID || shown.Detail.Session["hints"] == "" {
		t.Fatalf("show card: %+v", shown.Detail)
	}
	stdout, _, _ = runAgentDeck(t, home, "recall", "show", linked, "--turns", "3", "--json")
	mustJSON(t, stdout, &shown)
	if len(shown.Detail.Messages) != 3 {
		t.Fatalf("show excerpt: %d messages", len(shown.Detail.Messages))
	}
	if _, _, code = runAgentDeck(t, home, "recall", "show", "no-such-session"); code != 2 {
		t.Fatalf("show unknown: exit %d", code)
	}
	stdout, _, code = runAgentDeck(t, home, "recall", "sessions", "--tag", "auth", "--json")
	if code != 0 || strings.Count(stdout, `"native_id"`) != 1 || !strings.Contains(stdout, linked) {
		t.Fatalf("sessions --tag: %d %s", code, stdout)
	}

	// The durable/disposable invariant: delete recall.db, rebuild, and the
	// search and the annotation read back identically.
	annBefore, _, _ := runAgentDeck(t, home, "session", "annotate", "auth-fix", "--json")
	hintBefore := hitIDs(byHint)
	ticketBefore := hitIDs(byTicket)
	dbPath := filepath.Join(home, ".local", "share", "agent-deck", "recall.db")
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("recall.db not where the design puts it: %v", err)
	}
	stdout, stderr, code = runAgentDeck(t, home, "recall", "rebuild", "--json")
	if code != 0 {
		t.Fatalf("rebuild: %s %s", stdout, stderr)
	}
	annAfter, _, _ := runAgentDeck(t, home, "session", "annotate", "auth-fix", "--json")
	if annBefore != annAfter {
		t.Fatalf("annotate changed across a rebuild:\n%s\n%s", annBefore, annAfter)
	}
	if got := hitIDs(search("test", "--hint", "ticket=SB-412", "--json")); !reflect.DeepEqual(got, hintBefore) {
		t.Fatalf("hint search changed across a rebuild: %v vs %v", got, hintBefore)
	}
	if got := hitIDs(search("SB-412", "--json")); !reflect.DeepEqual(got, ticketBefore) {
		t.Fatalf("ticket search changed across a rebuild: %v vs %v", got, ticketBefore)
	}
	stdout, _, _ = runAgentDeck(t, home, "recall", "status", "--json")
	var st2 recallStatusJSON
	mustJSON(t, stdout, &st2)
	if st2.Status.Sessions != st.Status.Sessions || st2.Status.Messages != st.Status.Messages {
		t.Fatalf("rebuild changed counts: %+v vs %+v", st2.Status, st.Status)
	}

	// gc runs and reports.
	stdout, _, code = runAgentDeck(t, home, "recall", "gc", "--json")
	var gc struct {
		Result struct {
			FreePagesAfter int `json:"free_pages_after"`
		} `json:"result"`
	}
	mustJSON(t, stdout, &gc)
	if code != 0 || gc.Result.FreePagesAfter != 0 {
		t.Fatalf("gc: %d %s", code, stdout)
	}
	// A second sweep parses nothing.
	stdout, _, code = runAgentDeck(t, home, "recall", "sweep", "--json")
	mustJSON(t, stdout, &bf)
	if code != 0 || bf.Result.Parsed != 0 {
		t.Fatalf("sweep: %d %s", code, stdout)
	}
}

// The load gate reads the status column state.db already keeps: a session
// marked running blocks backfill, sweep and rebuild unless --force.
func TestRecall_GateRefusesWhileASessionIsBusy(t *testing.T) {
	home, _ := recallHome(t, 1)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runAgentDeck(t, home, "add", "-t", "busy-one", "-c", "shell", proj); code != 0 {
		t.Fatalf("add: %s", stderr)
	}
	dbPath := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open state.db at %s: %v", dbPath, err)
	}
	if _, err := db.DB().Exec(`UPDATE instances SET status='running' WHERE title='busy-one'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, sub := range []string{"backfill", "sweep", "rebuild"} {
		stdout, _, code := runAgentDeck(t, home, "recall", sub, "--json")
		if code != 3 || !strings.Contains(stdout, "busy-one") || !strings.Contains(stdout, "--force") {
			t.Fatalf("recall %s while busy: exit %d\n%s", sub, code, stdout)
		}
	}
	stdout, _, code := runAgentDeck(t, home, "recall", "status", "--json")
	var st recallStatusJSON
	mustJSON(t, stdout, &st)
	if code != 0 || st.Status.Sessions != 0 {
		t.Fatalf("gated commands must not have indexed anything: %d %s", code, stdout)
	}
	// Reads are never gated, and neither is the bounded interactive sweep.
	stdout, _, code = runAgentDeck(t, home, "recall", "search", "test", "--json")
	var sr recallSearchJSON
	mustJSON(t, stdout, &sr)
	if code != 0 || !sr.Index.Swept {
		t.Fatalf("search while busy: %d %s", code, stdout)
	}
	stdout, _, code = runAgentDeck(t, home, "recall", "backfill", "--force", "--json")
	// (The interactive sweep of the search above already indexed this
	// small corpus; --force must simply run and see it unchanged.)
	var bf struct {
		Result struct{ Discovered, Errors int } `json:"result"`
	}
	mustJSON(t, stdout, &bf)
	if code != 0 || bf.Result.Errors != 0 || bf.Result.Discovered == 0 {
		t.Fatalf("backfill --force: %d %s", code, stdout)
	}
}

// recall open re-registers a transcript that has no agent-deck record and
// starts the bound session when one exists.
func TestRecall_OpenPlansResumeOrStart(t *testing.T) {
	home, _ := recallHome(t, 1)
	cwd := filepath.Join(home, "app")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	const sid = "12345678-1234-4123-8123-123456789abc"
	proj := filepath.Join(home, ".claude", "projects", "-app")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := fmt.Sprintf(`{"type":"custom-title","customTitle":"clock skew fix","sessionId":%q}
{"type":"user","message":{"role":"user","content":"fix the clock skew"},"uuid":"u1","timestamp":"2026-09-19T10:00:00Z","sessionId":%q,"cwd":%q}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"output_tokens":1}},"uuid":"a1","timestamp":"2026-09-19T10:00:01Z","sessionId":%q,"cwd":%q}
`, sid, sid, cwd, sid, cwd)
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runAgentDeck(t, home, "recall", "backfill", "--quiet"); code != 0 {
		t.Fatalf("backfill: %s", stderr)
	}
	stdout, stderr, code := runAgentDeck(t, home, "recall", "open", "12345678", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("open dry-run: %d %s %s", code, stdout, stderr)
	}
	var plan struct {
		Action string   `json:"action"`
		Args   []string `json:"args"`
	}
	mustJSON(t, stdout, &plan)
	want := []string{"add", "-t", "clock skew fix", "-c", "claude", "--resume-session", sid, "--hint", "recalled_from=" + sid, "--account", "personal", cwd}
	if plan.Action != "add" || !reflect.DeepEqual(plan.Args, want) {
		t.Fatalf("plan: %s %v\nwant %v", plan.Action, plan.Args, want)
	}
	// Really register it (no start: add does not launch), then the plan
	// becomes a start of the bound session.
	stdout, stderr, code = runAgentDeck(t, home, "recall", "open", "12345678", "--json")
	if code != 0 {
		t.Fatalf("open: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID            string `json:"id"`
		ResumeSession string `json:"resume_session"`
	}
	mustJSON(t, stdout, &added)
	if added.ResumeSession != sid || added.ID == "" {
		t.Fatalf("open did not register a resume: %s", stdout)
	}
	stdout, _, code = runAgentDeck(t, home, "recall", "open", sid, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("open after add: %s", stdout)
	}
	mustJSON(t, stdout, &plan)
	if plan.Action != "start" || !reflect.DeepEqual(plan.Args, []string{"session", "start", added.ID}) {
		t.Fatalf("plan after add: %s %v", plan.Action, plan.Args)
	}
	// A vanished working directory is refused with a reason, not a crash.
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatal(err)
	}
	if _, _, code = runAgentDeck(t, home, "session", "remove", "clock skew fix", "--force"); code != 0 {
		t.Logf("session remove: exit %d (continuing)", code)
	}
	stdout, _, code = runAgentDeck(t, home, "recall", "open", sid, "--dry-run", "--json")
	if code != 2 || !strings.Contains(stdout, "no longer exists") {
		t.Fatalf("open with missing cwd: %d %s", code, stdout)
	}
}

// recall.db is machine-global, state.db is per profile. A conversation
// linked in the work profile (its transcript under the work config dir)
// must keep the work link's hints on its card and get its cost events in
// the work state.db when the sweep runs under the personal profile, and
// `recall open` must start it under its own profile.
func TestRecall_SweepUnderOneProfileKeepsTheOthersCardsAndCosts(t *testing.T) {
	home, stats := recallHome(t, 2)
	native := stats.Sessions[0]
	data, err := os.ReadFile(stats.Paths[0])
	if err != nil {
		t.Fatal(err)
	}
	// A switch-account copy: same conversation id under the work config
	// dir, its own session in the index.
	dst := filepath.Join(home, ".claude-work", "projects", "-p", filepath.Base(stats.Paths[0]))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	var added struct {
		ID string `json:"id"`
	}
	stdout, stderr, code := runAgentDeck(t, home, "-p", "personal", "add", "-t", "p-sess", "-c", "claude", proj, "--resume-session", native, "--ticket", "SB-P", "--json")
	if code != 0 {
		t.Fatalf("add personal: %s %s", stdout, stderr)
	}
	mustJSON(t, stdout, &added)
	deckP := added.ID
	stdout, stderr, code = runAgentDeck(t, home, "-p", "work", "add", "-t", "w-sess", "-c", "claude", proj, "--resume-session", native, "--ticket", "SB-W", "--json")
	if code != 0 {
		t.Fatalf("add work: %s %s", stdout, stderr)
	}
	mustJSON(t, stdout, &added)
	deckW := added.ID

	// Backfill under personal only; the reads below run under the default
	// profile, which links nothing itself.
	if stdout, stderr, code := runAgentDeck(t, home, "-p", "personal", "recall", "backfill", "--json"); code != 0 {
		t.Fatalf("backfill: %d %s %s", code, stdout, stderr)
	}
	var listed struct {
		Sessions []struct {
			NativeID string `json:"native_id"`
			Profile  string `json:"profile"`
			DeckID   string `json:"deck_id"`
			Hints    string `json:"hints"`
		} `json:"sessions"`
	}
	for _, want := range []struct{ profile, deck, ticket string }{{"personal", deckP, "ticket=SB-P"}, {"work", deckW, "ticket=SB-W"}} {
		stdout, _, code := runAgentDeck(t, home, "recall", "sessions", "--profile", want.profile, "--json")
		if code != 0 {
			t.Fatalf("sessions --profile %s: %s", want.profile, stdout)
		}
		mustJSON(t, stdout, &listed)
		found := false
		for _, s := range listed.Sessions {
			if s.NativeID != native {
				continue
			}
			found = true
			if s.Profile != want.profile || s.DeckID != want.deck || s.Hints != want.ticket {
				t.Fatalf("%s card after a personal sweep: %+v (want deck %s hints %q)", want.profile, s, want.deck, want.ticket)
			}
		}
		if !found {
			t.Fatalf("%s session not listed: %+v", want.profile, listed.Sessions)
		}
	}
	// Cost events landed in the state.db that owns each link.
	for _, want := range []struct{ profile, deck string }{{"personal", deckP}, {"work", deckW}} {
		db, err := statedb.OpenReadOnlyLive(profileStateDB(t, home, want.profile))
		if err != nil {
			t.Fatal(err)
		}
		var n int
		err = db.DB().QueryRow(`SELECT count(*) FROM cost_events WHERE session_id=?`, want.deck).Scan(&n)
		db.Close()
		if err != nil || n == 0 {
			t.Fatalf("%s cost events for %s: %d (%v)", want.profile, want.deck, n, err)
		}
	}
	// open resolves the work session's link in the work state.db and
	// starts it there.
	stdout, stderr, code = runAgentDeck(t, home, "recall", "open", deckW, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("open: %d %s %s", code, stdout, stderr)
	}
	var plan struct {
		Action string   `json:"action"`
		Args   []string `json:"args"`
	}
	mustJSON(t, stdout, &plan)
	if plan.Action != "start" || !reflect.DeepEqual(plan.Args, []string{"-p", "work", "session", "start", deckW}) {
		t.Fatalf("plan: %s %v", plan.Action, plan.Args)
	}
}

// profileStateDB finds a profile's state.db under a test HOME (XDG or
// legacy layout).
func profileStateDB(t *testing.T, home, profile string) string {
	t.Helper()
	for _, p := range []string{
		filepath.Join(home, ".local", "share", "agent-deck", "profiles", profile, "state.db"),
		filepath.Join(home, ".agent-deck", "profiles", profile, "state.db"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatalf("no state.db for profile %s under %s", profile, home)
	return ""
}
