package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

type MultiRepoWorktreeResult struct {
	MappedPaths []string
	Worktrees   []MultiRepoWorktree
	Warnings    []string
}

func CreateMultiRepoWorktrees(allPaths []string, parentDir string, branch string, setupTimeout time.Duration) MultiRepoWorktreeResult {
	return CreateMultiRepoWorktreesWithOptions(allPaths, parentDir, branch, setupTimeout, false)
}

// CreateMultiRepoWorktreesWithOptions is CreateMultiRepoWorktrees plus the
// #1708 sparse-checkout inheritance switch (`[worktree] sparse_checkout`,
// resolved by the caller). Each repo inherits from its OWN input path — the
// directory the user selected — because that, and not the base root this
// function derives from it, is the worktree carrying the sparse configuration.
func CreateMultiRepoWorktreesWithOptions(allPaths []string, parentDir string, branch string, setupTimeout time.Duration, inheritSparse bool) MultiRepoWorktreeResult {
	result, _ := createMultiRepoWorktrees(allPaths, parentDir, branch, setupTimeout, inheritSparse, false)
	return result
}

// CreateMultiRepoWorktreesStrictWithOptions refuses to replace a requested Git
// worktree with an original-repository symlink. On error, the result identifies
// worktrees already created so the owner can roll them back.
func CreateMultiRepoWorktreesStrictWithOptions(allPaths []string, parentDir, branch string, setupTimeout time.Duration, inheritSparse bool) (MultiRepoWorktreeResult, error) {
	return createMultiRepoWorktrees(allPaths, parentDir, branch, setupTimeout, inheritSparse, true)
}

func createMultiRepoWorktrees(allPaths []string, parentDir, branch string, setupTimeout time.Duration, inheritSparse, strict bool) (MultiRepoWorktreeResult, error) {
	var result MultiRepoWorktreeResult
	dirnames := DeduplicateDirnames(allPaths)

	for i, p := range allPaths {
		wtPath := filepath.Join(parentDir, dirnames[i])

		if git.IsGitRepoOrBareProjectRoot(p) {
			repoRoot, rootErr := git.GetWorktreeBaseRoot(p)
			if rootErr != nil {
				if strict {
					return result, fmt.Errorf("worktree root %s: %w", p, rootErr)
				}
				result.Warnings = append(result.Warnings, "worktree_skip: "+p+": "+rootErr.Error())
				_ = os.Symlink(p, wtPath)
				result.MappedPaths = append(result.MappedPaths, wtPath)
				continue
			}

			var buf bytes.Buffer
			setupErr, err := git.CreateWorktreeWithSetupOptions(
				repoRoot, wtPath, branch,
				git.WorktreeStateOptions{},
				git.SparseInheritOptions(inheritSparse, p),
				&buf, &buf, setupTimeout,
			)
			if err != nil {
				if strict {
					// A lower-level failure can leave a registered checkout. Record
					// this exclusively owned attempted path for verified cleanup too.
					result.Worktrees = append(result.Worktrees, MultiRepoWorktree{OriginalPath: p, WorktreePath: wtPath, RepoRoot: repoRoot, Branch: branch})
					return result, fmt.Errorf("create worktree %s: %w", p, err)
				}
				result.Warnings = append(result.Warnings, "worktree_create_fail: "+p+": "+err.Error())
				_ = os.Symlink(p, wtPath)
				result.MappedPaths = append(result.MappedPaths, wtPath)
				continue
			}
			if setupErr != nil {
				result.Warnings = append(result.Warnings, "worktree_setup_fail: "+p+": "+setupErr.Error())
			}

			result.Worktrees = append(result.Worktrees, MultiRepoWorktree{
				OriginalPath: p,
				WorktreePath: wtPath,
				RepoRoot:     repoRoot,
				Branch:       branch,
			})
			result.MappedPaths = append(result.MappedPaths, wtPath)
			if strict && setupErr != nil {
				return result, fmt.Errorf("setup worktree %s: %w", p, setupErr)
			}
		} else {
			if err := os.Symlink(p, wtPath); err != nil && strict {
				return result, fmt.Errorf("link project %s: %w", p, err)
			}
			result.MappedPaths = append(result.MappedPaths, wtPath)
		}
	}

	return result, nil
}
