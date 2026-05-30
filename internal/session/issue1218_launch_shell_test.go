package session

import (
	"os"
	"strings"
	"testing"
)

// Issue #1218: OpenCode session doesn't inherit ZSH env vars. When starting
// OpenCode directly from the TUI, env vars from ~/.zshrc aren't available to
// the agent process, causing MCP configs with {env:VAR} references to fail.
// The fix wraps agent commands with "$SHELL -l -c '<cmd>'" when the
// [shell].launch_shell config is enabled, so the login shell sources rc files
// before executing the agent.
//
// These tests pin the contract: flag ON wraps with login shell, flag OFF is
// unchanged, sandbox/SSH/shell sessions are excluded, and both per-session
// and global config levels work correctly.

// launchShellTestEnv isolates HOME and SHELL for deterministic testing.
func launchShellTestEnv(t *testing.T) {
	t.Helper()
	origHome := os.Getenv("HOME")
	origShell := os.Getenv("SHELL")
	os.Setenv("HOME", t.TempDir())
	os.Setenv("SHELL", "/bin/zsh") // Set a deterministic shell for tests
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		if origShell != "" {
			os.Setenv("SHELL", origShell)
		} else {
			os.Unsetenv("SHELL")
		}
		ClearUserConfigCache()
	})
}

// --- Happy path: flag ON wraps with login shell ---

func TestLaunchShell_OpenCodeWrappedWhenEnabled(t *testing.T) {
	launchShellTestEnv(t)

	inst := NewInstanceWithTool("ls-opencode", t.TempDir(), "opencode")
	inst.LaunchShell = boolPtr(true)

	raw := "opencode --model claude-3.5-sonnet"
	wrapped := inst.wrapLaunchShell(raw)

	if !strings.HasPrefix(wrapped, "/bin/zsh -l -c '") {
		t.Fatalf("wrapped command must start with '/bin/zsh -l -c ', got:\n%s", wrapped)
	}
	if !strings.Contains(wrapped, "opencode") {
		t.Fatalf("wrapped command must contain original command, got:\n%s", wrapped)
	}
}

func TestLaunchShell_ClaudeWrappedWhenEnabled(t *testing.T) {
	launchShellTestEnv(t)

	inst := NewInstanceWithTool("ls-claude", t.TempDir(), "claude")
	inst.LaunchShell = boolPtr(true)

	raw := "claude --session-id test-123"
	wrapped := inst.wrapLaunchShell(raw)

	if !strings.HasPrefix(wrapped, "/bin/zsh -l -c '") {
		t.Fatalf("wrapped command must start with '/bin/zsh -l -c ', got:\n%s", wrapped)
	}
	if !strings.Contains(wrapped, "claude") {
		t.Fatalf("wrapped command must contain original command, got:\n%s", wrapped)
	}
}

// --- Regression: flag OFF leaves command unchanged ---

func TestLaunchShell_DisabledLeavesCommandUnchanged(t *testing.T) {
	launchShellTestEnv(t)

	inst := NewInstanceWithTool("ls-off", t.TempDir(), "opencode")
	// No per-session override and no config flag -> default OFF

	raw := "opencode"
	wrapped := inst.wrapLaunchShell(raw)

	if wrapped != raw {
		t.Fatalf("flag OFF must not alter the command.\n raw:     %s\n wrapped: %s", raw, wrapped)
	}
	if strings.Contains(wrapped, "-l -c") {
		t.Fatalf("flag OFF must not add shell wrapper, got:\n%s", wrapped)
	}
}

// --- Shell and SSH sessions excluded ---

func TestLaunchShell_ShellToolExcluded(t *testing.T) {
	launchShellTestEnv(t)

	inst := NewInstanceWithTool("ls-shell", t.TempDir(), "shell")
	inst.LaunchShell = boolPtr(true)

	raw := "bash"
	wrapped := inst.wrapLaunchShell(raw)

	if wrapped != raw {
		t.Fatalf("shell tool must not be wrapped even with flag ON.\n raw:     %s\n wrapped: %s", raw, wrapped)
	}
}

func TestLaunchShell_SSHSessionExcluded(t *testing.T) {
	launchShellTestEnv(t)

	inst := NewInstanceWithTool("ls-ssh", t.TempDir(), "claude")
	inst.SSHHost = "remote.example.com"
	inst.LaunchShell = boolPtr(true)

	raw := "claude"
	wrapped := inst.wrapLaunchShell(raw)

	if wrapped != raw {
		t.Fatalf("SSH session must not be wrapped even with flag ON.\n raw:     %s\n wrapped: %s", raw, wrapped)
	}
}

// --- Sandbox sessions excluded ---

func TestLaunchShell_SandboxExcluded(t *testing.T) {
	launchShellTestEnv(t)

	inst := NewInstanceWithTool("ls-sandbox", t.TempDir(), "opencode")
	inst.Sandbox = &SandboxConfig{
		Image:   "test-image",
		Enabled: true,
	}
	inst.LaunchShell = boolPtr(true)

	raw := "opencode"
	wrapped := inst.wrapLaunchShell(raw)

	if wrapped != raw {
		t.Fatalf("sandbox session must not be wrapped even with flag ON.\n raw:     %s\n wrapped: %s", raw, wrapped)
	}
}

