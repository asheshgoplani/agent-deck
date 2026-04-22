package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/git"
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
	force := fs.Bool("force", false, "Remove even when the session is running/waiting/idle (destructive)")
	allErrored := fs.Bool("all-errored", false, "Remove every session currently in the 'error' state (bulk)")
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
		removeAllErrored(out, storage, instances, groups, *pruneWorktree)
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

	if *pruneWorktree {
		pruneSessionWorktree(inst)
	}

	if err := storage.DeleteInstance(inst.ID); err != nil {
		out.Error(fmt.Sprintf("failed to remove session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	instances = dropInstance(instances, inst.ID)
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

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

// removeAllErrored implements the --all-errored bulk path.
func removeAllErrored(
	out *CLIOutput,
	storage *session.Storage,
	instances []*session.Instance,
	groups []*session.GroupData,
	pruneWorktree bool,
) {
	var removed []map[string]interface{}
	remaining := instances[:0]
	for _, inst := range instances {
		if inst.Status == session.StatusError {
			if pruneWorktree {
				pruneSessionWorktree(inst)
			}
			if err := storage.DeleteInstance(inst.ID); err != nil {
				out.Error(fmt.Sprintf("failed to remove session %s: %v", inst.ID, err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			removed = append(removed, map[string]interface{}{"id": inst.ID, "title": inst.Title})
			continue
		}
		remaining = append(remaining, inst)
	}
	if err := saveSessionData(storage, remaining, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("Removed %d errored session(s)", len(removed)), map[string]interface{}{
		"success": true,
		"count":   len(removed),
		"removed": removed,
	})
}

// pruneSessionWorktree kills the session and removes its git worktree (if any).
// Errors are logged to stderr but never block the remove.
func pruneSessionWorktree(inst *session.Instance) {
	_ = inst.Kill()
	if inst.IsWorktree() {
		if err := git.RemoveWorktree(inst.WorktreeRepoRoot, inst.WorktreePath, true); err != nil {
			fmt.Fprintf(os.Stderr, "warn: worktree remove failed for %s: %v\n", inst.ID, err)
		}
		_ = git.PruneWorktrees(inst.WorktreeRepoRoot)
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
