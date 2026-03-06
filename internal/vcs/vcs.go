// Package vcs provides a VCS-agnostic interface for worktree/workspace operations.
// It supports git worktrees and jj (Jujutsu) workspaces behind a unified Backend interface.
package vcs

// VCSType identifies which version control system is in use.
type VCSType string

const (
	Git     VCSType = "git"
	Jujutsu VCSType = "jujutsu"
)

// Worktree represents a git worktree or jj workspace.
type Worktree struct {
	Path   string // Filesystem path to the worktree
	Branch string // Branch name (git) or bookmark (jj)
	Commit string // HEAD commit SHA or jj change ID
	Bare   bool   // Whether this is the bare repository (always false for jj)
}

// Backend defines the VCS operations needed for worktree management.
type Backend interface {
	Type() VCSType
	GetRepoRoot(dir string) (string, error)
	GetCurrentBranch(dir string) (string, error)
	BranchExists(repoDir, branchName string) bool
	GetWorktreeBaseRoot(dir string) (string, error)
	IsWorktree(dir string) bool
	GetMainWorktreePath(dir string) (string, error)
	CreateWorktree(repoDir, worktreePath, branchName string) error
	ListWorktrees(repoDir string) ([]Worktree, error)
	RemoveWorktree(repoDir, worktreePath string, force bool) error
	PruneWorktrees(repoDir string) error
	HasUncommittedChanges(dir string) (bool, error)
	GetDefaultBranch(repoDir string) (string, error)
	MergeBranch(repoDir, branchName string) error
	DeleteBranch(repoDir, branchName string, force bool) error
	CheckoutBranch(repoDir, branchName string) error
	AbortMerge(repoDir string) error
}
