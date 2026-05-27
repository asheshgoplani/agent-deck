package session

import (
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// IsRemovableWorktree reports whether agent-deck may safely delete the
// session's worktree directory on dismiss. It is deliberately conservative: a
// path is removable ONLY when every check passes, because a false positive
// means os.RemoveAll on a real repository (issue #1200 — silent data loss).
//
// All of the following must hold:
//  1. agent-deck recorded a worktree at all (non-empty WorktreePath and
//     WorktreeRepoRoot). A worktree_reuse session that pointed at the original
//     repo can leave these empty depending on the creation path.
//  2. the worktree path is NOT the original repo root (worktree_reuse points
//     WorktreePath at the user's primary working tree).
//  3. git itself confirms the path is a LINKED worktree (created via
//     `git worktree add`), never the main working tree or a non-worktree dir.
//     This is the location-independent proof that agent-deck created it:
//     worktree placement is user-configurable, so a fixed managed-directory
//     prefix check is unreliable.
func IsRemovableWorktree(inst *Instance) bool {
	if inst == nil {
		return false
	}
	wt := strings.TrimSpace(inst.WorktreePath)
	root := strings.TrimSpace(inst.WorktreeRepoRoot)
	if wt == "" || root == "" {
		return false
	}
	if canonicalPath(wt) == canonicalPath(root) {
		return false
	}
	return git.IsLinkedWorktree(wt)
}

// RemoveSessionWorktree removes the session's worktree directory if and only if
// IsRemovableWorktree permits it. It returns whether a removal was performed.
// A reused original repo (or any non-linked-worktree path) is left untouched —
// the caller should simply drop the session from the registry (#1200).
func RemoveSessionWorktree(inst *Instance) (removed bool, err error) {
	if !IsRemovableWorktree(inst) {
		return false, nil
	}
	if err := git.RemoveWorktree(inst.WorktreeRepoRoot, inst.WorktreePath, true); err != nil {
		return false, err
	}
	// Best-effort: drop the now-stale worktree administrative reference.
	_ = git.PruneWorktrees(inst.WorktreeRepoRoot)
	return true, nil
}

// canonicalPath resolves symlinks and cleans a path for equality comparison so
// that e.g. /var vs /private/var (macOS) or other symlinked roots do not let a
// reused repo slip past the path == root check. Falls back to a lexical clean
// when the path cannot be resolved (e.g. it no longer exists).
func canonicalPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(p)
}
