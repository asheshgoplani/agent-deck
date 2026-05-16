package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIssue949_ScratchInjectionGate locks the fix for #949: a prepared
// WorkerScratchConfigDir MUST NOT be injected into the spawn command when
// the instance has no explicit config_dir resolved (no env CLAUDE_CONFIG_DIR,
// no profile/group/conductor config_dir, empty [claude].config_dir).
//
// Background. v1.7.68 (issue #59) introduced a per-worker scratch
// CLAUDE_CONFIG_DIR to pin the telegram plugin off for non-conductor claude
// workers on hosts running a telegram conductor. v1.9.2 (#779 / b7d37f15)
// refactored the spawn-command gate to inject the scratch unconditionally
// — losing the v1.7.68 + v1.9.1 invariant that scratch was an OVERRIDE for
// an existing explicit config_dir, never a standalone injection.
//
// On macOS this broke OAuth and Claude Code first-run onboarding for every
// telegram-conductor host with no explicit config_dir: the claude binary
// was routed to an opaque scratch path the keychain never saw, triggering
// login + theme + trust prompts on every spawn. Fix restores the gating.
func TestIssue949_ScratchInjectionGate(t *testing.T) {
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
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	_ = os.Unsetenv("AGENTDECK_PROFILE")

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No [claude].config_dir, no [groups.X].claude.config_dir,
	// no [profiles.X].claude.config_dir, no [conductors.X].claude.config_dir.
	// Telegram token is present so the host satisfies
	// hostHasTelegramConductor() — the exact production shape that triggered
	// scratch creation on the reporter's host.
	cfg := `
[conductor.telegram]
token = "fake-token-for-test"
`
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	scratch := filepath.Join(agentDeckDir, "worker-scratch", "inst-test")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}

	t.Run("no_explicit_config_dir_keeps_scratch_dormant", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("inst-test", "/tmp/p", "code", "claude")
		inst.ID = "inst-test"
		inst.WorkerScratchConfigDir = scratch

		if IsClaudeConfigDirExplicitForInstance(inst) {
			t.Fatalf("precondition violated: IsClaudeConfigDirExplicitForInstance must be false for this fixture")
		}

		cmd := inst.buildClaudeCommandWithMessage("claude", "")
		if strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
			t.Errorf("scratch must NOT be injected when no explicit config_dir is set (#949)\ngot: %s", cmd)
		}

		exp := inst.buildBashExportPrefix()
		if strings.Contains(exp, "CLAUDE_CONFIG_DIR=") {
			t.Errorf("buildBashExportPrefix must NOT export CLAUDE_CONFIG_DIR when no explicit config_dir is set (#949)\ngot: %s", exp)
		}
	})

	t.Run("explicit_config_dir_lets_scratch_override", func(t *testing.T) {
		explicit := filepath.Join(tmpHome, ".claude-explicit")
		cfgExplicit := `
[claude]
config_dir = "~/.claude-explicit"

[conductor.telegram]
token = "fake-token-for-test"
`
		if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfgExplicit), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		ClearUserConfigCache()

		inst := NewInstanceWithGroupAndTool("inst-test", "/tmp/p", "code", "claude")
		inst.ID = "inst-test"
		inst.WorkerScratchConfigDir = scratch

		if !IsClaudeConfigDirExplicitForInstance(inst) {
			t.Fatalf("precondition violated: IsClaudeConfigDirExplicitForInstance must be true with [claude].config_dir set")
		}

		cmd := inst.buildClaudeCommandWithMessage("claude", "")
		if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR="+scratch) {
			t.Errorf("scratch must override explicit config_dir when both are set\nwant CLAUDE_CONFIG_DIR=%s\ngot: %s", scratch, cmd)
		}
		if strings.Contains(cmd, "CLAUDE_CONFIG_DIR="+explicit+" ") {
			t.Errorf("explicit path leaked despite scratch override\ngot: %s", cmd)
		}
	})
}
