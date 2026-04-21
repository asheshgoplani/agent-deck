package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// GhShim is a recorder + scriptable stub installed at $ShimDir/gh. Tests
// configure ScriptSuccess / ScriptFailure before the binary runs, then
// inspect recorded calls after.
type GhShim struct {
	t *testing.T

	binPath  string // $ShimDir/gh
	logPath  string // call-log file written by the shim
	modePath string // single-line file read by the shim: "success" | "failure"

	mu sync.Mutex
}

// GhCall is one recorded invocation of the gh shim.
type GhCall struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

func newGhShim(t *testing.T, shimDir string) *GhShim {
	t.Helper()

	logPath := filepath.Join(shimDir, "gh.log.jsonl")
	modePath := filepath.Join(shimDir, "gh.mode")
	binPath := filepath.Join(shimDir, "gh")

	// Shim script: record argv+stdin as one JSON line, then produce canned
	// stdout for the two call shapes the binary uses, then exit per mode.
	// Uses python3 for JSON emission (available on every Linux runner).
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -u
MODE_FILE=%q
LOG_FILE=%q

mode="success"
[ -f "$MODE_FILE" ] && mode=$(cat "$MODE_FILE")

# Capture stdin if piped (gh api graphql with -F body=@- etc.).
stdin=""
if [ ! -t 0 ]; then
  stdin=$(cat || true)
fi

AGENT_DECK_SHIM_STDIN="$stdin" AGENT_DECK_SHIM_LOG="$LOG_FILE" python3 -c '
import json, sys, os
rec = {"args": sys.argv[1:], "stdin": os.environ.get("AGENT_DECK_SHIM_STDIN", "")}
with open(os.environ["AGENT_DECK_SHIM_LOG"], "a") as f:
    f.write(json.dumps(rec) + "\n")
' "$@"

# Canned stdout for the two call shapes feedback_cmd.go uses.
case "${1:-}:${2:-}" in
  api:user) echo "eval-test-user" ;;
  api:graphql) echo '{"data":{"addDiscussionComment":{"comment":{"id":"FAKE"}}}}' ;;
  *) ;;
esac

if [ "$mode" = "failure" ]; then
  echo "eval-shim: scripted failure" >&2
  exit 1
fi
exit 0
`, modePath, logPath)

	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("gh shim write: %v", err)
	}
	if err := os.WriteFile(modePath, []byte("success"), 0o644); err != nil {
		t.Fatalf("gh shim mode write: %v", err)
	}

	return &GhShim{
		t:        t,
		binPath:  binPath,
		logPath:  logPath,
		modePath: modePath,
	}
}

// ScriptSuccess sets the shim to exit 0 on every call (default).
func (g *GhShim) ScriptSuccess() { g.setMode("success") }

// ScriptFailure sets the shim to exit 1 with a scripted stderr line.
func (g *GhShim) ScriptFailure() { g.setMode("failure") }

func (g *GhShim) setMode(s string) {
	g.t.Helper()
	if err := os.WriteFile(g.modePath, []byte(s), 0o644); err != nil {
		g.t.Fatalf("gh shim mode write: %v", err)
	}
}

// Calls returns a snapshot of recorded gh invocations, in order.
func (g *GhShim) Calls() []GhCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, err := os.ReadFile(g.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		g.t.Fatalf("gh shim log read: %v", err)
	}
	var calls []GhCall
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var c GhCall
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			g.t.Fatalf("gh shim log parse: %v\nline=%s", err, line)
		}
		calls = append(calls, c)
	}
	return calls
}

// CallsWith returns only the calls whose argv contains all of substrs.
func (g *GhShim) CallsWith(substrs ...string) []GhCall {
	var out []GhCall
outer:
	for _, c := range g.Calls() {
		joined := strings.Join(c.Args, " ")
		for _, s := range substrs {
			if !strings.Contains(joined, s) {
				continue outer
			}
		}
		out = append(out, c)
	}
	return out
}

// AssertNotCalled fails if any call matches all substrs. Empty substrs means
// "never called at all".
func (g *GhShim) AssertNotCalled(substrs ...string) {
	g.t.Helper()
	hits := g.CallsWith(substrs...)
	if len(hits) > 0 {
		g.t.Fatalf("gh shim: expected no call matching %v, got %d:\n%v",
			substrs, len(hits), hits)
	}
}

// AssertCalled fails if no call matches all substrs. Returns the first match.
func (g *GhShim) AssertCalled(substrs ...string) GhCall {
	g.t.Helper()
	hits := g.CallsWith(substrs...)
	if len(hits) == 0 {
		g.t.Fatalf("gh shim: expected a call matching %v, got none.\nAll calls: %v",
			substrs, g.Calls())
	}
	return hits[0]
}
