//go:build multi_conductor

// Package session — multi-conductor event delivery harness wrapper.
//
// This is the CI hook for tests/eval/scripts/multi_conductor_event_delivery_test.sh.
// It is gated by the `multi_conductor` build tag AND a runtime conductor-presence
// check, so `go test ./...` cannot accidentally pick it up. Run explicitly with:
//
//	go test -tags multi_conductor ./internal/session/... \
//	    -run TestMultiConductorEventDelivery -count=1 -v
//
// Required env (matches the shell script):
//   - agent-deck binary on PATH (or AGENT_DECK_BIN)
//   - AGENT_DECK_PROFILE (defaults to "personal")
//
// Asserted contracts (from issue #824):
//   - Each conductor on the host receives ≥ 1 delivery_result=sent for its
//     ephemeral test child.
//   - No duplicate fingerprints in transition-notifier.log per event.
//   - notifier-missed.log holds at most one re-fire entry per event.
package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMultiConductorEventDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-conductor harness in -short mode")
	}

	bin := resolveAgentDeckBin(t)
	profile := envDefault("AGENT_DECK_PROFILE", "personal")

	if !hostHasConductors(t, bin, profile) {
		t.Skip("no conductor sessions detected on host; this harness is host-bound")
	}

	scriptPath := locateHarnessScript(t)

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_BIN="+bin,
		"AGENT_DECK_PROFILE="+profile,
	)
	cmd.Stdout = newPrefixWriter(t, "[harness stdout] ")
	cmd.Stderr = newPrefixWriter(t, "[harness stderr] ")

	timer := time.AfterFunc(15*time.Minute, func() {
		_ = cmd.Process.Kill()
	})
	defer timer.Stop()

	err := cmd.Run()
	if err == nil {
		return
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("harness invocation failed: %v", err)
	}

	switch exitErr.ExitCode() {
	case 2:
		t.Skip("harness reported exit=2 (no conductors found at runtime)")
	case 1:
		t.Fatalf("harness reported FAIL — see report under tests/eval/reports/ for per-conductor reasons")
	default:
		t.Fatalf("harness exited with code=%d", exitErr.ExitCode())
	}
}

// resolveAgentDeckBin honours AGENT_DECK_BIN, then falls back to PATH lookup.
func resolveAgentDeckBin(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("AGENT_DECK_BIN"); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v
		}
	}
	p, err := exec.LookPath("agent-deck")
	if err != nil {
		t.Skipf("agent-deck binary not found on PATH: %v", err)
	}
	return p
}

func hostHasConductors(t *testing.T, bin, profile string) bool {
	t.Helper()
	out, err := exec.Command(bin, "-p", profile, "list", "-json").Output()
	if err != nil {
		t.Logf("conductor probe failed (treating as zero): %v", err)
		return false
	}
	// Cheap presence check that doesn't pull in a JSON dep: look for the
	// title prefix or the literal "agent-deck" title token in the output.
	s := string(out)
	return strings.Contains(s, `"title":"conductor-`) ||
		strings.Contains(s, `"title": "conductor-`) ||
		strings.Contains(s, `"title":"agent-deck"`) ||
		strings.Contains(s, `"title": "agent-deck"`)
}

func locateHarnessScript(t *testing.T) string {
	t.Helper()
	// runtime.Caller gives us this file's path; harness lives at a fixed
	// relative offset from the repo root.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	candidate := filepath.Join(repoRoot, "tests", "eval", "scripts",
		"multi_conductor_event_delivery_test.sh")
	if _, err := os.Stat(candidate); err != nil {
		t.Fatalf("harness script missing at %s: %v", candidate, err)
	}
	return candidate
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type prefixWriter struct {
	t      *testing.T
	prefix string
}

func newPrefixWriter(t *testing.T, prefix string) *prefixWriter {
	return &prefixWriter{t: t, prefix: prefix}
}

func (p *prefixWriter) Write(buf []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(buf), "\n"), "\n") {
		p.t.Log(p.prefix + line)
	}
	return len(buf), nil
}
