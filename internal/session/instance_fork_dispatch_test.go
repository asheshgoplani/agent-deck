package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateForkedInstanceForTool_OpenCodeUsesWorktreeDir(t *testing.T) {
	parent := NewInstanceWithTool("oc parent", "/tmp/original", "opencode")
	parent.OpenCodeSessionID = "ses_parent"
	parent.OpenCodeDetectedAt = time.Now()

	opts := &ClaudeOptions{
		WorkDir:          "/tmp/original-wt",
		WorktreePath:     "/tmp/original-wt",
		WorktreeRepoRoot: "/tmp/original",
		WorktreeBranch:   "fork/oc-parent",
	}

	forked, _, err := parent.CreateForkedInstanceForTool("oc fork", "", opts)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	if forked.Tool != "opencode" {
		t.Fatalf("forked tool = %q, want opencode", forked.Tool)
	}
	if forked.ProjectPath != "/tmp/original-wt" {
		t.Fatalf("ProjectPath = %q, want worktree dir", forked.ProjectPath)
	}
	if forked.WorktreePath != "/tmp/original-wt" ||
		forked.WorktreeRepoRoot != "/tmp/original" ||
		forked.WorktreeBranch != "fork/oc-parent" {
		t.Fatalf("worktree metadata not copied: %+v", forked)
	}
}

func TestCreateForkedInstanceForTool_CodexCompatibleUsesCodexFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sid := "11111111-2222-3333-4444-555555555555"
	dir := filepath.Join(home, "sessions", "2026", "06", "07")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-20260607T000000-"+sid+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "codex")
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	forked, cmd, err := parent.CreateForkedInstanceForTool("cx fork", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	if forked.Tool != "codex" {
		t.Fatalf("forked tool = %q, want codex", forked.Tool)
	}
	if !forked.IsForkAwaitingStart || forked.ForkStartCommand == "" {
		t.Fatal("codex fork must use deferred ForkStartCommand")
	}
	if !strings.Contains(cmd, " fork "+sid) {
		t.Fatalf("codex fork command missing parent sid: %s", cmd)
	}
}
