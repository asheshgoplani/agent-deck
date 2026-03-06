package vcs

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// GitBackend implements Backend for git repositories.
type GitBackend struct{}

func (b *GitBackend) Type() VCSType { return Git }

func (b *GitBackend) GetRepoRoot(dir string) (string, error) { return git.GetRepoRoot(dir) }

func (b *GitBackend) GetCurrentBranch(dir string) (string, error) { return git.GetCurrentBranch(dir) }

func (b *GitBackend) BranchExists(repoDir, branchName string) bool {
	return git.BranchExists(repoDir, branchName)
}

func (b *GitBackend) GetWorktreeBaseRoot(dir string) (string, error) {
	return git.GetWorktreeBaseRoot(dir)
}

func (b *GitBackend) IsWorktree(dir string) bool { return git.IsWorktree(dir) }

func (b *GitBackend) GetMainWorktreePath(dir string) (string, error) {
	return git.GetMainWorktreePath(dir)
}

func (b *GitBackend) CreateWorktree(repoDir, worktreePath, branchName string) error {
	return git.CreateWorktree(repoDir, worktreePath, branchName)
}

func (b *GitBackend) ListWorktrees(repoDir string) ([]Worktree, error) {
	gits, err := git.ListWorktrees(repoDir)
	if err != nil {
		return nil, err
	}
	result := make([]Worktree, len(gits))
	for i, wt := range gits {
		result[i] = Worktree{
			Path:   wt.Path,
			Branch: wt.Branch,
			Commit: wt.Commit,
			Bare:   wt.Bare,
		}
	}
	return result, nil
}

func (b *GitBackend) RemoveWorktree(repoDir, worktreePath string, force bool) error {
	return git.RemoveWorktree(repoDir, worktreePath, force)
}

func (b *GitBackend) PruneWorktrees(repoDir string) error { return git.PruneWorktrees(repoDir) }

func (b *GitBackend) HasUncommittedChanges(dir string) (bool, error) {
	return git.HasUncommittedChanges(dir)
}

func (b *GitBackend) GetDefaultBranch(repoDir string) (string, error) {
	return git.GetDefaultBranch(repoDir)
}

func (b *GitBackend) MergeBranch(repoDir, branchName string) error {
	return git.MergeBranch(repoDir, branchName)
}

func (b *GitBackend) DeleteBranch(repoDir, branchName string, force bool) error {
	return git.DeleteBranch(repoDir, branchName, force)
}

// CheckoutBranch checks out the given branch in the repository.
func (b *GitBackend) CheckoutBranch(repoDir, branchName string) error {
	cmd := exec.Command("git", "-C", repoDir, "checkout", branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to checkout %s: %s: %w", branchName, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// AbortMerge aborts a merge in progress.
func (b *GitBackend) AbortMerge(repoDir string) error {
	cmd := exec.Command("git", "-C", repoDir, "merge", "--abort")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to abort merge: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}
