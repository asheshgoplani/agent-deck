package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

// recallHome isolates HOME and the XDG dirs and lays out every harness's
// home under it, plus a Claude work profile.
func recallHome(t *testing.T, enabled bool) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	for _, d := range []string{filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".claude-work", "projects"), filepath.Join(home, ".gemini", "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testcorpus.CodexHome(filepath.Join(home, ".codex"), -1); err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.PiHome(filepath.Join(home, ".pi")); err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.OpenCodeTree(filepath.Join(home, "xdg-data")); err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.HermesHome(filepath.Join(home, ".hermes")); err != nil {
		t.Fatal(err)
	}
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{"work": {Claude: ProfileClaudeSettings{ConfigDir: filepath.Join(home, ".claude-work")}}}}
	cfg.Recall.Enabled = &enabled
	withConfig(t, cfg)
	return home
}

func TestRecallRoots_EveryHarnessPresentOnDisk(t *testing.T) {
	home := recallHome(t, true)
	roots := RecallRoots()
	got := map[string][]string{}
	for _, r := range roots {
		got[r.Harness] = append(got[r.Harness], r.Dir)
	}
	want := map[string]string{
		reader.HarnessCodex:    filepath.Join(home, ".codex"),
		reader.HarnessPi:       filepath.Join(home, ".pi"),
		reader.HarnessGemini:   filepath.Join(home, ".gemini"),
		reader.HarnessOpenCode: filepath.Join(home, "xdg-data", "opencode", "storage"),
		reader.HarnessHermes:   filepath.Join(home, ".hermes"),
	}
	for h, dir := range want {
		if len(got[h]) != 1 || got[h][0] != dir {
			t.Errorf("%s roots = %v want [%s]", h, got[h], dir)
		}
	}
	if len(got[reader.HarnessClaude]) < 2 {
		t.Errorf("claude roots = %v", got[reader.HarnessClaude])
	}
	// A configured subset drops the rest.
	cfg, _ := LoadUserConfig()
	cfg.Recall.Harnesses = []string{"Claude", " codex "}
	ClearUserConfigCache()
	withConfig(t, cfg)
	for _, r := range RecallRoots() {
		if r.Harness != reader.HarnessClaude && r.Harness != reader.HarnessCodex {
			t.Errorf("harness %s not in [recall] harnesses but rooted", r.Harness)
		}
	}
}

func TestRecallNotify_QueuesContainedPathsOnly(t *testing.T) {
	home := recallHome(t, true)
	qp, err := recall.QueuePath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(qp, filepath.Join(home, "xdg-data")) {
		t.Fatalf("queue %s is outside the sandbox", qp)
	}
	claude := filepath.Join(home, ".claude-work", "projects", "p", "aaaaaaaa-0000-4000-8000-000000000001.jsonl")
	if err := os.MkdirAll(filepath.Dir(claude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claude, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(home, ".codex", "sessions", "2026", "09", "19", "rollout-2026-09-19T13-04-34-"+testcorpus.CodexThread+".jsonl")
	pi := filepath.Join(home, ".pi", "agent", "sessions", "--private-tmp-phase2-exp--", "2026-08-23T22-51-45-235Z_"+testcorpus.PiID+".jsonl")
	gemini := filepath.Join(home, ".gemini", "tmp", "x", "chats", "session-1.json")
	outside := filepath.Join(home, "elsewhere", "rollout-2026-09-19T13-04-34-"+testcorpus.CodexThread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, harness string
		ok            bool
	}{
		{claude, "claude", true},
		{codex, "codex", true},
		{pi, "pi", true},
		{gemini, "", false}, // sweep-only harness: no Locate
		{outside, "", false},
		{filepath.Join(home, ".codex", "..", "elsewhere", "x.jsonl"), "", false},
		{"", "", false},
	}
	for _, c := range cases {
		resolved, harness, ok := RecallNotifyTranscript(c.path, "Stop", "inst-1")
		if ok != c.ok || harness != c.harness {
			t.Errorf("%s: ok=%v harness=%q want %v %q", c.path, ok, harness, c.ok, c.harness)
		}
		if ok && resolved == "" {
			t.Errorf("%s: no resolved path", c.path)
		}
	}
	entries, err := recall.Drain(qp)
	if err != nil || len(entries) != 3 {
		t.Fatalf("queued %d (%v): %+v", len(entries), err, entries)
	}
	for _, e := range entries {
		if e.Instance != "inst-1" || e.Event != "Stop" || e.Harness == "" {
			t.Errorf("entry %+v", e)
		}
	}

	// A managed Claude instance queues its transcript; a remote one never
	// resolves a local file.
	inst := &Instance{ID: "inst-2", Tool: "claude", ProjectPath: "/p", ClaudeSessionID: "aaaaaaaa-0000-4000-8000-000000000001"}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-work"))
	if !RecallNotifyInstance(inst, "stop") {
		t.Fatal("a local Claude instance with a transcript on disk must queue it")
	}
	remote := &Instance{ID: "inst-3", Tool: "claude", ProjectPath: "/p", ClaudeSessionID: "aaaaaaaa-0000-4000-8000-000000000001", SSHHost: "web1"}
	if RecallNotifyInstance(remote, "stop") {
		t.Fatal("a remote instance must queue nothing")
	}
	if entries, _ := recall.Drain(qp); len(entries) != 1 || entries[0].Instance != "inst-2" {
		t.Fatalf("instance queue: %+v", entries)
	}

	// The transition daemon's form: accepted at once, resolved and queued
	// by the background worker (finding 3 of the phase-3 review: no root
	// walk on the daemon goroutine).
	for i := 0; i < 3; i++ {
		if !RecallNotifyInstanceAsync(inst, "turn_end") {
			t.Fatal("the async notify must accept a local instance")
		}
	}
	if RecallNotifyInstanceAsync(nil, "turn_end") {
		t.Fatal("nil instance accepted")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n := recall.QueueLen(qp); n == 3 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("async notifies queued %d lines, want 3", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, _ = recall.Drain(qp)
	for _, e := range entries {
		if e.Instance != "inst-2" || e.Event != "turn_end" || e.Harness != "claude" {
			t.Errorf("async entry %+v", e)
		}
	}
}

func TestRecallNotify_OffDoesNothing(t *testing.T) {
	home := recallHome(t, false)
	codex := filepath.Join(home, ".codex", "sessions", "2026", "09", "19", "rollout-2026-09-19T13-04-34-"+testcorpus.CodexThread+".jsonl")
	if _, _, ok := RecallNotifyTranscript(codex, "Stop", "i"); ok {
		t.Fatal("recall off must not queue")
	}
	qp, _ := recall.QueuePath()
	if _, err := os.Stat(qp); !os.IsNotExist(err) {
		t.Fatal("queue file created while recall is off")
	}
}
