// Package vcs defines a version control system abstraction layer.
package vcs

// Worktree represents a VCS worktree.
type Worktree struct {
	Path   string // Filesystem path to the worktree
	Branch string // Branch name checked out in this worktree
	Commit string // HEAD commit SHA
	Bare   bool   // Whether this is the bare repository
}

// WorktreePathOptions configures worktree path generation for a Backend.
// Unlike git.WorktreePathOptions, it omits RepoDir because the backend supplies it.
type WorktreePathOptions struct {
	Branch    string
	Location  string
	SessionID string
	Template  string
}

type Type string

const (
	TypeGit Type = "git"
)

// Backend abstracts version control operations scoped to a repository.
type Backend interface {
	Type() Type

	// RepoDir returns the root directory of the repository.
	RepoDir() string

	// Branch operations
	BranchExists(branchName string) bool
	GetCurrentBranch() (string, error)
	GetDefaultBranch() (string, error)
	DeleteBranch(branchName string, force bool) error
	MergeBranch(branchName string) error

	// Worktree operations
	WorktreePath(opts WorktreePathOptions) string
	CreateWorktree(worktreePath, branchName string) error
	ListWorktrees() ([]Worktree, error)
	RemoveWorktree(worktreePath string, force bool) error
	GetWorktreeForBranch(branchName string) (string, error)
	PruneWorktrees() error
}
