package ui

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// resolveWorktreeTarget resolves the worktree path for a new or forked session
// whose worktree checkbox is enabled.
//
// It implements the #1185 fallback: when the worktree was enabled by config
// default (explicit == false) and the target path is NOT a git repository, it
// returns fallback == true so the caller creates a normal (non-worktree)
// session instead of erroring. When the worktree was EXPLICITLY requested
// (explicit == true) on a non-repo path, it returns a non-empty errMsg so the
// caller fails loudly, preserving explicit intent.
//
// On a git repo (or bare-repo project root) it computes and returns the
// worktree path and repo root unchanged from the previous behaviour.
func resolveWorktreeTarget(path, branch string, explicit bool) (worktreePath, repoRoot string, fallback bool, errMsg string) {
	// IsGitRepoOrBareProjectRoot accepts a directory that contains a nested
	// .bare/ even though the directory itself has no .git (#742 / #715).
	if !git.IsGitRepoOrBareProjectRoot(path) {
		if explicit {
			return "", "", false, "Path is not a git repository"
		}
		// #1185: worktree was on by config default, not explicit user intent —
		// fall back to a normal session on non-repo dirs instead of erroring.
		return "", "", true, ""
	}

	root, err := git.GetWorktreeBaseRoot(path)
	if err != nil {
		return "", "", false, fmt.Sprintf("Failed to get repo root: %v", err)
	}

	wtSettings := session.GetWorktreeSettings()
	worktreePath = git.WorktreePath(git.WorktreePathOptions{
		Branch:    branch,
		Location:  wtSettings.DefaultLocation,
		RepoDir:   root,
		SessionID: git.GeneratePathID(),
		Template:  wtSettings.Template(),
	})
	return worktreePath, root, false, ""
}
