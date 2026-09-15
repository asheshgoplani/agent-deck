package main

import (
	"encoding/json"
	"strings"
	"testing"
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
