package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestFeedHookStatuses_PiInstanceReceivesSamples is the regression test for
// #2222/P1-1: home.go:6244 used to gate the watcher-to-instance feed on a
// hand-written allowlist (IsClaudeCompatible || codex || gemini || hermes ||
// cursor) that omitted pi, so a pi instance's hookStatus was set once at cold
// load and then frozen. feedHookStatuses now gates on session.HookStatusTool,
// the single registry, and this test proves a pi instance is fed.
func TestFeedHookStatuses_PiInstanceReceivesSamples(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("AGENTDECK_PROFILE", "hook-feed-test")

	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}

	const instanceID = "pi-feed-test-instance"
	data, err := json.Marshal(map[string]any{
		"status":     "running",
		"session_id": "pi-sess-1",
		"event":      "turn_start",
		"ts":         time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("marshal hook status: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, instanceID+".json"), data, 0o644); err != nil {
		t.Fatalf("write hook status file: %v", err)
	}

	watcher, err := session.NewStatusFileWatcher(nil)
	if err != nil {
		t.Fatalf("NewStatusFileWatcher: %v", err)
	}
	go watcher.Start()
	t.Cleanup(watcher.Stop)

	deadline := time.Now().Add(5 * time.Second)
	for watcher.GetHookStatus(instanceID) == nil {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for watcher to load the pre-seeded hook status file")
		}
		time.Sleep(10 * time.Millisecond)
	}

	h := &Home{hookWatcher: watcher}
	inst := &session.Instance{ID: instanceID, Title: "demo-pi", Tool: "pi", Status: session.StatusWaiting}

	h.feedHookStatuses([]*session.Instance{inst})

	gotStatus, fresh := inst.GetHookStatus()
	if gotStatus != "running" {
		t.Errorf("inst.GetHookStatus() status = %q, want %q", gotStatus, "running")
	}
	if !fresh {
		t.Error("inst.GetHookStatus() fresh = false, want true (sample was just written)")
	}
}
