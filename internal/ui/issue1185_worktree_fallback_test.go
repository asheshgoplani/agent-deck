package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Regression tests for #1185: with [worktree] default_enabled=true, every new
// session tried to create a git worktree — including sessions on directories
// that are not git repos, which failed hard with "Path is not a git
// repository". The default-enabled path must silently fall back to a normal
// session on non-repo dirs, while an EXPLICIT worktree request must still fail
// loudly.

func makeGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test User"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %s failed: %v", args[0], err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %s failed: %v", args[0], err)
		}
	}
}

// Case 1: default_enabled=true + non-git dir → fall back to a normal session
// (fallback=true, no error).
func TestResolveWorktreeTarget_DefaultEnabledNonRepo_FallsBack(t *testing.T) {
	dir := t.TempDir() // not a git repo

	wtPath, repoRoot, fallback, errMsg := resolveWorktreeTarget(dir, "feature/x", false /* explicit */)

	if errMsg != "" {
		t.Fatalf("default-enabled on non-repo dir must not error, got %q", errMsg)
	}
	if !fallback {
		t.Fatalf("default-enabled on non-repo dir must fall back to a normal session")
	}
	if wtPath != "" || repoRoot != "" {
		t.Fatalf("fallback must not produce a worktree path/root, got wtPath=%q repoRoot=%q", wtPath, repoRoot)
	}
}

// Case 2 (regression): default_enabled=true + git repo → worktree created as
// before.
func TestResolveWorktreeTarget_RepoCreatesWorktree(t *testing.T) {
	dir := t.TempDir()
	makeGitRepo(t, dir)

	wtPath, repoRoot, fallback, errMsg := resolveWorktreeTarget(dir, "feature/x", false /* explicit */)

	if errMsg != "" {
		t.Fatalf("git repo must not error, got %q", errMsg)
	}
	if fallback {
		t.Fatalf("git repo must NOT fall back; a worktree should be created")
	}
	if wtPath == "" || repoRoot == "" {
		t.Fatalf("git repo must produce a worktree path and repo root, got wtPath=%q repoRoot=%q", wtPath, repoRoot)
	}
}

// Case 3: explicit worktree + non-git dir → still errors (explicit intent
// preserved).
func TestResolveWorktreeTarget_ExplicitNonRepo_Errors(t *testing.T) {
	dir := t.TempDir() // not a git repo

	wtPath, repoRoot, fallback, errMsg := resolveWorktreeTarget(dir, "feature/x", true /* explicit */)

	if errMsg == "" {
		t.Fatalf("explicit worktree on a non-repo dir must fail loudly")
	}
	if fallback {
		t.Fatalf("explicit worktree on a non-repo dir must NOT fall back silently")
	}
	if wtPath != "" || repoRoot != "" {
		t.Fatalf("errored resolution must not produce a worktree path/root")
	}
}

// Boundary: explicit worktree + git repo → worktree created (explicit on a real
// repo still works).
func TestResolveWorktreeTarget_ExplicitRepo_CreatesWorktree(t *testing.T) {
	dir := t.TempDir()
	makeGitRepo(t, dir)

	wtPath, repoRoot, fallback, errMsg := resolveWorktreeTarget(dir, "feature/x", true /* explicit */)

	if errMsg != "" || fallback || wtPath == "" || repoRoot == "" {
		t.Fatalf("explicit worktree on a git repo must create a worktree: errMsg=%q fallback=%v wtPath=%q repoRoot=%q", errMsg, fallback, wtPath, repoRoot)
	}
}

// Dialog explicitness: a worktree enabled purely by config default is NOT
// explicit; once the user toggles the checkbox it becomes explicit intent.
func TestNewDialog_IsWorktreeExplicit_DefaultVsToggle(t *testing.T) {
	d := NewNewDialog()

	// Simulate config default_enabled=true without an explicit user toggle.
	d.worktreeEnabled = true
	if d.IsWorktreeExplicit() {
		t.Fatalf("worktree enabled by config default must not be reported as explicit")
	}

	// User toggles the checkbox → explicit intent.
	d.ToggleWorktree()
	if !d.IsWorktreeExplicit() {
		t.Fatalf("worktree toggled by the user must be reported as explicit")
	}
}

func TestForkDialog_IsWorktreeExplicit_DefaultVsToggle(t *testing.T) {
	d := NewForkDialog()

	d.worktreeEnabled = true
	if d.IsWorktreeExplicit() {
		t.Fatalf("fork worktree enabled by config default must not be reported as explicit")
	}

	d.ToggleWorktree()
	if !d.IsWorktreeExplicit() {
		t.Fatalf("fork worktree toggled by the user must be reported as explicit")
	}
}
