package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MergeBack merges sourceBranch into targetBranch, handling both regular and
// bare-repository layouts.
//
// In a regular layout, projectRoot is a working tree: this is the historical
// path — `git checkout targetBranch` then `git merge sourceBranch`.
//
// In a bare-repo layout (issue #715/#891), projectRoot is the parent of a
// nested bare repo (typically `.bare/`). The bare dir has no working tree,
// so `checkout`/`merge` cannot run there directly. Instead:
//
//   - If targetBranch is an ancestor of sourceBranch (fast-forward case),
//     advance targetBranch via `update-ref` on the bare dir.
//   - Otherwise, create a throwaway worktree of targetBranch, perform the
//     merge there, then remove the worktree.
//
// Regression test: TestWorktree_MergeBack_BareRepo_RegressionFor891.
func MergeBack(projectRoot, sourceBranch, targetBranch string) error {
	if IsGitRepo(projectRoot) && !IsBareRepo(projectRoot) {
		return mergeBackInWorktree(projectRoot, sourceBranch, targetBranch)
	}

	// projectRoot is the bare repo itself (bare-at-root, or a caller
	// that handed us the .bare/ dir directly): use it as-is, without scanning
	// children. The child-scan path is only correct when projectRoot is a
	// non-git dir that contains a nested bare repo (the .bare/ layout).
	if IsBareRepo(projectRoot) {
		return mergeBackInBareRepo(projectRoot, sourceBranch, targetBranch)
	}

	bareDir := findNestedBareRepo(projectRoot)
	if bareDir == "" {
		return fmt.Errorf("not a git repository or bare-repo project root: %s", projectRoot)
	}

	return mergeBackInBareRepo(bareDir, sourceBranch, targetBranch)
}

func mergeBackInWorktree(repoDir, sourceBranch, targetBranch string) error {
	co := exec.Command("git", "-C", repoDir, "checkout", targetBranch)
	if out, err := co.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout %s: %s: %w", targetBranch, strings.TrimSpace(string(out)), err)
	}
	return MergeBranch(repoDir, sourceBranch)
}

func mergeBackInBareRepo(bareDir, sourceBranch, targetBranch string) error {
	sourceSHA, err := revParseInDir(bareDir, sourceBranch)
	if err != nil {
		return fmt.Errorf("resolve source branch %s: %w", sourceBranch, err)
	}

	ffPossible := exec.Command("git", "-C", bareDir, "merge-base", "--is-ancestor", targetBranch, sourceBranch).Run() == nil
	if ffPossible {
		out, err := exec.Command("git", "-C", bareDir, "update-ref",
			"refs/heads/"+targetBranch, sourceSHA).CombinedOutput()
		if err != nil {
			return fmt.Errorf("update-ref %s -> %s: %s: %w", targetBranch, sourceSHA, strings.TrimSpace(string(out)), err)
		}
		return nil
	}

	tmpWT, err := os.MkdirTemp("", "agent-deck-mergeback-")
	if err != nil {
		return fmt.Errorf("create temp worktree dir: %w", err)
	}
	tmpWT = filepath.Join(tmpWT, "wt")
	defer func() {
		_, _ = exec.Command("git", "-C", bareDir, "worktree", "remove", "--force", tmpWT).CombinedOutput()
		_, _ = exec.Command("git", "-C", bareDir, "worktree", "prune").CombinedOutput()
		_ = os.RemoveAll(filepath.Dir(tmpWT))
	}()

	if out, err := exec.Command("git", "-C", bareDir, "worktree", "add", tmpWT, targetBranch).CombinedOutput(); err != nil {
		return fmt.Errorf("worktree add %s: %s: %w", targetBranch, strings.TrimSpace(string(out)), err)
	}
	return MergeBranch(tmpWT, sourceBranch)
}

func revParseInDir(dir, ref string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
