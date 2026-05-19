package git

import (
	"errors"
	"os"
	"path/filepath"
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
