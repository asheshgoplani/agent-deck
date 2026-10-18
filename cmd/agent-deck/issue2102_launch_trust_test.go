package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2102: `agent-deck launch` into a directory Claude Code has never
// opened interactively reports success, then fails silently — the pane
// dies on the "do you trust the files in this folder?" prompt, no tmux
// session is created, and no log is written. PreAcceptClaudeTrust already
// solves this for conductor dirs (#1359) and worktree parents (#1149), but
// nothing on the ordinary `launch` path called it. preAcceptLaunchTrust is
// the launch-path caller that closes that gap.
func TestIssue2102_PreAcceptLaunchTrustSeedsUnopenedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	projectDir := t.TempDir()
	inst := &session.Instance{Tool: "claude", ProjectPath: projectDir}

	preAcceptLaunchTrust(inst)

	assertLaunchTrustSeeded(t, filepath.Join(home, ".claude.json"), projectDir)
}

// assertLaunchTrustSeeded checks claudeJSONPath has
// projects[realpath(projectDir)].hasTrustDialogAccepted = true.
func assertLaunchTrustSeeded(t *testing.T, claudeJSONPath, projectDir string) {
	t.Helper()
	data, err := os.ReadFile(claudeJSONPath)
	if err != nil {
		t.Fatalf("expected %s to be written: %v", claudeJSONPath, err)
	}

	var cfg struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse %s: %v", claudeJSONPath, err)
	}

	realDir, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatalf("resolve %s: %v", projectDir, err)
	}
	entry, ok := cfg.Projects[realDir]
	if !ok || !entry.HasTrustDialogAccepted {
		t.Fatalf("expected projects[%q].hasTrustDialogAccepted = true in %s, got %+v", realDir, claudeJSONPath, cfg.Projects)
	}
}

// A non-claude tool (shell, codex, ...) has no trust dialog to pre-seed;
// calling this for every tool would just write a useless ~/.claude.json.
func TestIssue2102_PreAcceptLaunchTrustSkipsNonClaudeTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inst := &session.Instance{Tool: "shell", ProjectPath: t.TempDir()}
	preAcceptLaunchTrust(inst)

	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no ~/.claude.json to be written for a non-claude tool, stat err = %v", err)
	}
}

// Issue #2673: `launch -account <slot>` runs Claude with
// CLAUDE_CONFIG_DIR=<slot config_dir>, so Claude reads
// <slot config_dir>/.claude.json, not ~/.claude.json. The launch pre-seed
// only wrote ~/.claude.json, so a session on another config root still
// died on the folder-trust prompt. The seed must land in the root the
// session will actually read.
func TestIssue2673_PreAcceptLaunchTrustSeedsAccountConfigRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	slotDir := filepath.Join(home, ".claude-work")
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	toml := "[profiles.work.claude]\nconfig_dir = \"~/.claude-work\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	projectDir := t.TempDir()
	inst := &session.Instance{Tool: "claude", ProjectPath: projectDir, Account: "work"}
	if got := session.GetClaudeConfigDirForInstance(inst); got != slotDir {
		t.Fatalf("test setup: account config dir = %q, want %q", got, slotDir)
	}

	preAcceptLaunchTrust(inst)

	assertLaunchTrustSeeded(t, filepath.Join(slotDir, ".claude.json"), projectDir)
	// The home root keeps its seed too (unchanged #2102 behavior).
	assertLaunchTrustSeeded(t, filepath.Join(home, ".claude.json"), projectDir)
}
