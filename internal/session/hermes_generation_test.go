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
