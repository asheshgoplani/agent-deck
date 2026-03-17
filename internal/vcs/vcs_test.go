package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestInterfaceConformance verifies both backends implement the Backend interface.
func TestInterfaceConformance(t *testing.T) {
	var _ Backend = (*GitBackend)(nil)
	var _ Backend = (*JJBackend)(nil)
}

// initGitRepo creates a real git repository in dir using git init.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}

func TestDetect_GitRepo(t *testing.T) {
	ClearCache()
	dir := t.TempDir()

	// Create a real git repository
	initGitRepo(t, dir)

	b := Detect(dir)
	if b == nil {
		t.Fatal("expected git backend, got nil")
	}
	if b.Type() != Git {
		t.Fatalf("expected Git type, got %s", b.Type())
	}
}

func TestDetect_JJRepo(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not installed, skipping jj detection test")
	}
	ClearCache()
	dir := t.TempDir()

	// Create a jj repository (jj init creates both .jj and .git)
	cmd := exec.Command("jj", "init", "--git", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jj init failed: %v\n%s", err, out)
	}

	b := Detect(dir)
	if b == nil {
		t.Fatal("expected jj backend, got nil")
	}
	if b.Type() != Jujutsu {
		t.Fatalf("expected Jujutsu type, got %s", b.Type())
	}
}

func TestDetect_JJTakesPrecedence(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not installed, skipping jj precedence test")
	}
	ClearCache()
	dir := t.TempDir()

	// jj init creates both .jj and .git — jj should win
	cmd := exec.Command("jj", "init", "--git", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jj init failed: %v\n%s", err, out)
	}

	b := Detect(dir)
	if b == nil {
		t.Fatal("expected jj backend, got nil")
	}
	if b.Type() != Jujutsu {
		t.Fatalf("expected Jujutsu when both .jj and .git present, got %s", b.Type())
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

	initGitRepo(t, dir)
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

	initGitRepo(t, dir)

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

	initGitRepo(t, dir)
	ClearCache()

	if Detect(dir) == nil {
		t.Fatal("expected non-nil for git repo")
	}
}
