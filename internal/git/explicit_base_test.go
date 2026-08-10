package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateWorktreeAtStartPointWithSetup_UsesExplicitBase(t *testing.T) {
	repo := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "marker"), []byte("main"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "marker")
	run(repo, "commit", "-m", "main")
	run(repo, "switch", "-c", "dev")
	if err := os.WriteFile(filepath.Join(repo, "marker"), []byte("dev"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "commit", "-am", "dev")
	devCommit, err := ResolveCommit(repo, "dev")
	if err != nil {
		t.Fatal(err)
	}
	run(repo, "switch", "main")

	wt := filepath.Join(t.TempDir(), "worktree")
	var stdout, stderr bytes.Buffer
	if _, err := CreateWorktreeAtStartPointWithSetup(repo, wt, "feature/from-dev", devCommit, WorktreeCreateOptions{}, &stdout, &stderr, 0); err != nil {
		t.Fatal(err)
	}
	if got := run(wt, "rev-parse", "HEAD"); got != devCommit {
		t.Fatalf("worktree HEAD = %s, want explicit dev base %s", got, devCommit)
	}
}
