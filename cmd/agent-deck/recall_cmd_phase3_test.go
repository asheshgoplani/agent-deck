package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

// recallHomeAllHarnesses is recallHome plus one fixture per other harness
// under the same HOME (Codex, pi, Gemini, OpenCode in the XDG data dir,
// Hermes).
func recallHomeAllHarnesses(t *testing.T) (string, testcorpus.Stats) {
	t.Helper()
	home, stats := recallHome(t, 2)
	if _, err := testcorpus.CodexHome(filepath.Join(home, ".codex"), -1); err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.PiHome(filepath.Join(home, ".pi")); err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.GeminiHome(filepath.Join(home, ".gemini")); err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.OpenCodeTree(filepath.Join(home, ".local", "share")); err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.HermesHome(filepath.Join(home, ".hermes")); err != nil {
		t.Fatal(err)
	}
	return home, stats
}

// TestRecall_EveryHarnessEndToEnd: backfill over all six harnesses, search
// and show per harness, open on a non-Claude conversation, and a fake
// Stop hook that indexes its own transcript before any sweep.
func TestRecall_EveryHarnessEndToEnd(t *testing.T) {
	home, stats := recallHomeAllHarnesses(t)
	stdout, stderr, code := runAgentDeck(t, home, "recall", "backfill", "--json")
	if code != 0 {
		t.Fatalf("backfill: %d\n%s\n%s", code, stdout, stderr)
	}
	var bf struct {
		Result struct{ Parsed, Errors int } `json:"result"`
	}
	mustJSON(t, stdout, &bf)
	if bf.Result.Parsed != stats.Files+6 || bf.Result.Errors != 0 {
		t.Fatalf("backfill parsed %d (claude %d + 6 others), errors %d", bf.Result.Parsed, stats.Files, bf.Result.Errors)
	}
	stdout, _, code = runAgentDeck(t, home, "recall", "status", "--json")
	if code != 0 {
		t.Fatalf("status: %s", stdout)
	}
	var st struct {
		Status struct {
			ByHarness map[string]int `json:"by_harness"`
		} `json:"status"`
		Roots  []map[string]any `json:"roots"`
		Queued int              `json:"queued"`
	}
	mustJSON(t, stdout, &st)
	for _, h := range []string{"claude", "codex", "pi", "gemini", "opencode", "hermes"} {
		if st.Status.ByHarness[h] == 0 {
			t.Errorf("status by_harness lacks %s: %v", h, st.Status.ByHarness)
		}
	}
	harnessRoots := map[string]bool{}
	for _, r := range st.Roots {
		harnessRoots[fmt.Sprint(r["harness"])] = true
	}
	if len(harnessRoots) != 6 {
		t.Fatalf("roots by harness: %v", harnessRoots)
	}
	human, _, _ := runAgentDeck(t, home, "recall", "status")
	if !strings.Contains(human, "harnesses  ") || !strings.Contains(human, "hermes") {
		t.Fatalf("human status: %s", human)
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
	for harness, want := range map[string][2]string{
		"codex":    {"clock skew", testcorpus.CodexThread},
		"pi":       {"Evaluate Hermes", testcorpus.PiID},
		"opencode": {"clock skew", testcorpus.OpenCodeSession},
		"hermes":   {"clock skew", testcorpus.HermesSession},
		"gemini":   {"clock skew", "session-2026-01-19T12-18-196a60d9"},
	} {
		r := search(want[0], "--harness", harness, "--json")
		if len(r.Result.Hits) != 1 || r.Result.Hits[0].NativeID != want[1] {
			t.Errorf("%s: hits %+v", harness, r.Result.Hits)
		}
	}
	// Titles rank first: the Codex thread name from session_index.jsonl.
	if r := search("flaky auth", "--harness", "codex", "--json"); len(r.Result.Hits) != 1 || !r.Result.Hits[0].CardHit {
		t.Fatalf("codex title hit: %+v", r.Result.Hits)
	}
	stdout, _, code = runAgentDeck(t, home, "recall", "show", "01a0b956", "--json")
	if code != 0 || !strings.Contains(stdout, `"harness": "codex"`) || !strings.Contains(stdout, "Summary so far") {
		t.Fatalf("show codex: %d %s", code, stdout)
	}
	// A conversation no session owns is searchable, not resumable.
	stdout, stderr, code = runAgentDeck(t, home, "recall", "open", "01a0b956", "--dry-run")
	if code != 2 || !strings.Contains(stdout+stderr, "not resumable") {
		t.Fatalf("open codex: %d %s %s", code, stdout, stderr)
	}

	// Fake Stop hook: a new Claude transcript is queued and nothing else
	// (the Stop hook is synchronous on Claude's turn end); the async
	// SessionEnd hook then indexes exactly that file, and --no-sweep
	// proves no sweep was needed to find it.
	sid := "eeeeeeee-0000-4000-8000-000000000009"
	path := filepath.Join(home, ".claude", "projects", "-tmp-hookproj", sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","message":{"role":"user","content":"the pelican fixture for the hook"},"uuid":"u1","timestamp":"2026-09-19T10:00:00.000Z","sessionId":"` + sid + `","cwd":"/tmp/hookproj"}
{"type":"assistant","message":{"role":"assistant","model":"claude-test","content":[{"type":"text","text":"Noted the pelican."}],"usage":{"input_tokens":3,"output_tokens":2}},"uuid":"a1","timestamp":"2026-09-19T10:00:01.000Z","sessionId":"` + sid + `"}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := `{"hook_event_name":"Stop","session_id":"` + sid + `","transcript_path":` + fmt.Sprintf("%q", path) + `,"cwd":` + fmt.Sprintf("%q", home) + `}`
	stdout, stderr, code = runAgentDeckEnv(t, home, payload, []string{"AGENTDECK_INSTANCE_ID=hook-inst"}, "hook-handler")
	if code != 0 {
		t.Fatalf("hook-handler: %d %s %s", code, stdout, stderr)
	}
	r := search("pelican", "--no-sweep", "--json")
	if len(r.Result.Hits) != 0 {
		t.Fatalf("the Stop hook must only queue, not index: %+v", r.Result.Hits)
	}
	stdout, _, _ = runAgentDeck(t, home, "recall", "status", "--json")
	mustJSON(t, stdout, &st)
	if st.Queued != 1 {
		t.Fatalf("queued = %d want the Stop hook's line", st.Queued)
	}
	payload = strings.Replace(payload, `"Stop"`, `"SessionEnd"`, 1)
	stdout, stderr, code = runAgentDeckEnv(t, home, payload, []string{"AGENTDECK_INSTANCE_ID=hook-inst"}, "hook-handler")
	if code != 0 {
		t.Fatalf("hook-handler: %d %s %s", code, stdout, stderr)
	}
	r = search("pelican", "--no-sweep", "--json")
	if len(r.Result.Hits) != 1 || r.Result.Hits[0].NativeID != sid {
		t.Fatalf("the SessionEnd hook did not index its transcript: %+v", r.Result.Hits)
	}
	// The listing's #n is what show accepts, in both forms.
	for _, ref := range []string{fmt.Sprint(r.Result.Hits[0].SessID), "#" + fmt.Sprint(r.Result.Hits[0].SessID)} {
		stdout, stderr, code = runAgentDeck(t, home, "recall", "show", ref)
		if code != 0 || !strings.Contains(stdout, "pelican") {
			t.Fatalf("show %q: %d %s %s", ref, code, stdout, stderr)
		}
	}
	// The next sweep drains both queue lines and finds nothing new to parse.
	stdout, _, code = runAgentDeck(t, home, "recall", "sweep", "--json")
	if code != 0 || !strings.Contains(stdout, `"queued": 2`) || !strings.Contains(stdout, `"parsed": 0`) {
		t.Fatalf("sweep: %d %s", code, stdout)
	}
	stdout, _, _ = runAgentDeck(t, home, "recall", "status", "--json")
	mustJSON(t, stdout, &st)
	if st.Queued != 0 {
		t.Fatalf("queue not drained: %d", st.Queued)
	}
}
