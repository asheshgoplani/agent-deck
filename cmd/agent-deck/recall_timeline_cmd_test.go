package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRecallTimelineAndFollowCLI(t *testing.T) {
	home, stats := recallHome(t, 1)
	if stdout, stderr, code := runAgentDeck(t, home, "recall", "backfill", "--json"); code != 0 {
		t.Fatalf("backfill: %d %s %s", code, stdout, stderr)
	}
	ref := stats.Sessions[0]
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", ref, "--json")
	if code != 0 {
		t.Fatalf("timeline: %d %s %s", code, stdout, stderr)
	}
	var timeline struct {
		Turns []struct {
			Kind string `json:"kind"`
		} `json:"turns"`
		ThroughCursor string `json:"through_cursor"`
	}
	if err := json.Unmarshal([]byte(stdout), &timeline); err != nil || len(timeline.Turns) == 0 || timeline.ThroughCursor == "" {
		t.Fatalf("timeline JSON: %v %s", err, stdout)
	}
	// Rewriting the source invalidates the old cursor. Follow must emit a
	// machine-readable resync frame and finish, so the client can restart.
	path := stats.Paths[0]
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"replacement"},"timestamp":"2026-09-11T01:00:00Z","sessionId":"`+ref+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runAgentDeck(t, home, "recall", "follow", ref, "--after", timeline.ThroughCursor, "--jsonl")
	if code != 0 || !strings.Contains(stdout, `"type":"resync_required"`) {
		t.Fatalf("follow resync: %d %s %s", code, stdout, stderr)
	}
}

func TestRecallTimelineAndFollowRequireEnabledGate(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"recall", "timeline", "session-id", "--json"},
		{"recall", "follow", "session-id", "--after", "cursor", "--jsonl"},
	} {
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 2 || !strings.Contains(stdout+stderr, "enabled = true") {
			t.Fatalf("%v bypassed recall gate: exit=%d stdout=%s stderr=%s", args, code, stdout, stderr)
		}
	}
}
