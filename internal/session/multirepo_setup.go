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

// AddPathToMultiRepo adds a new repository to an existing multi-repo session.
// Creates a symlink (or git worktree if the session was created with worktrees)
// in the session's MultiRepoTempDir and appends the new path to AdditionalPaths.
// Returns the path of the created symlink/worktree inside the parent dir.
func AddPathToMultiRepo(inst *Instance, newPath string) (string, error) {
	// Auto-initialize multi-repo if not already enabled.
	// Use the parent of the current ProjectPath as the workspace root —
	// useful when worktrees were already set up manually on disk.
	if !inst.MultiRepoEnabled || inst.MultiRepoTempDir == "" {
		parentDir := filepath.Dir(inst.ProjectPath)
		if parentDir == "" || parentDir == "." {
			return "", fmt.Errorf("session is not a multi-repo session; create it with --add-path")
		}
		inst.MultiRepoEnabled = true
		inst.MultiRepoTempDir = parentDir
	}

	abs, err := filepath.Abs(ExpandPath(newPath))
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("path does not exist: %s", abs)
	}

	// If session was created with worktrees, create a worktree for the new path too
	var worktreeBranch string
	if len(inst.MultiRepoWorktrees) > 0 {
		worktreeBranch = inst.MultiRepoWorktrees[0].Branch
	}

	// Find a unique name inside the parent dir
	candidate := filepath.Base(abs)
	targetPath := filepath.Join(inst.MultiRepoTempDir, candidate)
	for i := 1; ; i++ {
		if _, err := os.Stat(targetPath); os.IsNotExist(err) {
			break
		}
		targetPath = filepath.Join(inst.MultiRepoTempDir, fmt.Sprintf("%s-%d", candidate, i))
	}

	if worktreeBranch != "" && git.IsGitRepo(abs) {
		repoRoot, err := git.GetWorktreeBaseRoot(abs)
		if err == nil {
			if err := git.CreateWorktree(repoRoot, targetPath, worktreeBranch); err == nil {
				inst.MultiRepoWorktrees = append(inst.MultiRepoWorktrees, MultiRepoWorktree{
					OriginalPath: abs,
					WorktreePath: targetPath,
					RepoRoot:     repoRoot,
					Branch:       worktreeBranch,
				})
				inst.AdditionalPaths = append(inst.AdditionalPaths, targetPath)
				return targetPath, nil
			}
			slog.Warn("multirepo_add_worktree_fail",
				slog.String("path", abs),
				slog.String("error", err.Error()),
				slog.String("fallback", "symlink"))
		}
	}

	// Fallback: symlink. If the path is already directly inside the parent dir, use it as-is.
	if filepath.Dir(abs) == inst.MultiRepoTempDir {
		inst.AdditionalPaths = append(inst.AdditionalPaths, abs)
		return abs, nil
	}
	if err := os.Symlink(abs, targetPath); err != nil {
		return "", fmt.Errorf("failed to create symlink in %s: %w", inst.MultiRepoTempDir, err)
	}
	inst.AdditionalPaths = append(inst.AdditionalPaths, targetPath)
	return targetPath, nil
}
