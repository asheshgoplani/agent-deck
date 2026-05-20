package main

import (
	"fmt"
	"io"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/jujutsu"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

// gitBackend is a thin adapter that satisfies vcs.Backend by wrapping
// internal/git's existing free functions. It lives in cmd/agent-deck so
// the internal/git package keeps its current free-function API unchanged
// (see PR #754 takeover Option B).
type gitBackend struct {
	repoDir string
}

var _ vcs.Backend = (*gitBackend)(nil)

func newGitBackend(dir string) (*gitBackend, error) {
	if !git.IsGitRepoOrBareProjectRoot(dir) {
		return nil, fmt.Errorf("not a git repository: %s", dir)
	}
	root, err := git.GetWorktreeBaseRoot(dir)
	if err != nil {
		return nil, err
	}
	return &gitBackend{repoDir: root}, nil
}

func (g *gitBackend) Type() vcs.Type  { return vcs.TypeGit }
func (g *gitBackend) RepoDir() string { return g.repoDir }

func (g *gitBackend) WorktreePath(opts vcs.WorktreePathOptions) string {
	return git.WorktreePath(git.WorktreePathOptions{
		Branch:    opts.Branch,
		Location:  opts.Location,
		RepoDir:   g.repoDir,
		SessionID: opts.SessionID,
		Template:  opts.Template,
	})
}

func (g *gitBackend) BranchExists(name string) bool {
	return git.BranchExists(g.repoDir, name)
}

func (g *gitBackend) GetCurrentBranch() (string, error) {
	return git.GetCurrentBranch(g.repoDir)
}

func (g *gitBackend) GetDefaultBranch() (string, error) {
	return git.GetDefaultBranch(g.repoDir)
}

func (g *gitBackend) DeleteBranch(name string, force bool) error {
	return git.DeleteBranch(g.repoDir, name, force)
}

func (g *gitBackend) MergeBranch(name string) error {
	return git.MergeBranch(g.repoDir, name)
}

func (g *gitBackend) CreateWorktree(worktreePath, branchName string) error {
	return git.CreateWorktree(g.repoDir, worktreePath, branchName)
}

func (g *gitBackend) ListWorktrees() ([]vcs.Worktree, error) {
	wts, err := git.ListWorktrees(g.repoDir)
	if err != nil {
		return nil, err
	}
	out := make([]vcs.Worktree, len(wts))
	for i, w := range wts {
		out[i] = vcs.Worktree{Path: w.Path, Branch: w.Branch, Commit: w.Commit, Bare: w.Bare}
	}
	return out, nil
}

func (g *gitBackend) RemoveWorktree(worktreePath string, force bool) error {
	return git.RemoveWorktree(g.repoDir, worktreePath, force)
}

func (g *gitBackend) GetWorktreeForBranch(branchName string) (string, error) {
	return git.GetWorktreeForBranch(g.repoDir, branchName)
}

func (g *gitBackend) PruneWorktrees() error {
	return git.PruneWorktrees(g.repoDir)
}

// detectAndCreateBackend detects the VCS type for the given directory and
// returns the appropriate Backend. Jujutsu is preferred when both jj and git
// are present (matching jennings's original ordering in #754) so that mixed
// repos opt into jj semantics.
func detectAndCreateBackend(dir string) (vcs.Backend, error) {
	if b, err := jujutsu.NewJJBackend(dir); err == nil {
		return b, nil
	}
	if b, err := newGitBackend(dir); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("not a git or jujutsu repository: %s", dir)
}

// createWorktreeWithSetup creates a worktree via the backend. For git
// backends it also runs the per-repo worktree-setup script (git path
// retains the existing CreateWorktreeWithSetup contract). For non-git
// backends (jujutsu) the worktree is created without running a setup
// script — this is the Option B minimal-port limitation noted in the
// PR body.
func createWorktreeWithSetup(backend vcs.Backend, worktreePath, branchName string, stdout, stderr io.Writer, setupTimeout time.Duration) (setupErr error, err error) {
	if backend.Type() == vcs.TypeGit {
		return git.CreateWorktreeWithSetup(backend.RepoDir(), worktreePath, branchName, stdout, stderr, setupTimeout)
	}
	return nil, backend.CreateWorktree(worktreePath, branchName)
}
