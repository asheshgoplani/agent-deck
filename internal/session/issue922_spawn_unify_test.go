// Issue #922 — silent worker-scratch override of resolved CLAUDE_CONFIG_DIR.
//
// Pre-this-fix the three spawn-env builders that resolve CLAUDE_CONFIG_DIR
// (buildClaudeCommandWithMessage, buildBashExportPrefix, buildClaudeResumeCommand)
// each silently swapped the resolved value for WorkerScratchConfigDir when
// the latter was non-empty. Users whose per-group [groups.X.claude] config_dir
// was bypassed had no log line to grep — the override was invisible.
//
// Bug reporter: @bautrey (PR #946, audit pass 2026-05-10). This file
// re-lands the regression coverage from @bautrey's PR with the test
// shape adjusted for current main (post-#779, post-#950): the prep-call
// unification half is already merged via #950, so the remaining wedge
// is the silent override + log emission. These tests lock that down.
//
// Fix shape: route every override site through applyWorkerScratchOverride
// (worker_scratch.go) which emits one INFO event per swap, carrying the
// resolved dir and the scratch dir.

package session

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildClaudeCommand_WorkerScratchOverrideEmitsInfoLog locks the
// debuggability half of the fix: when WorkerScratchConfigDir overrides
// the resolved CLAUDE_CONFIG_DIR, an INFO line must record both the
// original resolved dir and the scratch dir so the override is no
// longer silent. Issue #922, reported by @bautrey.
func TestBuildClaudeCommand_WorkerScratchOverrideEmitsInfoLog(t *testing.T) {
	withTelegramConductorPresent(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := filepath.Join(home, ".claude")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", profile)

	inst := &Instance{
		ID:          "00000000-0000-0000-0000-000000000922",
		Tool:        "claude",
		Title:       "issue-922",
		ProjectPath: filepath.Join(home, "proj"),
	}
	scratch, err := inst.EnsureWorkerScratchConfigDir(profile)
	if err != nil {
		t.Fatalf("setup scratch: %v", err)
	}
	if scratch == "" {
		t.Fatal("setup: expected non-empty scratch dir")
	}
	inst.WorkerScratchConfigDir = scratch

	var buf bytes.Buffer
	origLog := sessionLog
	sessionLog = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	t.Cleanup(func() { sessionLog = origLog })

	_ = inst.buildClaudeCommand("claude")

	output := buf.String()
	if !strings.Contains(output, "worker_scratch_override") {
		t.Errorf("expected `worker_scratch_override` INFO event; got: %s", output)
	}
	if !strings.Contains(output, profile) {
		t.Errorf("override log must include the resolved (overridden) config_dir %q; got: %s", profile, output)
	}
	if !strings.Contains(output, scratch) {
		t.Errorf("override log must include the scratch dir %q; got: %s", scratch, output)
	}
	// buildClaudeCommand chains two spawn-env contributors that each
	// emit CLAUDE_CONFIG_DIR (the bash-export prefix and the inline
	// prefix). Both legitimately route through applyWorkerScratchOverride
	// and each should log — anything between 1 and 2 is OK.
	if got := strings.Count(output, "worker_scratch_override"); got < 1 || got > 2 {
		t.Errorf("expected 1 or 2 `worker_scratch_override` logs per build; got %d\noutput: %s", got, output)
	}
}

// TestBuildClaudeResume_WorkerScratchOverrideEmitsInfoLog covers the
// third override site (buildClaudeResumeCommand) — the one that the
// CLI restart path takes when claude_session_id is set. Bug #922 was
// most user-visible on this path because that's how restart-on-existing-
// session behaves.
func TestBuildClaudeResume_WorkerScratchOverrideEmitsInfoLog(t *testing.T) {
	withTelegramConductorPresent(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := filepath.Join(home, ".claude")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", profile)

	inst := &Instance{
		ID:              "00000000-0000-0000-0000-000000000922",
		Tool:            "claude",
		Title:           "issue-922-resume",
		ProjectPath:     filepath.Join(home, "proj"),
		ClaudeSessionID: "11111111-2222-3333-4444-555555555555",
	}
	scratch, err := inst.EnsureWorkerScratchConfigDir(profile)
	if err != nil {
		t.Fatalf("setup scratch: %v", err)
	}
	inst.WorkerScratchConfigDir = scratch

	var buf bytes.Buffer
	origLog := sessionLog
	sessionLog = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	t.Cleanup(func() { sessionLog = origLog })

	_ = inst.buildClaudeResumeCommand()

	output := buf.String()
	if !strings.Contains(output, "worker_scratch_override") {
		t.Errorf("buildClaudeResumeCommand: expected `worker_scratch_override` INFO event; got: %s", output)
	}
	if !strings.Contains(output, scratch) {
		t.Errorf("override log must include the scratch dir %q; got: %s", scratch, output)
	}
}

// TestBuildClaudeCommand_NoOverrideNoLog guards against a noisy fix:
// when WorkerScratchConfigDir is empty (conductor / channel-owner /
// non-claude-worker), no override log must fire.
func TestBuildClaudeCommand_NoOverrideNoLog(t *testing.T) {
	withTelegramConductorPresent(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := filepath.Join(home, ".claude")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", profile)

	// Conductor — keeps ambient profile, never gets a scratch dir.
	inst := &Instance{
		ID:          "00000000-0000-0000-0000-000000000a00",
		Tool:        "claude",
		Title:       "conductor-x",
		ProjectPath: filepath.Join(home, "proj"),
	}

	var buf bytes.Buffer
	origLog := sessionLog
	sessionLog = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	t.Cleanup(func() { sessionLog = origLog })

	_ = inst.buildClaudeCommand("claude")

	if strings.Contains(buf.String(), "worker_scratch_override") {
		t.Errorf("override log must not fire when WorkerScratchConfigDir is empty; got: %s", buf.String())
	}
}
