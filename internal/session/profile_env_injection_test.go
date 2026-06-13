package session

import (
	"strings"
	"testing"
	"time"
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

// --- Host-side (tool-agnostic) profile injection ---
//
// Some respawn-pane branches in Restart() rebuild a bare resume command that
// carries NO inline AGENTDECK_PROFILE prefix (gemini, opencode, generic), and
// every respawn branch returns before reaching the fallback recreate path that
// sets it host-side. ensureProfileEnv() is the shared safety net those branches
// (and the spawn paths) call so the tmux session always carries the var.

// TestEnsureProfileEnv_SetsHostSideEnv pins that ensureProfileEnv writes
// AGENTDECK_PROFILE into the live tmux session environment as the session's own
// resolved profile. This is the exact call every Restart() respawn-pane branch
// now makes, so it stands in for the tmux-dependent respawn paths (gemini/
// opencode/codex/generic) that CI cannot launch (no tool binary).
func TestEnsureProfileEnv_SetsHostSideEnv(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)
	withProfileEnv(t, "work")

	title := uniqueShellTestTitle("EnsureProfileEnv")
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	// Clear it first so we prove ensureProfileEnv is what sets it (Start also
	// sets it, but we want to isolate the helper the respawn branches call).
	if err := inst.tmuxSession.SetEnvironment("AGENTDECK_PROFILE", "stale"); err != nil {
		t.Fatalf("seed SetEnvironment failed: %v", err)
	}

	inst.ensureProfileEnv()

	got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PROFILE")
	if err != nil {
		t.Fatalf("GetEnvironment(AGENTDECK_PROFILE) failed: %v", err)
	}
	if got != "work" {
		t.Errorf("AGENTDECK_PROFILE in tmux env = %q, want %q", got, "work")
	}
}

// TestEnsureProfileEnv_NilTmuxSession_NoPanic pins the nil guard: a respawn
// branch must never panic if the instance has no tmux session.
func TestEnsureProfileEnv_NilTmuxSession_NoPanic(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	inst.tmuxSession = nil
	inst.ensureProfileEnv() // must not panic
}

// TestRestart_ShellSession_CarriesProfileEnv is the end-to-end contract: after
// Restart(), the session's tmux environment carries AGENTDECK_PROFILE set to the
// session's own resolved profile. Shell sessions take the fallback recreate path
// (no resume support), but this proves ensureProfileEnv is wired through Restart.
func TestRestart_ShellSession_CarriesProfileEnv(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)
	withProfileEnv(t, "personal")

	title := uniqueShellTestTitle("RestartProfileEnv")
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}
	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q does not exist after Restart", inst.tmuxSession.Name)
	}

	got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PROFILE")
	if err != nil {
		t.Fatalf("GetEnvironment(AGENTDECK_PROFILE) after Restart failed: %v", err)
	}
	if got != "personal" {
		t.Errorf("after Restart, AGENTDECK_PROFILE = %q, want %q", got, "personal")
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
