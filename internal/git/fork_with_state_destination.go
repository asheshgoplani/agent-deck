package git

import "fmt"

// Recognized values for DestinationCollisionError.Kind.
const (
	CollisionWorktreeExists = "worktree_exists"
	CollisionBranchExists   = "branch_exists"
)

// DestinationCollisionError is returned by ValidateForkWithStateDestination
// when the requested destination branch already has a worktree or already
// exists as a local branch. Callers own user-facing wording.
type DestinationCollisionError struct {
	Kind   string // CollisionWorktreeExists or CollisionBranchExists
	Branch string
	Path   string // populated when Kind == CollisionWorktreeExists
}

func (e *DestinationCollisionError) Error() string {
	switch e.Kind {
	case CollisionWorktreeExists:
		return fmt.Sprintf("branch %q already has a worktree at %s", e.Branch, e.Path)
	case CollisionBranchExists:
		return fmt.Sprintf("branch %q already exists", e.Branch)
	default:
		return fmt.Sprintf("destination collision for branch %q", e.Branch)
	}
}

// ValidateForkWithStateDestination is the shared CLI/TUI destination-collision
// gate for fork-with-state. Worktree-collision is checked first so the more
// specific error (with path) is surfaced when both conditions are true.
func ValidateForkWithStateDestination(repoRoot, branch string) error {
	if path, err := GetWorktreeForBranch(repoRoot, branch); err == nil && path != "" {
		return &DestinationCollisionError{Kind: CollisionWorktreeExists, Branch: branch, Path: path}
	}
	if BranchExists(repoRoot, branch) {
		return &DestinationCollisionError{Kind: CollisionBranchExists, Branch: branch}
	}
	return nil
}
