// Package vcsbackend exposes a single Detect entry point that returns the
// correct vcs.Backend (git or jujutsu) for a directory. It lives in its
// own package so cmd/agent-deck and internal/ui can share backend
// selection without re-declaring the gitBackend adapter (issue #1126).
package vcsbackend

import (
	"fmt"
	"io"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/jujutsu"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

// gitBackend is a thin adapter that satisfies vcs.Backend by wrapping the
// internal/git free functions. Kept identical to the long-standing copy in
// cmd/agent-deck/vcs_helper.go so behavior is unchanged.
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

// Detect returns the Backend for dir, preferring jujutsu when both jj and
// git are present (matching the cmd/agent-deck ordering established in PR
// #754).
func Detect(dir string) (vcs.Backend, error) {
	if b, err := jujutsu.NewJJBackend(dir); err == nil {
		return b, nil
	}
	if b, err := newGitBackend(dir); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("not a git or jujutsu repository: %s", dir)
}

// CreateWorktreeWithSetup creates a worktree via the backend. For git
// backends it also runs the per-repo worktree-setup script. For non-git
// backends (jujutsu) the worktree is created without running a setup
// script. Same semantics as cmd/agent-deck's createWorktreeWithSetup
// helper.
func CreateWorktreeWithSetup(backend vcs.Backend, worktreePath, branchName string, stdout, stderr io.Writer, setupTimeout time.Duration) (setupErr error, err error) {
	if backend.Type() == vcs.TypeGit {
		return git.CreateWorktreeWithSetup(backend.RepoDir(), worktreePath, branchName, stdout, stderr, setupTimeout)
	}
	return nil, backend.CreateWorktree(worktreePath, branchName)
}
