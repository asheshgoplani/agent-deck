//go:build capability_e2e

package capability

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// newGitRepo creates a real throwaway git repository with one commit inside the
// sandbox HOME and returns its path. Worktree creation requires a repo with at
// least one commit and a branch to fork from. Uses CleanGitEnv so the
// subprocess git never inherits the outer repo's GIT_DIR routing.
func newGitRepo(t *testing.T, c *capSandbox) string {
	t.Helper()
	repo := filepath.Join(c.Home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	env := append(testutil.CleanGitEnv(os.Environ()),
		"GIT_AUTHOR_NAME=Cap", "GIT_AUTHOR_EMAIL=cap@test",
		"GIT_COMMITTER_NAME=Cap", "GIT_COMMITTER_EMAIL=cap@test",
		"HOME="+c.Home,
	)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

// worktreeInfo is the subset of `worktree info --json` we assert on. It reads
// the stored worktree fields (no cwd dependency), so it works from any dir.
type worktreeInfo struct {
	WorktreePath   string `json:"worktree_path"`
	MainRepo       string `json:"main_repo"`
	WorktreeExists bool   `json:"worktree_exists"`
	Branch         string `json:"branch"`
}

func (c *capSandbox) worktreeInfo(t *testing.T, ref string) worktreeInfo {
	t.Helper()
	out := c.run(t, "worktree", "info", ref, "--json")
	var wi worktreeInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &wi); err != nil {
		t.Fatalf("parse worktree info --json: %v\nraw: %s", err, out)
	}
	return wi
}

// TestCapability_Worktree_CreateFinish proves the full worktree lifecycle
// through the binary: `add --worktree -b` creates a real git worktree on disk
// for a new branch, and `worktree finish` removes the worktree + branch and
// drops the session, WITHOUT touching the original repository (the #1185/#1200
// data-loss guard).
//
// Surfaces: CLI (add --worktree, worktree info/finish) + Persistence (registry
// WorktreePath/WorktreeRepoRoot) + Cross-platform (git worktree path handling).
func TestCapability_Worktree_CreateFinish(t *testing.T) {
	c := newCapSandbox(t)
	repo := newGitRepo(t, c)

	// Create the worktree session for a brand-new branch off the repo.
	c.run(t, "add", "-c", "bash", "-t", "cap-wt", "-w", "capfeature", "-b", repo)

	row, ok := c.findByTitle(t, "cap-wt")
	if !ok {
		t.Fatalf("worktree add did not create a registry row.\nrows: %+v", c.list(t))
	}

	wi := c.worktreeInfo(t, "cap-wt")
	if !wi.WorktreeExists {
		t.Fatalf("worktree info reports the worktree missing right after creation: %+v", wi)
	}
	if _, err := os.Stat(wi.WorktreePath); err != nil {
		t.Fatalf("worktree dir should exist on disk at %s: %v", wi.WorktreePath, err)
	}
	if wi.WorktreePath == repo {
		t.Fatalf("worktree path must be distinct from the main repo, both are %s", repo)
	}
	if !strings.Contains(wi.Branch, "capfeature") {
		t.Errorf("worktree branch = %q, want it to contain capfeature", wi.Branch)
	}
	// The session's working dir is the worktree, not the original repo.
	if row.Path != wi.WorktreePath {
		t.Errorf("session path = %q, want the worktree path %q", row.Path, wi.WorktreePath)
	}

	// Capture the created-state snapshot before finish removes the session.
	created := c.run(t, "worktree", "info", "cap-wt")

	// Finish: no merge (PR-style), force past confirm/uncommitted checks.
	c.run(t, "worktree", "finish", "cap-wt", "--no-merge", "--force", "--json")

	// The worktree dir is gone, the session row is gone, but the ORIGINAL repo
	// is untouched (the #1200 data-loss guard: dismissing a worktree must never
	// delete the source repository).
	if _, err := os.Stat(wi.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree dir should be removed after finish, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("the original repo must survive finish (#1200 guard), but %s/.git is gone: %v", repo, err)
	}
	if _, ok := c.findByTitle(t, "cap-wt"); ok {
		t.Fatalf("after worktree finish, the session row should be absent from the registry")
	}

	// Display proof: the worktree info a human sees after create (a live
	// worktree on its own branch), then the registry after finish removed it.
	snapshot(t, "worktree", "$ agent-deck worktree info cap-wt   (after add --worktree -b)\n"+
		strings.TrimRight(created, "\n")+
		"\n\n$ agent-deck list   (after worktree finish: session gone, repo intact)\n"+
		c.run(t, "list"))
}