// --- Quote escaping ---

func TestLaunchShell_SingleQuotesEscaped(t *testing.T) {
	launchShellTestEnv(t)

	inst := NewInstanceWithTool("ls-quotes", t.TempDir(), "opencode")
	inst.LaunchShell = boolPtr(true)

	raw := "opencode --query 'hello world'"
	wrapped := inst.wrapLaunchShell(raw)

	// Single quotes in the command should be escaped as '"'"'
	if !strings.Contains(wrapped, `opencode --query '"'"'hello world'"'"'`) &&
		!strings.Contains(wrapped, `opencode --query '\"'\"'hello world'\"'\"'`) {
		t.Fatalf("single quotes must be escaped, got:\n%s", wrapped)
	}
}

// --- Integration with prepareCommand ---

func TestLaunchShell_IntegrationWithPrepareCommand(t *testing.T) {
	launchShellTestEnv(t)

	inst := NewInstanceWithTool("ls-integration", t.TempDir(), "opencode")
	inst.LaunchShell = boolPtr(true)

	raw := "opencode"
	prepared, _, err := inst.prepareCommand(raw)
	if err != nil {
		t.Fatalf("prepareCommand failed: %v", err)
	}

	if !strings.Contains(prepared, "-l -c") {
		t.Fatalf("prepareCommand must apply launch_shell wrap, got:\n%s", prepared)
	}
}

// --- Global config fallback ---

func TestLaunchShell_GlobalConfigFallback(t *testing.T) {
	launchShellTestEnv(t)

	// Create .agent-deck directory and config file
	agentDeckDir := os.Getenv("HOME") + "/.agent-deck"
	if err := os.MkdirAll(agentDeckDir, 0755); err != nil {
		t.Fatalf("failed to create .agent-deck dir: %v", err)
	}
	configPath := agentDeckDir + "/config.toml"
	configContent := `
[shell]
launch_shell = true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithTool("ls-global", t.TempDir(), "opencode")
	// No per-session override, should fall back to global config

	raw := "opencode"
	wrapped := inst.wrapLaunchShell(raw)

	if !strings.Contains(wrapped, "-l -c") {
		t.Fatalf("global config launch_shell=true must wrap command, got:\n%s", wrapped)
	}
}

// --- Per-session override takes precedence ---

func TestLaunchShell_PerSessionOverride(t *testing.T) {
	launchShellTestEnv(t)

	// Create .agent-deck directory and config file with launch_shell = false
	agentDeckDir := os.Getenv("HOME") + "/.agent-deck"
	if err := os.MkdirAll(agentDeckDir, 0755); err != nil {
		t.Fatalf("failed to create .agent-deck dir: %v", err)
	}
	configPath := agentDeckDir + "/config.toml"
	configContent := `
[shell]
launch_shell = false
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithTool("ls-override", t.TempDir(), "opencode")
	inst.LaunchShell = boolPtr(true) // Per-session override to true

	raw := "opencode"
	wrapped := inst.wrapLaunchShell(raw)

	if !strings.Contains(wrapped, "-l -c") {
		t.Fatalf("per-session override must take precedence over global config, got:\n%s", wrapped)
	}
}

// --- Empty SHELL env uses default ---

func TestLaunchShell_DefaultsToBashdWhenSHELLUnset(t *testing.T) {
	launchShellTestEnv(t)
	os.Unsetenv("SHELL") // Clear SHELL env var

	inst := NewInstanceWithTool("ls-default", t.TempDir(), "opencode")
	inst.LaunchShell = boolPtr(true)

	raw := "opencode"
	wrapped := inst.wrapLaunchShell(raw)

	if !strings.HasPrefix(wrapped, "/bin/bash -l -c '") {
		t.Fatalf("when SHELL is unset, should default to /bin/bash, got:\n%s", wrapped)
	}
}

// --- Interaction with exit_to_shell ---

func TestLaunchShell_CombinedWithExitToShell(t *testing.T) {
	launchShellTestEnv(t)

	inst := NewInstanceWithTool("ls-combined", t.TempDir(), "claude")
	inst.LaunchShell = boolPtr(true)
	inst.ExitToShell = boolPtr(true)

	// Build the command with both wraps applied via prepareCommand
	raw := "claude"
	prepared, _, err := inst.prepareCommand(raw)
	if err != nil {
		t.Fatalf("prepareCommand failed: %v", err)
	}

	// Should have both: exit-to-shell suffix AND launch-shell wrapper
	if !strings.Contains(prepared, `exec "$SHELL" -i`) {
		t.Fatalf("must have exit-to-shell suffix, got:\n%s", prepared)
	}
	if !strings.Contains(prepared, "-l -c") {
		t.Fatalf("must have launch-shell wrapper, got:\n%s", prepared)
	}
}
