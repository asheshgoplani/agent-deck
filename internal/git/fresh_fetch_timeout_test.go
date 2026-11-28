package git

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFreshOriginDefaultBranchRef_HungFetchTimesOut pins the bound on the
// default-branch fetch. A remote transport that never answers (here an ssh
// command that only sleeps, standing in for a connection orphaned by laptop
// sleep) must fail the fetch after defaultBranchFetchTimeout rather than block
// worktree creation indefinitely.
func TestFreshOriginDefaultBranchRef_HungFetchTimesOut(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "local")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@test.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("c1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "c1")

	hang := filepath.Join(tmp, "hang-ssh")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatalf("write ssh stub: %v", err)
	}
	runGit(t, repo, "remote", "add", "origin", "ssh://git@unreachable.invalid/repo.git")
	runGit(t, repo, "config", "core.sshCommand", hang)

	prev := defaultBranchFetchTimeout
	defaultBranchFetchTimeout = 500 * time.Millisecond
	t.Cleanup(func() { defaultBranchFetchTimeout = prev })

	start := time.Now()
	ref, ok := freshOriginDefaultBranchRef(repo)
	elapsed := time.Since(start)
	if ok {
		t.Fatalf("hung fetch reported success with ref %q", ref)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("hung fetch blocked for %s; the timeout did not bound it", elapsed)
	}
}
