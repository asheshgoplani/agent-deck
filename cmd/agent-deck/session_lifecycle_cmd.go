package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// handleSessionStart starts a session's tmux process
func handleSessionStart(profile string, args []string) {
	fs := flag.NewFlagSet("session start", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	message := fs.String("message", "", "Initial message to send once agent is ready")
	messageShort := fs.String("m", "", "Initial message to send once agent is ready (short)")
	messageFile := fs.String("message-file", "", "Read the initial message from a file ('-' for stdin); avoids shell quoting of long prompts")
	ignoreBudget := fs.Bool("ignore-budget", false, ignoreBudgetFlagHelp)
	yoloMode := fs.Bool("yolo", false, "Enable YOLO mode when starting Gemini or Codex sessions")
	attach := fs.Bool("attach", false, "Attach to the session after starting (requires an interactive terminal)")
	noWait := fs.Bool("no-wait", false, "Return as soon as the process is spawned instead of waiting up to 3s for the tool's session id (a caller that attaches right away; the id is still captured by hooks)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session start <id|title> [options]")
		fmt.Println()
		fmt.Println("Start a session's tmux process.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session start my-project")
		fmt.Println("  agent-deck session start my-project --message \"Research MCP patterns\"")
		fmt.Println("  agent-deck session start my-project -m \"Explain this codebase\"")
		fmt.Println("  agent-deck session start my-project --message-file task.md   # long prompt from file, no shell quoting")
		fmt.Println("  git diff | agent-deck session start my-project --message-file -   # initial message from stdin")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Merge message flags
	initialMessage, err := resolveMessageInput(mergeFlags(*message, *messageShort), *messageFile, os.Stdin)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Load sessions
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if already running
	if inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is already running", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	if err := applyCLIYoloOverride(inst, *yoloMode); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// An initial message is a send: an enforced cost budget refuses it before
	// the start, and before it is queued at the group cap (as launch and the
	// registry path do). The send worker checks again when it types a queued
	// message.
	if initialMessage != "" && !*ignoreBudget {
		enforceSendBudget(profile, "start", storage, inst, instances, out, quietMode, nil)
	}

	// v1.9.1 group concurrency cap: if the target group is at its
	// max_concurrent cap, mark this session queued instead of starting.
	// The queue drains in handleSessionStop. Groups with max_concurrent<=0
	// (legacy default) skip this check entirely.
	tree := session.NewGroupTreeWithGroups(instances, groups)
	max := session.GroupMaxConcurrent(tree, inst.GroupPath)
	if session.ShouldQueue(instances, inst.GroupPath, max) {
		inst.MarkQueued(initialMessage)
		if err := saveSessionData(storage, instances, groups); err != nil {
			out.Error(fmt.Sprintf("failed to save queued state: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		out.Success(
			fmt.Sprintf("Queued session: %s (group at cap %d)", inst.Title, max),
			map[string]interface{}{
				"success":        true,
				"id":             inst.ID,
				"title":          inst.Title,
				"status":         "queued",
				"group":          inst.GroupPath,
				"max_concurrent": max,
			},
		)
		return
	}

	// Start the session (with or without initial message).
	if initialMessage != "" {
		if err := inst.StartWithMessage(initialMessage); err != nil {
			out.Error(fmt.Sprintf("failed to start session: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	} else {
		if err := inst.Start(); err != nil {
			out.Error(fmt.Sprintf("failed to start session: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}

	// #2099: Start() returning nil only means tmux accepted the spawn. Read
	// the result back before claiming success: a pane that died at once (or
	// was never created) exits non-zero with the recorded reason instead of
	// a false "Started".
	if err := inst.VerifySpawned(spawnVerifyWait); err != nil {
		failSpawnVerification(out, "start", storage, instances, groups, inst, err)
	}

	// Capture session ID from tmux env before saving to JSON
	// Claude: UUID is set by bash capture-resume pattern before exec.
	// --no-wait skips this bounded wait for a caller that attaches at once
	// (the remote TUI create path); hooks and the status loop capture the
	// id shortly after.
	if !*noWait {
		inst.PostStartSync(3 * time.Second)
	}

	// Save updated state
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	if warning := inst.ConductorRecoveryWarning(); warning != "" && !*jsonOutput {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}

	// --attach: drop the user into the freshly started session's pane. This
	// suspends the CLI into tmux and blocks until the user detaches, so the
	// normal success output below is skipped. Refused loudly (never silently)
	// without an interactive terminal or under --json; the session stays
	// started in both cases.
	if *attach {
		if *jsonOutput {
			out.Error("--attach cannot be combined with --json; session was started", ErrCodeInvalidOperation)
			exitCLI(3)
		}
		if err := attachInstanceInteractive(inst); err != nil {
			if errors.Is(err, errAttachNoTTY) {
				fmt.Fprintf(os.Stderr, "Error: %v; session was started\n", err)
				exitCLI(3)
			}
			fmt.Fprintf(os.Stderr, "Error: failed to attach: %v\n", err)
			exitCLI(1)
		}
		return
	}

	// Output success
	jsonData := map[string]interface{}{
		"success": true,
		"id":      inst.ID,
		"title":   inst.Title,
	}
	if warning := inst.ConductorRecoveryWarning(); warning != "" {
		jsonData["warning"] = warning
	}
	if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
		jsonData["tmux"] = tmuxSess.Name
	}
	if inst.ClaudeSessionID != "" {
		jsonData["claude_session_id"] = inst.ClaudeSessionID
	}
	if initialMessage != "" {
		jsonData["message"] = initialMessage
		jsonData["message_pending"] = false
		out.Success(fmt.Sprintf("Started session: %s (message sent)", inst.Title), jsonData)
	} else {
		out.Success(fmt.Sprintf("Started session: %s", inst.Title), jsonData)
	}
}

// spawnFailureJSON is the one --json shape for a spawn-failure record, shared
// by `session show`, `session start` and `session restart`.
func spawnFailureJSON(rec *session.SpawnFailureRecord) map[string]interface{} {
	return map[string]interface{}{
		"reason":       rec.Reason,
		"command":      rec.Command,
		"dying_output": rec.DyingOutput,
		"elapsed_ms":   rec.ElapsedMs,
		"ts":           rec.Timestamp,
	}
}

// spawnFailureOutput renders a failed spawn verification (#2099) as the
// human error line and the --json payload. verb is "start" or "restart".
func spawnFailureOutput(verb string, inst *session.Instance, err error) (string, map[string]interface{}) {
	data := map[string]interface{}{
		"id":    inst.ID,
		"title": inst.Title,
	}
	var spawnErr *session.SpawnFailedError
	if errors.As(err, &spawnErr) {
		data["tmux"] = spawnErr.TmuxName
		if spawnErr.Record != nil {
			data["reason"] = spawnErr.Record.Reason
			data["spawn_failure"] = spawnFailureJSON(spawnErr.Record)
		} else {
			data["reason"] = "tmux_session_missing"
		}
	} else {
		// The probe never settled (busy server, protocol mismatch): not a
		// recorded spawn failure, but not a confirmed start either.
		data["reason"] = "spawn_unverified"
	}
	return fmt.Sprintf("failed to %s session: %v", verb, err), data
}

// failSpawnVerification persists whatever Start()/Restart() changed on the
// instance (tmux name, timestamps), reports the spawn failure and exits 1.
func failSpawnVerification(out *CLIOutput, verb string, storage *session.Storage, instances []*session.Instance, groups []*session.GroupData, inst *session.Instance, err error) {
	inst.Status = session.StatusError
	if saveErr := saveSessionData(storage, instances, groups); saveErr != nil && !out.jsonMode {
		fmt.Fprintf(os.Stderr, "Warning: failed to save session state: %v\n", saveErr)
	}
	msg, data := spawnFailureOutput(verb, inst, err)
	out.ErrorWithData(msg, ErrCodeInvalidOperation, data)
	exitCLI(1)
}

// handleSessionStop stops a session process
func handleSessionStop(profile string, args []string) {
	fs := flag.NewFlagSet("session stop", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session stop <id|title> [options]")
		fmt.Println()
		fmt.Println("Stop/kill a session's process (tmux session remains).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if not running
	if !inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is not running", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	drained, err := stopInstanceAndSave(storage, inst, instances, groups)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Output success
	result := map[string]interface{}{
		"success": true,
		"id":      inst.ID,
		"title":   inst.Title,
	}
	if drained != nil {
		result["drained"] = drained.ID
		result["drained_title"] = drained.Title
	}
	out.Success(fmt.Sprintf("Stopped session: %s", inst.Title), result)

	// Journaled after the verdict, not before it: RecordSessionEvent writes
	// synchronously, so a slow health volume would otherwise delay the answer
	// the user is waiting on.
	session.RecordSessionEvent(profile, inst.ID, health.KindStop, nil)
	session.RecallNotifyInstance(inst, health.KindStop)
}

// stopInstanceAndSave is the stop path shared by `session stop` and
// `session command <id> /exit`: it kills only this instance's tmux session,
// drains one queued sibling and persists the state.
func stopInstanceAndSave(storage *session.Storage, inst *session.Instance, instances []*session.Instance, groups []*session.GroupData) (*session.Instance, error) {
	// Capture tool conversation IDs from tmux env before killing the session.
	// This ensures IDs are saved to storage even if PostStartSync timed out
	// during start (e.g., tool started late on slow WSL2 machines).
	// Must happen before Kill() because tmux show-environment fails on dead sessions.
	inst.SyncSessionIDsFromTmux()
	adoptLiveCodexIdentity(storage, inst)
	loaded := session.BeforeStopKill(storage, inst.ID)

	// Stop the session by killing the tmux session
	if err := inst.Kill(); err != nil {
		return nil, fmt.Errorf("failed to stop session: %w", err)
	}
	session.WriteStoppedAfterKill(storage, inst, loaded) // defect 317
	inst.RecordTelemetryEnd(telemetry.EndStop)

	// Save updated state before the drain: a drained start waits for its
	// agent to be ready, and this full-list save must not run after that wait.
	if err := saveSessionData(storage, instances, groups); err != nil {
		return nil, fmt.Errorf("failed to save session state: %w", err)
	}

	// v1.9.1 queue drain: a slot freed up. If the group has a cap and a
	// queued sibling is waiting, start the oldest one. Only one drain per
	// stop: if max_concurrent>=2 and multiple slots are now free, the next
	// stop drains the next entry. The drain persists only its own row.
	return drainGroupQueue(storage, inst.GroupPath, instances, groups), nil
}

// handleSessionArchive stops a session and marks it archived so it is hidden
// from active lists but retained in storage. Mirrors the TUI archive action
// (home.go archiveSession) and WebMutator.ArchiveSession.
func handleSessionArchive(profile string, args []string) {
	fs := flag.NewFlagSet("session archive", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	expectedVersion := fs.String("expected-content-version", "", "Archive only a waiting child with this exact transcript version (requires --expected-parent)")
	expectedParent := fs.String("expected-parent", "", "Archive only if the session has this parent (requires --expected-content-version)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session archive <id|title> [options]")
		fmt.Println()
		fmt.Println("Stop a session and hide it from active lists (retained in storage).")
		fmt.Println("For conditional archival, provide both --expected-parent and --expected-content-version.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// An empty identifier is a usage error, not a missing session: exit 1 (not
	// the ResolveSession NOT_FOUND exit 2, which is reserved for a genuinely
	// unknown id/title).
	if identifier == "" {
		out.Error("session <id|title> required", ErrCodeInvalidOperation)
		if !*jsonOutput {
			fs.Usage()
		}
		exitCLI(1)
	}

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	if inst.IsArchived() {
		out.Error(fmt.Sprintf("session '%s' is already archived", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	if conditionalArchiveRequested(fs) {
		adoptLiveCodexIdentity(storage, inst)
		lockedCodexID := inst.CodexSessionID
		if session.IsCodexCompatible(inst.Tool) {
			lock, err := session.AcquireCodexAcceptanceLock(lockedCodexID, codexAcceptanceLockTimeout)
			if err != nil {
				out.Error(fmt.Sprintf("cannot lock archive guard: %v", err), ErrCodeInvalidOperation)
				exitCLI(1)
			}
			defer lock.Release()
		} else if session.IsClaudeCompatible(inst.Tool) {
			lock, err := session.AcquireSendLock(inst.ID, session.SendTargetLockWait)
			if err != nil {
				out.Error(fmt.Sprintf("cannot lock archive guard: %v", err), ErrCodeInvalidOperation)
				exitCLI(1)
			}
			defer lock.Release()
		}
		if err := inst.UpdateStatus(); err != nil {
			out.Error(fmt.Sprintf("cannot refresh archive guard: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		if session.IsCodexCompatible(inst.Tool) && inst.CodexSessionID != lockedCodexID {
			out.Error("conditional archive refused: Codex identity changed while locked", ErrCodeInvalidOperation)
			exitCLI(1)
		}
		if err := validateConditionalArchive(inst, instances, *expectedParent, *expectedVersion); err != nil {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}

	// Only kill a live tmux session. Killing an already-dead session returns a
	// fatal error that would abort the archive (see idempotent-Kill history),
	// so gate on Exists() the way handleSessionStop does. Kill() sets
	// Status=stopped in memory only; persistArchivedCLI persists it below.
	//
	// Unlike handleSessionStop we deliberately do NOT SyncSessionIDsFromTmux()
	// here: archive persists via a targeted UPDATE (to survive concurrent TUI
	// writers), which cannot carry the whole-row tool-id fields the sync
	// populates. Late-discovered ids are dropped rather than saved via a
	// non-targeted write that would reintroduce the archive-clobber race. The
	// session's normal lifecycle already persists its tool ids.
	//
	// Codex is the exception: launch left its identity unpersisted, and the
	// live process is the only evidence of it, so bind it with the targeted
	// Codex write before the kill destroys that evidence (#2400).
	killed := false
	if inst.Exists() {
		adoptLiveCodexIdentity(storage, inst)
		if err := inst.Kill(); err != nil {
			out.Error(fmt.Sprintf("failed to stop session: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		killed = true
	}

	inst.SetArchivedAtThreadSafe(time.Now().UTC())
	if err := persistArchivedCLI(storage, inst, killed); err != nil {
		out.Error(fmt.Sprintf("failed to persist archive: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	out.Success(fmt.Sprintf("Archived session: %s", inst.Title), map[string]interface{}{
		"success":  true,
		"id":       inst.ID,
		"title":    inst.Title,
		"archived": true,
	})
}

// conditionalArchiveRequested reports whether either guard flag was passed,
// even with an empty value. An explicitly empty guard (for example an unset
// shell variable) must reach validateConditionalArchive and be refused rather
// than silently falling back to an unconditional archive.
func conditionalArchiveRequested(fs *flag.FlagSet) bool {
	requested := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "expected-content-version" || f.Name == "expected-parent" {
			requested = true
		}
	})
	return requested
}

func validateConditionalArchive(inst *session.Instance, peers []*session.Instance, parent, version string) error {
	if parent == "" || version == "" || inst.ParentSessionID != parent {
		return fmt.Errorf("conditional archive requires matching parent and transcript version")
	}
	if !session.IsCodexCompatible(inst.Tool) && !session.IsClaudeCompatible(inst.Tool) {
		return fmt.Errorf("conditional archive supports only Codex and Claude terminal-turn fences")
	}
	if inst.Status != session.StatusWaiting {
		return fmt.Errorf("conditional archive requires a waiting terminal worker")
	}
	currentVersion := inst.ResponseContentVersion(peers)
	if currentVersion == "" || currentVersion != version {
		return fmt.Errorf("conditional archive refused: transcript changed or exact version unavailable")
	}
	if session.IsCodexCompatible(inst.Tool) {
		response, versioned, err := inst.GetLastResponseAtVersion(peers, currentVersion)
		latestTurn, turnErr := inst.LatestCodexTurnGeneration()
		if err != nil || turnErr != nil || !versioned || latestTurn == "" ||
			response.CodexTurnGeneration != latestTurn {
			return fmt.Errorf("conditional archive refused: latest Codex turn is not the completed response")
		}
		if _, err := session.ReconcileCodexSubmissionMarker(inst.ID, inst.CodexSessionID, latestTurn, inst.CodexTurnTookInputSince); err != nil {
			return fmt.Errorf("conditional archive refused: %w", err)
		}
	} else if session.IsClaudeCompatible(inst.Tool) {
		path, err := inst.GetJSONLPathChecked(peers)
		if err != nil {
			return fmt.Errorf("conditional archive refused: cannot resolve exact Claude transcript: %w", err)
		}
		_, found, pending := session.ScanTranscriptTailForDone(path)
		if !found || pending {
			return fmt.Errorf("conditional archive refused: latest Claude turn has no flushed terminal report")
		}
	}
	return nil
}

// handleSessionUnarchive clears the archive flag without restarting tmux.
// Mirrors the TUI unarchiveSession and WebMutator.UnarchiveSession.
func handleSessionUnarchive(profile string, args []string) {
	fs := flag.NewFlagSet("session unarchive", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session unarchive <id|title> [options]")
		fmt.Println()
		fmt.Println("Restore an archived session (does not restart its process).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Empty identifier is a usage error (exit 1), mirroring archive.
	if identifier == "" {
		out.Error("session <id|title> required", ErrCodeInvalidOperation)
		if !*jsonOutput {
			fs.Usage()
		}
		exitCLI(1)
	}

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	if !inst.IsArchived() {
		out.Error(fmt.Sprintf("session '%s' is not archived", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// unarchive never kills tmux, so there is no post-kill status to persist.
	inst.SetArchivedAtThreadSafe(time.Time{})
	if err := persistArchivedCLI(storage, inst, false); err != nil {
		out.Error(fmt.Sprintf("failed to persist unarchive: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	out.Success(fmt.Sprintf("Unarchived session: %s", inst.Title), map[string]interface{}{
		"success":  true,
		"id":       inst.ID,
		"title":    inst.Title,
		"archived": false,
	})
}

// persistArchivedCLI writes the archive timestamp (and, when persistStatus is
// set, the post-kill Status) via targeted UPDATEs. It deliberately avoids
// saveSessionData: the full-save path has an external-change guard that aborts
// and reloads under concurrent writers (a running TUI), which would silently
// revert the archive. This mirrors home.go's persistArchived.
//
// persistStatus is true only when archive killed a live session: Kill() sets
// Status=stopped in memory but writes nothing to the DB, so without this the
// row keeps its pre-kill running/idle status and a later load misclassifies the
// stopped session. PersistInstanceStatusesTx is the same targeted, abort-safe
// primitive revive uses (single status column, no whole-row clobber).
func persistArchivedCLI(storage *session.Storage, inst *session.Instance, persistStatus bool) error {
	db := storage.GetDB()
	if db == nil {
		return fmt.Errorf("state database unavailable")
	}
	if persistStatus {
		if err := db.PersistInstanceStatusesTx([]statedb.InstanceStatusUpdate{
			{ID: inst.ID, Status: string(inst.Status)},
		}); err != nil {
			return err
		}
	}
	return db.SetArchived(inst.ID, inst.ArchivedAtThreadSafe())
}

// drainGroupQueue starts the oldest queued instance in groupPath when a slot
// is available. Returns the drained instance (or nil if nothing to drain).
// It persists only the drained instance's row (session.Storage.StartQueued),
// which also puts its queued message on the send queue without waiting for
// the agent. A session another drainer claimed first is left to it, and the
// next queued one is tried while the group is below its cap.
func drainGroupQueue(storage *session.Storage, groupPath string, instances []*session.Instance, groups []*session.GroupData) *session.Instance {
	tree := session.NewGroupTreeWithGroups(instances, groups)
	max := session.GroupMaxConcurrent(tree, groupPath)
	// The cap is checked on every pass, counting starting sessions too: a
	// claim lost to another drainer (or to a direct start) means that session
	// now holds a slot, and its stale copy here is marked starting.
	for !session.IsAtCap(session.CountOccupiedInGroup(instances, groupPath), max) {
		next := session.FindNextQueued(instances, groupPath)
		if next == nil {
			return nil
		}
		started, err := storage.StartQueued(next)
		if started.Warning != "" {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", started.Warning)
		}
		if err != nil {
			// Drain is best-effort. StartQueued persisted the outcome; log it
			// and don't fail the stop.
			fmt.Fprintf(os.Stderr, "queue drain failed to start %s: %v\n", next.Title, err)
			return nil
		}
		if started.Started {
			return next
		}
		// Claimed by another drainer: this copy is stale.
		next.Status = session.StatusStarting
	}
	return nil
}

// handleSessionRestart restarts a session (or all active sessions with --all)
func handleSessionRestart(profile string, args []string) {
	fs := flag.NewFlagSet("session restart", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	force := fs.Bool("force", false, "Restart even if the session is already healthy and fresh (bypasses issue #30 guard)")
	all := fs.Bool("all", false, "Restart all active sessions")
	ifIdle := fs.Bool("if-idle", false, "Restart only if the session is idle at an empty prompt, checked under the send lock (skipped otherwise); a conductor gets no recovery turn")
	envFlags := newEnvVarFlags()
	fs.Var(envFlags, "env", "Environment variable in KEY=VALUE format for the restarted process (can be repeated)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session restart [id|title] [options]")
		fmt.Println()
		fmt.Println("Restart a session. For Claude sessions, this reloads MCPs.")
		fmt.Println()
		fmt.Println("By default, a restart is skipped (no-op) when the session is already")
		fmt.Println("healthy (running/waiting/idle/starting) and was started within the last")
		fmt.Println("60 seconds. This prevents watchdog double-fires from destroying a")
		fmt.Println("just-created tmux scope (issue #30). Use --force to restart anyway.")
		fmt.Println()
		fmt.Println("A restart is also skipped when the session's agent could not authenticate")
		fmt.Println("(401 / invalid credentials): a restart cannot fix a credential, and each")
		fmt.Println("attempt races the rotating token shared by every session on this host.")
		fmt.Println("Re-authenticate (run /login), then restart — --force overrides the hold.")
		fmt.Println()
		fmt.Println("--all paces restarts with a jittered stagger, caps how many un-verified")
		fmt.Println("boots run at once, skips auth-held sessions, and STOPS early if several")
		fmt.Println("restarts in a row die on authentication (reported as auth_tripped).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session restart my-project")
		fmt.Println("  agent-deck session restart my-project --env API_URL=https://api.example.com")
		fmt.Println("  agent-deck session restart my-project --env FOO=one --env BAR=two")
		fmt.Println("  agent-deck session restart --all")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	exitOnEnvFlagError(fs, envFlags)

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	if *all {
		restartAllSessions(profile, out, storage, instances, groups, envFlags.Map())
		return
	}

	identifier := fs.Arg(0)
	if identifier == "" {
		out.Error("session identifier required (or use --all)", ErrCodeInvalidOperation)
		fs.Usage()
		exitCLI(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Issue #30: freshness guard. Skip the restart (keep the current tmux
	// scope intact) when the session is healthy and was started very
	// recently. A watchdog racing `start` → `restart` on the same session
	// must not tear down the fresh scope.
	if skip, reason := session.ShouldSkipRestart(inst, time.Now(), *force || len(envFlags.Map()) > 0); skip {
		data := map[string]interface{}{
			"success": true,
			"skipped": true,
			"reason":  reason,
			"id":      inst.ID,
			"title":   inst.Title,
		}
		out.Success(fmt.Sprintf("Skipped restart of %s: %s", inst.Title, reason), data)
		markCLINoop()
		return
	}

	// --if-idle (the idle build refresh): hold the send lock from the last
	// idle check through the restart, so a send or wake in between is either
	// seen by the check or delivered to the new process, never lost.
	if *ifIdle {
		reason := ""
		lock, lockErr := session.AcquireSendLock(inst.ID, 10*time.Second)
		if lockErr != nil {
			reason = "a send is in progress"
		} else {
			defer lock.Release()
			if blocker := inst.IdleRefreshRecheck(); blocker != "" {
				reason = "not idle: " + blocker
			}
		}
		if reason != "" {
			data := map[string]interface{}{"success": true, "skipped": true, "reason": reason, "id": inst.ID, "title": inst.Title}
			out.Success(fmt.Sprintf("Skipped restart of %s: %s", inst.Title, reason), data)
			markCLINoop()
			return
		}
		inst.SkipConductorRecoveryTurn()
		inst.SetRestartReason(session.RestartReasonIdleBuildRefresh)
	}

	// Restart the session
	if err := inst.RestartWithEnv(envFlags.Map()); err != nil {
		out.Error(fmt.Sprintf("failed to restart session: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	// #2099: confirm the new pane is actually there before reporting success.
	if err := inst.VerifySpawned(spawnVerifyWait); err != nil {
		failSpawnVerification(out, "restart", storage, instances, groups, inst, err)
	}
	// Stamp the persisted freshness marker so subsequent watchdog ticks see
	// this session as "just started" and skip (issue #30).
	inst.LastStartedAt = time.Now()
	warning := strings.TrimSpace(inst.ConsumeCodexRestartWarning() + "\n" + inst.ConductorRecoveryWarning())
	if warning != "" && !*jsonOutput {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}

	// If restart created a fresh session (no prior ID), capture the new ID
	if session.IsClaudeCompatible(inst.Tool) && inst.ClaudeSessionID == "" {
		inst.PostStartSync(3 * time.Second)
	}

	// Save updated state
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Output success
	data := map[string]interface{}{
		"success": true,
		"id":      inst.ID,
		"title":   inst.Title,
	}
	if warning != "" {
		data["warning"] = warning
	}
	out.Success(fmt.Sprintf("Restarted session: %s", inst.Title), data)

	// Journaled after the verdict, same reasoning as handleSessionStop.
	session.RecordSessionEvent(profile, inst.ID, health.KindRestart, nil)
}

// restartAllSessions restarts every active session, paced and gated by
// session.BootSweep.
//
// This is the path that turned an expired token into a fleet outage on
// 2026-07-26: it used to boot every session back-to-back with no brake, so
// during a credential failure it both wasted every restart AND had every fresh
// agent race the single rotating refresh token. The sweep now skips sessions
// already held for auth, staggers boots with jitter, caps how many unverified
// boots contend for the token at once, and stops entirely after a few
// consecutive auth-deaths with one loud message instead of burning the fleet.
func restartAllSessions(profile string, out *CLIOutput, storage *session.Storage, instances []*session.Instance, groups []*session.GroupData, env map[string]string) {
	var active []*session.Instance
	for _, inst := range instances {
		if inst.Exists() {
			active = append(active, inst)
		}
	}

	if len(active) == 0 {
		out.Error("no active sessions to restart", ErrCodeNotFound)
		exitCLI(1)
	}

	results := make(map[string]map[string]interface{}, len(active))
	// restarted collects the sessions actually restarted so they can be
	// journaled once every verdict in this batch is printed, rather than
	// inside the sweep where a slow health volume would delay every remaining
	// session's restart (same reasoning as handleSessionStop).
	restarted := make([]string, 0, len(active))

	sweep := session.NewBootSweep()
	sweepResult := sweep.Run(active, func(inst *session.Instance) error {
		result := map[string]interface{}{
			"id":    inst.ID,
			"title": inst.Title,
		}
		results[inst.ID] = result

		if !out.jsonMode {
			fmt.Printf("Restarting %s...\n", inst.Title)
		}

		if err := inst.RestartWithEnv(env); err != nil {
			errMsg := fmt.Sprintf("failed to restart session '%s': %v", inst.Title, err)
			if !out.jsonMode {
				fmt.Fprintf(os.Stderr, "  Error: %s\n", errMsg)
			}
			result["success"] = false
			result["error"] = errMsg
			return err
		}
		// #2099: a restart whose pane is already gone is a failure, not a boot.
		if err := inst.VerifySpawned(spawnVerifyWait); err != nil {
			errMsg, data := spawnFailureOutput("restart", inst, err)
			if !out.jsonMode {
				fmt.Fprintf(os.Stderr, "  Error: %s\n", errMsg)
			}
			result["success"] = false
			result["error"] = errMsg
			if sf, ok := data["spawn_failure"]; ok {
				result["spawn_failure"] = sf
			}
			return err
		}
		inst.LastStartedAt = time.Now()
		restarted = append(restarted, inst.ID)

		warning := strings.TrimSpace(inst.ConsumeCodexRestartWarning() + "\n" + inst.ConductorRecoveryWarning())
		if warning != "" && !out.jsonMode {
			fmt.Fprintf(os.Stderr, "  Warning: %s\n", warning)
		}

		// If restart created a fresh session (no prior ID), capture the new ID
		if session.IsClaudeCompatible(inst.Tool) && inst.ClaudeSessionID == "" {
			inst.PostStartSync(3 * time.Second)
		}

		result["success"] = true
		if warning != "" {
			result["warning"] = warning
		}

		if !out.jsonMode {
			fmt.Printf("  Done: %s\n", inst.Title)
		}
		return nil
	})

	ordered := restartAllSessionRecords(results, sweepResult.Attempts)
	for _, attempt := range sweepResult.Attempts {
		if attempt.Skipped && !out.jsonMode && !out.quietMode {
			fmt.Printf("Skipped %s: %s\n", attempt.Title, attempt.SkipReason)
		}
	}

	// Save updated state after all restarts
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	if sweepResult.TripMessage != "" && !out.jsonMode {
		fmt.Fprintf(os.Stderr, "\n🔒 %s\n", sweepResult.TripMessage)
	}

	if out.jsonMode {
		out.Success("", restartAllSessionsJSONPayload(len(active), sweepResult, ordered))
	} else if !out.quietMode {
		fmt.Printf("Restarted %d/%d sessions", sweepResult.Booted, len(active))
		if sweepResult.Failed > 0 {
			fmt.Printf(" (%d failed)", sweepResult.Failed)
		}
		if sweepResult.SkippedHeld > 0 {
			fmt.Printf(" (%d held for auth)", sweepResult.SkippedHeld)
		}
		if sweepResult.Abandoned > 0 {
			fmt.Printf(" (%d abandoned after auth circuit tripped)", sweepResult.Abandoned)
		}
		fmt.Println()
	}

	for _, id := range restarted {
		session.RecordSessionEvent(profile, id, health.KindRestart, nil)
	}

	if restartAllSessionsExitCode(sweepResult) != 0 {
		exitCLI(1)
	}
}

// branchCleanupHint builds the trailing "&& git branch -D ..." fragment of
// the manual-cleanup hint shown when fork-with-state cleanup partially fails.
// Returns empty string when the branch wasn't created by this operation.
func branchCleanupHint(createdBranch bool, repoRoot, branchName string) string {
	if !createdBranch {
		return ""
	}
	return fmt.Sprintf(" && git -C %s branch -D %s", shellescape.Quote(repoRoot), shellescape.Quote(branchName))
}
