package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
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
	fs := flag.NewFlagSet("session remove", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	force := fs.Bool("force", false, "Remove even when the session is running/waiting/idle; with --all-errored, also include pinned sessions (destructive)")
	allErrored := fs.Bool("all-errored", false, "Remove every unpinned session currently in the 'error' state (bulk); pinned sessions are skipped unless --force is given")
	pruneWorktree := fs.Bool("prune-worktree", false, "Also kill the process and remove any git worktree (destructive)")

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

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if *allErrored {
		removeAllErrored(out, storage, instances, groups, *pruneWorktree, *force)
		return
	}

	identifier := fs.Arg(0)
	if identifier == "" {
		out.Error("usage: session remove <id|title> OR --all-errored", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return
	}

	if !*force && !isRemovableStatus(inst.Status) {
		out.Error(
			fmt.Sprintf(
				"session '%s' is in state '%s'; only stopped/error sessions may be removed without --force",
				inst.Title, inst.Status,
			),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
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
	_ = inst.KillAndWait()

	if *pruneWorktree {
		pruneSessionWorktree(inst)
	}

	// v1.9.1 (#909): RemoveSessionAndVerify replaces the
	// DeleteInstance+saveSessionData pair. The old pair would silently
	// resurrect the row when a concurrent rewriter loaded the instance
	// list before our DELETE — exactly the "session remove --force
	// reports success but row stays" failure noted in the bug report.
	instances = dropInstance(instances, inst.ID)
	queueTx, err := session.BeginRuntimeQueueTransaction(inst.ID)
	if err != nil {
		out.Error(fmt.Sprintf("failed to discard runtime queue: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	if err := commitRuntimeQueueRemoval(queueTx, func() error {
		return sessionRemovePersist(storage, inst.ID, instances, groupTree)
	}); err != nil {
		queueTx.Release()
		out.Error(fmt.Sprintf("failed to remove session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	queueTx.Release()

	// Best-effort transition-notifier cleanup for issue #910 — see the
	// matching block in handleRemove for rationale.
	_, _ = session.SweepInboxesForChildSession(inst.ID)
	_, _ = session.RemoveNotifyStateRecord(inst.ID)
	// Drop any prompt this session was queued with, so it cannot be delivered
	// to a later session that happens to reuse the id.
	session.DiscardQueuedMessage(inst.ID)

	out.Success(fmt.Sprintf("Removed session: %s", inst.Title), map[string]interface{}{
		"success": true,
		"id":      inst.ID,
		"title":   inst.Title,
	})
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
	groups []*session.GroupData,
	pruneWorktree bool,
	force bool,
) {
	var doomed []*session.Instance
	skipped := 0
	for _, inst := range instances {
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

	removed := bulkRemoveSessions(out, storage, instances, groups, doomed, pruneWorktree)

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
//     destroying uncommitted work. Never infer it.
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
) []removedSessionRow {
	removed := make([]removedSessionRow, 0, len(doomed))
	removedIDs := make([]string, 0, len(doomed))
	queueTxs := make([]*session.RuntimeQueueTransaction, 0, len(doomed))
	remaining := append([]*session.Instance(nil), instances...)
	for _, inst := range doomed {
		_ = inst.KillAndWait()
		queueTx, err := session.BeginRuntimeQueueTransaction(inst.ID)
		if err != nil {
			out.Error(fmt.Sprintf("failed to discard runtime queue for %s: %v", inst.ID, err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		if pruneWorktree {
			pruneSessionWorktree(inst)
		}
		nextRemaining := dropInstance(remaining, inst.ID)
		groupTree := session.NewGroupTreeWithGroups(nextRemaining, groups)
		if err := bulkSessionRemovePersist(storage, inst.ID, nextRemaining, groupTree); err != nil {
			queueTx.Release()
			releaseRuntimeQueueTransactions(queueTxs)
			out.Error(fmt.Sprintf("failed to remove session %s: %v", inst.ID, err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		remaining = nextRemaining
		removedIDs = append(removedIDs, inst.ID)
		removed = append(removed, map[string]interface{}{"id": inst.ID, "title": inst.Title})
		queueTxs = append(queueTxs, queueTx)
	}

	// A concurrent full-table writer can resurrect an early removal after its
	// per-item verification while later items are still being processed. Sweep
	// only the successfully committed IDs once more against the final survivor
	// set. Failed/unattempted sessions never enter removedIDs, so their queues
	// and rows remain untouched.
	if err := verifyBulkRemovals(storage, removedIDs, remaining, groups); err != nil {
		releaseRuntimeQueueTransactions(queueTxs)
		out.Error(fmt.Sprintf("failed to verify bulk session removal: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	for i, tx := range queueTxs {
		if err := tx.Discard(); err != nil {
			releaseRuntimeQueueTransactions(queueTxs[i:])
			out.Error(fmt.Sprintf("failed to discard runtime queue for %s: %v", removedIDs[i], err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		tx.Release()
	}

	for _, id := range removedIDs {
		// Best-effort transition-notifier cleanup (issue #910).
		_, _ = session.SweepInboxesForChildSession(id)
		_, _ = session.RemoveNotifyStateRecord(id)
		session.DiscardQueuedMessage(id)
	}
	return removed
}

const bulkFinalVerifyAttempts = 6

func verifyBulkRemovals(storage *session.Storage, removedIDs []string, remaining []*session.Instance, groups []*session.GroupData) error {
	finalGroupTree := session.NewGroupTreeWithGroups(remaining, groups)
	for pass := 0; pass < bulkFinalVerifyAttempts; pass++ {
		for _, id := range removedIDs {
			if err := bulkSessionReverifyPersist(storage, id, remaining, finalGroupTree); err != nil {
				return fmt.Errorf("reverify %s: %w", id, err)
			}
		}

		allAbsent := true
		for _, id := range removedIDs {
			exists, err := storage.InstanceExists(id)
			if err != nil {
				return fmt.Errorf("observe %s: %w", id, err)
			}
			if exists {
				allAbsent = false
			}
		}
		if allAbsent {
			return nil
		}
	}
	return fmt.Errorf("removed rows kept reappearing after %d verification passes", bulkFinalVerifyAttempts)
}

func releaseRuntimeQueueTransactions(txs []*session.RuntimeQueueTransaction) {
	for _, tx := range txs {
		tx.Release()
	}
}

var (
	sessionRemovePersist = func(storage *session.Storage, id string, remaining []*session.Instance, tree *session.GroupTree) error {
		return storage.RemoveSessionAndVerify(id, remaining, tree)
	}
	bulkSessionRemovePersist = func(storage *session.Storage, id string, remaining []*session.Instance, tree *session.GroupTree) error {
		return storage.RemoveSessionAndVerify(id, remaining, tree)
	}
	bulkSessionReverifyPersist = func(storage *session.Storage, id string, remaining []*session.Instance, tree *session.GroupTree) error {
		return storage.RemoveSessionAndVerify(id, remaining, tree)
	}
)

func commitRuntimeQueueRemoval(tx *session.RuntimeQueueTransaction, persistRemoval func() error) error {
	if err := persistRemoval(); err != nil {
		return fmt.Errorf("persist removal: %w", err)
	}
	if err := tx.Discard(); err != nil {
		return fmt.Errorf("discard runtime queue: %w", err)
	}
	return nil
}

// pruneSessionWorktree kills the session and removes its git worktree (if any).
// Errors are logged to stderr but never block the remove.
//
// Uses KillAndWait so the SIGTERM→SIGKILL escalation completes before
// this short-lived CLI exits (issue #59, v1.7.68).
func pruneSessionWorktree(inst *session.Instance) {
	_ = inst.KillAndWait()
	if inst.IsWorktree() {
		if backend, err := detectAndCreateBackend(inst.WorktreeRepoRoot); err == nil {
			if err := backend.RemoveWorktree(inst.WorktreePath, true); err != nil {
				fmt.Fprintf(os.Stderr, "warn: worktree remove failed for %s: %v\n", inst.ID, err)
			}
			_ = backend.PruneWorktrees()
		}
	}
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
