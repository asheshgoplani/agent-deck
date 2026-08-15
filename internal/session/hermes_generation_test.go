package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHermesHookGenerationRejectsDelayedAndCleansBothScopes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := &Instance{ID: "hermes-gen", Tool: "hermes"}
	g1, err := inst.seedHermesHookGeneration("waiting", false)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := inst.seedHermesHookGeneration("starting", false)
	if err != nil {
		t.Fatal(err)
	}
	if g1 == g2 || g1 == "" || g2 == "" {
		t.Fatalf("non-unique generations: %q %q", g1, g2)
	}
	if got := inst.buildHermesCommand("hermes"); !strings.Contains(got, "AGENTDECK_HOOK_GENERATION=") {
		t.Fatalf("generation missing from command: %s", got)
	}
	for _, p := range hermesHookArtifactPaths(inst.ID) {
		if strings.HasSuffix(p, ".json") {
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	inst.clearHermesHookArtifacts()
	for _, p := range hermesHookArtifactPaths(inst.ID) {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("artifact survived: %s (%v)", p, err)
		}
	}
}

func TestHermesOnlyAgentCompletionIsTransitionCandidate(t *testing.T) {
	for _, tc := range []struct {
		event string
		want  bool
	}{{"post_llm_call", true}, {"on_session_end", true}, {"post_tool_call", false}, {"on_session_start", false}} {
		_, got := terminalHookTransitionCandidate("hermes", &HookStatus{Status: "waiting", Event: tc.event, UpdatedAt: time.Now()})
		if got != tc.want {
			t.Errorf("event %s candidate=%v want %v", tc.event, got, tc.want)
		}
	}
}

