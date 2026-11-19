package session

import (
	"errors"
	"fmt"
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

// OtherSessionsShareWorktree reports whether any instance in others (other than
// target itself) still references target's worktree directory. Paths are
// compared canonically (EvalSymlinks), so a symlinked alias to the same
// directory counts as a sharer. nil entries, target's own ID, and non-worktree
// instances are skipped.
//
// This is the missing guard behind issue #1449: finishing one of several
// sessions that share a single worktree must not remove the shared directory or
// delete the branch while a sibling session still uses it.
func OtherSessionsShareWorktree(target *Instance, others []*Instance) bool {
	return len(SessionsSharingWorktree(target, others)) > 0
}

// SessionsSharingWorktree returns the instances in others (other than target
// itself) that use target's worktree directory: their worktree path or one of
// their multi-repo worktree paths is that directory, or their project path or
// one of their additional paths is that directory or inside it (a session
// added by plain path at the worktree records no worktree fields). Only local
// sessions count: a remote (--ssh) session runs on another machine and its
// ProjectPath is a local placeholder, not a checkout (LocationOf). Paths are
// compared canonically as in OtherSessionsShareWorktree. Callers that refuse
// or skip a removal use it to name the sessions that still need the worktree.
func SessionsSharingWorktree(target *Instance, others []*Instance) []*Instance {
	if target == nil {
		return nil
	}
	wt := strings.TrimSpace(target.WorktreePath)
	if wt == "" {
		return nil
	}
	targetCanon := canonicalPath(wt)
	var sharers []*Instance
	for _, o := range others {
		if o == nil || o.ID == target.ID || !LocationOf(o).IsLocal() {
			continue
		}
		if usesWorktree(o, targetCanon) {
			sharers = append(sharers, o)
		}
	}
	return sharers
}

// usesWorktree reports whether the local session o uses the worktree whose
// canonical path is wtCanon.
func usesWorktree(o *Instance, wtCanon string) bool {
	worktrees := []string{o.WorktreePath}
	for _, mw := range o.MultiRepoWorktrees {
		worktrees = append(worktrees, mw.WorktreePath)
	}
	for _, w := range worktrees {
		if w = strings.TrimSpace(w); w != "" && canonicalPath(w) == wtCanon {
			return true
		}
	}
	for _, p := range append([]string{o.ProjectPath}, o.AdditionalPaths...) {
		if p = strings.TrimSpace(p); p != "" && pathIsOrUnder(canonicalPath(p), wtCanon) {
			return true
		}
	}
	return false
}

// OtherProfileInstances loads the sessions of every local profile except
// currentProfile, so a worktree guard also sees users in other profiles. An
// error means some profile could not be read: callers must treat its
// sessions as unknown, never as absent. When the profiles could be listed,
// the error is an *UnreadableProfilesError naming every profile that could
// not be read, and the sessions of the readable ones are still returned.
func OtherProfileInstances(currentProfile string) ([]*Instance, error) {
	profiles, err := ListProfiles()
	if err != nil {
		return nil, fmt.Errorf("list profiles for worktree ownership: %w", err)
	}
	current := GetEffectiveProfile(currentProfile)
	var all []*Instance
	unreadable := &UnreadableProfilesError{}
	for _, profile := range profiles {
		if profile == currentProfile || profile == current {
			continue
		}
		storage, err := NewStorageWithProfile(profile)
		if err != nil {
			unreadable.add(profile, fmt.Errorf("open profile %q for worktree ownership: %w", profile, err))
			continue
		}
		instances, _, err := storage.LoadWithGroups()
		_ = storage.Close()
		if err != nil {
			unreadable.add(profile, fmt.Errorf("load profile %q for worktree ownership: %w", profile, err))
			continue
		}
		all = append(all, instances...)
	}
	if len(unreadable.Profiles) > 0 {
		return all, unreadable
	}
	return all, nil
}

// UnreadableProfilesError lists the local profiles OtherProfileInstances
// could not read.
type UnreadableProfilesError struct {
	Profiles []string
	errs     []error
}

func (e *UnreadableProfilesError) add(profile string, err error) {
	e.Profiles = append(e.Profiles, profile)
	e.errs = append(e.errs, err)
}

func (e *UnreadableProfilesError) Error() string {
	msgs := make([]string, 0, len(e.errs))
	for _, err := range e.errs {
		msgs = append(msgs, err.Error())
	}
	return strings.Join(msgs, "; ")
}

func (e *UnreadableProfilesError) Unwrap() []error { return e.errs }

// UncheckedProfiles names the profiles a failed OtherProfileInstances call
// could not check, for reports such as the CLI's "profiles_unchecked" field.
// When the profile list itself could not be read it returns ["*"] (every
// other profile); for a nil error it returns nil.
func UncheckedProfiles(err error) []string {
	if err == nil {
		return nil
	}
	var u *UnreadableProfilesError
	if errors.As(err, &u) {
		return append([]string(nil), u.Profiles...)
	}
	return []string{"*"}
}

// WorktreeUsersInOtherProfiles returns the sessions in local profiles other
// than currentProfile that use target's worktree (SessionsSharingWorktree
// over OtherProfileInstances). Every path that removes a session's worktree
// checks this on top of the current profile's sessions: the CLI through
// worktreeRemovalGuard (cmd/agent-deck), the TUI delete and finish, and the
// web finish. On error the caller must keep the worktree unless the user
// forced the removal. With an error, the users found in the profiles that
// could be read are still returned.
func WorktreeUsersInOtherProfiles(target *Instance, currentProfile string) ([]*Instance, error) {
	others, err := OtherProfileInstances(currentProfile)
	return SessionsSharingWorktree(target, others), err
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

// RemoveSessionWorktreeUnlessShared is the shared-worktree-safe entry point
// (issue #1449). It removes the session's worktree directory only when no OTHER
// live session still references that worktree; otherwise it skips the
// destructive git steps and reports removed=false so the caller merely detaches
// this session from the registry. When this is the last sharer, behaviour is
// identical to RemoveSessionWorktree (including the #1200 reuse guard).
func RemoveSessionWorktreeUnlessShared(inst *Instance, others []*Instance) (removed bool, err error) {
	if OtherSessionsShareWorktree(inst, others) {
		return false, nil
	}
	return RemoveSessionWorktree(inst)
}

// pathIsOrUnder reports whether p is dir or a path inside it (both canonical).
func pathIsOrUnder(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// canonicalPath resolves symlinks and cleans a path for equality comparison so
// that e.g. /var vs /private/var (macOS) or other symlinked roots do not let a
// reused repo slip past the path == root check. Falls back to a lexical clean
// when the path cannot be resolved (e.g. it no longer exists).
func canonicalPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	// EvalSymlinks failed (e.g. the path no longer exists, or a broken/
	// inaccessible symlink alias). Fall back to an absolute lexical clean so a
	// relative vs absolute spelling of the same path still compares equal. For
	// the shared-worktree check this matters: a false negative here would let a
	// still-shared worktree reach destructive cleanup, so normalize before the
	// lexical compare rather than trusting the raw input.
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}
