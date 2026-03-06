package vcs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInterfaceConformance verifies both backends implement the Backend interface.
func TestInterfaceConformance(t *testing.T) {
	var _ Backend = (*GitBackend)(nil)
}

func TestDetect_GitRepo(t *testing.T) {
	ClearCache()
	dir := t.TempDir()

	// Create a .git directory
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	b := Detect(dir)
	if b == nil {
		t.Fatal("expected git backend, got nil")
	}
	if b.Type() != Git {
		t.Fatalf("expected Git type, got %s", b.Type())
	}
}

func TestDetect_NoRepo(t *testing.T) {
	ClearCache()
	dir := t.TempDir()

	b := Detect(dir)
	if b != nil {
		t.Fatalf("expected nil for non-repo directory, got %s backend", b.Type())
	}
}

func TestDetect_Subdirectory(t *testing.T) {
	ClearCache()
	dir := t.TempDir()

	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(dir, "sub", "deep")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	b := Detect(subDir)
	if b == nil {
		t.Fatal("expected git backend from subdirectory, got nil")
	}
	if b.Type() != Git {
		t.Fatalf("expected Git type, got %s", b.Type())
	}
}

func TestDetect_CachingWorks(t *testing.T) {
	ClearCache()
	dir := t.TempDir()

	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	b1 := Detect(dir)
	b2 := Detect(dir)

	if b1 == nil || b2 == nil {
		t.Fatal("expected non-nil backends")
	}

	// Same pointer from cache
	if b1 != b2 {
		t.Fatal("expected cached backend to be same instance")
	}
}

func TestDetect_AsIsRepoReplacement(t *testing.T) {
	ClearCache()
	dir := t.TempDir()

	if Detect(dir) != nil {
		t.Fatal("expected nil for non-repo")
	}

	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	ClearCache()

	if Detect(dir) == nil {
		t.Fatal("expected non-nil for git repo")
	}
}
