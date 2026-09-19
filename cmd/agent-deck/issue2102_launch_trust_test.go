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

	projectDir := t.TempDir()
	inst := &session.Instance{Tool: "claude", ProjectPath: projectDir}

	preAcceptLaunchTrust(inst)

	claudeJSONPath := filepath.Join(home, ".claude.json")
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
		t.Fatalf("expected projects[%q].hasTrustDialogAccepted = true, got %+v", realDir, cfg.Projects)
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
