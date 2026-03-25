package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// SetupMultiRepo configures inst for a multi-repo session by creating a parent
// directory with either git worktrees (when worktreeBranch is set) or symlinks
// (otherwise) for each repo path.
//
// baseDir is the parent directory under which session dirs are created.
// If empty, defaults to ~/.agent-deck/multi-repo-worktrees.
//
// The function mutates inst: sets MultiRepoEnabled, MultiRepoTempDir,
// MultiRepoWorktrees, ProjectPath, AdditionalPaths, and the tmux WorkDir.
func SetupMultiRepo(inst *Instance, additionalPaths []string, worktreeBranch, baseDir string) error {
	if len(additionalPaths) == 0 {
		return nil
	}

	inst.MultiRepoEnabled = true
	inst.AdditionalPaths = additionalPaths
	allPaths := inst.AllProjectPaths()

	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		baseDir = filepath.Join(home, ".agent-deck", "multi-repo-worktrees")
	}

	var parentDir string
	if worktreeBranch != "" {
		sanitized := strings.ReplaceAll(worktreeBranch, "/", "-")
		sanitized = strings.ReplaceAll(sanitized, " ", "-")
		parentDir = filepath.Join(baseDir, fmt.Sprintf("%s-%s", sanitized, inst.ID[:8]))
	} else {
		parentDir = filepath.Join(baseDir, inst.ID[:8])
	}

	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return fmt.Errorf("failed to create multi-repo dir: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(parentDir); err == nil {
		parentDir = resolved
	}
	inst.MultiRepoTempDir = parentDir

	dirnames := DeduplicateDirnames(allPaths)
	var newProjectPath string
	var newAdditionalPaths []string

	for i, p := range allPaths {
		targetPath := filepath.Join(parentDir, dirnames[i])

		if worktreeBranch != "" {
			if git.IsGitRepo(p) {
				repoRoot, err := git.GetWorktreeBaseRoot(p)
				if err != nil {
					slog.Warn("multirepo_worktree_skip",
						slog.String("path", p),
						slog.String("error", err.Error()))
					_ = os.Symlink(p, targetPath)
				} else if err := git.CreateWorktree(repoRoot, targetPath, worktreeBranch); err != nil {
					slog.Warn("multirepo_worktree_create_fail",
						slog.String("path", p),
						slog.String("error", err.Error()))
					_ = os.Symlink(p, targetPath)
				} else {
					inst.MultiRepoWorktrees = append(inst.MultiRepoWorktrees, MultiRepoWorktree{
						OriginalPath: p,
						WorktreePath: targetPath,
						RepoRoot:     repoRoot,
						Branch:       worktreeBranch,
					})
				}
			} else {
				_ = os.Symlink(p, targetPath)
			}
		} else {
			_ = os.Symlink(p, targetPath)
		}

		if i == 0 {
			newProjectPath = targetPath
		} else {
			newAdditionalPaths = append(newAdditionalPaths, targetPath)
		}
	}

	inst.ProjectPath = newProjectPath
	inst.AdditionalPaths = newAdditionalPaths

	if inst.GetTmuxSession() != nil {
		inst.GetTmuxSession().WorkDir = inst.MultiRepoTempDir
	}

	return nil
}
