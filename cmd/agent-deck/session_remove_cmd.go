package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// handleSessionRemove deletes a session from the registry.
//
// By default only sessions in stopped/error state may be removed; --force
// bypasses the gate. --all-errored removes every session in error state.
// --prune-worktree additionally kills the tmux process and removes any git
// worktree associated with the session (registry-only by default).
//
// Claude transcripts under ~/.claude/projects/<slug>/ are never touched.
func handleSessionRemove(profile string, args []string) {
	fs := flag.NewFlagSet("session remove", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	force := fs.Bool("force", false, "Remove even when the session is running/waiting/idle; with --all-errored, also include pinned sessions; with --prune-worktree, also remove a worktree when other profiles that cannot be read might use it (reported as profiles_unchecked in --json) (destructive)")
	allErrored := fs.Bool("all-errored", false, "Remove every unpinned session currently in the 'error' state (bulk); pinned sessions are skipped unless --force is given")
	pruneWorktree := fs.Bool("prune-worktree", false, "Also kill the process and remove any git worktree (destructive; a worktree another session still uses is kept)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session remove <id|title> [options]")
		fmt.Println("       agent-deck session remove --all-errored [options]")
		fmt.Println()
		fmt.Println("Remove a session from the registry. By default only stopped or")
		fmt.Println("errored sessions may be removed; use --force to bypass.")
		fmt.Println()
		fmt.Println("This is registry-only by default: Claude transcripts under")
		fmt.Println("~/.claude/projects/ are preserved. Pass --prune-worktree to also")
		fmt.Println("kill the process and delete the git worktree (destructive).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	if *allErrored {
		// A bulk delete needs two agreeing reads: the stored status and
		// the status list shows must both be error. Live alone would delete
		// a healthy row whose tmux is gone for one sample (another process
		// mid-restart); stored alone deleted rows list showed as idle.
		storedErrored := make(map[*session.Instance]bool)
		for _, inst := range instances {
			if inst != nil && inst.Status == session.StatusError {
				storedErrored[inst] = true
			}
		}
		refreshLiveStatus(instances)
		var errored []*session.Instance
		for _, inst := range instances {
			if storedErrored[inst] {
				errored = append(errored, inst)
			}
		}
		removeAllErrored(out, storage, instances, errored, groups, *pruneWorktree, *force, newWorktreeRemovalGuard(profile))
		return
	}

	identifier := fs.Arg(0)
	if identifier == "" {
		out.Error("usage: session remove <id|title> OR --all-errored", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return
	}

	// The guard reads the status list shows, not the stored one.
	refreshLiveStatus([]*session.Instance{inst})
	if !*force && !isRemovableStatus(inst.Status) {
		out.Error(
			fmt.Sprintf(
				"session '%s' is in state '%s'; only stopped/error sessions may be removed without --force",
				inst.Title, inst.Status,
			),
			ErrCodeInvalidOperation,
		)
		exitCLI(1)
	}

	// Always kill the tmux scope + its process tree before deleting the
	// registry row (issue #59, v1.7.68). Previously Kill() was only
	// called inside pruneSessionWorktree, so `session remove --force`
	// on a running session left the claude child running as an orphan
	// — observed on the maintainer's host as a 33-hour orphan claude
	// process with a since-deleted AGENTDECK_INSTANCE_ID.
	//
	// KillAndWait runs the SIGTERM→SIGKILL escalation synchronously so
	// the kill completes before this short-lived CLI exits.
	saveStoppedBeforeRemoval(storage, inst)
	_ = inst.KillAndWait()
	inst.RecordTelemetryEnd(telemetry.EndDelete)

	worktreeKept := false
	var profilesUnchecked []string
	if *pruneWorktree {
		worktreeKept, profilesUnchecked = pruneSessionWorktree(inst, instances, newWorktreeRemovalGuard(profile), *force, *jsonOutput)
	}

	// v1.9.1 (#909): RemoveSessionAndVerify replaces the
	// DeleteInstance+saveSessionData pair. The old pair would silently
	// resurrect the row when a concurrent rewriter loaded the instance
	// list before our DELETE — exactly the "session remove --force
	// reports success but row stays" failure noted in the bug report.
	instances = dropInstance(instances, inst.ID)
	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.RemoveSessionAndVerify(inst.ID, instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to remove session: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Best-effort cleanup for the removed row (issue #910 sweep,
	// macapp.session.closed) — see the matching block in handleRemove.
	session.CleanupRemovedSession(storage.Profile(), inst.ID)

	result := map[string]interface{}{
		"success":       true,
		"id":            inst.ID,
		"title":         inst.Title,
		"worktree_kept": worktreeKept, // another session still uses it
	}
	if len(profilesUnchecked) > 0 {
		// --force removed the worktree without reading these profiles.
		result["profiles_unchecked"] = profilesUnchecked
	}
	out.Success(fmt.Sprintf("Removed session: %s", inst.Title), result)
}

// isRemovableStatus returns true for states where a session can be removed
// from the registry without --force.
func isRemovableStatus(s session.Status) bool {
	return s == session.StatusStopped || s == session.StatusError
}

// removedSessionRow is the {id,title} payload emitted for each removed session.
type removedSessionRow = map[string]interface{}

// removeAllErrored implements the --all-errored bulk path.
func removeAllErrored(
	out *CLIOutput,
	storage *session.Storage,
	instances []*session.Instance,
	candidates []*session.Instance,
	groups []*session.GroupData,
	pruneWorktree bool,
	force bool,
	guard *worktreeRemovalGuard,
) {
	var doomed []*session.Instance
	skipped := 0
	for _, inst := range candidates {
		if inst.Status != session.StatusError {
			continue
		}
		// pin-protects-from-stop: a pinned errored session is retained
		// unless --force is given.
		if inst.Pin != session.PinNone && !force {
			skipped++
			continue
		}
		doomed = append(doomed, inst)
	}

	removed := bulkRemoveSessions(out, storage, instances, groups, doomed, pruneWorktree, guard, force)

	msg := fmt.Sprintf("Removed %d errored session(s)", len(removed))
	if skipped > 0 {
		msg += fmt.Sprintf(" (skipped %d pinned — use --force to include)", skipped)
	}
	out.Success(msg, map[string]interface{}{
		"success": true,
		"count":   len(removed),
		"removed": removed,
		"skipped": skipped,
	})
}

// bulkRemoveSessions is the ONE implementation of the safety-critical bulk
// delete choreography, shared by `session remove --all-errored` and
// `session cleanup`. Keeping it single-sourced matters: the two copies it
// replaced had already drifted apart (only one did KillAndWait / pin-skip), and
// each of the invariants below was a separate production bug.
//
//   - KillAndWait before DELETE (#59, v1.7.68): an errored/dead session often
//     still owns a live agent child. Deleting the row without a synchronous
//     SIGTERM→SIGKILL escalation is how a 33-hour orphan claude process with a
//     since-deleted AGENTDECK_INSTANCE_ID was created.
//   - pruneWorktree is OPT-IN: it force-deletes the git worktree directory,
//     destroying uncommitted work. Never infer it. A worktree that a session
//     in this or another local profile still uses is kept (guard); force
//     only overrides an unreadable other profile, and the row then lists it
//     under profiles_unchecked.
//   - SaveGroupsOnly, never SaveWithGroups (#909): a full-table rewrite's
//     INSERT OR REPLACE resurrects the rows we just deleted. NewGroupTreeWithGroups
//     over the SURVIVING set also preserves groups whose last session just went
//     away (empty groups otherwise vanish forever).
//   - Verify + re-DELETE: a concurrent writer (a live TUI) can still resurrect a
//     row between our DELETE and our save.
//   - Inbox / notify-state sweep (#910).
//
// Returns the {id,title} payload rows for the sessions actually removed.
func bulkRemoveSessions(
	out *CLIOutput,
	storage *session.Storage,
	instances []*session.Instance,
	groups []*session.GroupData,
	doomed []*session.Instance,
	pruneWorktree bool,
	guard *worktreeRemovalGuard,
	force bool,
) []removedSessionRow {
	doomedIDs := make(map[string]bool, len(doomed))
	for _, inst := range doomed {
		doomedIDs[inst.ID] = true
	}

	removed := make([]removedSessionRow, 0, len(doomed))
	removedIDs := make([]string, 0, len(doomed))
	// Sessions not removed yet: a worktree shared only with sessions this
	// sweep already removed goes with the last of them.
	live := append([]*session.Instance(nil), instances...)
	for _, inst := range doomed {
		saveStoppedBeforeRemoval(storage, inst)
		_ = inst.KillAndWait()
		var profilesUnchecked []string
		if pruneWorktree {
			_, profilesUnchecked = pruneSessionWorktree(inst, live, guard, force, out.jsonMode)
		}
		live = dropInstance(live, inst.ID)
		if err := storage.DeleteInstance(inst.ID); err != nil {
			out.Error(fmt.Sprintf("failed to remove session %s: %v", inst.ID, err), ErrCodeInvalidOperation)
			exitBulkRemove(1)
		}
		// Clean up per deleted row, right away: a later failure exits, and
		// the rows already deleted must still be swept and announced.
		session.CleanupRemovedSession(storage.Profile(), inst.ID)
		removedIDs = append(removedIDs, inst.ID)
		row := map[string]interface{}{"id": inst.ID, "title": inst.Title}
		if len(profilesUnchecked) > 0 {
			// --force removed the worktree without reading these profiles.
			row["profiles_unchecked"] = profilesUnchecked
		}
		removed = append(removed, row)
	}

	remaining := make([]*session.Instance, 0, len(instances)-len(removedIDs))
	for _, inst := range instances {
		if !doomedIDs[inst.ID] {
			remaining = append(remaining, inst)
		}
	}
	groupTree := session.NewGroupTreeWithGroups(remaining, groups)
	if err := storage.SaveGroupsOnly(groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		exitBulkRemove(1)
	}

	for _, id := range removedIDs {
		if exists, _ := storage.InstanceExists(id); exists {
			_ = storage.DeleteInstance(id)
		}
	}
	return removed
}

// exitBulkRemove exits a bulk remove that failed part way. os.Exit skips
// main's deferred events drain, so the macapp.session.closed frames of the
// rows already deleted are flushed first.
func exitBulkRemove(code int) {
	_ = events.CloseDefault()
	exitCLI(code)
}

// warnRemovedSessionCleanup prints the best-effort cleanup outcome of a
// removed session to stderr (never in --json mode).
func warnRemovedSessionCleanup(r session.RemovedSessionCleanup, id string, jsonOutput bool) {
	if jsonOutput {
		return
	}
	if r.InboxErr != nil {
		fmt.Fprintf(os.Stderr, "warn: inbox sweep for %s failed: %v\n", id, r.InboxErr)
	} else if r.InboxSwept > 0 {
		fmt.Fprintf(os.Stderr, "swept %d stale inbox event(s) for removed session\n", r.InboxSwept)
	}
	if r.NotifyStateErr != nil {
		fmt.Fprintf(os.Stderr, "warn: notify-state sweep for %s failed: %v\n", id, r.NotifyStateErr)
	}
}

// saveStoppedBeforeRemoval saves the row as stopped before a removal kills
// the session (#2806). The row is deleted only after the kill and, with a
// worktree, after `git worktree remove`, which can take longer than the
// notify daemon's 5 s death grace on a big tree. Until then the daemon would
// read the old row with no pane and tell the parent the child "exited while
// idle". A stopped row is a deliberate end, so it never does. Best-effort: a
// failed write leaves the old behavior.
func saveStoppedBeforeRemoval(storage *session.Storage, inst *session.Instance) {
	if storage == nil || inst == nil {
		return
	}
	if db := storage.GetDB(); db != nil {
		_ = db.WriteStatus(inst.ID, string(session.StatusStopped), inst.Tool)
	}
}

// pruneSessionWorktree kills the session and removes its git worktree (if any)
// unless another session, in this or any other local profile, still uses it;
// kept reports that case. unchecked names the profiles that could not be read
// when force removed the worktree anyway. Errors are logged to stderr but
// never block the remove.
//
// Uses KillAndWait so the SIGTERM→SIGKILL escalation completes before
// this short-lived CLI exits (issue #59, v1.7.68).
func pruneSessionWorktree(inst *session.Instance, instances []*session.Instance, guard *worktreeRemovalGuard, force, jsonOutput bool) (kept bool, unchecked []string) {
	_ = inst.KillAndWait()
	if !inst.IsWorktree() {
		return false, nil
	}
	keep, unchecked := keepSharedWorktree(inst, instances, guard, force, jsonOutput)
	if keep {
		return true, nil
	}
	if backend, err := detectAndCreateBackend(inst.WorktreeRepoRoot); err == nil {
		if err := backend.RemoveWorktree(inst.WorktreePath, true); err != nil {
			fmt.Fprintf(os.Stderr, "warn: worktree remove failed for %s: %v\n", inst.ID, err)
		}
		_ = backend.PruneWorktrees()
	}
	return false, unchecked
}

// worktreeRemovalGuard is the one check every CLI path runs before it removes
// a session's worktree: `remove`, `session remove --prune-worktree`, the
// --all-errored and `session cleanup` sweeps, and `worktree finish`. A
// worktree is in use when a session in the current profile (the instances
// the command loaded) or in any other local profile uses it. Other profiles
// are loaded once per command, on first use, so a remove that touches no
// worktree never opens them. `worktree cleanup` checks the same profiles
// through allProfileSessionPaths.
type worktreeRemovalGuard struct {
	profile string
	loaded  bool
	others  []*session.Instance
	err     error
}

func newWorktreeRemovalGuard(profile string) *worktreeRemovalGuard {
	return &worktreeRemovalGuard{profile: profile}
}

// sharers returns the sessions other than inst, in current or in another
// local profile, that use inst's worktree. A non-nil error means another
// profile could not be read, so its users are unknown; sharers still holds
// every user found.
func (g *worktreeRemovalGuard) sharers(inst *session.Instance, current []*session.Instance) ([]*session.Instance, error) {
	if !g.loaded {
		g.others, g.err = session.OtherProfileInstances(g.profile)
		g.loaded = true
	}
	all := append(append([]*session.Instance{}, current...), g.others...)
	return session.SessionsSharingWorktree(inst, all), g.err
}

// keepSharedWorktree reports whether inst's worktree must survive its removal
// because another session in this or another local profile still uses it
// (#1449, the same rule as the TUI delete), or because another profile could
// not be read and force is not set. It says so on stderr outside --json mode.
// When force removes it anyway, unchecked names the profiles that were not
// read, so --json output can report them (the stderr warning is suppressed
// there).
func keepSharedWorktree(inst *session.Instance, instances []*session.Instance, guard *worktreeRemovalGuard, force, jsonOutput bool) (keep bool, unchecked []string) {
	sharers, err := guard.sharers(inst, instances)
	if len(sharers) > 0 {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Kept worktree %s: still used by %s\n", FormatPath(inst.WorktreePath), sessionNameList(sharers))
		}
		return true, nil
	}
	if err != nil && !force {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Kept worktree %s: could not check other profiles for users: %v\n", FormatPath(inst.WorktreePath), err)
		}
		return true, nil
	}
	if err != nil && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Warning: --force: removing worktree %s without checking other profiles: %v\n", FormatPath(inst.WorktreePath), err)
	}
	return false, session.UncheckedProfiles(err)
}

// sessionNameList renders sessions as "title (id8), ..." for messages.
func sessionNameList(insts []*session.Instance) string {
	names := make([]string, 0, len(insts))
	for _, s := range insts {
		names = append(names, fmt.Sprintf("'%s' (%s)", s.Title, shortID(s.ID)))
	}
	return strings.Join(names, ", ")
}

// dropInstance returns a new slice with the given id filtered out.
func dropInstance(instances []*session.Instance, id string) []*session.Instance {
	out := instances[:0]
	for _, i := range instances {
		if i.ID != id {
			out = append(out, i)
		}
	}
	return out
}
