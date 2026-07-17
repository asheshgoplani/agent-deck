package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// cleanupDefaultDays is the default age threshold for `session cleanup`: a
// session must have had no activity for at least this many days (in addition to
// being dead) before it is a purge candidate.
const cleanupDefaultDays = 30

// cleanupStartupGrace is how long a session may sit in a mid-lifecycle status
// (starting/queued) before that status stops protecting it from cleanup.
//
// The guard exists because Exists() can be transiently false while a session
// boots. It must NOT be open-ended: a process that dies mid-start (crash,
// reboot) leaves status='starting' in the DB with nothing to ever rewrite it,
// and an unconditional exemption made those ghosts permanently unpurgeable —
// precisely the dead-stale class this command exists to collect. Beyond the
// grace window a starting/queued session is judged on liveness like any other;
// one that really is still coming up answers the isDead probe as alive.
const cleanupStartupGrace = 5 * time.Minute

// isCleanupCandidate reports whether a session should be purged by
// `session cleanup`. A session qualifies iff ALL of:
//   - it is not within cleanupStartupGrace of a mid-lifecycle status
//     (starting/queued) — never purge a session that is still coming up;
//   - it is NOT pinned, unless force (pin-protects-from-stop, matching
//     `session remove --all-errored`);
//   - it is NOT archived, unless includeArchived (archiving is a deliberate
//     keep);
//   - it has had no activity for longer than maxAge (see cleanupLastTouched);
//   - it is dead (isDead) — injected so tests need no tmux. Production passes a
//     socket-complete tmux probe, the authoritative liveness signal,
//     deliberately NOT the stored Status (StatusError has a known
//     false-positive class).
//
// The cheap predicates are checked before isDead so the tmux probe only runs
// for sessions that are otherwise eligible.
func isCleanupCandidate(
	inst *session.Instance,
	now time.Time,
	maxAge time.Duration,
	includeArchived bool,
	force bool,
	isDead func(*session.Instance) bool,
) bool {
	if inst == nil {
		return false
	}
	if inst.Status == session.StatusStarting || inst.Status == session.StatusQueued {
		if now.Sub(cleanupLastTouched(inst)) <= cleanupStartupGrace {
			return false
		}
	}
	if inst.Pin != session.PinNone && !force {
		return false
	}
	if inst.IsArchived() && !includeArchived {
		return false
	}
	if now.Sub(cleanupLastTouched(inst)) <= maxAge {
		return false
	}
	return isDead(inst)
}

// cleanupLastTouched returns the most protective "last touched" timestamp for a
// session: the later of CreatedAt and LastAccessedAt (when the user last
// attached). GetLastActivityTime is deliberately NOT used — for a dead session
// its tmux tracker is gone, so it collapses to CreatedAt and would ignore a
// recent attach. Using the max of both timestamps means a recently-used session
// that has since died is not purged just because it was created long ago.
func cleanupLastTouched(inst *session.Instance) time.Time {
	last := inst.CreatedAt
	if inst.LastAccessedAt.After(last) {
		last = inst.LastAccessedAt
	}
	return last
}

// selectCleanupCandidates returns the subset of instances that qualify for
// cleanup, preserving input order, plus the count skipped for being pinned
// (reported so the user knows something was deliberately retained).
func selectCleanupCandidates(
	instances []*session.Instance,
	now time.Time,
	maxAge time.Duration,
	includeArchived bool,
	force bool,
	isDead func(*session.Instance) bool,
) (candidates []*session.Instance, pinnedSkipped int) {
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if inst.Pin != session.PinNone && !force {
			// Only report a pin skip for a session that would OTHERWISE have
			// been purged, so the count means "retained because pinned" rather
			// than "is pinned".
			if isCleanupCandidate(inst, now, maxAge, includeArchived, true, isDead) {
				pinnedSkipped++
			}
			continue
		}
		if isCleanupCandidate(inst, now, maxAge, includeArchived, force, isDead) {
			candidates = append(candidates, inst)
		}
	}
	return candidates, pinnedSkipped
}

// newTmuxLivenessProbe returns an isDead func backed by a socket-complete view
// of live tmux sessions: ONE `list-sessions` per distinct socket, rather than
// one `has-session` per session.
//
// This matters on exactly the deck this command targets. Probing ~1000 dead
// sessions individually costs ~1000 sequential subprocesses in a cold CLI
// process (no session cache, no pipes), and if the server is wedged each probe
// burns its full timeout — a dry run that should be instant takes tens of
// minutes before printing anything.
//
// Socket-completeness is the safety requirement, not an optimization: a flat
// name-keyed set built from one arbitrary socket reports live sessions on OTHER
// sockets as missing, and this command deletes what it thinks is missing. A
// socket whose probe fails or times out is INDETERMINATE — every session on it
// is reported alive, so it can never become a purge candidate.
func newTmuxLivenessProbe(instances []*session.Instance) func(*session.Instance) bool {
	live := map[string]map[string]struct{}{} // socket -> live session names
	indeterminate := map[string]bool{}       // socket -> probe failed, assume alive

	sockets := map[string]bool{}
	for _, inst := range instances {
		if inst != nil {
			sockets[inst.TmuxSocketName] = true
		}
	}
	for socket := range sockets {
		names, err := tmux.ListSessionNamesOnSocket(socket)
		if err != nil {
			indeterminate[socket] = true
			continue
		}
		live[socket] = names
	}

	return func(inst *session.Instance) bool {
		if inst == nil {
			return false
		}
		if indeterminate[inst.TmuxSocketName] {
			return false // probe inconclusive: assume alive, never purge
		}
		ts := inst.GetTmuxSession()
		if ts == nil {
			return true // no tmux session bound: nothing to be alive
		}
		_, alive := live[inst.TmuxSocketName][ts.Name]
		return !alive
	}
}

