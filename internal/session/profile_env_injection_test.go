package session

import (
	"strings"
	"testing"
)

// Profile env injection (AGENTDECK_PROFILE) at spawn time.
//
// agent-deck injects AGENTDECK_INSTANCE_ID into every spawned session so hook
// subprocesses can find their session. These tests pin the sibling injection of
// AGENTDECK_PROFILE: without it, a bare `agent-deck` command run *inside* a
// non-default-profile session has no AGENTDECK_PROFILE in its shell, so
// GetEffectiveProfile falls back to "default" — resolving the wrong profile and
// silently orphaning auto-parent routing. The value injected must be the
// session's OWN resolved profile, set explicitly as a command-prefix assignment
// so it overrides (not inherits) any stale parent value at exec.

// withProfileEnv pins AGENTDECK_PROFILE so GetEffectiveProfile("") resolves
// deterministically to profile (priority #2, ahead of CLAUDE_CONFIG_DIR/config).
// t.Setenv restores the previous value at test end.
func withProfileEnv(t *testing.T, profile string) {
	t.Helper()
	t.Setenv("AGENTDECK_PROFILE", profile)
}

func TestBuildClaudeCommand_ExportsProfile(t *testing.T) {
	withProfileEnv(t, "work")

	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	cmd := inst.buildClaudeCommand("claude")

	if !strings.Contains(cmd, "AGENTDECK_PROFILE=work") {
		t.Errorf("claude command should inject AGENTDECK_PROFILE=work, got: %s", cmd)
	}
}

func TestBuildClaudeResumeCommand_ExportsProfile(t *testing.T) {
	withProfileEnv(t, "work")

	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	inst.ClaudeSessionID = "abc-123-def"
	cmd := inst.buildClaudeResumeCommand()

	if !strings.Contains(cmd, "AGENTDECK_PROFILE=work") {
		t.Errorf("claude resume command should inject AGENTDECK_PROFILE=work, got: %s", cmd)
	}
}

func TestBuildCodexCommand_ExportsProfile(t *testing.T) {
	withProfileEnv(t, "work")

	inst := NewInstanceWithTool("test", "/tmp/test", "codex")
	cmd := inst.buildCodexCommand("codex")

	if !strings.Contains(cmd, "AGENTDECK_PROFILE=work") {
		t.Errorf("codex command should inject AGENTDECK_PROFILE=work, got: %s", cmd)
	}
}

// TestBuildBashExportPrefix_ExportsProfile covers the custom-command / conductor
// wrapper path, which exports the per-session vars via `export VAR=...;`.
func TestBuildBashExportPrefix_ExportsProfile(t *testing.T) {
	withProfileEnv(t, "work")

	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	prefix := inst.buildBashExportPrefix()

	if !strings.Contains(prefix, "export AGENTDECK_PROFILE=work;") {
		t.Errorf("bash export prefix should export AGENTDECK_PROFILE=work, got: %s", prefix)
	}
}

// TestSpawnProfile_IsExplicitResolvedNotDefault verifies the injected value is
// the session's RESOLVED profile placed as an explicit command-prefix
// assignment (which overrides any inherited AGENTDECK_PROFILE at exec), not the
// hardcoded "default" fallback. This is the non-inherit property: a child
// spawned from a non-default-profile session carries that session's own profile.
func TestSpawnProfile_IsExplicitResolvedNotDefault(t *testing.T) {
	withProfileEnv(t, "personal")

	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	cmd := inst.buildClaudeCommand("claude")

	// The resolved profile ("personal") is injected explicitly...
	if !strings.Contains(cmd, "AGENTDECK_PROFILE=personal") {
		t.Fatalf("expected explicit AGENTDECK_PROFILE=personal, got: %s", cmd)
	}
	// ...and the "default" fallback is NOT what gets injected.
	if strings.Contains(cmd, "AGENTDECK_PROFILE=default") {
		t.Errorf("must not inject the default fallback when a real profile resolves, got: %s", cmd)
	}
}
