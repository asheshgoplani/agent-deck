package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	path, err := GetWorktreeForBranch(repoRoot, branch)
	if err != nil {
		return fmt.Errorf("checking existing worktrees: %w", err)
	}
	if path != "" {
		return &DestinationCollisionError{Kind: CollisionWorktreeExists, Branch: branch, Path: path}
	}
	if BranchExists(repoRoot, branch) {
		return &DestinationCollisionError{Kind: CollisionBranchExists, Branch: branch}
	}
	return nil
}

// HasSubmodules returns true if repoDir contains a .gitmodules file (regular
// file, not directory). Used by fork-with-state callers to emit a warning
// that submodules will be copied as files, not recursed into. Submodule
// detection is intentionally minimal — just checks for the canonical
// .gitmodules file at the repo root. No parsing.
func HasSubmodules(repoDir string) bool {
	info, err := os.Stat(filepath.Join(repoDir, ".gitmodules"))
	return err == nil && !info.IsDir()
}

// DetectInProgressOperation returns the in-progress operation kind ("rebase",
// "merge", "cherry-pick", "revert", "bisect") if the parent's git directory
// shows an unsafe in-progress operation, or empty string if clean. Returns an
// error only if the git directory cannot be resolved.
//
// This duplicates the check table used internally by materialize_wip.go's
// refuseUnsafeParentState. We do our own check so fork-with-state callers can
// surface actionable error messages BEFORE creating a worktree, instead of
// letting MaterializeWipFromParent return upstream's terser wording AFTER the
// worktree exists. Upstream's check remains as a backstop.
//
// Keep this check table aligned with refuseUnsafeParentState in
// internal/git/materialize_wip.go. If upstream adds a new operation kind,
// add it here too.
func DetectInProgressOperation(repoDir string) (string, error) {
	gitDir, err := resolveGitDirForDetection(repoDir)
	if err != nil {
		return "", err
	}
	checks := []struct {
		path string
		kind string
	}{
		{filepath.Join(gitDir, "rebase-merge"), "rebase"},
		{filepath.Join(gitDir, "rebase-apply"), "rebase"},
		{filepath.Join(gitDir, "MERGE_HEAD"), "merge"},
		{filepath.Join(gitDir, "CHERRY_PICK_HEAD"), "cherry-pick"},
		{filepath.Join(gitDir, "REVERT_HEAD"), "revert"},
		{filepath.Join(gitDir, "BISECT_LOG"), "bisect"},
	}
	for _, c := range checks {
		if _, err := os.Stat(c.path); err == nil {
			return c.kind, nil
		}
	}
	return "", nil
}

// resolveGitDirForDetection resolves repoDir to its .git directory via
// `git rev-parse --git-dir`. Works for normal repos, linked worktrees, and
// bare-repo project roots. Named distinctly from materialize_wip.go's
// internal gitDirOf to avoid same-package naming collision.
func resolveGitDirForDetection(repoDir string) (string, error) {
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse --git-dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoDir, gitDir)
	}
	return gitDir, nil
}
