package git

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateWorktreeWithSetup_ProgressMessages verifies #768: the setup
// script lifecycle (start → completion) is announced on stderr so the user
// can tell whether the script ran, finished, and finished cleanly. Without
// these signals, the TUI silently hands off to claude before the user knows
// the script completed.
//
// The "Running worktree setup script..." preamble already existed; this
// test pins the explicit completion line that #768 asks for.
func TestCreateWorktreeWithSetup_ProgressMessages_Success(t *testing.T) {
	dir := t.TempDir()
	createTestRepoForSetup(t, dir)

	scriptDir := filepath.Join(dir, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\necho running\n"
	if err := os.WriteFile(filepath.Join(scriptDir, "worktree-setup.sh"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	backend, err := NewGitBackend(dir)
	if err != nil {
		t.Fatalf("failed to create GitBackend: %v", err)
	}

	worktreePath := filepath.Join(dir, ".worktrees", "progress-ok")
	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithSetup(backend, worktreePath, "progress-ok", &stdout, &stderr, 0)
	if err != nil {
		t.Fatalf("worktree creation failed: %v", err)
	}
	if setupErr != nil {
		t.Fatalf("expected setup success, got: %v\nstderr: %s", setupErr, stderr.String())
	}

	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Running worktree setup script") {
		t.Errorf("expected start preamble on stderr, got: %q", stderrStr)
	}
	if !strings.Contains(stderrStr, "Worktree setup script completed") {
		t.Errorf("expected explicit completion line on stderr, got: %q", stderrStr)
	}
}

func TestCreateWorktreeWithSetup_ProgressMessages_Failure(t *testing.T) {
	dir := t.TempDir()
	createTestRepoForSetup(t, dir)

	scriptDir := filepath.Join(dir, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\necho oops >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(scriptDir, "worktree-setup.sh"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	backend, err := NewGitBackend(dir)
	if err != nil {
		t.Fatalf("failed to create GitBackend: %v", err)
	}

	worktreePath := filepath.Join(dir, ".worktrees", "progress-fail")
	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithSetup(backend, worktreePath, "progress-fail", &stdout, &stderr, 0)
	if err != nil {
		t.Fatalf("worktree creation should succeed even when setup fails: %v", err)
	}
	if setupErr == nil {
		t.Fatal("expected setupErr from non-zero-exit script")
	}

	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Worktree setup script failed") {
		t.Errorf("expected explicit failure line on stderr, got: %q", stderrStr)
	}
}
