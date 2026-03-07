package vcs

import (
	"github.com/asheshgoplani/agent-deck/internal/jujutsu"
)

// JJBackend implements Backend for Jujutsu repositories.
type JJBackend struct{}

func (b *JJBackend) Type() VCSType { return Jujutsu }

func (b *JJBackend) IsRepo(dir string) bool { return jujutsu.IsJJRepo(dir) }

func (b *JJBackend) GetRepoRoot(dir string) (string, error) { return jujutsu.GetRepoRoot(dir) }

func (b *JJBackend) GetCurrentBranch(dir string) (string, error) {
	return jujutsu.GetCurrentBranch(dir)
}

func (b *JJBackend) BranchExists(repoDir, branchName string) bool {
	return jujutsu.BranchExists(repoDir, branchName)
}

func (b *JJBackend) GetWorktreeBaseRoot(dir string) (string, error) {
	return jujutsu.GetWorktreeBaseRoot(dir)
}

func (b *JJBackend) IsWorktree(dir string) bool { return jujutsu.IsWorktree(dir) }

func (b *JJBackend) GetMainWorktreePath(dir string) (string, error) {
	return jujutsu.GetMainWorktreePath(dir)
}

func (b *JJBackend) CreateWorktree(repoDir, worktreePath, branchName string) error {
	return jujutsu.CreateWorkspace(repoDir, worktreePath, branchName)
}

func (b *JJBackend) ListWorktrees(repoDir string) ([]Worktree, error) {
	workspaces, err := jujutsu.ListWorkspaces(repoDir)
	if err != nil {
		return nil, err
	}
	var result []Worktree
	for _, ws := range workspaces {
		path, _ := jujutsu.GetWorkspacePath(repoDir, ws.Name)
		wt := Worktree{
			Path:   path,
			Branch: ws.Name, // workspace name serves as identifier
		}
		result = append(result, wt)
	}
	return result, nil
}

func (b *JJBackend) RemoveWorktree(repoDir, worktreePath string, force bool) error {
	return jujutsu.RemoveWorkspace(repoDir, worktreePath, force)
}

func (b *JJBackend) PruneWorktrees(repoDir string) error {
	return jujutsu.PruneWorkspaces(repoDir)
}

func (b *JJBackend) HasUncommittedChanges(dir string) (bool, error) {
	return jujutsu.HasUncommittedChanges(dir)
}

func (b *JJBackend) GetDefaultBranch(repoDir string) (string, error) {
	return jujutsu.GetDefaultBranch(repoDir)
}

func (b *JJBackend) MergeBranch(repoDir, branchName string) error {
	return jujutsu.MergeBranch(repoDir, branchName)
}

func (b *JJBackend) DeleteBranch(repoDir, branchName string, force bool) error {
	return jujutsu.DeleteBranch(repoDir, branchName, force)
}

func (b *JJBackend) CheckoutBranch(repoDir, branchName string) error {
	return jujutsu.CheckoutBranch(repoDir, branchName)
}

func (b *JJBackend) AbortMerge(repoDir string) error {
	return jujutsu.AbortMerge(repoDir)
}
