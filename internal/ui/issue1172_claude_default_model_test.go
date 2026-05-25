package ui

// Regression tests for #1172 — the new-session dialog must preselect the
// configured [claude].default_model instead of always defaulting to the first
// catalog entry (claude-sonnet-4-6). Reported by @marekaf.
//
// The model the dialog will launch with is exactly GetLaunchModelID(); these
// tests assert that value after ShowInGroup loads the user config.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// showClaudeDialogWithConfig writes a config.toml under a temp HOME, points the
// loader at it, then opens a fresh new-session dialog with the Claude tool
// preselected. It returns the dialog so the caller can inspect GetLaunchModelID().
func showClaudeDialogWithConfig(t *testing.T, cfg *session.UserConfig) *NewDialog {
	t.Helper()
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	t.Cleanup(func() { os.Setenv("HOME", originalHome) })

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()

	d := NewNewDialog()
	d.SetDefaultTool("claude")
	d.SetSize(100, 50)
	d.ShowInGroup("projects", "Projects", "/tmp", nil, "")
	return d
}

// Happy path: [claude].default_model set to a catalog model => the dialog
// preselects it, so a new Claude session launches with --model that value.
func TestIssue1172_ClaudeDefaultModelPreselected(t *testing.T) {
	d := showClaudeDialogWithConfig(t, &session.UserConfig{
		Claude: session.ClaudeSettings{DefaultModel: "claude-opus-4-7"},
	})

	if got := d.GetLaunchModelID(); got != "claude-opus-4-7" {
		t.Fatalf("GetLaunchModelID() = %q, want claude-opus-4-7 (the configured default)", got)
	}
}

// Regression: no [claude].default_model => the model field stays empty, which
// is the pre-#1172 behavior (Claude uses its own default). This guards against
// the fix accidentally forcing a model when none is configured.
func TestIssue1172_NoDefaultModelFallsBackToEmpty(t *testing.T) {
	d := showClaudeDialogWithConfig(t, &session.UserConfig{
		Claude: session.ClaudeSettings{}, // no default_model
	})

	if got := d.GetLaunchModelID(); got != "" {
		t.Fatalf("GetLaunchModelID() = %q, want empty when no default_model configured", got)
	}
}

// Failure mode: a configured default that is NOT in the known catalog (typo,
// stale pin, or alias like "opus") must degrade gracefully — no crash, and the
// field falls back to empty rather than launching a bogus --model flag.
func TestIssue1172_DefaultModelNotInCatalogGracefulFallback(t *testing.T) {
	d := showClaudeDialogWithConfig(t, &session.UserConfig{
		Claude: session.ClaudeSettings{DefaultModel: "claude-made-up-9000"},
	})

	if got := d.GetLaunchModelID(); got != "" {
		t.Fatalf("GetLaunchModelID() = %q, want empty for a non-catalog default (graceful fallback)", got)
	}
}

// Boundary: the Claude default must not leak into a non-Claude tool. Selecting
// gemini with only [claude].default_model configured leaves gemini's model
// empty (gemini has its own default_model path at command-build time).
func TestIssue1172_ClaudeDefaultDoesNotLeakToOtherTools(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	t.Cleanup(func() { os.Setenv("HOME", originalHome) })

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	if err := os.MkdirAll(filepath.Join(tempDir, ".agent-deck"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := session.SaveUserConfig(&session.UserConfig{
		Claude: session.ClaudeSettings{DefaultModel: "claude-opus-4-7"},
	}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()

	d := NewNewDialog()
	d.SetDefaultTool("gemini")
	d.SetSize(100, 50)
	d.ShowInGroup("projects", "Projects", "/tmp", nil, "")

	if got := d.GetLaunchModelID(); got != "" {
		t.Fatalf("GetLaunchModelID() for gemini = %q, want empty (Claude default must not leak)", got)
	}
}
