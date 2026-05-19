package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateForkWithStateDestination_Clean(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	if err := ValidateForkWithStateDestination(dir, "fork/new"); err != nil {
		t.Fatalf("clean repo + fresh branch should pass, got %v", err)
	}
}

func TestValidateForkWithStateDestination_BranchExists(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	runGit(t, dir, "branch", "fork/existing")

	err := ValidateForkWithStateDestination(dir, "fork/existing")
	if err == nil {
		t.Fatal("expected DestinationCollisionError")
	}
	var collErr *DestinationCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error = %T %v, want *DestinationCollisionError", err, err)
	}
	if collErr.Kind != CollisionBranchExists || collErr.Branch != "fork/existing" {
		t.Fatalf("unexpected error: %+v", collErr)
	}
}

func TestValidateForkWithStateDestination_WorktreeExists_TakesPrecedence(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	createTestRepo(t, base)
	wtPath := filepath.Join(root, "fork-wt")
	if err := CreateWorktree(base, wtPath, "fork/used"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if !BranchExists(base, "fork/used") {
		t.Fatal("setup invariant: branch should also exist (CreateWorktree creates it); test cannot prove precedence otherwise")
	}

	err := ValidateForkWithStateDestination(base, "fork/used")
	if err == nil {
		t.Fatal("expected DestinationCollisionError")
	}
	var collErr *DestinationCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error = %T %v, want *DestinationCollisionError", err, err)
	}
	if collErr.Kind != CollisionWorktreeExists || collErr.Path == "" {
		t.Fatalf("unexpected error: %+v", collErr)
	}
}

func TestValidateForkWithStateDestination_PropagatesWorktreeCheckError(t *testing.T) {
	dir := t.TempDir()

	err := ValidateForkWithStateDestination(dir, "fork/new")
	if err == nil {
		t.Fatal("expected worktree check error for non-git repo, got nil")
	}
	var collErr *DestinationCollisionError
	if errors.As(err, &collErr) {
		t.Fatalf("error = %+v, want underlying worktree check error", collErr)
	}
	if !strings.Contains(err.Error(), "checking existing worktrees") {
		t.Fatalf("error = %q, want checking existing worktrees context", err.Error())
	}
}

func TestHasSubmodules_None(t *testing.T) {
	dir := t.TempDir()
	if HasSubmodules(dir) {
		t.Fatal("empty dir should report HasSubmodules=false")
	}
}

func TestHasSubmodules_Present(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte("[submodule \"lib\"]\n\tpath = lib\n\turl = https://example.invalid/lib.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasSubmodules(dir) {
		t.Fatal("dir with .gitmodules should report HasSubmodules=true")
	}
}

// TestDetectInProgressOperation_Clean — a freshly-initialized repo with no
// in-flight state files must return "" and no error.
func TestDetectInProgressOperation_Clean(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	kind, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("clean repo: unexpected error %v", err)
	}
	if kind != "" {
		t.Fatalf("clean repo: expected empty kind, got %q", kind)
	}
}

// TestDetectInProgressOperation_Rebase — .git/rebase-merge is a directory git
// creates during an interactive rebase; presence must return "rebase".
func TestDetectInProgressOperation_Rebase(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	kind, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != "rebase" {
		t.Fatalf("expected kind=rebase, got %q", kind)
	}
}

// TestDetectInProgressOperation_Merge — .git/MERGE_HEAD is a file containing
// the SHA of the commit being merged; presence must return "merge".
func TestDetectInProgressOperation_Merge(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".git", "MERGE_HEAD"), []byte("deadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != "merge" {
		t.Fatalf("expected kind=merge, got %q", kind)
	}
}

// TestDetectInProgressOperation_CherryPick — .git/CHERRY_PICK_HEAD is a file
// containing the SHA being cherry-picked; presence must return "cherry-pick".
func TestDetectInProgressOperation_CherryPick(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD"), []byte("deadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != "cherry-pick" {
		t.Fatalf("expected kind=cherry-pick, got %q", kind)
	}
}

// TestDetectInProgressOperation_Revert — .git/REVERT_HEAD is a file containing
// the SHA being reverted; presence must return "revert".
func TestDetectInProgressOperation_Revert(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".git", "REVERT_HEAD"), []byte("deadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != "revert" {
		t.Fatalf("expected kind=revert, got %q", kind)
	}
}

// TestDetectInProgressOperation_Bisect — .git/BISECT_LOG is the bisect log
// file; presence must return "bisect".
func TestDetectInProgressOperation_Bisect(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".git", "BISECT_LOG"), []byte("git bisect start\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != "bisect" {
		t.Fatalf("expected kind=bisect, got %q", kind)
	}
}
