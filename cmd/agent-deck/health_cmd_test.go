package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
)

func TestHealthCLIEmptyAndValidation(t *testing.T) {
	home := t.TempDir()
	out, stderr, code := runAgentDeck(t, home, "health", "--json", "--since", "1h")
	if code != 0 {
		t.Fatalf("health exit %d: %s %s", code, out, stderr)
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if _, ok := report["processes"]; !ok {
		t.Fatalf("missing processes: %s", out)
	}
	out, stderr, code = runAgentDeck(t, home, "health")
	if code != 0 || !strings.Contains(strings.ToLower(out), "unknown") {
		t.Fatalf("empty must be unknown: %d %s %s", code, out, stderr)
	}
	for _, args := range [][]string{{"--since", "0s"}, {"--since", "-1h"}, {"--since", "bad"}, {"unexpected"}} {
		_, _, code = runAgentDeck(t, home, append([]string{"health"}, args...)...)
		if code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
	out, stderr, code = runAgentDeck(t, home, "doctor", "--json")
	if code != 0 {
		t.Fatalf("doctor: %d %s %s", code, out, stderr)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if _, ok := report["health"]; !ok {
		t.Fatalf("doctor missing health: %s", out)
	}
}

func TestHealthJSONIncludesBinaryVersion(t *testing.T) {
	home := t.TempDir()
	// runAgentDeck sets AGENTDECK_PROFILE=ch_support_test; the fixture must land
	// under that profile's directory for the CLI to find it.
	dir := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "logs", "health")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sample := health.Sample{Version: 1, BinaryVersion: "1.16.11", Timestamp: now, StartedAt: now.Add(-time.Minute), Role: "tui", PID: 1}
	data, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"health", "--json", "--since", "1h"},
		{"doctor", "--json"},
	} {
		out, stderr, code := runAgentDeck(t, home, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s %s", args, code, out, stderr)
		}
		if !strings.Contains(out, `"binary_version":"1.16.11"`) {
			t.Fatalf("%v: missing binary_version in latest sample: %s", args, out)
		}
	}
}
