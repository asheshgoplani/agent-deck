//go:build eval_smoke

// Behavioral eval for the `agent-deck --version` update-nudge annotation
// shipped in v1.7.59 (conductor task #45). Motivated by 4 users on
// 2026-04-22 posting feedback from versions 15-39 releases old — they
// were hitting bugs we already fixed. The annotation tells a user they
// are out of date the moment they run `--version`, without adding a
// network round-trip.
//
// Why this eval exists: a Go unit test proves writeVersionOutput emits
// the right bytes to an io.Writer. That test can't catch a regression
// where the flag dispatch in main.go gets rewired to bypass
// writeVersionOutput (easy to drop on a merge). This eval drives the
// real binary and reads what a human would see on stdout.

package session_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_VersionFlag_ShowsUpdateAnnotationFromCache seeds the scratch
// HOME with an update-cache.json claiming the user is 30 releases behind,
// then runs `agent-deck --version` and asserts the annotation appears on
// stdout. The spawn is offline — no network required.
func TestEval_VersionFlag_ShowsUpdateAnnotationFromCache(t *testing.T) {
	sb := harness.NewSandbox(t)

	// Seed the on-disk cache that CachedUpdateInfo will read. The JSON
	// shape mirrors internal/update.UpdateCache; hand-written here so the
	// eval package doesn't depend on internal/update (which it could not
	// import under Go's internal-package rule anyway).
	cacheDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	// 9.9.99 is an impossible-future sentinel; using a real near-future
	// version (e.g., 1.8.99) silently flips this test once the binary
	// crosses it — already happened once before v1.8.0 ship. See
	// test-correctness audit #8/#9.
	cache := map[string]any{
		"checked_at":      time.Now().Format(time.RFC3339Nano),
		"latest_version":  "9.9.99",
		"current_version": "1.7.20",
		"download_url":    "https://example.invalid/download",
		"release_url":     "https://example.invalid/releases/v9.9.99",
		"releases_behind": 30,
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "update-cache.json"), data, 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	// Run the real binary. We deliberately spawn via exec.Command (not
	// PTY) — `--version` is stdout-only with no terminal dependency, and
	// we want the clean stdout bytes.
	cmd := exec.Command(sb.BinPath, "--version")
	cmd.Env = sb.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent-deck --version failed: %v\noutput: %s", err, string(out))
	}
	got := string(out)

	if !strings.Contains(got, "Agent Deck v") {
		t.Fatalf("missing version header; got: %q", got)
	}
	if !strings.Contains(got, "(update available: v9.9.99)") {
		t.Fatalf("missing update annotation — did the flag dispatch bypass writeVersionOutput?\n"+
			"got: %q\n"+
			"wanted substring: %q\n"+
			"Fix hint: check cmd/agent-deck/main.go line ~213 ('case \"version\", \"--version\", \"-v\":') "+
			"still calls writeVersionOutput(os.Stdout, Version).",
			got, "(update available: v9.9.99)")
	}
}

// TestEval_VersionFlag_EnvSkipSuppressesAnnotation pins the kill-switch
// contract: AGENTDECK_SKIP_UPDATE_CHECK=1 strips the annotation even when
// the cache says we are behind. Locked-down CI/air-gapped environments
// rely on this.
func TestEval_VersionFlag_EnvSkipSuppressesAnnotation(t *testing.T) {
	sb := harness.NewSandbox(t)

	cacheDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	cache := map[string]any{
		"checked_at":      time.Now().Format(time.RFC3339Nano),
		"latest_version":  "9.9.99",
		"current_version": "1.7.20",
		"releases_behind": 30,
	}
	data, _ := json.MarshalIndent(cache, "", "  ")
	_ = os.WriteFile(filepath.Join(cacheDir, "update-cache.json"), data, 0o644)

	// Baseline: without the kill-switch the annotation MUST appear. If this
	// fails, the test below would pass for the wrong reason (a deleted
	// kill-switch or a globally-disabled annotation looks identical to a
	// working kill-switch). See test-correctness audit #9.
	baseCmd := exec.Command(sb.BinPath, "--version")
	baseCmd.Env = sb.Env()
	baseOut, baseErr := baseCmd.CombinedOutput()
	if baseErr != nil {
		t.Fatalf("baseline agent-deck --version failed: %v\noutput: %s", baseErr, string(baseOut))
	}
	if !strings.Contains(string(baseOut), "update available") {
		t.Fatalf("baseline failed: annotation absent without kill-switch — "+
			"the kill-switch test below would pass for the wrong reason.\ngot: %q",
			string(baseOut))
	}

	cmd := exec.Command(sb.BinPath, "--version")
	cmd.Env = append(sb.Env(), "AGENTDECK_SKIP_UPDATE_CHECK=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent-deck --version failed: %v\noutput: %s", err, string(out))
	}
	got := string(out)

	if strings.Contains(got, "update available") {
		t.Fatalf("env kill-switch leaked: --version emitted the annotation despite "+
			"AGENTDECK_SKIP_UPDATE_CHECK=1.\ngot: %q", got)
	}
}
