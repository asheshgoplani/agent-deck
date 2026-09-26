package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2366: `[worktree] checkout_git_config` reaches the git commands that
// materialize a new worktree as `git -c`, so "core.hooksPath=/dev/null" skips
// the repository's post-checkout hook on both the plain and the sparse path,
// without persisting anything in the new worktree's config.

// installPostCheckoutHook makes repoDir's post-checkout hook append a line to
// a marker file and returns a func reporting how many times it ran.
func installPostCheckoutHook(t *testing.T, repoDir string) func() int {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "post-checkout.log")
	hooksDir := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "--path-format=absolute", "--git-path", "hooks"))
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho ran >> '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "post-checkout"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() int {
		data, err := os.ReadFile(marker)
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "ran")
	}
}

var skipHooks = []string{"core.hooksPath=/dev/null"}

func TestCreateWorktreeWithOptions_CheckoutGitConfigSkipsHooks(t *testing.T) {
	repo := t.TempDir()
	createTestRepo(t, repo)
	hookRuns := installPostCheckoutHook(t, repo)

	withHooks := filepath.Join(t.TempDir(), "with-hooks")
	if err := CreateWorktreeWithOptions(repo, withHooks, "with-hooks", WorktreeCreateOptions{}); err != nil {
		t.Fatalf("create without config: %v", err)
	}
	if hookRuns() == 0 {
		t.Fatal("fixture: post-checkout hook should run without checkout_git_config")
	}

	before := hookRuns()
	noHooks := filepath.Join(t.TempDir(), "no-hooks")
	if err := CreateWorktreeWithOptions(repo, noHooks, "no-hooks", WorktreeCreateOptions{GitConfig: skipHooks}); err != nil {
		t.Fatalf("create with config: %v", err)
	}
	if got := hookRuns(); got != before {
		t.Fatalf("post-checkout hook ran %d more time(s) with core.hooksPath=/dev/null", got-before)
	}
	if out, err := exec.Command("git", "-C", noHooks, "config", "--get", "core.hooksPath").Output(); err == nil {
		t.Fatalf("core.hooksPath persisted in the new worktree: %q", out)
	}
}

func TestCreateWorktreeWithOptions_CheckoutGitConfigReachesSparseCheckout(t *testing.T) {
	base, sparseWT := newSparseFixture(t)
	hookRuns := installPostCheckoutHook(t, base)

	wt := filepath.Join(t.TempDir(), "sparse-no-hooks")
	opts := WorktreeCreateOptions{SparseSourceDir: sparseWT, GitConfig: skipHooks}
	if err := CreateWorktreeWithOptions(base, wt, "sparse-no-hooks", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	assertSparseWorktree(t, wt, true, []string{"keep"})
	if got := hookRuns(); got != 0 {
		t.Fatalf("post-checkout hook ran %d time(s) during sparse materialization", got)
	}
}

func TestCreateWorktreeWithOptions_RejectsInvalidCheckoutGitConfig(t *testing.T) {
	repo := t.TempDir()
	createTestRepo(t, repo)

	for _, entry := range []string{"core.hooksPath", "=value", "--upload-pack=x", ""} {
		wt := filepath.Join(t.TempDir(), "wt")
		err := CreateWorktreeWithOptions(repo, wt, "invalid-config", WorktreeCreateOptions{GitConfig: []string{entry}})
		if err == nil || !strings.Contains(err.Error(), "checkout_git_config") {
			t.Fatalf("entry %q: err = %v, want a checkout_git_config error", entry, err)
		}
		if dirExists(wt) {
			t.Fatalf("entry %q: worktree was created despite the invalid entry", entry)
		}
	}
}
