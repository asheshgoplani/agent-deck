package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
	"github.com/asheshgoplani/agent-deck/internal/vcsbackend"
)

// resolveWorktreeTarget resolves the worktree path for a new or forked session
// whose worktree checkbox is enabled.
//
// It implements the #1185 fallback: when the worktree was enabled by config
// default (explicit == false) and the target path is NOT a supported VCS
// repository, it
// returns fallback == true so the caller creates a normal (non-worktree)
// session instead of erroring. When the worktree was EXPLICITLY requested
// (explicit == true) on a non-repo path, it returns a non-empty errMsg so the
// caller fails loudly, preserving explicit intent.
//
// On a supported repo (git or jujutsu) it computes and returns the backend's
// worktree/workspace path plus repo root.
func resolveWorktreeTarget(path, branch string, explicit bool) (worktreePath, repoRoot string, fallback bool, errMsg string) {
	backend, err := vcsbackend.Detect(path)
	if err != nil {
		if explicit {
			return "", "", false, "Path is not a git or jujutsu repository"
		}
		// #1185: worktree was on by config default, not explicit user intent —
		// fall back to a normal session on non-repo dirs instead of erroring.
		return "", "", true, ""
	}
	root := backend.RepoDir()

	wtSettings := session.GetWorktreeSettings()
	worktreePath = backend.WorktreePath(vcs.WorktreePathOptions{
		Branch:    branch,
		Location:  wtSettings.DefaultLocation,
		SessionID: git.GeneratePathID(),
		Template:  wtSettings.Template(),
	})
	return worktreePath, root, false, ""
}
