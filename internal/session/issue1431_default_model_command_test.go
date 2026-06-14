package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #1431: conductors spawn children that silently run on Claude's
// built-in default (Fable) instead of the configured [claude].default_model
// (Opus). #1390 wired default_model into NewClaudeOptions, which is only
// consulted when an Instance has *no* persisted ToolOptionsJSON. A session
// that carries any other persisted Claude option (skip-permissions, chrome,
// teammate-mode, etc.) but no explicit model has a NON-NIL ClaudeOptions with
// Model=="", so GetClaudeOptions() short-circuits the NewClaudeOptions
// fallback and the launch command omits --model entirely. The session boots
// on Fable and silently no-ops account-wide.
//
// The contract these tests pin: when [claude].default_model is configured and
// the session has no explicit per-session model, the built launch command
// MUST carry `--model <default>` — regardless of whether ToolOptionsJSON is
// nil or a non-nil options struct with an empty Model.

// issue1431ConfigEnv points LoadUserConfig at a temp HOME holding a
// config.toml with the given [claude].default_model, isolated from the host.
func issue1431ConfigEnv(t *testing.T, defaultModel string) {
	t.Helper()
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	origClaudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
		}
		if origClaudeDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origClaudeDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})

	_ = os.Setenv("HOME", tmpHome)
	_ = os.Unsetenv("AGENTDECK_PROFILE")
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configContent := "[claude]\ndefault_model = \"" + defaultModel + "\"\n"
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(configContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
}

// Baseline: a fresh claude session with no persisted options must pick up
// [claude].default_model at command-build time (the #1390 nil-fallback path).
func TestIssue1431_DefaultModelApplied_NilOptions(t *testing.T) {
	issue1431ConfigEnv(t, "claude-opus-4-8")

	inst := NewInstanceWithTool("dm-nil", t.TempDir(), "claude")
	if inst.GetClaudeOptions() != nil {
		t.Fatalf("precondition: expected nil ClaudeOptions on a fresh instance")
	}

	cmd := inst.buildClaudeCommand("claude")
	if !strings.Contains(cmd, "--model claude-opus-4-8") {
		t.Fatalf("nil-options spawn dropped [claude].default_model; command missing --model claude-opus-4-8:\n%s", cmd)
	}
}

// The #1431 gap: a session carrying a non-model Claude option (here
// skip-permissions) has a non-nil ClaudeOptions with Model=="". The
// NewClaudeOptions fallback is skipped, so without a final default_model
// resolution the command omits --model and the child runs on Fable.
func TestIssue1431_DefaultModelApplied_NonNilEmptyModelOptions(t *testing.T) {
	issue1431ConfigEnv(t, "claude-opus-4-8")

	inst := NewInstanceWithTool("dm-nonnil", t.TempDir(), "claude")
	// Persist options WITHOUT a model — mimics any session that set another
	// Claude option (skip-permissions/chrome/teammate-mode) at create time.
	if err := inst.SetClaudeOptions(&ClaudeOptions{SessionMode: "new", SkipPermissions: true}); err != nil {
		t.Fatalf("SetClaudeOptions: %v", err)
	}
	if opts := inst.GetClaudeOptions(); opts == nil || opts.Model != "" {
		t.Fatalf("precondition: expected non-nil options with empty Model")
	}

	cmd := inst.buildClaudeCommand("claude")
	if !strings.Contains(cmd, "--model claude-opus-4-8") {
		t.Fatalf("non-nil/empty-model spawn dropped [claude].default_model; command missing --model claude-opus-4-8:\n%s", cmd)
	}
}

// Guard: an explicit per-session model must win over [claude].default_model —
// the default is a fallback, never an override.
func TestIssue1431_ExplicitModelBeatsDefault(t *testing.T) {
	issue1431ConfigEnv(t, "claude-opus-4-8")

	inst := NewInstanceWithTool("dm-explicit", t.TempDir(), "claude")
	if err := inst.SetClaudeOptions(&ClaudeOptions{SessionMode: "new", Model: "claude-haiku-4-5"}); err != nil {
		t.Fatalf("SetClaudeOptions: %v", err)
	}

	cmd := inst.buildClaudeCommand("claude")
	if !strings.Contains(cmd, "--model claude-haiku-4-5") {
		t.Fatalf("explicit per-session model lost; command:\n%s", cmd)
	}
	if strings.Contains(cmd, "claude-opus-4-8") {
		t.Fatalf("default_model wrongly overrode explicit per-session model:\n%s", cmd)
	}
}

// Guard: when the operator supplies --model through --extra-arg (the form the
// ValidateClaudeExtraArgToken error message recommends), the default_model
// fallback must NOT also inject a second --model. claude is last-wins so the
// duplicate is harmless, but the command must carry exactly one --model and it
// must be the user's, not the default.
func TestIssue1431_ExtraArgModelSuppressesDefault(t *testing.T) {
	issue1431ConfigEnv(t, "claude-opus-4-8")

	inst := NewInstanceWithTool("dm-extraarg", t.TempDir(), "claude")
	inst.ExtraArgs = []string{"--model", "claude-haiku-4-5"}

	cmd := inst.buildClaudeCommand("claude")
	if strings.Contains(cmd, "claude-opus-4-8") {
		t.Fatalf("default_model injected alongside user --extra-arg --model:\n%s", cmd)
	}
	if strings.Count(cmd, "--model") != 1 {
		t.Fatalf("expected exactly one --model, got %d:\n%s", strings.Count(cmd, "--model"), cmd)
	}
}