// shortID returns the first 8 characters of an id (or the whole id if shorter),
// matching how other CLI output abbreviates session IDs.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// humanizeAge renders a duration as a coarse age like "47d" / "5h" / "12m".
func humanizeAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return "<1m"
	}
}

// handleSessionCleanup purges sessions that are dead AND untouched for at least
// --days days. It is dry-run by default: nothing is deleted without --yes or an
// explicit interactive "yes". Archived and pinned sessions are excluded unless
// --include-archived / --force. Registry-only unless --prune-worktree.
func handleSessionCleanup(profile string, args []string) {
	fs := flag.NewFlagSet("session cleanup", flag.ExitOnError)
	days := fs.Int("days", cleanupDefaultDays, "Minimum days without activity before a dead session is a purge candidate")
	yes := fs.Bool("yes", false, "Actually delete (skip the confirmation prompt)")
	yesShort := fs.Bool("y", false, "Actually delete (short for --yes)")
	dryRun := fs.Bool("dry-run", false, "Preview only; never delete even with --yes")
	includeArchived := fs.Bool("include-archived", false, "Also consider archived sessions (excluded by default)")
	force := fs.Bool("force", false, "Also include pinned sessions (pinned are retained by default)")
	pruneWorktree := fs.Bool("prune-worktree", false, "Also delete each session's git worktree directory (DESTRUCTIVE: discards uncommitted work)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session cleanup [options]")
		fmt.Println()
		fmt.Println("Purge sessions that are DEAD (no live tmux pane) AND have had no")
		fmt.Println("activity for at least --days days. Dry-run by default: nothing is")
		fmt.Println("deleted without --yes or an explicit interactive confirmation.")
		fmt.Println()
		fmt.Println("Archived sessions are never touched unless --include-archived.")
		fmt.Println("Pinned sessions are retained unless --force.")
		fmt.Println()
		fmt.Println("Registry-only by default: Claude transcripts under ~/.claude/projects/")
		fmt.Println("AND git worktree directories are kept. Pass --prune-worktree to also")
		fmt.Println("delete each session's worktree (DESTRUCTIVE: uncommitted work is lost;")
		fmt.Println("the preview marks affected sessions with 'wt' in the WT column).")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session cleanup                 # preview dead sessions idle 30+ days")
		fmt.Println("  agent-deck session cleanup --days 7        # preview with a 7-day cutoff")
		fmt.Println("  agent-deck session cleanup --days 60 --yes # actually delete (registry only)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	if *days < 0 {
		out.Error("--days must be zero or positive", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	now := time.Now()
	maxAge := time.Duration(*days) * 24 * time.Hour
	isDead := newTmuxLivenessProbe(instances)
	candidates, pinnedSkipped := selectCleanupCandidates(instances, now, maxAge, *includeArchived, *force, isDead)

	if len(candidates) == 0 {
		msg := fmt.Sprintf("No dead sessions idle for %d+ days to clean up.", *days)
		if pinnedSkipped > 0 {
			msg += fmt.Sprintf(" (skipped %d pinned — use --force to include)", pinnedSkipped)
		}
		out.Success(msg, map[string]interface{}{
			"success": true,
			"count":   0,
			"removed": []interface{}{},
			"skipped": pinnedSkipped,
		})
		return
	}

	// Decide whether to actually delete.
	//   --dry-run          -> never delete (explicit preview)
	//   --yes / -y         -> delete without prompting
	//   --json (no --yes)  -> preview only (no interactive prompt in JSON mode)
	//   otherwise          -> interactive [y/N] prompt
	execute := (*yes || *yesShort) && !*dryRun

	if !execute {
		// Preview path (dry-run, or JSON without --yes, or the pre-prompt render).
		printCleanupPreview(out, candidates, now, *days, pinnedSkipped, *pruneWorktree, *jsonOutput)
		if *dryRun || *jsonOutput {
			return
		}
		fmt.Printf("Delete %d session(s)? [y/N] ", len(candidates))
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		if !isYesConfirmation(line) {
			fmt.Println("Aborted. Nothing deleted.")
			return
		}
	}

	// TOCTOU guard, on BOTH paths. The candidate list was computed before this
	// point; everything below (KillAndWait, worktree removal, DELETE) is
	// destructive and irreversible. The interactive [y/N] prompt can sit open
	// for minutes while the TUI, the reviver, or another terminal restarts a
	// candidate — and even --yes leaves a window. Re-confirm liveness against a
	// FRESH socket-complete tmux view and drop anything that came back to life.
	candidates = dropRevivedCandidates(candidates)
	if len(candidates) == 0 {
		out.Success("All candidates came back to life before deletion. Nothing deleted.", map[string]interface{}{
			"success": true,
			"count":   0,
			"removed": []interface{}{},
			"skipped": pinnedSkipped,
		})
		return
	}

	removed := bulkRemoveSessions(out, storage, instances, groups, candidates, *pruneWorktree)

	msg := fmt.Sprintf("Removed %d dead session(s).", len(removed))
	if pinnedSkipped > 0 {
		msg += fmt.Sprintf(" (skipped %d pinned — use --force to include)", pinnedSkipped)
	}
	out.Success(msg, map[string]interface{}{
		"success": true,
		"count":   len(removed),
		"removed": removed,
		"skipped": pinnedSkipped,
	})
}

// dropRevivedCandidates re-probes liveness against a FRESH socket-complete view
// and returns only the candidates that are still dead.
//
// It deliberately rebuilds the probe (one `list-sessions` per distinct socket)
// rather than calling inst.Exists() per candidate: a purge run can carry a
// thousand candidates, and per-session `has-session` probes would be a thousand
// sequential subprocesses on the destructive path — the very cost the selection
// pass was rewritten to avoid. The socket-complete probe is also the safe way
// to read a NEGATIVE (see newTmuxLivenessProbe): a socket whose probe fails is
// indeterminate, so its sessions report alive and survive.
func dropRevivedCandidates(candidates []*session.Instance) []*session.Instance {
	isDead := newTmuxLivenessProbe(candidates)
	stillDead := make([]*session.Instance, 0, len(candidates))
	for _, inst := range candidates {
		if !isDead(inst) {
			fmt.Fprintf(os.Stderr, "skip: session %s (%s) came back to life; not deleting\n",
				shortID(inst.ID), inst.Title)
			continue
		}
		stillDead = append(stillDead, inst)
	}
	return stillDead
}

// printCleanupPreview renders the candidate table (human) or emits the preview
// payload (JSON). It never deletes.
func printCleanupPreview(
	out *CLIOutput,
	candidates []*session.Instance,
	now time.Time,
	days int,
	pinnedSkipped int,
	pruneWorktree bool,
	jsonOutput bool,
) {
	worktrees := 0
	rows := make([]map[string]interface{}, 0, len(candidates))
	for _, inst := range candidates {
		if inst.IsWorktree() {
			worktrees++
		}
		rows = append(rows, map[string]interface{}{
			"id":            inst.ID,
			"title":         inst.Title,
			"group":         inst.GroupPath,
			"status":        string(inst.Status),
			"idle_days":     int(now.Sub(cleanupLastTouched(inst)).Hours()) / 24,
			"archived":      inst.IsArchived(),
			"worktree":      inst.IsWorktree(),
			"worktree_path": inst.WorktreePath,
		})
	}

	if jsonOutput {
		out.Print("", map[string]interface{}{
			"dry_run":        true,
			"count":          len(candidates),
			"candidates":     rows,
			"skipped":        pinnedSkipped,
			"prune_worktree": pruneWorktree,
		})
		return
	}

	fmt.Printf("Cleanup candidates (dead + idle %d+ days): %d\n\n", days, len(candidates))
	fmt.Printf("  %-10s  %-30s  %-16s  %-8s  %-6s  %s\n", "ID", "TITLE", "GROUP", "STATUS", "IDLE", "WT")
	for _, inst := range candidates {
		age := humanizeAge(now.Sub(cleanupLastTouched(inst)))
		wt := ""
		if inst.IsWorktree() {
			wt = "wt"
		}
		fmt.Printf("  %-10s  %-30s  %-16s  %-8s  %-6s  %s\n",
			shortID(inst.ID), truncate(inst.Title, 30), truncate(inst.GroupPath, 16), inst.Status, age, wt)
	}
	fmt.Println()

	if worktrees > 0 {
		if pruneWorktree {
			fmt.Printf("WARNING: --prune-worktree will DELETE the git worktree directory of %d session(s)\n", worktrees)
			fmt.Println("         marked 'wt' above, discarding any uncommitted work in them.")
		} else {
			fmt.Printf("Note: %d session(s) marked 'wt' own a git worktree. Their directories are\n", worktrees)
			fmt.Println("      KEPT (registry-only). Pass --prune-worktree to delete them too.")
		}
		fmt.Println()
	}
	if pinnedSkipped > 0 {
		fmt.Printf("Skipping %d pinned session(s) — use --force to include them.\n\n", pinnedSkipped)
	}
}