func TestHermesGenerationSeedUsesScopedPathAndPending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := &Instance{ID: "hermes-sandbox", Tool: "hermes", Sandbox: &SandboxConfig{Enabled: true}}
	g, err := inst.seedHermesHookGeneration("running", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(GetHooksDir(), "sandbox", inst.ID, inst.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Generation string `json:"hook_generation"`
		Pending    bool   `json:"initial_message_pending"`
		Sequence   uint64 `json:"sequence"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Generation != g || !got.Pending || got.Sequence != 0 {
		t.Fatalf("bad seed: %+v", got)
	}
}

func TestHermesTerminalClassification(t *testing.T) {
	if isTerminalHookEvent("on_session_finalize") != true {
		t.Fatal("finalize must be terminal")
	}
	if isTerminalHookEvent("on_session_end") {
		t.Fatal("on_session_end is per-turn, not session-terminal")
	}
}

func TestHermesClearStatusPreservesLiveGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := &Instance{ID: "hermes-attached", Tool: "hermes"}
	if _, err := inst.seedHermesHookGeneration("waiting", false); err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(GetHooksDir(), inst.ID+".generation.json")
	lock := filepath.Join(GetHooksDir(), inst.ID+".lock")
	inst.ClearHookStatus()
	if _, err := os.Stat(control); err != nil {
		t.Fatalf("live control removed: %v", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("live lock removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(GetHooksDir(), inst.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("status not cleared: %v", err)
	}
}

func TestHermesRestartWaitingSeedCarriesCurrentGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := &Instance{ID: "hermes-restart", Tool: "hermes"}
	if _, err := inst.seedHermesHookGeneration("starting", false); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.seedHermesHookGeneration("waiting", false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(GetHooksDir(), inst.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var seed hermesHookSeed
	if err := json.Unmarshal(b, &seed); err != nil {
		t.Fatal(err)
	}
	if seed.Status != "waiting" || seed.HookGeneration == "" || seed.HookGeneration != inst.HermesHookGeneration {
		t.Fatalf("restart baseline is not generation-aware: %+v current=%q", seed, inst.HermesHookGeneration)
	}
	if command := inst.buildHermesCommand("hermes"); !strings.Contains(command, "AGENTDECK_HOOK_GENERATION="+seed.HookGeneration) {
		t.Fatalf("replacement command does not export baseline generation: %s", command)
	}
}

func TestHermesGenerationSeedRefreshesInMemoryBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	staleAt := time.Now().Add(-time.Minute)
	inst := &Instance{
		ID:             "hermes-memory-baseline",
		Tool:           "hermes",
		hookStatus:     "dead",
		hookEvent:      "on_session_finalize",
		hookLastUpdate: staleAt,
	}
	started := time.Now()
	if _, err := inst.seedHermesHookGeneration("waiting", false); err != nil {
		t.Fatal(err)
	}
	status, fresh := inst.GetHookStatus()
	if status != "waiting" || !fresh {
		t.Fatalf("immediate hook status = %q fresh=%v, want waiting/true", status, fresh)
	}
	inst.mu.RLock()
	event, updated := inst.hookEvent, inst.hookLastUpdate
	inst.mu.RUnlock()
	if event != "agentdeck_spawn_seed" {
		t.Fatalf("hook event = %q, want spawn seed", event)
	}
	if updated.Before(started) || !updated.After(staleAt) {
		t.Fatalf("hook timestamp was not refreshed: got %v, started %v, stale %v", updated, started, staleAt)
	}
}

func TestHermesRestartStartingHookOverridesStaleError(t *testing.T) {
	skipIfNoTmuxBinary(t)
	t.Setenv("HOME", t.TempDir())

	// Use a harmless shell command to provide the live tmux session that
	// UpdateStatus requires, then model the Hermes restart capture window.
	inst := NewInstanceWithTool("hermes-restart-starting", t.TempDir(), "shell")
	inst.Command = "sleep 30"
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inst.Kill() }()

	inst.mu.Lock()
	inst.Tool = "hermes"
	inst.Status = StatusError
	inst.hookStatus = "dead"
	inst.hookLastUpdate = time.Now()
	inst.mu.Unlock()

	if _, err := inst.seedHermesHookGeneration("starting", false); err != nil {
		t.Fatal(err)
	}
	if err := inst.UpdateStatus(); err != nil {
		t.Fatal(err)
	}
	if inst.Status != StatusStarting {
		t.Fatalf("restart status = %q, want %q", inst.Status, StatusStarting)
	}
}

func TestHermesSeedClearsOppositeLayout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := &Instance{ID: "hermes-mode-change", Tool: "hermes"}
	scoped := filepath.Join(GetHooksDir(), "sandbox", inst.ID)
	if err := os.MkdirAll(scoped, 0700); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".json", ".generation.json"} {
		if err := os.WriteFile(filepath.Join(scoped, inst.ID+suffix), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := inst.seedHermesHookGeneration("waiting", false); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".json", ".generation.json"} {
		if _, err := os.Stat(filepath.Join(scoped, inst.ID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("opposite artifact survived: %s (%v)", suffix, err)
		}
	}
}

func TestHermesGenerationAuthorityRejectsOppositeLegacyStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "hermes-authority"
	if err := atomicHermesJSON(filepath.Join(GetHooksDir(), id+".generation.json"), hermesHookControl{Generation: "current"}); err != nil {
		t.Fatal(err)
	}
	scoped := filepath.Join(GetHooksDir(), "sandbox", id)
	if err := os.MkdirAll(scoped, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scoped, id+".json"), []byte(`{"status":"waiting","event":"post_llm_call","ts":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readHookStatusFile(id); got != nil {
		t.Fatalf("opposite legacy status accepted: %+v", got)
	}
}

func TestHermesGenerationAuthorityAmbiguityFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "hermes-ambiguous"
	root := GetHooksDir()
	scoped := filepath.Join(root, "sandbox", id)
	if err := os.MkdirAll(scoped, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicHermesJSON(filepath.Join(root, id+".generation.json"), hermesHookControl{Generation: "flat"}); err != nil {
		t.Fatal(err)
	}
	if err := atomicHermesJSON(filepath.Join(scoped, id+".generation.json"), hermesHookControl{Generation: "scoped"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scoped, id+".json"), []byte(`{"status":"waiting","event":"post_llm_call","ts":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readHookStatusFile(id); got != nil {
		t.Fatalf("ambiguous authority accepted status: %+v", got)
	}
}

func TestHermesGenerationAuthorityMalformedFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "hermes-malformed"
	if err := os.MkdirAll(GetHooksDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(GetHooksDir(), id+".generation.json"), []byte(`{"generation":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(GetHooksDir(), id+".json"), []byte(`{"status":"waiting","event":"post_llm_call","ts":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readHookStatusFile(id); got != nil {
		t.Fatalf("malformed authority accepted status: %+v", got)
	}
}
