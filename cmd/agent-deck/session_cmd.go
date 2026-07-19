package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/jujutsu"
	"github.com/asheshgoplani/agent-deck/internal/profile"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/ui"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

// handleSession dispatches session subcommands
func handleSession(profile string, args []string) {
	if len(args) == 0 {
		printSessionHelp()
		os.Exit(1)
	}

	switch args[0] {
	case "start":
		handleSessionStart(profile, args[1:])
	case "stop":
		handleSessionStop(profile, args[1:])
	case "remove":
		handleSessionRemove(profile, args[1:])
	case "cleanup", "prune":
		handleSessionCleanup(profile, args[1:])
	case "archive":
		handleSessionArchive(profile, args[1:])
	case "unarchive":
		handleSessionUnarchive(profile, args[1:])
	case "restart":
		handleSessionRestart(profile, args[1:])
	case "revive":
		handleSessionRevive(profile, args[1:])
	case "fork":
		handleSessionFork(profile, args[1:])
	case "handoff":
		handleSessionHandoff(profile, args[1:])
	case "attach":
		handleSessionAttach(profile, args[1:])
	case "focus":
		handleSessionFocus(profile, args[1:])
	case "show":
		handleSessionShow(profile, args[1:])
	case "current":
		handleSessionCurrent(profile, args[1:])
	case "set-parent":
		handleSessionSetParent(profile, args[1:])
	case "unset-parent":
		handleSessionUnsetParent(profile, args[1:])
	case "update":
		// Issue #974: users expect `session update <id> --no-parent` and
		// `session update <id> --parent <p>` to mirror typical CRUD verbs.
		// Route to the existing canonical handlers.
		handleSessionUpdate(profile, args[1:])
	case "set-transition-notify":
		handleSessionSetTransitionNotify(profile, args[1:])
	case "set-title-lock":
		handleSessionSetTitleLock(profile, args[1:])
	case "set":
		handleSessionSet(profile, args[1:])
	case "switch-account":
		handleSessionSwitchAccount(profile, args[1:])
	case "move", "mv":
		handleSessionMove(profile, args[1:])
	case "send":
		handleSessionSend(profile, args[1:])
	case "approve":
		handleSessionApprove(profile, args[1:])
	case "send-keys":
		handleSessionSendKeys(profile, args[1:])
	case "output":
		handleSessionOutput(profile, args[1:])
	case "children":
		handleSessionChildren(profile, args[1:])
	case "search":
		handleSessionSearch(profile, args[1:])
	case "help", "--help", "-h":
		printSessionHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown session command: %s\n", args[0])
		printSessionHelp()
		os.Exit(1)
	}
}

// printSessionHelp prints help for session commands
func printSessionHelp() {
	fmt.Println("Usage: agent-deck session <command> [options]")
	fmt.Println()
	fmt.Println("Manage individual sessions.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start <id>              Start a session's tmux process")
	fmt.Println("  stop <id>               Stop/kill session process")
	fmt.Println("  remove <id>             Remove session from registry (stopped/error only; --force to bypass)")
	fmt.Println("  cleanup [--days N]      Purge dead sessions idle N+ days (dry-run unless --yes)")
	fmt.Println("  archive <id|title>      Stop session and hide it from active lists (retained in storage)")
	fmt.Println("  unarchive <id|title>    Restore an archived session (does not restart it)")
	fmt.Println("  restart [id] [--all] [--env KEY=VALUE]  Restart session (Claude: reload MCPs)")
	fmt.Println("  revive [--all|--name]   Rebuild dead control pipes for errored sessions")
	fmt.Println("  fork <id>               Fork Claude, OpenCode, Pi, or Codex session with context")
	fmt.Println("  handoff <id>            Build a cross-tool handoff prompt from the session's conversation (read-only)")
	fmt.Println("  attach <id>             Attach to session interactively")
	fmt.Println("  focus <id> [--attach]   Signal the running TUI to select (or --attach) a session")
	fmt.Println("  show [id]               Show session details (auto-detect current if no id)")
	fmt.Println("  current                 Show current session and profile (auto-detect)")
	fmt.Println("  set <id> <field> <value>  Update session property")
	fmt.Println("  switch-account <id> <account>  Switch Claude account and migrate the conversation")
	fmt.Println("  move <id> <path>        Move session to a new path (migrates Claude history)")
	fmt.Println("  send <id> <message>     Send a message to a running session")
	fmt.Println("  approve <id> [choice]   Resolve a visible Codex approval prompt")
	fmt.Println("  output <id>             Get the last response from a session")
	fmt.Println("  children [id]           List sub-sessions with status + last completion")
	fmt.Println("  search <query>          Search message content across Claude sessions")
	fmt.Println("  set-parent <id> <parent>  Link session as sub-session of parent")
	fmt.Println("  unset-parent <id>       Remove sub-session link")
	fmt.Println("  update <id> --no-parent          Alias for unset-parent <id>")
	fmt.Println("  update <id> --parent <pid>       Alias for set-parent <id> <pid>")
	fmt.Println("  set-transition-notify <id> <on|off>  Enable/disable transition notifications")
	fmt.Println("  set-title-lock <id> <on|off>         Lock/unlock title from Claude session-name sync (#697)")
	fmt.Println()
	fmt.Println("Global Options:")
	fmt.Println("  -p, --profile <name>   Use specific profile")
	fmt.Println("  --json                 Output as JSON")
	fmt.Println("  -q, --quiet            Minimal output (exit codes only)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck session start my-project")
	fmt.Println("  agent-deck session stop abc123")
	fmt.Println("  agent-deck session restart my-project")
	fmt.Println("  agent-deck session restart --all                # Restart all active sessions")
	fmt.Println("  agent-deck session fork my-project -t \"my-project-fork\"")
	fmt.Println("  agent-deck session attach my-project")
	fmt.Println("  agent-deck session show                  # Auto-detect current session")
	fmt.Println("  agent-deck session show my-project --json")
	fmt.Println("  agent-deck session set-parent sub-task main-project  # Make sub-task a sub-session")
	fmt.Println("  agent-deck session unset-parent sub-task             # Remove sub-session link")
	fmt.Println("  agent-deck session set-transition-notify worker off    # Suppress notifications")
	fmt.Println("  agent-deck session set-transition-notify worker on     # Re-enable notifications")
	fmt.Println("  agent-deck session set-title-lock SCRUM-351 on         # Prevent Claude from renaming it")
	fmt.Println("  agent-deck session set-title-lock SCRUM-351 off        # Re-enable title sync")
	fmt.Println("  agent-deck session output my-project                 # Get last response from session")
	fmt.Println("  agent-deck session output my-project --json          # Get response as JSON")
	fmt.Println("  agent-deck session archive my-project                # Stop and hide the session")
	fmt.Println("  agent-deck session unarchive my-project              # Restore an archived session")
	fmt.Println()
	fmt.Println("Set command fields:")
	fmt.Println("  title              Session title")
	fmt.Println("  path               Project path")
	fmt.Println("  command            Command to run")
	fmt.Println("  tool               Tool type (claude, gemini, shell, etc.)")
	fmt.Println("  wrapper            Wrapper command (use {command} to include tool command)")
	fmt.Println("  claude-session-id  Claude conversation ID (for fork/resume)")
	fmt.Println("  gemini-session-id  Gemini conversation ID (for resume)")
	fmt.Println()
	fmt.Println("Set examples:")
	fmt.Println("  agent-deck session set my-project title \"New Title\"")
	fmt.Println("  agent-deck session set my-project claude-session-id \"abc123-def456\"")
	fmt.Println("  agent-deck session set my-project tool claude")
	fmt.Println("  agent-deck session set my-project wrapper \"nvim +'terminal {command}'\"")
}

// handleSessionStart starts a session's tmux process
func handleSessionStart(profile string, args []string) {
	fs := flag.NewFlagSet("session start", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	message := fs.String("message", "", "Initial message to send once agent is ready")
	messageShort := fs.String("m", "", "Initial message to send once agent is ready (short)")
	messageFile := fs.String("message-file", "", "Read the initial message from a file ('-' for stdin); avoids shell quoting of long prompts")
	yoloMode := fs.Bool("yolo", false, "Enable YOLO mode when starting Gemini or Codex sessions")
	attach := fs.Bool("attach", false, "Attach to the session after starting (requires an interactive terminal)")

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

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Merge message flags
	initialMessage, err := resolveMessageInput(mergeFlags(*message, *messageShort), *messageFile, os.Stdin)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Load sessions
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if already running
	if inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is already running", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if err := applyCLIYoloOverride(inst, *yoloMode); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// v1.9.1 group concurrency cap: if the target group is at its
	// max_concurrent cap, mark this session queued instead of starting.
	// The queue drains in handleSessionStop. Groups with max_concurrent<=0
	// (legacy default) skip this check entirely.
	tree := session.NewGroupTreeWithGroups(instances, groups)
	max := session.GroupMaxConcurrent(tree, inst.GroupPath)
	if session.ShouldQueue(instances, inst.GroupPath, max) {
		inst.Status = session.StatusQueued
		if err := saveSessionData(storage, instances, groups); err != nil {
			out.Error(fmt.Sprintf("failed to save queued state: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
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

	// Start the session (with or without initial message)
	if initialMessage != "" {
		if err := inst.StartWithMessage(initialMessage); err != nil {
			out.Error(fmt.Sprintf("failed to start session: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	} else {
		if err := inst.Start(); err != nil {
			out.Error(fmt.Sprintf("failed to start session: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Capture session ID from tmux env before saving to JSON
	// Claude: UUID is set by bash capture-resume pattern before exec
	inst.PostStartSync(3 * time.Second)

	// Save updated state
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// --attach: drop the user into the freshly started session's pane. This
	// suspends the CLI into tmux and blocks until the user detaches, so the
	// normal success output below is skipped. Refused loudly (never silently)
	// without an interactive terminal or under --json; the session stays
	// started in both cases.
	if *attach {
		if *jsonOutput {
			out.Error("--attach cannot be combined with --json; session was started", ErrCodeInvalidOperation)
			os.Exit(3)
		}
		if err := attachInstanceInteractive(inst); err != nil {
			if errors.Is(err, errAttachNoTTY) {
				fmt.Fprintf(os.Stderr, "Error: %v; session was started\n", err)
				os.Exit(3)
			}
			fmt.Fprintf(os.Stderr, "Error: failed to attach: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Output success
	jsonData := map[string]interface{}{
		"success": true,
		"id":      inst.ID,
		"title":   inst.Title,
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

// handleSessionStop stops a session process
func handleSessionStop(profile string, args []string) {
	fs := flag.NewFlagSet("session stop", flag.ExitOnError)
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

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if not running
	if !inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is not running", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Capture tool conversation IDs from tmux env before killing the session.
	// This ensures IDs are saved to storage even if PostStartSync timed out
	// during start (e.g., tool started late on slow WSL2 machines).
	// Must happen before Kill() because tmux show-environment fails on dead sessions.
	inst.SyncSessionIDsFromTmux()

	// Stop the session by killing the tmux session
	if err := inst.Kill(); err != nil {
		out.Error(fmt.Sprintf("failed to stop session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// v1.9.1 queue drain: a slot freed up. If the group has a cap and a
	// queued sibling is waiting, start the oldest one. Only one drain per
	// stop: if max_concurrent>=2 and multiple slots are now free, the next
	// stop drains the next entry.
	drained := drainGroupQueue(inst.GroupPath, instances, groups)

	// Save updated state
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
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
}

// handleSessionArchive stops a session and marks it archived so it is hidden
// from active lists but retained in storage. Mirrors the TUI archive action
// (home.go archiveSession) and WebMutator.ArchiveSession.
func handleSessionArchive(profile string, args []string) {
	fs := flag.NewFlagSet("session archive", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session archive <id|title> [options]")
		fmt.Println()
		fmt.Println("Stop a session and hide it from active lists (retained in storage).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
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
		os.Exit(1)
	}

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	if inst.IsArchived() {
		out.Error(fmt.Sprintf("session '%s' is already archived", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
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
	killed := false
	if inst.Exists() {
		if err := inst.Kill(); err != nil {
			out.Error(fmt.Sprintf("failed to stop session: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		killed = true
	}

	inst.ArchivedAt = time.Now().UTC()
	if err := persistArchivedCLI(storage, inst, killed); err != nil {
		out.Error(fmt.Sprintf("failed to persist archive: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Archived session: %s", inst.Title), map[string]interface{}{
		"success":  true,
		"id":       inst.ID,
		"title":    inst.Title,
		"archived": true,
	})
}

// handleSessionUnarchive clears the archive flag without restarting tmux.
// Mirrors the TUI unarchiveSession and WebMutator.UnarchiveSession.
func handleSessionUnarchive(profile string, args []string) {
	fs := flag.NewFlagSet("session unarchive", flag.ExitOnError)
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

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
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
		os.Exit(1)
	}

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	if !inst.IsArchived() {
		out.Error(fmt.Sprintf("session '%s' is not archived", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// unarchive never kills tmux, so there is no post-kill status to persist.
	inst.ArchivedAt = time.Time{}
	if err := persistArchivedCLI(storage, inst, false); err != nil {
		out.Error(fmt.Sprintf("failed to persist unarchive: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
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
	return db.SetArchived(inst.ID, inst.ArchivedAt)
}

// drainGroupQueue starts the oldest queued instance in groupPath when a slot
// is available. Returns the drained instance (or nil if nothing to drain).
// The caller is responsible for persisting state afterward.
func drainGroupQueue(groupPath string, instances []*session.Instance, groups []*session.GroupData) *session.Instance {
	tree := session.NewGroupTreeWithGroups(instances, groups)
	max := session.GroupMaxConcurrent(tree, groupPath)
	if session.IsAtCap(session.CountRunningInGroup(instances, groupPath), max) {
		return nil
	}
	next := session.FindNextQueued(instances, groupPath)
	if next == nil {
		return nil
	}
	if err := next.Start(); err != nil {
		// Drain is best-effort. Surface as queued + log; don't fail the stop.
		next.Status = session.StatusError
		fmt.Fprintf(os.Stderr, "queue drain failed to start %s: %v\n", next.Title, err)
		return nil
	}
	return next
}

// handleSessionRestart restarts a session (or all active sessions with --all)
func handleSessionRestart(profile string, args []string) {
	fs := flag.NewFlagSet("session restart", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	force := fs.Bool("force", false, "Restart even if the session is already healthy and fresh (bypasses issue #30 guard)")
	all := fs.Bool("all", false, "Restart all active sessions")
	envFlags := make(envVarFlags)
	fs.Var(&envFlags, "env", "Environment variable in KEY=VALUE format for the restarted process (can be repeated)")

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
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session restart my-project")
		fmt.Println("  agent-deck session restart my-project --env API_URL=https://api.example.com")
		fmt.Println("  agent-deck session restart my-project --env FOO=one --env BAR=two")
		fmt.Println("  agent-deck session restart --all")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	if *all {
		restartAllSessions(out, storage, instances, groups, envFlags)
		return
	}

	identifier := fs.Arg(0)
	if identifier == "" {
		out.Error("session identifier required (or use --all)", ErrCodeInvalidOperation)
		fs.Usage()
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Issue #30: freshness guard. Skip the restart (keep the current tmux
	// scope intact) when the session is healthy and was started very
	// recently. A watchdog racing `start` → `restart` on the same session
	// must not tear down the fresh scope.
	if skip, reason := session.ShouldSkipRestart(inst, time.Now(), *force || len(envFlags) > 0); skip {
		data := map[string]interface{}{
			"success": true,
			"skipped": true,
			"reason":  reason,
			"id":      inst.ID,
			"title":   inst.Title,
		}
		out.Success(fmt.Sprintf("Skipped restart of %s: %s", inst.Title, reason), data)
		return
	}

	// Restart the session
	if err := inst.RestartWithEnv(envFlags); err != nil {
		out.Error(fmt.Sprintf("failed to restart session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	// Stamp the persisted freshness marker so subsequent watchdog ticks see
	// this session as "just started" and skip (issue #30).
	inst.LastStartedAt = time.Now()
	warning := inst.ConsumeCodexRestartWarning()
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
		os.Exit(1)
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
}

// restartAllSessions restarts every active session one by one.
func restartAllSessions(out *CLIOutput, storage *session.Storage, instances []*session.Instance, groups []*session.GroupData, env map[string]string) {
	var active []*session.Instance
	for _, inst := range instances {
		if inst.Exists() {
			active = append(active, inst)
		}
	}

	if len(active) == 0 {
		out.Error("no active sessions to restart", ErrCodeNotFound)
		os.Exit(1)
	}

	var results []map[string]interface{}
	var failed int

	for _, inst := range active {
		result := map[string]interface{}{
			"id":    inst.ID,
			"title": inst.Title,
		}

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
			failed++
			results = append(results, result)
			continue
		}
		inst.LastStartedAt = time.Now()

		warning := inst.ConsumeCodexRestartWarning()
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
		results = append(results, result)

		if !out.jsonMode {
			fmt.Printf("  Done: %s\n", inst.Title)
		}
	}

	// Save updated state after all restarts
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if out.jsonMode {
		out.Success("", map[string]interface{}{
			"success":   failed == 0,
			"total":     len(active),
			"restarted": len(active) - failed,
			"failed":    failed,
			"sessions":  results,
		})
	} else if !out.quietMode {
		fmt.Printf("Restarted %d/%d sessions", len(active)-failed, len(active))
		if failed > 0 {
			fmt.Printf(" (%d failed)", failed)
		}
		fmt.Println()
	}

	if failed > 0 {
		os.Exit(1)
	}
}

// sessionForkBeforeStartHook is nil in production. Tests assign it to inspect
// the fully-prepared fork before tmux Start() mutates the environment. When
// the hook is set, handleSessionFork invokes it and returns immediately —
// no tmux session, no persistence, no Start(). This lets contract tests
// assert option propagation without spawning real sessions.
var sessionForkBeforeStartHook func(parent *session.Instance, forked *session.Instance, state git.WorktreeStateOptions)

// branchCleanupHint builds the trailing "&& git branch -D ..." fragment of
// the manual-cleanup hint shown when fork-with-state cleanup partially fails.
// Returns empty string when the branch wasn't created by this operation.
func branchCleanupHint(createdBranch bool, repoRoot, branchName string) string {
	if !createdBranch {
		return ""
	}
	return fmt.Sprintf(" && git -C %s branch -D %s", shellescape.Quote(repoRoot), shellescape.Quote(branchName))
}

// handleSessionFork forks a supported tool session
func handleSessionFork(profile string, args []string) {
	fs := flag.NewFlagSet("session fork", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	title := fs.String("title", "", "Title for forked session")
	titleShort := fs.String("t", "", "Title for forked session (short)")
	group := fs.String("group", "", "Group for forked session")
	groupShort := fs.String("g", "", "Group for forked session (short)")
	worktreeBranch := fs.String("w", "", "Create fork in a worktree/workspace for branch (git or jj)")
	worktreeBranchLong := fs.String("worktree", "", "Create fork in a worktree/workspace for branch (git or jj)")
	newBranch := fs.Bool("b", false, "Create new branch/bookmark (use with --worktree)")
	newBranchLong := fs.Bool("new-branch", false, "Create new branch/bookmark")
	withState := fs.Bool("with-state", false, "Carry parent's uncommitted working state into the new worktree/workspace (git or jj; #1029/#1305, requires -w)")
	withStateGitignored := fs.Bool("with-state-and-gitignored", false, "Like --with-state, plus gitignored files (e.g. .env). Implies --with-state. Requires -w.")
	sandbox := fs.Bool("sandbox", false, "Run forked session in Docker sandbox")
	sandboxImage := fs.String("sandbox-image", "", "Docker image for sandbox (overrides config default)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session fork <id|title> [options]")
		fmt.Println()
		fmt.Println("Fork a Claude, OpenCode, Pi, or Codex session with conversation context.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session fork my-project")
		fmt.Println("  agent-deck session fork my-project -t \"my-fork\"")
		fmt.Println("  agent-deck session fork my-project -t \"my-fork\" -g \"experiments\"")
		fmt.Println("  agent-deck session fork my-project -w fork/experiment")
		fmt.Println("  agent-deck session fork my-project -w fork/new-idea -b")
		fmt.Println("  agent-deck session fork my-project -w fork/wip -b --with-state")
		fmt.Println("  agent-deck session fork my-project -w fork/wip -b --with-state-and-gitignored")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Merge short and long flags
	forkTitle := mergeFlags(*title, *titleShort)
	forkGroup := mergeFlags(*group, *groupShort)

	// Load sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Verify this tool has a session-fork implementation.
	isClaudeFork := session.IsClaudeCompatible(inst.Tool)
	isPiFork := inst.Tool == "pi"
	isOpenCodeFork := inst.Tool == "opencode"
	isCodexFork := session.IsCodexCompatible(inst.Tool)
	if !isClaudeFork && !isPiFork && !isOpenCodeFork && !isCodexFork {
		out.Error(
			fmt.Sprintf("session '%s' is not a forkable session (tool: %s)", inst.Title, inst.Tool),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}

	// Try to capture Claude session ID from tmux if missing (handles pre-fix sessions).
	if isClaudeFork && inst.ClaudeSessionID == "" && inst.Exists() {
		inst.PostStartSync(2 * time.Second)
	}

	// Verify it can be forked.
	if !inst.CanFork() {
		out.Error(
			fmt.Sprintf("session '%s' cannot be forked: no resumable session for tool %s", inst.Title, inst.Tool),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}

	// Default title if not provided. An explicitly passed -t/--title is user
	// intent and gets TitleLocked below (mirrors the TUI fork dialog); the
	// auto-generated "<title>-fork" default keeps the #572 name sync enabled
	// (mirrors quick fork).
	explicitTitle := forkTitle != ""
	if !explicitTitle {
		forkTitle = inst.Title + "-fork"
	}

	// Default group to parent's group
	if forkGroup == "" {
		forkGroup = inst.GroupPath
	}

	// Resolve worktree flags
	wtBranch := *worktreeBranch
	if *worktreeBranchLong != "" {
		wtBranch = *worktreeBranchLong
	}
	createNewBranch := *newBranch || *newBranchLong

	// #1029: --with-state-and-gitignored implies --with-state.
	wantState := *withState || *withStateGitignored
	if wantState && wtBranch == "" {
		out.Error("--with-state requires an explicit worktree branch (-w/--worktree)", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Handle worktree creation
	var opts *session.ClaudeOptions
	var worktreeType string
	if wtBranch != "" {
		backend, err := detectAndCreateBackend(inst.ProjectPath)
		if err != nil {
			out.Error(fmt.Sprintf("%v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		worktreeType = string(backend.Type())
		repoRoot := backend.RepoDir()

		// --with-state* anchors the new worktree/workspace at the parent's
		// committed point and materializes the parent's working state. git and
		// jujutsu both support it (jj since #1305); any other backend can't, so
		// reject early. The git-direct collision gate and anchoring below are
		// reached only on the git branch; jujutsu has its own branch.
		if wantState && backend.Type() != vcs.TypeGit && backend.Type() != vcs.TypeJujutsu {
			out.Error("--with-state is not supported for this repository's VCS backend", ErrCodeInvalidOperation)
			os.Exit(1)
		}

		// Apply configured branch prefix before validation/existence checks
		wtSettings := session.GetWorktreeSettings()
		wtBranch = wtSettings.ApplyBranchPrefix(wtBranch)

		// Destination gate (BUG-01/08). With-state forks create a NEW branch
		// anchored at the parent's HEAD, so they must refuse any pre-existing
		// branch or worktree — one well-defined collision gate, evaluated before
		// path computation and the legacy reuse check. Non-with-state forks keep
		// upstream's "branch must already exist (use -b to create)" contract.
		// These two are mutually exclusive: with-state requires the branch ABSENT,
		// the else-branch requires it PRESENT — never flatten them.
		if wantState && backend.Type() == vcs.TypeGit {
			if err := git.ValidateForkWithStateDestination(repoRoot, wtBranch); err != nil {
				var collErr *git.DestinationCollisionError
				if errors.As(err, &collErr) {
					switch collErr.Kind {
					case git.CollisionWorktreeExists:
						out.Error(fmt.Sprintf("branch '%s' already has a worktree at %s; choose a new destination branch for --with-state", collErr.Branch, collErr.Path), ErrCodeInvalidOperation)
					case git.CollisionBranchExists:
						out.Error(fmt.Sprintf("branch '%s' already exists; choose a new destination branch for --with-state", collErr.Branch), ErrCodeInvalidOperation)
					default:
						out.Error(collErr.Error(), ErrCodeInvalidOperation)
					}
					os.Exit(1)
				}
				out.Error(fmt.Sprintf("failed to validate destination: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
		} else if wantState {
			// jujutsu with-state: a fresh destination bookmark is required, mirroring
			// the git collision gate. (Workspace-path collision is caught by the
			// os.Stat check below.)
			exists, bmErr := jujutsu.BookmarkExists(repoRoot, wtBranch)
			if bmErr != nil {
				out.Error(fmt.Sprintf("failed to validate destination: %v", bmErr), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			if exists {
				out.Error(fmt.Sprintf("bookmark '%s' already exists; choose a new destination branch for --with-state", wtBranch), ErrCodeInvalidOperation)
				os.Exit(1)
			}
		} else if !createNewBranch && !backend.BranchExists(wtBranch) {
			out.Error(fmt.Sprintf("branch '%s' does not exist (use -b to create)", wtBranch), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		worktreePath := backend.WorktreePath(vcs.WorktreePathOptions{
			Branch:    wtBranch,
			Location:  wtSettings.DefaultLocation,
			SessionID: git.GeneratePathID(),
			Template:  wtSettings.Template(),
		})

		// Check for an existing worktree for this branch before creating a new
		// one. Routed through the backend so jujutsu reuse keeps working. The
		// with-state path is validated above and must NEVER reuse a worktree, so
		// the reuse assignment is gated on !wantState (BUG-01/08).
		reuseExistingWorktree := false
		if !wantState {
			if existingPath, err := backend.GetWorktreeForBranch(wtBranch); err == nil && existingPath != "" {
				fmt.Fprintf(os.Stderr, "Reusing existing worktree at %s for branch %s\n", existingPath, wtBranch)
				worktreePath = existingPath
				reuseExistingWorktree = true
			}
		}
		if !reuseExistingWorktree {
			if _, statErr := os.Stat(worktreePath); statErr == nil {
				out.Error(fmt.Sprintf("worktree path already exists: %s", worktreePath), ErrCodeInvalidOperation)
				os.Exit(1)
			}

			if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
				out.Error(fmt.Sprintf("failed to create directory: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}

			var setupErr error
			if wantState && backend.Type() == vcs.TypeGit {
				//
				// Mid-op refusal: surface an actionable error BEFORE creating the
				// worktree, so the user sees the exact abort command for their
				// parent instead of MaterializeWipFromParent's terse backstop
				// wording (which fires AFTER worktree creation and triggers
				// cleanup-on-error). The backstop in materialize_wip.go's
				// refuseUnsafeParentState still covers detectErr != nil cases — we
				// fall through silently there.
				if kind, detectErr := git.DetectInProgressOperation(inst.ProjectPath); detectErr == nil && kind != "" {
					abortCmd := map[string]string{
						"rebase":      "git rebase --abort",
						"merge":       "git merge --abort",
						"cherry-pick": "git cherry-pick --abort",
						"revert":      "git revert --abort",
						"bisect":      "git bisect reset",
					}[kind]
					out.Error(fmt.Sprintf("parent session is mid-%s; finish or abort the %s before forking with state (cd %s && %s)",
						kind, kind, inst.ProjectPath, abortCmd), ErrCodeInvalidOperation)
					os.Exit(1)
				}

				if git.HasSubmodules(inst.ProjectPath) {
					fmt.Fprintln(os.Stderr, "Warning: submodules detected — copied as files, not recursed (parent's submodule states preserved)")
				}

				// Capture parent's HEAD so linked-worktree parents anchor correctly.
				parentHead, hcErr := git.HeadCommit(inst.ProjectPath)
				if hcErr != nil {
					out.Error(fmt.Sprintf("failed to resolve parent session HEAD: %v", hcErr), ErrCodeInvalidOperation)
					os.Exit(1)
				}

				createdBranch, cwErr := git.CreateWorktreeAtStartPoint(repoRoot, worktreePath, wtBranch, parentHead)
				if cwErr != nil {
					out.Error(fmt.Sprintf("worktree creation failed: %v", cwErr), ErrCodeInvalidOperation)
					os.Exit(1)
				}

				// Materialize parent state, with cleanup-on-error.
				if matErr := git.MaterializeWipFromParent(inst.ProjectPath, worktreePath, *withStateGitignored); matErr != nil {
					var cleanupErrs []string
					if rmErr := git.RemoveWorktree(repoRoot, worktreePath, true); rmErr != nil {
						cleanupErrs = append(cleanupErrs, fmt.Sprintf("worktree remove failed: %v", rmErr))
					}
					if createdBranch {
						if brErr := exec.Command("git", "-C", repoRoot, "branch", "-D", wtBranch).Run(); brErr != nil {
							cleanupErrs = append(cleanupErrs, fmt.Sprintf("branch delete failed: %v", brErr))
						}
					}
					if len(cleanupErrs) == 0 {
						out.Error(fmt.Sprintf("failed to materialize parent state: %v; new worktree cleaned up", matErr), ErrCodeInvalidOperation)
					} else {
						out.Error(fmt.Sprintf("failed to materialize parent state: %v; cleanup also failed (%s); manual cleanup required: rm -rf %s%s",
							matErr,
							strings.Join(cleanupErrs, "; "),
							shellescape.Quote(worktreePath),
							branchCleanupHint(createdBranch, repoRoot, wtBranch),
						), ErrCodeInvalidOperation)
					}
					os.Exit(1)
				}

				// Continue upstream's wrapper tail: worktreeinclude + setup hook.
				if inclErr := git.ProcessWorktreeInclude(repoRoot, worktreePath, os.Stderr); inclErr != nil {
					fmt.Fprintf(os.Stderr, "worktreeinclude: %v\n", inclErr)
				}
				setupErr = git.RunWorktreeSetupAfterCreate(repoRoot, worktreePath, os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
			} else if wantState {
				// jujutsu with-state (#1305): anchor the new workspace at the
				// parent's committed point (@-) and materialize its working copy.
				parentBase, pbErr := jujutsu.WorkingCopyParentRevision(inst.ProjectPath)
				if pbErr != nil {
					out.Error(fmt.Sprintf("failed to resolve parent session committed anchor: %v", pbErr), ErrCodeInvalidOperation)
					os.Exit(1)
				}
				if cwErr := jujutsu.CreateWorkspaceAtRevision(repoRoot, worktreePath, wtBranch, parentBase); cwErr != nil {
					out.Error(fmt.Sprintf("workspace creation failed: %v", cwErr), ErrCodeInvalidOperation)
					os.Exit(1)
				}
				if matErr := jujutsu.MaterializeWipFromParent(inst.ProjectPath, worktreePath, *withStateGitignored); matErr != nil {
					var cleanupErrs []string
					if rmErr := backend.RemoveWorktree(worktreePath, true); rmErr != nil {
						cleanupErrs = append(cleanupErrs, fmt.Sprintf("workspace forget failed: %v", rmErr))
					}
					if brErr := backend.DeleteBranch(wtBranch, true); brErr != nil {
						cleanupErrs = append(cleanupErrs, fmt.Sprintf("bookmark delete failed: %v", brErr))
					}
					if len(cleanupErrs) == 0 {
						out.Error(fmt.Sprintf("failed to materialize parent state: %v; new workspace cleaned up", matErr), ErrCodeInvalidOperation)
					} else {
						out.Error(fmt.Sprintf("failed to materialize parent state: %v; cleanup also failed (%s); manual cleanup required: rm -rf %s",
							matErr, strings.Join(cleanupErrs, "; "), shellescape.Quote(worktreePath)), ErrCodeInvalidOperation)
					}
					os.Exit(1)
				}
				if *withStateGitignored && !jujutsu.SupportsGitignoredCopy(inst.ProjectPath) {
					fmt.Fprintln(os.Stderr, "Warning: forked without gitignored files: this jj repo has no git metadata to copy them")
				}
			} else if backend.Type() == vcs.TypeGit {
				// Non-with-state git path: upstream's combined wrapper unchanged.
				var cwErr error
				setupErr, cwErr = git.CreateWorktreeWithStateAndSetup(
					repoRoot, worktreePath, wtBranch,
					git.WorktreeStateOptions{},
					os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
				if cwErr != nil {
					out.Error(fmt.Sprintf("worktree creation failed: %v", cwErr), ErrCodeInvalidOperation)
					os.Exit(1)
				}
			} else {
				// Non-git backend (jujutsu): with-state already rejected above.
				if err := backend.CreateWorktree(worktreePath, wtBranch); err != nil {
					out.Error(fmt.Sprintf("worktree creation failed: %v", err), ErrCodeInvalidOperation)
					os.Exit(1)
				}
			}
			if setupErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: worktree setup script failed: %v\n", setupErr)
			}
		}

		userConfig, _ := session.LoadUserConfig()
		opts = session.NewClaudeOptions(userConfig)
		opts.WorkDir = worktreePath
		opts.WorktreePath = worktreePath
		opts.WorktreeRepoRoot = repoRoot
		opts.WorktreeBranch = wtBranch
	}

	// Create the forked instance
	var forkedInst *session.Instance
	forkedInst, _, err = inst.CreateForkedInstanceForTool(forkTitle, forkGroup, opts)
	if err != nil {
		out.Error(fmt.Sprintf("failed to create fork: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if explicitTitle {
		forkedInst.TitleLocked = true
	}

	if worktreeType != "" {
		forkedInst.WorktreeType = worktreeType
	}

	// Apply sandbox config if requested.
	if *sandbox {
		forkedInst.Sandbox = session.NewSandboxConfig(*sandboxImage)
	}

	// Test seam: when set, capture the fully-prepared fork before tmux Start()
	// mutates the environment and return early. Production runs leave the hook
	// nil, so this is a no-op outside of tests.
	if sessionForkBeforeStartHook != nil {
		sessionForkBeforeStartHook(inst, forkedInst, git.WorktreeStateOptions{WithState: wantState, WithIgnored: *withStateGitignored})
		return
	}

	// Start the forked session
	if err := forkedInst.Start(); err != nil {
		out.Error(fmt.Sprintf("failed to start forked session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Capture forked session's new session ID
	forkedInst.PostStartSync(3 * time.Second)

	// Add to instances
	instances = append(instances, forkedInst)

	// Rebuild group tree and ensure group exists
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	forkCfg, _ := session.LoadUserConfig()
	groupTree.DefaultMaxConcurrent = forkCfg.GroupDefaults.MaxConcurrent
	if forkedInst.GroupPath != "" {
		groupTree.CreateGroupPath(forkedInst.GroupPath)
	}

	// Save
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Output success
	out.Success(
		fmt.Sprintf("Forked session: %s -> %s (%s)", inst.Title, forkedInst.Title, TruncateID(forkedInst.ID)),
		map[string]interface{}{
			"success":   true,
			"parent_id": inst.ID,
			"new_id":    forkedInst.ID,
			"new_title": forkedInst.Title,
		},
	)
}

// handleSessionAttach attaches to a session interactively
func handleSessionAttach(profile string, args []string) {
	fs := flag.NewFlagSet("session attach", flag.ExitOnError)

	detachByte := ui.ResolvedDetachByte(session.GetHotkeyOverrides())
	detachLabel := ui.DetachByteLabel(detachByte)

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session attach <id|title>")
		fmt.Println()
		fmt.Println("Attach to a session interactively.")
		fmt.Printf("Press %s to detach.\n", detachLabel)
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)

	// Load sessions
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Resolve session (allow current session detection)
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", errMsg)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if session exists
	if !inst.Exists() {
		fmt.Fprintf(os.Stderr, "Error: session '%s' is not running\n", inst.Title)
		os.Exit(1)
	}

	// Attach to the session
	tmuxSession := inst.GetTmuxSession()
	if tmuxSession == nil {
		fmt.Fprintf(os.Stderr, "Error: no tmux session for '%s'\n", inst.Title)
		os.Exit(1)
	}

	// Create context for attach
	ctx := context.Background()

	if err := tmuxSession.Attach(ctx, detachByte); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to attach: %v\n", err)
		os.Exit(1)
	}
}

// errFocusNotFound signals that `session focus` was given an id absent from the
// current profile. Callers map it to a distinct (exit 2) "not found" code.
var errFocusNotFound = errors.New("session not found")

// liveSwitcher attempts to move the attached terminal straight into a session's
// tmux pane (the Ctrl+b N quick-switch path), so a notification click lands you
// in the session even while the TUI is paused inside another attach. Injected
// into routeFocus so the attached-vs-list routing is unit-testable without a
// real tmux server.
type liveSwitcher interface {
	// switchInto moves the client attached to inst's tmux server into inst's
	// pane. Returns switched=true iff a client was attached and moved; false
	// (no error) when inst has no live pane or no client is attached on its
	// socket, signalling the caller to fall back to a focus_request row.
	switchInto(inst *session.Instance) (bool, error)
}

// tmuxLiveSwitcher is the production liveSwitcher. It mirrors the Ctrl+b N
// quick-switch: tmux switch-client + an ack-signal write, both of which work
// while the Bubble Tea TUI is suspended during tea.Exec.
type tmuxLiveSwitcher struct{}

func (tmuxLiveSwitcher) switchInto(inst *session.Instance) (bool, error) {
	if inst == nil || !inst.Exists() {
		return false, nil
	}
	ts := inst.GetTmuxSession()
	if ts == nil || ts.Name == "" {
		return false, nil
	}
	// Query/switch on the target's own socket: switch-client only works when the
	// attached client and the target session share a tmux server, so a client on
	// a different socket simply yields switched=false and the focus_request
	// fallback takes over (matches the Ctrl+b N same-server limitation).
	return tmux.SwitchAttachedClients(inst.TmuxSocketName, ts.Name, inst.ID)
}

// clientDetacher detaches the user's currently-attached tmux client when the
// live switch could not move it. switch-client cannot cross tmux servers, so a
// notification target on a different socket than the attached session yields
// switched=false; detaching that client makes the paused TUI resume and consume
// the focus_request (attaching the target on its own socket) instead of the
// switch silently waiting for a manual Ctrl+Q. Injected into routeFocus so the
// cross-socket routing is unit-testable without a real tmux server.
type clientDetacher interface {
	// detachClientsOn detaches every real (non-control) client attached on any
	// of sockets. Returns detached=true iff at least one client was detached.
	detachClientsOn(sockets []string) (bool, error)
}

// tmuxClientDetacher is the production clientDetacher.
type tmuxClientDetacher struct{}

func (tmuxClientDetacher) detachClientsOn(sockets []string) (bool, error) {
	return tmux.DetachClientsOnSockets(sockets...)
}

// findFocusInstance returns the instance with the given id, or nil.
func findFocusInstance(instances []*session.Instance, id string) *session.Instance {
	for _, inst := range instances {
		if inst.ID == id {
			return inst
		}
	}
	return nil
}

// focusOtherSockets returns the distinct tmux socket names used by instances,
// excluding exclude (the target's own socket, where the live switch already
// looked). Order-preserving and deduped. These are the sockets that may host
// the user's currently-attached client when the target lives elsewhere.
func focusOtherSockets(instances []*session.Instance, exclude string) []string {
	seen := map[string]bool{exclude: true}
	var out []string
	for _, inst := range instances {
		s := inst.TmuxSocketName
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// routeFocus drives `session focus <id>`. With --attach it first tries a live
// switch-while-attached (so the click lands you straight in the session even
// when the TUI is paused inside another attach); if no client is attached to the
// target's tmux server it falls back to the foreground focus_request row, which
// the TUI consumes on its next tick. Without --attach it always writes the
// (select-only) focus_request. Split out of handleSessionFocus so it is
// unit-testable without os.Exit; switcher is injected for the same reason.
func routeFocus(db *statedb.StateDB, instances []*session.Instance, id string, nowNano int64, attach bool, switcher liveSwitcher, detacher clientDetacher) error {
	if id == "" {
		return fmt.Errorf("session focus requires an <id>")
	}
	inst := findFocusInstance(instances, id)
	if inst == nil {
		return fmt.Errorf("%w: %q", errFocusNotFound, id)
	}
	if attach && switcher != nil {
		// The contract reserves (false, nil) for the benign fallback (no live
		// pane / no client attached on the socket); a non-nil error is a real
		// tmux failure that must surface, not be silently swallowed into the
		// fallback path.
		switched, err := switcher.switchInto(inst)
		if err != nil {
			return err
		}
		if switched {
			return nil
		}
		// Live switch couldn't move the client: it's attached to a different tmux
		// server than the target (switch-client can't cross servers). Write the
		// focus_request FIRST, then detach that client so agent-deck's paused
		// attach returns and the resumed TUI consumes the row on its next tick —
		// attaching the target on its own socket, instead of waiting for a manual
		// Ctrl+Q. When no client is attached elsewhere (e.g. the TUI is already in
		// the list view), the detach is a harmless no-op and the row is consumed
		// normally.
		if detacher != nil {
			if err := session.WriteFocusRequestAttach(db, id, nowNano, attach); err != nil {
				return err
			}
			// The focus_request is already persisted, so a detach failure still
			// leaves the row to be consumed on the next tick — but surface it so
			// the immediate-switch path's failure isn't hidden.
			if _, err := detacher.detachClientsOn(focusOtherSockets(instances, inst.TmuxSocketName)); err != nil {
				return err
			}
			return nil
		}
	}
	return session.WriteFocusRequestAttach(db, id, nowNano, attach)
}

// resolveAndWriteFocus validates id against the loaded instances and, on a
// match, writes the focus_request row. Retained as the switcher-less path
// (select-only / no live switch); delegates to routeFocus.
func resolveAndWriteFocus(db *statedb.StateDB, instances []*session.Instance, id string, nowNano int64, attach bool) error {
	return routeFocus(db, instances, id, nowNano, attach, nil, nil)
}

// handleSessionFocus signals the running TUI (same profile) to select <id> on
// its next poll. Fire-and-forget: no stdout on success. Unknown id exits 2.
// With --attach, the TUI opens/attaches the session instead of only selecting it.
func handleSessionFocus(profile string, args []string) {
	fs := flag.NewFlagSet("session focus", flag.ExitOnError)
	attach := fs.Bool("attach", false, "Open/attach the session, not just select it")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session focus <id> [--attach]")
		fmt.Println()
		fmt.Println("Signal the running agent-deck TUI (same profile) to reveal and")
		fmt.Println("select the session with the given instance id on its next refresh.")
		fmt.Println("With --attach, the TUI opens/attaches the session (as if you")
		fmt.Println("pressed Enter on it) instead of only moving the cursor.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	id := fs.Arg(0)

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	db := storage.GetDB()
	if db == nil {
		fmt.Fprintln(os.Stderr, "Error: no state database available")
		os.Exit(1)
	}

	var switcher liveSwitcher
	var detacher clientDetacher
	if *attach {
		switcher = tmuxLiveSwitcher{}
		detacher = tmuxClientDetacher{}
	}
	if err := routeFocus(db, instances, id, time.Now().UnixNano(), *attach, switcher, detacher); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if errors.Is(err, errFocusNotFound) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// handleSessionShow shows session details
func handleSessionShow(profile string, args []string) {
	fs := flag.NewFlagSet("session show", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session show [id|title] [options]")
		fmt.Println()
		fmt.Println("Show session details. If no ID is provided, auto-detects current session.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session (allow current session detection)
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		// If no identifier was provided and we're in tmux, try fallback detection
		if identifier == "" && os.Getenv("TMUX") != "" {
			// First try current profile
			inst = findSessionByTmux(instances)
			if inst == nil {
				// Search ALL profiles for matching tmux session
				var foundProfile string
				inst, foundProfile = findSessionByTmuxAcrossProfiles()
				if inst != nil && foundProfile != profile {
					// Found in a different profile - show which profile
					// (jsonData will include the profile info)
					profile = foundProfile
				}
			}
			if inst == nil {
				// Still not found, show raw tmux info
				showTmuxSessionInfo(out, *jsonOutput)
				return
			}
		} else {
			out.Error(errMsg, errCode)
			if errCode == ErrCodeNotFound {
				os.Exit(2)
			}
			os.Exit(1)
		}
	}

	// Warm tmux pane-title cache + load hook status so `session show --json`
	// reports the same Status the TUI and /api/menu do (issue #610).
	session.RefreshInstancesForCLIStatus([]*session.Instance{inst})
	// Update status
	_ = inst.UpdateStatus()

	// Get MCP info if Claude session
	var mcpInfo *session.MCPInfo
	if session.IsClaudeCompatible(inst.Tool) {
		mcpInfo = inst.GetMCPInfo()
	}

	// Prepare JSON output
	jsonData := map[string]interface{}{
		"id":                   inst.ID,
		"title":                inst.Title,
		"profile":              profile,
		"status":               StatusString(inst.Status),
		"path":                 inst.ProjectPath,
		"group":                inst.GroupPath,
		"parent_session_id":    inst.ParentSessionID,
		"parent_project_path":  inst.ParentProjectPath,
		"no_transition_notify": inst.NoTransitionNotify,
		"title_locked":         inst.TitleLocked,
		"tool":                 inst.Tool,
		"created_at":           inst.CreatedAt.Format(time.RFC3339),
	}
	// Honest Status v2: additive substate refinement (omit when none so the
	// existing keys stay byte-stable for consumers that don't expect it).
	if sub := string(inst.Substate()); sub != "" {
		jsonData["substate"] = sub
	}
	modelInfo := inst.LaunchModelInfo()
	addModelInfoJSON(jsonData, modelInfo)
	addAutoNameJSON(jsonData, inst)

	if inst.Command != "" {
		jsonData["command"] = inst.Command
	}

	if session.IsClaudeCompatible(inst.Tool) {
		jsonData["claude_session_id"] = inst.ClaudeSessionID
		jsonData["can_fork"] = inst.CanFork()
		jsonData["can_restart"] = inst.CanRestart()

		if mcps := mcpInfoForJSON(mcpInfo); mcps != nil {
			jsonData["mcps"] = mcps
		}

		// Always include channels for claude sessions — omitting when empty
		// would make absence-of-field ambiguous with absence-of-value. Match
		// the `list --json` emitter which surfaces this field unconditionally.
		if len(inst.Channels) > 0 {
			jsonData["channels"] = inst.Channels
		}

		// Plugins (RFC docs/rfc/PLUGIN_ATTACH.md §10.5) — surface when
		// non-empty so downstream tooling can introspect per-session
		// enabledPlugins state without parsing the scratch settings.json.
		if len(inst.Plugins) > 0 {
			jsonData["plugins"] = inst.Plugins
		}
		// Surface the auto-link opt-out (RFC §4.7) when set, so tooling
		// can distinguish "user disabled auto-link" from "no plugins".
		if inst.PluginChannelLinkDisabled {
			jsonData["plugin_channel_link_disabled"] = true
		}
		// AutoLinkedChannels (RFC §4.7, G4/C2 fix) — internal-ish state
		// for ownership tracking, but exposing in JSON helps downstream
		// tooling distinguish auto-linked vs user-managed channels.
		if len(inst.AutoLinkedChannels) > 0 {
			jsonData["auto_linked_channels"] = inst.AutoLinkedChannels
		}
	}

	if tmuxSession := inst.GetTmuxSession(); tmuxSession != nil {
		jsonData["tmux_session"] = tmuxSession.Name
	}

	// #1580: surface a spawn-failure diagnostic when the session errored at
	// startup (bare "error" with no pane). Include the structured record in
	// --json so tooling can read it too.
	spawnFailure := inst.SpawnFailure()
	if spawnFailure != nil {
		jsonData["spawn_failure"] = map[string]interface{}{
			"reason":       spawnFailure.Reason,
			"command":      spawnFailure.Command,
			"dying_output": spawnFailure.DyingOutput,
			"elapsed_ms":   spawnFailure.ElapsedMs,
			"ts":           spawnFailure.Timestamp,
		}
	}

	// Build human-readable output
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Session: %s\n", inst.Title))
	sb.WriteString(fmt.Sprintf("Profile: %s\n", profile))
	sb.WriteString(fmt.Sprintf("ID:      %s\n", inst.ID))
	sb.WriteString(fmt.Sprintf("Status:  %s %s\n", StatusSymbol(inst.Status), StatusString(inst.Status)))
	sb.WriteString(fmt.Sprintf("Path:    %s\n", FormatPath(inst.ProjectPath)))

	if inst.GroupPath != "" {
		sb.WriteString(fmt.Sprintf("Group:   %s\n", inst.GroupPath))
	}

	sb.WriteString(fmt.Sprintf("Tool:    %s\n", inst.Tool))
	if modelInfo.ModelID != "" {
		if modelInfo.Model != "" {
			sb.WriteString(fmt.Sprintf("Model:   %s\n", modelInfo.Model))
		}
		if modelInfo.Version != "" {
			sb.WriteString(fmt.Sprintf("Version: %s\n", modelInfo.Version))
		}
		sb.WriteString(fmt.Sprintf("ModelID: %s\n", modelInfo.ModelID))
	} else if session.SupportsLaunchModel(inst.Tool) {
		sb.WriteString("Model:   tool default\n")
	}

	if inst.Command != "" {
		sb.WriteString(fmt.Sprintf("Command: %s\n", inst.Command))
	}

	if session.IsClaudeCompatible(inst.Tool) {
		if inst.ClaudeSessionID != "" {
			truncatedID := inst.ClaudeSessionID
			if len(truncatedID) > 36 {
				truncatedID = truncatedID[:36] + "..."
			}
			canForkStr := "no"
			if inst.CanFork() {
				canForkStr = "yes"
			}
			sb.WriteString(fmt.Sprintf("Claude:  session_id=%s (can fork: %s)\n", truncatedID, canForkStr))
		} else {
			sb.WriteString("Claude:  no session ID detected\n")
		}

		if mcpInfo != nil && mcpInfo.HasAny() {
			var mcpParts []string
			for _, name := range mcpInfo.Local() {
				mcpParts = append(mcpParts, name+" (local)")
			}
			for _, name := range mcpInfo.Global {
				mcpParts = append(mcpParts, name+" (global)")
			}
			for _, name := range mcpInfo.Project {
				mcpParts = append(mcpParts, name+" (project)")
			}
			sb.WriteString(fmt.Sprintf("MCPs:    %s\n", strings.Join(mcpParts, ", ")))
		}

		// Channels and Plugins (RFC docs/rfc/PLUGIN_ATTACH.md). Surfaced
		// for claude sessions so users can verify per-session topology
		// without parsing state.db or the scratch settings.json.
		if len(inst.Channels) > 0 {
			sb.WriteString(fmt.Sprintf("Channels:%s\n", " "+strings.Join(inst.Channels, ", ")))
		}
		if len(inst.Plugins) > 0 {
			sb.WriteString(fmt.Sprintf("Plugins: %s\n", strings.Join(inst.Plugins, ", ")))
			if inst.PluginChannelLinkDisabled {
				sb.WriteString("         (auto-channel-link disabled — RFC §4.7)\n")
			}
		}
	}

	if inst.NoTransitionNotify {
		sb.WriteString("Notify:  transition events suppressed\n")
	}
	sb.WriteString(fmt.Sprintf("Created: %s\n", inst.CreatedAt.Format("2006-01-02 15:04:05")))

	if !inst.LastAccessedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("Accessed: %s\n", inst.LastAccessedAt.Format("2006-01-02 15:04:05")))
	}

	if inst.Exists() {
		tmuxSession := inst.GetTmuxSession()
		if tmuxSession != nil {
			sb.WriteString(fmt.Sprintf("Tmux:    %s\n", tmuxSession.Name))
		}
	}

	// #1580: print the spawn-failure block so `session show` on an errored
	// session explains why it died instead of leaving the user with a bare
	// "error".
	if spawnFailure != nil {
		sb.WriteString("\n")
		sb.WriteString(spawnFailure.FormatForDisplay())
	}

	out.Print(sb.String(), jsonData)
}

func mcpInfoForJSON(mcpInfo *session.MCPInfo) map[string]interface{} {
	if mcpInfo == nil || !mcpInfo.HasAny() {
		return nil
	}
	return map[string]interface{}{
		"local":   mcpInfo.Local(),
		"global":  mcpInfo.Global,
		"project": mcpInfo.Project,
	}
}

// handleSessionSet updates a session property
func handleSessionSet(profile string, args []string) {
	fs := flag.NewFlagSet("session set", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session set <id|title> <field> <value> [options]")
		fmt.Println()
		fmt.Println("Update a session property.")
		fmt.Println()
		fmt.Println("Fields:")
		fmt.Println("  title              Session title")
		fmt.Println("  path               Project path")
		fmt.Println("  command            Command to run")
		fmt.Println("  tool               Tool type (claude, gemini, shell, etc.)")
		fmt.Println("  wrapper            Wrapper command (use {command} to include tool command)")
		fmt.Println("  channels           Comma-separated plugin channel ids (claude only)")
		fmt.Printf("  plugins            Comma-separated plugin catalog names (claude only) — see [plugins.<name>] in %s\n", effectiveUserConfigPathForHelp())
		fmt.Println("  extra-args         Extra claude CLI tokens (claude only; use `-- --flag value` for tokens starting with -; persisted plaintext — no secrets)")
		fmt.Println("  model              Per-session model override (e.g. opus/sonnet/haiku or a gemini model); persists across restart (#1436). Empty clears it.")
		fmt.Println("  color              Optional TUI row tint: '#RRGGBB' or ANSI '0'..'255' or '' (issue #391)")
		fmt.Println("  claude-session-id  Claude conversation ID")
		fmt.Println("  gemini-session-id  Gemini conversation ID")
		fmt.Println("  account            Named account slot (#924) — resolves via [profiles.<account>.claude].config_dir; restart required")
		fmt.Println("  idle-timeout       Auto-stop after no tmux output for this duration (#1143; Go duration: 30m, 1h, 24h; 0 disables)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session set my-project title \"New Title\"")
		fmt.Println("  agent-deck session set my-project claude-session-id \"abc123-def456\"")
		fmt.Println("  agent-deck session set my-project path /new/path/to/project")
		fmt.Println("  agent-deck session set my-project wrapper \"nvim +'terminal {command}'\"")
		fmt.Println("  agent-deck session set my-project color \"#ff00aa\"     # truecolor hex tint")
		fmt.Println("  agent-deck session set my-project color 203              # ANSI 256-palette pink")
		fmt.Println("  agent-deck session set my-project color \"\"              # clear (opt-out)")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if fs.NArg() < 3 {
		fs.Usage()
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	field := fs.Arg(1)
	value := fs.Arg(2)
	// For extra-args: accept an arbitrary number of positional tokens after
	// the field name. Use `--` terminator so Go's flag package leaves tokens
	// starting with `-` alone, e.g.:
	//   agent-deck session set <id> extra-args -- --model opus
	extraArgTokens := fs.Args()[2:]
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// #924 follow-up: the conversation follows the account. Capture the old
	// account's config dir before SetField mutates resolution.
	var preAccountConfigDir string
	if field == session.FieldAccount && inst.Tool == "claude" {
		preAccountConfigDir = session.GetClaudeConfigDirForInstance(inst)
	}

	// Delegate to session.SetField so CLI and TUI share validation. The
	// extraArgTokens slice carries pre-tokenized argv for extra-args (CLI
	// preserves values with spaces); SetField ignores it for other fields.
	oldValue, postCommit, setErr := session.SetField(inst, field, value, extraArgTokens)
	if setErr != nil {
		out.Error(setErr.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}
	// CLI holds no lock — run tmux side effects inline. TUI defers them
	// until after instancesMu.Unlock.
	if postCommit != nil {
		postCommit()
	}

	// Copy the conversation into the new account's config dir so the
	// restart-required switch resumes with full context. Copy-only; a fresh
	// session (no conversation yet) is not an error.
	if preAccountConfigDir != "" {
		targetDir := session.GetClaudeConfigDirForInstance(inst)
		if migrated, merr := session.MigrateConversationFrom(inst, preAccountConfigDir, targetDir); merr != nil {
			if !errors.Is(merr, session.ErrNoConversation) {
				fmt.Fprintf(os.Stderr, "Warning: account set, but conversation not migrated: %v\n", merr)
			}
		} else if migrated != "" && !quietMode && !*jsonOutput {
			fmt.Printf("Conversation migrated to %s\n", migrated)
		}
	}

	// Save
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Output success
	out.Success(fmt.Sprintf("Updated %s: %q -> %q", field, oldValue, value), map[string]interface{}{
		"success":   true,
		"id":        inst.ID,
		"title":     inst.Title,
		"field":     field,
		"old_value": oldValue,
		"new_value": value,
	})

	maybeEmitSessionSetTelegramWarnings(os.Stderr, session.GetClaudeConfigDirForGroup(inst.GroupPath), inst, field)
}

// maybeEmitSessionSetTelegramWarnings is the post-mutation telegram-topology
// hook for `agent-deck session set` (v1.7.22 / #658). Gated to wrapper and
// channels — other fields are silent. claudeCfgDir lets tests inject a temp
// dir without touching the real ~/.claude lookup.
func maybeEmitSessionSetTelegramWarnings(out io.Writer, claudeCfgDir string, inst *session.Instance, field string) {
	if field != "wrapper" && field != "channels" {
		return
	}
	globalTelegramEnabled, _ := readTelegramGloballyEnabled(claudeCfgDir)
	emitTelegramWarnings(out, session.TelegramValidatorInput{
		GlobalEnabled:   globalTelegramEnabled,
		SessionChannels: inst.Channels,
		SessionWrapper:  inst.Wrapper,
	})
}

// loadSessionData loads storage and session data for a profile
// The Storage.LoadWithGroups() method already handles tmux reconnection internally
func loadSessionData(profile string) (*session.Storage, []*session.Instance, []*session.GroupData, error) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	instances, groupsData, err := storage.LoadWithGroups()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to load sessions: %w", err)
	}

	// LoadWithGroups reconnects tmux sessions with lazy loading.
	// Status uses cached values from JSON; session IDs are not synced at load time.

	return storage, instances, groupsData, nil
}

// saveSessionData saves session data with groups, preserving stored group metadata (sort_order).
func saveSessionData(storage *session.Storage, instances []*session.Instance, groups []*session.GroupData) error {
	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	return storage.SaveWithGroups(instances, groupTree)
}

// findSessionByTmuxAcrossProfiles searches all profiles for a session matching current tmux session
// Returns the instance and the profile it was found in
func findSessionByTmuxAcrossProfiles() (*session.Instance, string) {
	profiles, err := session.ListProfiles()
	if err != nil {
		return nil, ""
	}

	for _, p := range profiles {
		_, instances, _, err := loadSessionData(p)
		if err != nil {
			continue
		}
		if inst := findSessionByTmux(instances); inst != nil {
			return inst, p
		}
	}
	return nil, ""
}

// findSessionByTmux tries to find a session by matching tmux session name or working directory
func findSessionByTmux(instances []*session.Instance) *session.Instance {
	// Get current tmux session name
	cmd := exec.Command("tmux", "display-message", "-p", "#{session_name}\t#{pane_current_path}")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	parts := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(parts) < 2 {
		return nil
	}

	sessionName := parts[0]
	currentPath := parts[1]

	// Parse agent-deck session name: agentdeck_<title>_<id>
	if strings.HasPrefix(sessionName, "agentdeck_") {
		// Extract title (everything between agentdeck_ and the last _id)
		withoutPrefix := strings.TrimPrefix(sessionName, "agentdeck_")
		lastUnderscore := strings.LastIndex(withoutPrefix, "_")
		if lastUnderscore > 0 {
			title := withoutPrefix[:lastUnderscore]

			// Try to find by title
			for _, inst := range instances {
				if strings.EqualFold(inst.Title, title) {
					return inst
				}
			}

			// Try to find by sanitized title (replace - with space, etc.)
			normalizedTitle := strings.ReplaceAll(title, "-", " ")
			for _, inst := range instances {
				if strings.EqualFold(inst.Title, normalizedTitle) {
					return inst
				}
			}

			// For agentdeck sessions, we have the title - don't fall back to path matching
			// as that could match a different session with same path in another profile
			return nil
		}
	}

	// Try to find by path (only for non-agentdeck tmux sessions)
	for _, inst := range instances {
		if inst.ProjectPath == currentPath {
			return inst
		}
	}

	return nil
}

// showTmuxSessionInfo shows information about the current tmux session (unregistered)
func showTmuxSessionInfo(out *CLIOutput, jsonOutput bool) {
	// Get tmux session info
	cmd := exec.Command("tmux", "display-message", "-p",
		"#{session_name}\t#{pane_current_path}\t#{session_created}\t#{window_name}")
	output, err := cmd.Output()
	if err != nil {
		out.Error("failed to get tmux session info", ErrCodeNotFound)
		os.Exit(1)
	}

	parts := strings.Split(strings.TrimSpace(string(output)), "\t")
	sessionName := ""
	currentPath := ""
	windowName := ""
	if len(parts) >= 1 {
		sessionName = parts[0]
	}
	if len(parts) >= 2 {
		currentPath = parts[1]
	}
	if len(parts) >= 4 {
		windowName = parts[3]
	}

	// Parse title from session name
	title := sessionName
	idFragment := ""
	if strings.HasPrefix(sessionName, "agentdeck_") {
		withoutPrefix := strings.TrimPrefix(sessionName, "agentdeck_")
		lastUnderscore := strings.LastIndex(withoutPrefix, "_")
		if lastUnderscore > 0 {
			title = withoutPrefix[:lastUnderscore]
			idFragment = withoutPrefix[lastUnderscore+1:]
		}
	}

	jsonData := map[string]interface{}{
		"tmux_session": sessionName,
		"title":        title,
		"path":         currentPath,
		"window":       windowName,
		"registered":   false,
	}
	if idFragment != "" {
		jsonData["id_fragment"] = idFragment
	}

	var sb strings.Builder
	sb.WriteString("⚠ Session not registered in agent-deck\n")
	sb.WriteString(fmt.Sprintf("Tmux:    %s\n", sessionName))
	sb.WriteString(fmt.Sprintf("Title:   %s\n", title))
	if idFragment != "" {
		sb.WriteString(fmt.Sprintf("ID:      %s (stale)\n", idFragment))
	}
	sb.WriteString(fmt.Sprintf("Path:    %s\n", FormatPath(currentPath)))
	if windowName != "" {
		sb.WriteString(fmt.Sprintf("Window:  %s\n", windowName))
	}
	sb.WriteString("\nTo register this session:\n")
	sb.WriteString(fmt.Sprintf("  agent-deck add -t \"%s\" -g <group> -c claude %s\n", title, currentPath))

	out.Print(sb.String(), jsonData)
}

// handleSessionSetParent links a session as a sub-session of another
func handleSessionSetParent(profile string, args []string) {
	fs := flag.NewFlagSet("session set-parent", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	// #786: post-hoc set-parent must not silently rewrite the child's
	// group. Inheritance is opt-in via this flag.
	inheritGroup := fs.Bool("inherit-group", false,
		"Also rewrite child's group to match parent's (off by default; #786)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session set-parent <session> <parent> [--inherit-group]")
		fmt.Println()
		fmt.Println("Link a session as a sub-session of another session.")
		fmt.Println("The session's group is preserved by default; pass --inherit-group")
		fmt.Println("to also adopt the parent's group.")
		fmt.Println("This works for any session, including those created with --no-parent.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}

	sessionID := fs.Arg(0)
	parentID := fs.Arg(1)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve the session to be linked
	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Resolve the parent session
	parentInst, errMsg, errCode := ResolveSession(parentID, instances)
	if parentInst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Validate: can't set self as parent
	if inst.ID == parentInst.ID {
		out.Error("cannot set session as its own parent", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Validate: parent can't be a sub-session (single level only)
	if parentInst.IsSubSession() {
		out.Error("cannot set parent to a sub-session (single level only)", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Validate: session can't already have sub-sessions
	for _, other := range instances {
		if other.ParentSessionID == inst.ID {
			out.Error(
				fmt.Sprintf("session '%s' already has sub-sessions, cannot become a sub-session", inst.Title),
				ErrCodeInvalidOperation,
			)
			os.Exit(1)
		}
	}

	// Set parent (with project path for --add-dir access). Group is only
	// rewritten on explicit --inherit-group opt-in; see #786.
	inst.SetParentWithPath(parentInst.ID, parentInst.ProjectPath)
	if *inheritGroup {
		inst.GroupPath = parentInst.GroupPath
	}

	// Save
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Linked '%s' as sub-session of '%s'", inst.Title, parentInst.Title), map[string]interface{}{
		"success":         true,
		"session_id":      inst.ID,
		"session_title":   inst.Title,
		"parent_id":       parentInst.ID,
		"parent_title":    parentInst.Title,
		"group":           inst.GroupPath,
		"group_inherited": *inheritGroup,
	})
}

// resolveSessionUpdateAlias maps `session update <id>` invocations with
// CRUD-style flags onto the existing canonical handlers. Returns the
// canonical verb (`unset-parent` or `set-parent`) and the rewritten args
// that handler expects.
//
// Issue #974: `session update <id> --no-parent` should behave the same as
// `session unset-parent <id>`; `session update <id> --parent <pid>` should
// behave the same as `session set-parent <id> <pid>`. If neither flag is
// present we route to the generic `set` handler so the verb stays useful
// for other field updates.
//
// Pure function — no I/O, safe to unit test.
func resolveSessionUpdateAlias(args []string) (canonical string, newArgs []string) {
	hasNoParent := false
	hasParent := false
	parentVal := ""
	filtered := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--no-parent" || a == "-no-parent":
			hasNoParent = true
		case a == "--parent" || a == "-parent":
			if i+1 < len(args) {
				parentVal = args[i+1]
				i++
			}
			hasParent = true
		case strings.HasPrefix(a, "--parent="):
			parentVal = strings.TrimPrefix(a, "--parent=")
			hasParent = true
		case strings.HasPrefix(a, "-parent="):
			parentVal = strings.TrimPrefix(a, "-parent=")
			hasParent = true
		default:
			filtered = append(filtered, a)
		}
	}

	switch {
	case hasNoParent:
		// `set-parent` and `--no-parent` together is contradictory; prefer
		// the explicit detach (`--no-parent`) — matches the user's stated
		// intent in the issue reproducer.
		return "unset-parent", filtered
	case hasParent:
		return "set-parent", append(filtered, parentVal)
	default:
		return "set", filtered
	}
}

// handleSessionUpdate dispatches `session update <id> [flags]` to the
// appropriate canonical handler. See resolveSessionUpdateAlias for the
// mapping rationale.
func handleSessionUpdate(profile string, args []string) {
	canonical, rewritten := resolveSessionUpdateAlias(args)
	switch canonical {
	case "unset-parent":
		handleSessionUnsetParent(profile, rewritten)
	case "set-parent":
		handleSessionSetParent(profile, rewritten)
	default:
		handleSessionSet(profile, rewritten)
	}
}

// handleSessionUnsetParent removes the sub-session link
func handleSessionUnsetParent(profile string, args []string) {
	fs := flag.NewFlagSet("session unset-parent", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session unset-parent <session>")
		fmt.Println()
		fmt.Println("Remove the sub-session link from a session.")
		fmt.Println("The session will remain in its current group.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	sessionID := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve the session
	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if it's actually a sub-session
	if !inst.IsSubSession() {
		out.Error(fmt.Sprintf("session '%s' is not a sub-session", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Get parent title for output
	var parentTitle string
	for _, other := range instances {
		if other.ID == inst.ParentSessionID {
			parentTitle = other.Title
			break
		}
	}

	// Clear parent
	inst.ClearParent()

	// Save
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(
		fmt.Sprintf("Removed sub-session link from '%s' (was linked to '%s')", inst.Title, parentTitle),
		map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"former_parent": parentTitle,
		},
	)
}

// handleSessionSetTransitionNotify enables or disables transition notifications for a session
func handleSessionSetTransitionNotify(profile string, args []string) {
	fs := flag.NewFlagSet("session set-transition-notify", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session set-transition-notify <session> <on|off>")
		fmt.Println()
		fmt.Println("Enable or disable transition event notifications for a session.")
		fmt.Println("When off, the transition daemon will not send tmux messages to the")
		fmt.Println("parent session when this session changes status (e.g., running → waiting).")
		fmt.Println("This does not affect the parent link itself.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session set-transition-notify worker off")
		fmt.Println("  agent-deck session set-transition-notify worker on")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}

	sessionID := fs.Arg(0)
	value := strings.ToLower(strings.TrimSpace(fs.Arg(1)))
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	var suppress bool
	switch value {
	case "on":
		suppress = false
	case "off":
		suppress = true
	default:
		out.Error(fmt.Sprintf("invalid value %q: must be 'on' or 'off'", value), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return
	}

	inst.NoTransitionNotify = suppress

	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	stateStr := "on"
	if suppress {
		stateStr = "off"
	}
	out.Success(fmt.Sprintf("Transition notifications for '%s': %s", inst.Title, stateStr), map[string]interface{}{
		"success":              true,
		"session_id":           inst.ID,
		"session_title":        inst.Title,
		"no_transition_notify": suppress,
	})
}

// handleSessionSetTitleLock toggles Instance.TitleLocked (#697). When on, the
// claude-hook name-sync path (applyClaudeTitleSync) is a no-op for this
// session, preserving the conductor-assigned title across Claude renames.
func handleSessionSetTitleLock(profile string, args []string) {
	fs := flag.NewFlagSet("session set-title-lock", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session set-title-lock <session> <on|off|true|false>")
		fmt.Println()
		fmt.Println("Lock or unlock a session's title from Claude session-name sync (#697).")
		fmt.Println("When locked, Claude's --name / /rename will not overwrite the")
		fmt.Println("agent-deck title. Conductors rely on this so semantic titles like")
		fmt.Println("'SCRUM-351' survive Claude's auto-generated summaries.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session set-title-lock SCRUM-351 on")
		fmt.Println("  agent-deck session set-title-lock SCRUM-351 off")
		fmt.Println("  agent-deck session set-title-lock worker true")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}

	sessionID := fs.Arg(0)
	value := strings.ToLower(strings.TrimSpace(fs.Arg(1)))
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	var locked bool
	switch value {
	case "on", "true", "1", "yes":
		locked = true
	case "off", "false", "0", "no":
		locked = false
	default:
		out.Error(fmt.Sprintf("invalid value %q: must be 'on' or 'off' (also true/false/1/0)", value), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return
	}

	inst.TitleLocked = locked

	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	stateStr := "off"
	if locked {
		stateStr = "on"
	}
	out.Success(fmt.Sprintf("Title lock for '%s': %s", inst.Title, stateStr), map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"title_locked":  locked,
	})
}

// fetchHookDrivenStatus reloads the target from storage and reports the same
// hook-driven status string that `agent-deck list --json` shows. `session send
// --defer-if-busy` polls this so its hold gate keys off the turn-finished
// Stop-hook signal (a true edge) rather than WaitForAgentReady's pane-diff
// readiness heuristic, which false-positives to idle during tool calls and
// thinking pauses (#1578).
//
// It mirrors handleList's status pipeline exactly: reload -> warm caches +
// cold-load hook files via RefreshInstancesForCLIStatus -> UpdateStatus. The
// reload each poll is deliberate: a fresh OS process has no StatusFileWatcher,
// so the only way to observe the target's newest hook edge is to re-read it
// from disk.
func fetchHookDrivenStatus(profile, sessionRef string) (string, error) {
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return "", err
	}
	inst, errMsg, _ := ResolveSession(sessionRef, instances)
	if inst == nil {
		return "", fmt.Errorf("%s", errMsg)
	}
	// Cold-load the on-disk hook file into the instance — a fresh CLI process
	// has no StatusFileWatcher, so the target's newest hook edge only reaches us
	// by re-reading it from disk each poll.
	session.RefreshInstancesForCLIStatus([]*session.Instance{inst})
	// Prefer the FRESH hook-driven signal. It is the true turn-finished edge
	// (Claude's UserPromptSubmit hook -> "running", Stop hook -> "waiting") and,
	// unlike UpdateStatus, is not gated on a live tmux handle — exactly the
	// property #1578 needs so the hold gate keys off "turn finished" rather than
	// a pane-diff heuristic. Fall back to the full list --json pipeline when no
	// fresh hook signal exists (non-hook tools, or a stale/absent hook file).
	if hs, fresh := inst.GetHookStatus(); fresh && hs != "" {
		return hs, nil
	}
	_ = inst.UpdateStatus()
	return StatusString(inst.Status), nil
}

// handleSessionSend sends a message to a running session
// Waits for the agent to be ready before sending (Claude, Gemini, etc.)
func handleSessionSend(profile string, args []string) {
	fs := flag.NewFlagSet("session send", flag.ExitOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")
	noWait := fs.Bool("no-wait", false, "Don't wait for agent to be ready (send immediately)")
	wait := fs.Bool("wait", false, "Block until agent finishes processing, then print output")
	stream := fs.Bool("stream", false, "Stream JSONL events (Claude only) to stdout instead of returning a snapshot")
	draft := fs.Bool("draft", false, "Pre-fill the prompt without submitting (incompatible with --wait/--stream/--no-wait)")
	messageFile := fs.String("message-file", "", "Read the message from a file ('-' for stdin) instead of a positional argument; avoids shell quoting of long prompts")
	deferIfBusy := fs.Bool("defer-if-busy", false, "Hold delivery until the target is idle (turn-finished, hook-driven) instead of interrupting a mid-generation turn (incompatible with --no-wait)")
	deferTimeout := fs.Duration("defer-timeout", 30*time.Minute, "Max time --defer-if-busy holds a busy target before dropping the message with a non-zero exit")
	timeout := fs.Duration("timeout", 10*time.Minute, "Max time to wait for the agent to become ready and (with --wait) to finish processing")
	streamIdle := fs.Duration("stream-idle", 10*time.Second, "Max idle time before --stream aborts with error")
	streamCharBudget := fs.Int("stream-char-budget", 4000, "Char budget for text flush in --stream mode")
	streamToolBudget := fs.Int("stream-tool-budget", 3, "Tool-event budget for text flush in --stream mode")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session send <id|title> <message> [options]")
		fmt.Println()
		fmt.Println("Send a message to a running session.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session send my-project \"Summarize recent changes\"")
		fmt.Println("  agent-deck session send my-project \"run tests\" --wait")
		fmt.Println("  agent-deck session send my-project \"quick ping\" --no-wait")
		fmt.Println("  agent-deck session send my-project \"trace progress\" --stream")
		fmt.Println("  agent-deck session send my-project \"cwd: /path/to/dir\" --draft")
		fmt.Println("  agent-deck session send my-project --message-file answer.md   # long reply from file")
		fmt.Println("  git diff | agent-deck session send my-project --message-file -   # message from stdin")
		fmt.Println("  agent-deck session send parent \"child done\" --defer-if-busy --defer-timeout 30m")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	remaining := fs.Args()

	out := NewCLIOutput(*jsonOutput, *quiet)

	needPositionalMessage := *messageFile == ""
	if len(remaining) < 1 || (needPositionalMessage && len(remaining) < 2) {
		fs.Usage()
		out.Error("session and message (or --message-file) are required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if *stream && *wait {
		out.Error("--stream and --wait are mutually exclusive", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if *draft && (*wait || *stream || *noWait) {
		out.Error("--draft is incompatible with --wait, --stream, and --no-wait", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// #1578: --defer-if-busy holds delivery until the target is turn-finished;
	// --no-wait fires immediately. They are opposites.
	if *deferIfBusy && *noWait {
		out.Error("--defer-if-busy is incompatible with --no-wait", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	sessionRef := remaining[0]
	message, err := resolveMessageInput(strings.Join(remaining[1:], " "), *messageFile, os.Stdin)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Load sessions
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(sessionRef, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// --stream is Claude-only in Phase 1. Non-Claude tools error cleanly
	// with a stable message so the CLI contract stays legible.
	if *stream {
		if msg := streamPreconditionError(inst.Tool); msg != "" {
			out.Error(msg, ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Check if session is running
	if !inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is not running", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if shouldSkipConductorHeartbeatSend(inst, message) {
		out.Success(fmt.Sprintf("Skipped heartbeat for '%s'", inst.Title), map[string]interface{}{
			"success":       true,
			"skipped":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"message":       message,
		})
		return
	}

	// Get tmux session
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		out.Error("could not determine tmux session", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// #1578: --defer-if-busy holds delivery until the target is turn-finished.
	// Runs BEFORE WaitForAgentReady + the composer-draft Ctrl+C guard, so a
	// mid-generation target is never interrupted. Keys off the hook-driven
	// status (the same turn-finished signal `list --json` reports), not the
	// pane-diff readiness heuristic that false-positives idle mid-turn.
	if *deferIfBusy {
		if err := send.WaitUntilNotBusy(func() (string, error) {
			return fetchHookDrivenStatus(profile, sessionRef)
		}, *deferTimeout, send.DeferPollInterval, time.Sleep); err != nil {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Wait for agent to be ready (unless --no-wait is specified).
	// Issue #957: honor --timeout for the readiness phase too, not just the
	// post-ready completion wait. Otherwise --timeout 5m against a busy
	// recipient silently fails at ~80s.
	if !*noWait {
		if err := send.WaitForAgentReady(tmuxSess, inst.Tool, *timeout, send.PromptGates{
			ClaudeComposer: session.IsClaudeCompatible(inst.Tool),
			CodexPrompt:    session.IsCodexCompatible(inst.Tool),
		}); err != nil {
			out.Error(fmt.Sprintf("timeout waiting for agent: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		// Issue #966: after a restart, Claude reaches "waiting" + composer
		// visible before its slash-command parser registers. Bare `/foo`
		// in that window is silently dropped. Hold back only when needed.
		if shouldGateSlashRegistration(inst.Tool, message) {
			slashTimeout := *timeout
			if slashTimeout <= 0 || slashTimeout > 10*time.Second {
				slashTimeout = 10 * time.Second
			}
			if err := waitForSlashCommandReady(tmuxSess, inst.Tool, slashTimeout); err != nil {
				out.Error(fmt.Sprintf("timeout waiting for slash-command registration: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
		}
	}

	// Record send time before the actual send so we can verify output freshness.
	// Captured early to avoid false negatives from clock skew.
	sentAt := time.Now()

	// --draft: type text into the prompt without pressing Enter, letting the
	// user review and submit manually.
	if *draft {
		if err := executeDraft(tmuxSess, message); err != nil {
			out.Error(fmt.Sprintf("failed to pre-fill prompt: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		out.Success(fmt.Sprintf("Pre-filled prompt in '%s'", inst.Title), map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"message":       message,
		})
		return
	}

	// Send message atomically (text + Enter in single tmux invocation).
	// --no-wait: skip full readiness waiting, but run a capped preflight
	// barrier + extended verification loop to avoid the #616 race where
	// Claude's composer renders after the loop has already returned
	// success on startup "active" status, leaving the message unsubmitted.
	// default mode: full retry budget after readiness check.
	//
	// Both modes run the composer-draft guard (issue #1409) and submit
	// verification with a machine-checkable delivery status (issue #1413).
	tun := defaultSendTuning()
	if *noWait {
		tun = noWaitSendTuning()
	}
	sendRes, sendErr := executeSend(tmuxSess, inst.Tool, message, *noWait, tun)
	if sendErr != nil {
		extra := sendRes.jsonFields()
		extra["session_id"] = inst.ID
		extra["session_title"] = inst.Title
		if sendRes.delivery == deliveryTypedNotSubmitted {
			out.ErrorWithData(fmt.Sprintf("message typed but not submitted to '%s': %v", inst.Title, sendErr), ErrCodeDeliveryFailed, extra)
		} else {
			out.ErrorWithData(fmt.Sprintf("failed to send message: %v", sendErr), ErrCodeInvalidOperation, extra)
		}
		os.Exit(1)
	}

	// Self-heal Stage 1: stamp the "we talked to it" clock. A delivered send is
	// exactly the event the idle_at_empty_prompt dwell is measured from — a
	// session is only stuck at an empty prompt if WE sent it something and
	// nothing happened. Targeted single-column write (never SaveInstances);
	// best-effort, never blocks or fails the send.
	if db := statedb.GetGlobal(); db != nil {
		_ = db.WriteLastSentAt(inst.ID, sentAt.Unix())
	}

	// Delivery succeeded, but if an operator draft was cleared and could not
	// be typed back, it's no longer on screen — surface it on stderr (it's
	// also in saved_draft in --json) so the operator can recover it rather
	// than discovering a silent loss. draft_restore_failed never blocks the
	// send: the automated message did go through.
	if sendRes.draftSaved != "" && sendRes.draftRestoreFailed {
		fmt.Fprintf(os.Stderr,
			"Warning: cleared the operator draft to deliver this message but could not restore it. Recover it from: %s\n",
			sendRes.draftSaved)
	}

	if !*stream {
		data := map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"message":       message,
		}
		for k, v := range sendRes.jsonFields() {
			data[k] = v
		}
		out.Success(fmt.Sprintf("Sent message to '%s'", inst.Title), data)
	}

	// --stream: tail the Claude transcript and pipe JSONL events to
	// stdout until end_turn, idle timeout, or error. Issue #689.
	if *stream {
		if err := streamSessionSend(inst, sessionRef, profile, sentAt, streamOptions{
			idle:       *streamIdle,
			charBudget: *streamCharBudget,
			toolBudget: *streamToolBudget,
			timeout:    *timeout,
		}); err != nil {
			// Error already serialized as a stream event; exit 1.
			os.Exit(1)
		}
		return
	}

	// If --wait, block until the agent finishes processing, then print output
	if *wait {
		finalStatus, err := waitForCompletion(tmuxSess, *timeout)
		if err != nil {
			out.Error(fmt.Sprintf("timeout waiting for completion: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		// Refresh session ID: the instance was loaded before sending the message,
		// so the ClaudeSessionID may be stale (e.g., PostStartSync timed out,
		// TUI updated it during the wait, or /clear created a new session).
		// First try tmux env (fast), then fall back to reloading from DB.
		if session.IsClaudeCompatible(inst.Tool) {
			if freshID := inst.GetSessionIDFromTmux(); freshID != "" {
				inst.ClaudeSessionID = freshID
				inst.ClaudeDetectedAt = time.Now()
			}
		}

		// Wait for the JSONL to contain a response newer than sentAt.
		// The status check (waitForCompletion) detects the UI prompt reappearing,
		// but the JSONL file may not be flushed yet — poll until it is.
		response, err := waitForFreshOutput(inst, sentAt, instances)
		if err != nil {
			// Fallback: reload session from DB in case tmux env was also stale
			// (e.g., /clear created a new session that TUI or hooks detected)
			if _, freshInstances, _, loadErr := loadSessionData(profile); loadErr == nil {
				if freshInst, _, _ := ResolveSession(sessionRef, freshInstances); freshInst != nil {
					response, err = waitForFreshOutput(freshInst, sentAt, freshInstances)
				}
			}
		}
		if err != nil {
			out.Error(fmt.Sprintf("failed to get response: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		fmt.Println(response.Content)

		// Exit 1 for error/inactive status
		if finalStatus == "inactive" || finalStatus == "error" {
			os.Exit(1)
		}
	}
}

// defaultSendOptions returns the verification-loop options used by the default
// (non-`--no-wait`) CLI send path. verifyDelivery is enabled so the CLI
// surfaces silent drops as errors rather than returning false success — see
// issue #876.
func defaultSendOptions() sendRetryOptions {
	return sendRetryOptions{
		maxRetries:     50,
		checkDelay:     300 * time.Millisecond,
		verifyDelivery: true,
	}
}

func shouldSkipConductorHeartbeatSend(inst *session.Instance, message string) bool {
	if inst == nil || !session.IsConductorHeartbeatMessage(message) {
		return false
	}
	name := strings.TrimPrefix(inst.Title, session.ConductorSessionTitlePrefix)
	if name == inst.Title || name == "" {
		return false
	}
	meta, err := session.LoadConductorMeta(name)
	if err != nil {
		return false
	}
	idleMinutes := meta.GetHeartbeatIdleMinutes()
	if idleMinutes <= 0 {
		return false
	}
	lastActivity, err := session.GetConductorLastActivity(name, meta.Profile)
	if err != nil {
		return false
	}
	if lastActivity.IsZero() {
		return false
	}
	return time.Since(lastActivity) >= time.Duration(idleMinutes)*time.Minute
}

// Delivery status values surfaced by the `session send` path (issue #1413).
// They are part of the `--json` contract: callers (watchers, conductors,
// bridges) key off the `delivery` field to distinguish a confirmed submit
// from a message left typed-but-unsubmitted at the composer.
const (
	// deliverySubmitted: positive evidence the agent accepted the message
	// (an "active" transition, or the composer cleared after holding it).
	deliverySubmitted = "submitted"
	// deliveryUnverified: the message was sent but this tool's TUI exposes
	// no Claude-shaped verification signals, so submission is unverified
	// (non-Claude tools; legacy best-effort contract).
	deliveryUnverified = "unverified"
	// deliveryTypedNotSubmitted: the message body is still sitting unsent in
	// the composer after the bounded Enter-retry budget (issue #1413).
	deliveryTypedNotSubmitted = "typed_not_submitted"
	// deliveryNoEvidence: no positive delivery signal was ever observed
	// (issue #876 silent-drop classification).
	deliveryNoEvidence = "no_evidence"
	// deliverySendFailed: the initial tmux send-keys itself failed.
	deliverySendFailed = "send_failed"
)

// sendDeliveryResult is the prompt-state-aware outcome of executeSend.
type sendDeliveryResult struct {
	// delivery is one of the delivery* constants above.
	delivery string
	// held is how long the composer guard waited/worked before the send
	// (issue #1409 hold-and-retry plus save-clear time).
	held time.Duration
	// draftSaved is the operator draft that was cleared from the composer to
	// make way for the automated send (empty when no clear was needed).
	draftSaved string
	// draftCleared reports whether the guard confirmed the composer emptied
	// after Ctrl+C.
	draftCleared bool
	// draftRestored reports whether the saved operator draft was typed back
	// (without Enter) after the automated delivery.
	draftRestored bool
	// draftRestoreFailed reports that a saved operator draft was cleared but
	// the type-back failed (SendKeysChunked errored) — the draft is held in
	// draftSaved for recovery and must be surfaced, not silently dropped.
	draftRestoreFailed bool
}

// jsonFields returns the delivery-status fields added to `session send`
// success and error payloads in --json mode (issue #1413 machine-checkable
// contract; #1409 draft-guard observability).
func (r sendDeliveryResult) jsonFields() map[string]interface{} {
	fields := map[string]interface{}{}
	if r.delivery != "" {
		fields["delivery"] = r.delivery
	}
	if ms := r.held.Milliseconds(); ms > 0 {
		fields["held_for_composer_ms"] = ms
	}
	if r.draftSaved != "" {
		fields["saved_draft"] = r.draftSaved
		fields["draft_restored"] = r.draftRestored
		if r.draftRestoreFailed {
			fields["draft_restore_failed"] = true
		}
	}
	return fields
}

// sendExecTuning bundles the bounded budgets of the full executeSend
// pipeline (preflight barrier, composer-draft guard, verification loop) so
// tests can shrink them and production paths share one definition.
type sendExecTuning struct {
	// guardHold bounds the #1409 hold-and-retry phase: how long an automated
	// send waits for a non-empty operator draft to clear on its own before
	// falling back to save-clear-restore.
	guardHold      time.Duration
	guardPoll      time.Duration
	guardClearWait time.Duration
	// preflightWait/preflightPoll bound the --no-wait composer-visibility
	// barrier (issue #616).
	preflightWait time.Duration
	preflightPoll time.Duration
	// settleDelay is the post-composer-render settle pause (issue #616).
	settleDelay time.Duration
	// retry is the verification-loop budget (issues #876, #1413).
	retry sendRetryOptions
}

// defaultSendTuning is the tuning for the default (readiness-waited) send
// path. The guard hold is generous because the caller already waited for
// readiness; an operator mid-keystroke gets up to 10s to finish or pause.
func defaultSendTuning() sendExecTuning {
	return sendExecTuning{
		guardHold:      10 * time.Second,
		guardPoll:      250 * time.Millisecond,
		guardClearWait: 1500 * time.Millisecond,
		retry:          defaultSendOptions(),
	}
}

// noWaitSendTuning is the tuning for `session send --no-wait`. --no-wait
// skips the readiness wait, NOT the composer guard or submit verification —
// but its guard hold is kept small (2s) so automated callers (heartbeats,
// inbox nudges, watchers) pay minimal added latency. When the composer is
// empty the guard costs a single pane capture.
func noWaitSendTuning() sendExecTuning {
	return sendExecTuning{
		guardHold:      2 * time.Second,
		guardPoll:      150 * time.Millisecond,
		guardClearWait: time.Second,
		preflightWait:  5 * time.Second,
		preflightPoll:  100 * time.Millisecond,
		settleDelay:    500 * time.Millisecond,
		retry:          noWaitSendOptions(),
	}
}

// executeSend is the prompt-state-aware send pipeline used by
// `session send` (issues #1409 + #1413):
//
//  1. --no-wait only: capped preflight barrier until the Claude composer is
//     visible, plus a short settle delay (issue #616).
//  2. Composer-draft guard (issue #1409): hold while the composer shows a
//     non-empty operator draft; at the bound, save the draft and clear the
//     composer (Ctrl+C) so the automated message cannot merge with it.
//  3. Send + bounded submit verification (issues #876, #1413), classifying
//     the outcome into a delivery status.
//  4. Restore the saved operator draft (typed back without Enter) once the
//     automated delivery is not stuck in the composer. When the automated
//     message itself ends typed_not_submitted the draft is NOT retyped (it
//     would merge into the stuck composer) — it is surfaced in the result
//     instead so the caller can report it.
//
// Steps 1, 2 and 4 are Claude-only: composer introspection is Claude-shaped
// and non-Claude tools gate readiness upstream.
func executeSend(target sendRetryTarget, tool, message string, noWait bool, tun sendExecTuning) (sendDeliveryResult, error) {
	res := sendDeliveryResult{}
	claudeLike := session.IsClaudeCompatible(tool)

	if noWait && claudeLike {
		if awaitComposerReadyBestEffort(target, tun.preflightWait, tun.preflightPoll) {
			// Post-composer settle: React mount can lag behind the
			// composer glyph by a few hundred ms on cold starts.
			if tun.settleDelay > 0 {
				time.Sleep(tun.settleDelay)
			}
		}
	}

	if claudeLike {
		guard := send.GuardComposerDraft(target, send.ComposerGuardOptions{
			HoldWait:     tun.guardHold,
			PollInterval: tun.guardPoll,
			ClearWait:    tun.guardClearWait,
			Strip:        tmux.StripANSI,
		})
		res.held = guard.Held
		res.draftSaved = guard.SavedDraft
		res.draftCleared = guard.DraftCleared
	}

	delivery, err := sendWithRetryTarget(target, message, skipClaudeDeliveryVerify(tool), tun.retry)
	res.delivery = delivery

	if res.draftSaved != "" && delivery != deliveryTypedNotSubmitted {
		if restoreErr := target.SendKeysChunked(res.draftSaved); restoreErr == nil {
			res.draftRestored = true
		} else {
			// The composer was cleared (Ctrl+C) but the type-back failed, so
			// the operator's draft is no longer on screen. Don't silently
			// drop it: flag the failure so the caller surfaces draftSaved for
			// recovery instead of reporting a clean success.
			res.draftRestoreFailed = true
		}
	}
	return res, err
}

// skipClaudeDeliveryVerify reports whether the Claude-tuned post-send delivery
// verification (issue #876) should be skipped for tool. The verify keys off
// Claude-specific TUI signals (an "active" transition, the composer glyph,
// unsent-paste markers); non-Claude tools never surface those, so running it
// false-negatives a delivered message as "dropped silently" (#1238, #1205,
// #876). Claude tools keep the verify; every non-Claude tool skips it — the
// general superset of #1228's codex-only skip.
func skipClaudeDeliveryVerify(tool string) bool {
	return !session.UsesClaudeDeliveryVerify(tool)
}

// draftSender is implemented by *tmux.Session for the --draft path.
type draftSender interface {
	SendKeysChunked(string) error
}

// executeDraft pre-fills the prompt without pressing Enter.
func executeDraft(target draftSender, message string) error {
	return target.SendKeysChunked(message)
}

// noWaitSendOptions returns the verification-loop options used by the
// `session send --no-wait` path.
//
// Budget sizing (issue #616): a fresh Claude session with MCPs can take
// 5-40s before its TUI input handler is interactive. If verification
// returns on `activeChecks>=2` (from startup animations) before the
// composer renders, a swallowed Enter leaves the message typed-but-not-
// submitted. Budget must be long enough to see the composer either
// accept or reject the submission.
//
// maxFullResends=-1 is load-bearing: it disables the Ctrl+C-then-resend
// path (issue #479 — would otherwise double-send).
func noWaitSendOptions() sendRetryOptions {
	return sendRetryOptions{
		maxRetries:     30,
		checkDelay:     200 * time.Millisecond,
		maxFullResends: -1,
		// Issue #876: even on the --no-wait path, callers expect that a
		// `Sent` exit means the message reached the agent. Without this,
		// the verification loop would still fall through to nil on a
		// silent drop.
		verifyDelivery: true,
	}
}

// awaitComposerReadyBestEffort polls the pane until the Claude composer
// prompt (`❯`) appears, returning true. If the composer never appears
// within maxWait, returns false without blocking longer — preserving the
// `--no-wait` spirit when the session is slow or broken.
//
// Added for issue #616: eliminates the race where `session send --no-wait`
// fires before Claude's TUI input handler is mounted.
func awaitComposerReadyBestEffort(target sendRetryTarget, maxWait, pollInterval time.Duration) bool {
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(maxWait)
	for {
		if rawContent, err := target.CapturePaneFresh(); err == nil {
			if send.HasCurrentComposerPrompt(tmux.StripANSI(rawContent)) {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		remaining := time.Until(deadline)
		sleep := pollInterval
		if remaining < sleep {
			sleep = remaining
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
	}
}

// The `session send --no-wait` semantics for the CLI live in executeSend
// (called with noWait=true and noWaitSendTuning()). The historical issue
// #616 fix is preserved there as three layers, applied in order:
//
//  1. Preflight readiness barrier (capped at 5s): polls the pane for a
//     visible Claude composer `❯`. Without this, the initial paste
//     lands in the TTY before Claude's Ink TUI has rendered the input
//     surface — the keystrokes are discarded by pre-mount handlers.
//
//  2. Post-composer settle delay (500ms): Claude's composer glyph can
//     render BEFORE React completes mounting the input handler. Without
//     this delay, the paste can still be partially swallowed by the
//     mount transition (observed live: message vanished entirely, no
//     unsent prompt to retry on). 500ms is empirically enough.
//
//  3. Extended verification budget via noWaitSendOptions() (6s, 30×200ms):
//     after the initial send, keeps detecting unsent-prompt markers and
//     re-firing SendEnter if the composer still holds our message.
//
// maxFullResends=-1 is load-bearing for the #479 regression (never
// double-send). Non-Claude tools skip the preflight — they have their
// own readiness shapes and upstream gating. Issue #1409 added a fourth
// layer between 2 and 3: the composer-draft guard.

type sendRetryTarget interface {
	SendKeysAndEnter(string) error
	GetStatus() (string, error)
	SendEnter() error
	SendCtrlC() error
	SendKeysChunked(string) error
	CapturePaneFresh() (string, error)
}

type sendRetryOptions struct {
	maxRetries     int
	checkDelay     time.Duration
	maxFullResends int // >0 overrides default (3); <0 disables Ctrl+C-then-resend; 0 uses default

	// verifyDelivery, when true, requires the verification loop to observe at
	// least one positive signal that the message reached the inner agent (an
	// "active" status transition, an unsent-prompt composer marker, a full
	// resend, or the message body appearing in the captured pane). If the
	// budget is exhausted without any such signal, the function returns an
	// error instead of the prior best-effort `nil`. Closes the silent-drop
	// path reported in issue #876.
	verifyDelivery bool
}

// sendWithRetryTarget sends the message and runs the bounded submit
// verification loop. It returns a delivery status (one of the delivery*
// constants) alongside the error so callers can expose a machine-checkable
// outcome (issue #1413).
func sendWithRetryTarget(target sendRetryTarget, message string, skipVerify bool, opts sendRetryOptions) (string, error) {
	if opts.maxRetries <= 0 {
		opts.maxRetries = 1
	}
	if opts.checkDelay < 0 {
		opts.checkDelay = 0
	}

	if err := target.SendKeysAndEnter(message); err != nil {
		return deliverySendFailed, fmt.Errorf("failed to send message: %w", err)
	}

	if skipVerify {
		return deliveryUnverified, nil
	}

	// Verify the agent accepted Enter and began processing.
	// Strategy:
	// - If unsent prompt is visible, press Enter again immediately.
	// - Consider success only after sustained post-send activity ("active").
	// - If we never observe active and remain in waiting/idle, keep a periodic
	//   fallback Enter cadence instead of returning early (handles late unsent
	//   prompt rendering races seen in Claude startup).
	// - If the message appears completely lost (no prompt marker, no activity
	//   after several retries), clear stale input with Ctrl+C and re-send the
	//   full message. This handles the TUI init race where the prompt renders
	//   before the input handler is ready, causing sent keys to be discarded.
	const activeSuccessThreshold = 2
	const waitingAfterActiveThreshold = 2
	// fullResendThreshold: after this many consecutive waiting/idle checks
	// with no activity and no unsent prompt, assume the message was lost
	// during TUI init and re-send the full message.
	const fullResendThreshold = 8
	maxFullResends := 3 // default
	if opts.maxFullResends > 0 {
		maxFullResends = opts.maxFullResends
	} else if opts.maxFullResends < 0 {
		maxFullResends = 0
	}
	waitingNoMarkerChecks := 0
	waitingNoActivityChecks := 0
	activeChecks := 0
	sawActiveAfterSend := false
	fullResendCount := 0
	// sawDeliveryEvidence flips true on any positive signal that the message
	// reached the agent: an "active" status transition, an unsent-prompt
	// composer marker, the message body appearing verbatim in the pane, or a
	// successful full resend. When opts.verifyDelivery is set and this stays
	// false for the entire budget, the function returns an error instead of
	// silently succeeding (issue #876).
	sawDeliveryEvidence := false
	// Snippet of the message body to look for in captured pane content. Some
	// TUI frameworks (and non-Claude tools) won't render a "[Pasted text …]"
	// or "❯ <msg>" marker, so direct verbatim content is the only signal.
	// Take the first run of non-whitespace content, capped, to avoid false
	// positives from matching common short strings.
	deliveryToken := messageDeliveryToken(message)
	for retry := 0; retry < opts.maxRetries; retry++ {
		time.Sleep(opts.checkDelay)

		unsentPromptDetected := false
		if rawContent, captureErr := target.CapturePaneFresh(); captureErr == nil {
			content := tmux.StripANSI(rawContent)
			unsentPromptDetected = send.HasUnsentPastedPrompt(content) || send.HasUnsentComposerPrompt(content, message)
			if !sawDeliveryEvidence && deliveryToken != "" && strings.Contains(content, deliveryToken) {
				sawDeliveryEvidence = true
			}
		}
		status, err := target.GetStatus()

		if unsentPromptDetected {
			sawDeliveryEvidence = true
			waitingNoMarkerChecks = 0
			waitingNoActivityChecks = 0
			activeChecks = 0
			_ = target.SendEnter()
			continue
		}

		if err == nil && status == "active" {
			sawActiveAfterSend = true
			sawDeliveryEvidence = true
			waitingNoMarkerChecks = 0
			waitingNoActivityChecks = 0
			activeChecks++
			if activeChecks >= activeSuccessThreshold {
				return deliverySubmitted, nil
			}
			continue
		}
		activeChecks = 0

		if err == nil && (status == "waiting" || status == "idle") {
			if sawActiveAfterSend {
				waitingNoMarkerChecks++
				waitingNoActivityChecks = 0
				if waitingNoMarkerChecks >= waitingAfterActiveThreshold {
					return deliverySubmitted, nil
				}
			} else {
				waitingNoMarkerChecks = 0
				waitingNoActivityChecks++

				// Message may have been lost during TUI init: the prompt was
				// visible but the input handler wasn't ready, so sent keys were
				// discarded. Clear stale input and re-send the full message.
				if waitingNoActivityChecks >= fullResendThreshold && fullResendCount < maxFullResends {
					fullResendCount++
					waitingNoActivityChecks = 0
					_ = target.SendCtrlC()
					time.Sleep(200 * time.Millisecond)
					// A successful resend is not yet evidence of receipt — the
					// next iteration must still observe a positive signal — so
					// we intentionally do NOT set sawDeliveryEvidence here, even
					// when SendKeysAndEnter returns nil. The send attempt is
					// recorded only so verifyDelivery can distinguish "pipe ever
					// fired" from "never even acked".
					_ = target.SendKeysAndEnter(message)
					continue
				}

				// We haven't observed any post-send activity yet. Nudge Enter
				// aggressively in the early window (every iteration for first 5
				// retries) then every 2nd iteration. This addresses bracketed
				// paste timing failures that are most likely early on.
				if retry < 5 || retry%2 == 0 {
					_ = target.SendEnter()
				}
			}
			continue
		}
		waitingNoMarkerChecks = 0
		waitingNoActivityChecks = 0

		// Ambiguous state: keep a best-effort Enter retry budget.
		// Increased from 2 to 4 because some TUI frameworks take longer
		// to process and reflect state.
		if retry < 4 {
			_ = target.SendEnter()
		}
	}

	// Budget exhausted without a confirmed submit. Classify the final state
	// (issue #1413): a message still sitting unsent in the composer after
	// every bounded Enter retry must surface as typed_not_submitted (nonzero
	// exit + `delivery` in --json) instead of the historical silent exit 0.
	if opts.verifyDelivery {
		if rawContent, captureErr := target.CapturePaneFresh(); captureErr == nil {
			content := tmux.StripANSI(rawContent)
			if send.HasUnsentPastedPrompt(content) || send.HasUnsentComposerPrompt(content, message) {
				return deliveryTypedNotSubmitted, fmt.Errorf(
					"message typed but not submitted after %d verification checks (issue #1413): "+
						"the composer still holds the message despite bounded Enter retries. "+
						"The recipient agent's input handler is not accepting Enter", opts.maxRetries)
			}
		}

		// Issue #876: with verifyDelivery, refuse to claim success when no
		// positive signal was ever observed — the message was very likely
		// dropped silently.
		if !sawDeliveryEvidence {
			return deliveryNoEvidence, fmt.Errorf("send dropped silently: no evidence of delivery after %d checks (issue #876). "+
				"The agent never transitioned to 'active', no composer/unsent-paste marker appeared, "+
				"and the message body was not visible in the pane. Verify the inner agent is reading from "+
				"its TTY before retrying", opts.maxRetries)
		}
		// Evidence was observed and the composer no longer holds the message:
		// it was accepted at some point during the budget.
		return deliverySubmitted, nil
	}

	// Legacy best-effort contract for paths that gate verification elsewhere.
	return deliveryUnverified, nil
}

// messageDeliveryToken returns a short, content-bearing slice of the message
// suitable for "did this body appear in the pane?" verification. Returns "" if
// the message contains no usefully-distinctive token (e.g. all whitespace, or
// only short common words).
func messageDeliveryToken(message string) string {
	const minTokenLen = 12
	const maxTokenLen = 64
	trimmed := strings.TrimSpace(message)
	if len(trimmed) < minTokenLen {
		return ""
	}
	if len(trimmed) > maxTokenLen {
		trimmed = trimmed[:maxTokenLen]
	}
	return trimmed
}

// shouldGateSlashRegistration reports whether a send needs to wait for
// Claude's slash-command parser to finish registering before relaying.
//
// Issue #966: after `session restart`, Claude reaches "waiting" with the
// composer prompt visible *before* its slash-command router is armed. A
// bare `/foo` sent in that window is silently dropped. The gate fires only
// for the trigger condition — Claude tool plus a bare slash payload — so
// conversational text and non-Claude tools don't pay the latency.
func shouldGateSlashRegistration(tool, message string) bool {
	if tool != "claude" {
		return false
	}
	trimmed := strings.TrimLeft(message, " \t")
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "/")
}

// waitForSlashCommandReady polls the pane until the composer prompt has been
// continuously visible for the slash-registration settle window, then returns.
// Callers must have already passed send.WaitForAgentReady; this is an additional
// hold-back specifically for the #966 race.
//
// The function probes (rather than blind-sleeps) so a long-already-ready
// Claude returns near-immediately on retries, while a freshly restarted
// Claude pays the full settle window.
func waitForSlashCommandReady(target send.AgentReadyChecker, tool string, timeout time.Duration) error {
	const pollInterval = 100 * time.Millisecond
	// Eight stable composer observations (~800ms) is the empirical floor
	// for Claude to finish registering its slash-command parser after the
	// composer first renders. Bumping this is a no-op for healthy sessions
	// (we early-return as soon as stability is met); it only delays the
	// first send after a restart.
	const minStableHits = 8

	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)

	stable := 0
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		rawContent, err := target.CapturePaneFresh()
		if err != nil {
			stable = 0
			continue
		}
		content := tmux.StripANSI(rawContent)
		if !send.HasCurrentComposerPrompt(content) {
			stable = 0
			continue
		}
		stable++
		if stable >= minStableHits {
			return nil
		}
	}

	return fmt.Errorf("slash-command registration not ready after %s (tool=%s)", timeout, tool)
}

// statusChecker abstracts tmux status polling so waitForCompletion is testable.
type statusChecker interface {
	GetStatus() (string, error)
}

// waitForCompletion polls until the agent finishes processing (status leaves "active").
// Returns the final status string ("waiting", "idle", "inactive") or an error on timeout.
func waitForCompletion(checker statusChecker, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	const pollInterval = 2 * time.Second

	// Initial grace period: wait for the agent to start processing.
	// sendWithRetry already checks for "active", but give a small buffer.
	time.Sleep(1 * time.Second)

	consecutiveErrors := 0
	const maxConsecutiveErrors = 5

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("agent still running after %s", timeout)
		default:
		}

		status, err := checker.GetStatus()
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveErrors {
				return "error", nil // Session likely died
			}
			time.Sleep(pollInterval)
			continue
		}
		consecutiveErrors = 0

		// "active" means still processing, keep waiting
		if status == "active" {
			time.Sleep(pollInterval)
			continue
		}

		// Any non-active status means the agent is done
		return status, nil
	}
}

// freshOutputConfig holds tunable parameters for waitForFreshOutput.
// Tests override these via freshOutputTestConfig; production uses defaults.
type freshOutputConfig struct {
	pollInterval time.Duration
	timeout      time.Duration
}

// freshOutputTestConfig, when non-nil, overrides the default timing constants.
// Only set from tests.
var freshOutputTestConfig *freshOutputConfig

// waitForFreshOutput polls the session's JSONL file until it contains an assistant
// response with a timestamp not before sentAt (with a 250ms skew tolerance).
// This bridges the gap between the UI prompt reappearing (detected by
// waitForCompletion) and the JSONL being flushed to disk.
//
// For non-Claude tools (Codex, Gemini, etc.) the JSONL freshness check is
// skipped entirely to avoid an unnecessary 5s penalty, since those tools
// don't use the same JSONL format.
//
// Falls back to the best-effort response if the freshness timeout expires,
// logging a warning to stderr so the caller knows the data may be stale.
//
// peers carries the profile snapshot for the #1400 collision guard: a
// claude_session_id shared by multiple live instances resolves to ONE
// transcript, so waiting on it would return another session's output.
// Fail fast (same semantics as --stream's #1352 guard) instead of polling
// a colliding transcript until the freshness timeout.
func waitForFreshOutput(inst *session.Instance, sentAt time.Time, peers []*session.Instance) (*session.ResponseOutput, error) {
	// Non-Claude tools don't use JSONL timestamps — skip the freshness loop.
	if !session.IsClaudeCompatible(inst.Tool) {
		return inst.GetLastResponseBestEffort()
	}

	// #1400: refuse a colliding transcript before entering the poll loop.
	if _, err := inst.GetJSONLPathChecked(peers); err != nil {
		return nil, fmt.Errorf("refusing to read a colliding transcript: %w", err)
	}

	pollInterval := 250 * time.Millisecond
	timeout := 5 * time.Second
	if cfg := freshOutputTestConfig; cfg != nil {
		pollInterval = cfg.pollInterval
		timeout = cfg.timeout
	}

	// Allow 250ms of clock skew / rounding tolerance.
	// Claude's JSONL timestamps may have only second precision, and local
	// time.Now() can be slightly ahead of Claude's clock. Tighter than the
	// original 2s to reduce false positives on genuinely stale output.
	threshold := sentAt.Add(-250 * time.Millisecond)

	deadline := time.Now().Add(timeout)
	var lastResp *session.ResponseOutput
	var lastErr error

	for time.Now().Before(deadline) {
		resp, err := inst.GetLastResponseBestEffort()
		if err != nil {
			lastErr = err
			time.Sleep(pollInterval)
			continue
		}
		lastResp = resp
		lastErr = nil

		// If the response has a timestamp, check freshness
		if resp.Timestamp != "" {
			if ts, parseErr := time.Parse(time.RFC3339Nano, resp.Timestamp); parseErr == nil {
				if !ts.Before(threshold) {
					return resp, nil
				}
			} else if ts, parseErr := time.Parse(time.RFC3339, resp.Timestamp); parseErr == nil {
				if !ts.Before(threshold) {
					return resp, nil
				}
			}
		}

		time.Sleep(pollInterval)
	}

	// Freshness timeout: return whatever we have but warn that it may be stale
	if lastResp != nil {
		fmt.Fprintf(os.Stderr, "Warning: output freshness timeout (%s) — response may be stale\n", timeout)
		return lastResp, nil
	}
	return nil, lastErr
}

// streamPreconditionError returns a non-empty error message when the given
// tool is not supported by --stream. Phase 1 is Claude-only (issue #689);
// non-Claude tools error cleanly here rather than silently producing empty
// output.
func streamPreconditionError(tool string) string {
	if session.IsClaudeCompatible(tool) {
		return ""
	}
	return fmt.Sprintf("--stream is not supported for tool %q (Phase 1 supports Claude-compatible tools only)", tool)
}

// streamOptions carries caller-tunable knobs for --stream.
type streamOptions struct {
	idle       time.Duration
	charBudget int
	toolBudget int
	timeout    time.Duration
}

// streamSessionSend tails the Claude session JSONL for a freshly sent
// message and writes structured stream events as JSONL to stdout until
// the assistant reaches end_turn, idle-times out, or ctx is cancelled.
//
// Overall budget: streamOptions.timeout bounds the entire stream (not just
// idle gaps), matching the semantics of --wait's --timeout.
func streamSessionSend(inst *session.Instance, sessionRef, profile string, sentAt time.Time, opts streamOptions) error {
	// Resolve JSONL path. Claude writes the file after the first
	// assistant chunk, so we poll briefly for its existence.
	resolvedInst := inst
	if session.IsClaudeCompatible(inst.Tool) {
		if fresh := inst.GetSessionIDFromTmux(); fresh != "" {
			inst.ClaudeSessionID = fresh
			inst.ClaudeDetectedAt = time.Now()
		}
	}

	var jsonlPath string
	// peers carries the latest profile snapshot so the resolve can refuse a
	// transcript path that collides with another live instance's session id
	// (issue #1349 defense-in-depth #2): streaming the wrong transcript is one
	// of the corruption symptoms the rebind bug caused.
	var peers []*session.Instance
	if _, initial, _, loadErr := loadSessionData(profile); loadErr == nil {
		peers = initial
	}
	deadline := time.Now().Add(opts.timeout)
	for time.Now().Before(deadline) {
		p, resolveErr := resolvedInst.GetJSONLPathChecked(peers)
		if resolveErr != nil {
			return fmt.Errorf("refusing to stream a colliding transcript: %w", resolveErr)
		}
		jsonlPath = p
		if jsonlPath != "" {
			break
		}
		// Refresh from DB in case the session was just created.
		if _, freshInstances, _, loadErr := loadSessionData(profile); loadErr == nil {
			peers = freshInstances
			if fi, _, _ := ResolveSession(sessionRef, freshInstances); fi != nil {
				resolvedInst = fi
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	if jsonlPath == "" {
		// Emit a single error event to stdout so --stream consumers
		// always get a parseable response. Matches the schema so they
		// don't need a separate error channel.
		errEv := map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("session transcript not found within %s (session id=%s)", opts.timeout, resolvedInst.ClaudeSessionID),
			"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		}
		b, _ := json.Marshal(errEv)
		fmt.Println(string(b))
		return fmt.Errorf("no transcript")
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	return session.StreamTranscript(ctx, jsonlPath, resolvedInst.ClaudeSessionID, sentAt, os.Stdout, session.StreamConfig{
		IdleTimeout: opts.idle,
		CharBudget:  opts.charBudget,
		ToolBudget:  opts.toolBudget,
	})
}

// handleSessionOutput gets the last response from a session
func handleSessionOutput(profile string, args []string) {
	fs := flag.NewFlagSet("session output", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	copyFlag := fs.Bool("copy", false, "Copy output to system clipboard")
	// #1101: --pane returns the raw tmux capture-pane content (with ANSI escapes
	// and the tool's full UI chrome) instead of the parsed transcript "last
	// response". The local TUI preview uses capture-pane; remote sessions
	// fetched via SSH need this same content to render claude-formatted output.
	paneFlag := fs.Bool("pane", false, "Return tmux capture-pane content (full UI with ANSI)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session output [id|title] [options]")
		fmt.Println()
		fmt.Println("Get the last response from a session. If no ID is provided, auto-detects current session.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session (allow current session detection)
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Refresh session ID from tmux env before reading output.
	// The DB-stored ClaudeSessionID may be stale if /clear created a new session
	// or PostStartSync timed out. This matches the refresh in handleSessionSend.
	if session.IsClaudeCompatible(inst.Tool) {
		if freshID := inst.GetSessionIDFromTmux(); freshID != "" {
			inst.ClaudeSessionID = freshID
			inst.ClaudeDetectedAt = time.Now()
		}
	}

	// #1101: --pane short-circuits the transcript path and returns the live
	// tmux pane capture so remote previews can render the same claude-formatted
	// content the local preview shows. We still emit a ResponseOutput-shaped
	// JSON so the wire format is unchanged.
	if *paneFlag {
		paneContent, paneErr := inst.PreviewFull()
		if paneErr != nil {
			out.Error(fmt.Sprintf("failed to capture pane: %v", paneErr), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		jsonData := map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"tool":          inst.Tool,
			"role":          "pane",
			"content":       paneContent,
		}
		if quietMode {
			fmt.Println(paneContent)
			return
		}
		out.Print(paneContent, jsonData)
		return
	}

	// Get the last response (best-effort fallback for smoother CLI reads).
	// Collision-checked (#1400): multiple live instances sharing one
	// claude_session_id resolve to the SAME transcript, so the parsed "last
	// response" (-q / --json / default / --copy) would be byte-identical for
	// all of them. Refuse the read instead — the same guard `session output
	// --stream` got in #1352.
	response, err := inst.GetLastResponseBestEffortChecked(instances)
	if err != nil {
		out.Error(fmt.Sprintf("failed to get response: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Copy to clipboard mode
	if *copyFlag {
		termInfo := tmux.GetTerminalInfo()
		result, err := clipboard.Copy(response.Content, termInfo.SupportsOSC52)
		if err != nil {
			out.Error(fmt.Sprintf("clipboard: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		jsonData := map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"lines_copied":  result.LineCount,
			"bytes_copied":  result.ByteSize,
			"method":        result.Method,
		}
		out.Print(
			fmt.Sprintf("Copied %d lines to clipboard via %s (%s)", result.LineCount, result.Method, inst.Title),
			jsonData,
		)
		return
	}

	// Quiet mode: just print raw content
	if quietMode {
		fmt.Println(response.Content)
		return
	}

	// Build JSON data with tool-specific conversation session ID key
	jsonData := map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"tool":          response.Tool,
		"role":          response.Role,
		"content":       response.Content,
		"timestamp":     response.Timestamp,
	}
	// Add tool-specific conversation session ID
	if response.SessionID != "" {
		switch response.Tool {
		case "claude":
			jsonData["claude_session_id"] = response.SessionID
		case "gemini":
			jsonData["gemini_session_id"] = response.SessionID
		default:
			jsonData["conversation_id"] = response.SessionID
		}
	}

	// Build human-readable output
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session: %s (%s)\n", inst.Title, response.Tool))
	if response.Timestamp != "" {
		sb.WriteString(fmt.Sprintf("Time: %s\n", response.Timestamp))
	}
	sb.WriteString("---\n")
	sb.WriteString(response.Content)

	out.Print(sb.String(), jsonData)
}

// handleSessionCurrent shows current session and profile (auto-detected)
// Uses a fast path that reads session data without tmux initialization (LoadLite).
func handleSessionCurrent(profileArg string, args []string) {
	fs := flag.NewFlagSet("session current", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session current [options]")
		fmt.Println()
		fmt.Println("Show current session and profile (auto-detected from environment).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Check if we're in a tmux session
	if os.Getenv("TMUX") == "" {
		out.Error("not in a tmux session", ErrCodeNotFound)
		os.Exit(1)
	}

	// ═══════════════════════════════════════════════════════════════════
	// FAST PATH: Get current tmux session name (1 subprocess call)
	// Then match against session data without full tmux initialization
	// ═══════════════════════════════════════════════════════════════════
	tmuxSessionName, err := getCurrentTmuxSessionName()
	if err != nil {
		out.Error(fmt.Sprintf("failed to get current tmux session: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Detect profile: use explicit arg if provided, otherwise auto-detect
	detectedProfile := profileArg
	if detectedProfile == "" || detectedProfile == session.DefaultProfile {
		detectedProfile = profile.DetectCurrentProfile()
	}

	// Try fast path: LoadLite + match by tmux session name
	instData, foundProfile := findInstanceDataByTmuxFast(tmuxSessionName, detectedProfile)

	if instData == nil {
		out.Error(
			"current tmux session is not an agent-deck session\nHint: Run 'agent-deck list' to see available sessions",
			ErrCodeNotFound,
		)
		os.Exit(1)
	}

	if foundProfile != "" {
		detectedProfile = foundProfile
	}

	// Quiet mode: just print session name
	if quietMode {
		fmt.Println(instData.Title)
		return
	}

	// Determine status from saved data (no live tmux check in fast path)
	status := StatusString(instData.Status)

	// Prepare JSON output
	jsonData := map[string]interface{}{
		"session": instData.Title,
		"profile": detectedProfile,
		"id":      instData.ID,
		"path":    instData.ProjectPath,
		"status":  status,
	}

	if instData.TmuxSession != "" {
		jsonData["tmux_session"] = instData.TmuxSession
	}

	if instData.GroupPath != "" {
		jsonData["group"] = instData.GroupPath
	}

	// Build human-readable output
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session: %s\n", instData.Title))
	sb.WriteString(fmt.Sprintf("Profile: %s\n", detectedProfile))
	sb.WriteString(fmt.Sprintf("ID:      %s\n", instData.ID))
	sb.WriteString(fmt.Sprintf("Status:  %s %s\n", StatusSymbol(instData.Status), status))
	sb.WriteString(fmt.Sprintf("Path:    %s\n", FormatPath(instData.ProjectPath)))
	if instData.GroupPath != "" {
		sb.WriteString(fmt.Sprintf("Group:   %s\n", instData.GroupPath))
	}

	out.Print(sb.String(), jsonData)
}

// getCurrentTmuxSessionName gets the current tmux session name (single subprocess call)
func getCurrentTmuxSessionName() (string, error) {
	cmd := exec.Command("tmux", "display-message", "-p", "#{session_name}")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// findInstanceDataByTmuxFast finds a session by tmux name using LoadLite (no tmux initialization)
// First tries the specified profile, then searches all profiles if not found.
// Returns the InstanceData and the profile it was found in.
func findInstanceDataByTmuxFast(tmuxSessionName, preferredProfile string) (*session.InstanceData, string) {
	// Try preferred profile first
	storage, err := session.NewStorageWithProfile(preferredProfile)
	if err == nil {
		instances, _, err := storage.LoadLite()
		if err == nil {
			if inst := matchInstanceDataByTmuxName(instances, tmuxSessionName); inst != nil {
				return inst, preferredProfile
			}
		}
	}

	// Search all profiles
	profiles, err := session.ListProfiles()
	if err != nil {
		return nil, ""
	}

	for _, p := range profiles {
		if p == preferredProfile {
			continue // Already checked
		}
		storage, err := session.NewStorageWithProfile(p)
		if err != nil {
			continue
		}
		instances, _, err := storage.LoadLite()
		if err != nil {
			continue
		}
		if inst := matchInstanceDataByTmuxName(instances, tmuxSessionName); inst != nil {
			return inst, p
		}
	}

	return nil, ""
}

// matchInstanceDataByTmuxName finds an InstanceData by exact tmux session name match
func matchInstanceDataByTmuxName(instances []*session.InstanceData, tmuxSessionName string) *session.InstanceData {
	for _, inst := range instances {
		if inst.TmuxSession == tmuxSessionName {
			return inst
		}
	}
	return nil
}

// isValidSessionColor is a thin delegator to session.IsValidSessionColor
// (issue #391). The validator now lives in the session package so the TUI
// EditSessionDialog and CLI session_set share one source of truth; this
// wrapper stays so cmd-package callers and the existing
// TestIsValidSessionColor table in session_color_test.go keep working.
func isValidSessionColor(v string) bool {
	return session.IsValidSessionColor(v)
}

// childrenOf returns the direct sub-sessions of parentID, preserving the input
// order. Pure helper so the filtering is unit-testable without a live registry.
func childrenOf(parentID string, instances []*session.Instance) []*session.Instance {
	var out []*session.Instance
	for _, inst := range instances {
		if inst != nil && inst.ParentSessionID == parentID {
			out = append(out, inst)
		}
	}
	return out
}

// handleSessionChildren implements `session children [id]` — a read-only fleet
// view that lists a session's sub-sessions with live status and each child's
// last asserted completion (from the non-destructive completion ledger). It
// defaults to the current session and never clears the inbox, so a parent can
// poll it from any chat without disturbing delivery.
func handleSessionChildren(profile string, args []string) {
	fs := flag.NewFlagSet("session children", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	follow := fs.Bool("follow", false, "Stream child state changes as JSONL (one event per line) until interrupted")
	interval := fs.Duration("interval", 2*time.Second, "Poll interval for --follow")
	heartbeat := fs.Duration("heartbeat", 60*time.Second, "Heartbeat event interval for --follow (0 disables)")
	untilDone := fs.Bool("until-done", false, "With --follow: exit 0 once every child is terminal (done sentinel, error, or stopped)")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session children [id|title] [options]")
		fmt.Println()
		fmt.Println("List a session's sub-sessions with live status and last completion.")
		fmt.Println("Defaults to the current session. Read-only; does not clear the inbox.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("--follow emits JSONL events: snapshot (initial state per child), added,")
		fmt.Println("status (from/to transition), done (completion sentinel), removed, error,")
		fmt.Println("plus periodic heartbeat and a final complete line with --until-done.")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session children --json")
		fmt.Println("  agent-deck session children --follow                    # live fleet event stream")
		fmt.Println("  agent-deck session children --follow --until-done      # exits when all children finish")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}
	// Default to the caller's own session. resolveSelfSessionID prefers
	// AGENTDECK_INSTANCE_ID (the authoritative full id) over the tmux session
	// name, whose suffix is only a short hash and won't resolve.
	if strings.TrimSpace(identifier) == "" {
		self, err := resolveSelfSessionID()
		if err != nil {
			out.Error(err.Error(), ErrCodeNotFound)
			os.Exit(2)
		}
		identifier = self
	}
	parent, errMsg, errCode := ResolveSession(identifier, instances)
	if parent == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
	}

	if *untilDone && !*follow {
		out.Error("--until-done requires --follow", ErrCodeInvalidOperation)
		os.Exit(1)
	}
	// A non-positive interval makes time.Sleep a no-op, turning the poll into a
	// busy-loop that reopens storage every pass. Reject rather than clamp: a
	// silently different interval than asked for is its own surprise.
	if *follow && *interval <= 0 {
		out.Error("--interval must be positive", ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if *follow {
		// The stream is JSONL by contract; --json/-q are irrelevant here.
		os.Exit(runChildrenFollow(profile, parent.ID, *interval, *heartbeat, *untilDone, os.Stdout))
	}

	kids := childrenOf(parent.ID, instances)
	session.RefreshInstancesForCLIStatus(kids)

	rows := buildChildRows(kids)
	var human strings.Builder
	fmt.Fprintf(&human, "Children of %s (%s):\n", parent.Title, parent.ID)
	for _, row := range rows {
		done := row.DoneStatus
		if done == "" {
			done = "-"
		}
		fmt.Fprintf(&human, "  %s  %-20s  %-8s  done=%s  %s\n", row.ID, row.Title, row.Status, done, row.DoneSummary)
	}
	if len(kids) == 0 {
		human.WriteString("  (no sub-sessions)\n")
	}
	out.Print(human.String(), map[string]interface{}{"parent": parent.ID, "children": rows})
}

// handleSessionSearch implements issue #483 — search across Claude session
// message content (not just titles). Wraps the internal global-search index
// behind a CLI surface so users can find past prompts / responses without
// dropping into the TUI.
func handleSessionSearch(profile string, args []string) {
	_ = profile // reserved: future per-profile claudeDir lookup
	fs := flag.NewFlagSet("session search", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	limit := fs.Int("limit", 20, "Maximum number of results to return")
	recentDays := fs.Int("days", 30, "Only search sessions modified within the last N days (0 = all)")
	tierFlag := fs.String("tier", "auto", "Index tier: instant, balanced, auto")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session search <query> [options]")
		fmt.Println()
		fmt.Println("Search message content across all Claude sessions.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  <query>   Free-text query (case-insensitive substring match)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session search \"MCP server\"")
		fmt.Println("  agent-deck session search authentication --json")
		fmt.Println("  agent-deck session search \"database migration\" --limit 5")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(*jsonOutput, *quiet || *quietShort)

	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		out.Error("query is required", ErrCodeNotFound)
		fs.Usage()
		os.Exit(1)
	}

	claudeDir := session.GetClaudeConfigDir()
	searchEnabled := true
	cfg := session.GlobalSearchSettings{
		Enabled:        &searchEnabled,
		Tier:           *tierFlag,
		MemoryLimitMB:  100,
		RecentDays:     *recentDays,
		IndexRateLimit: 200,
	}
	index, err := session.NewGlobalSearchIndex(claudeDir, cfg)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize search index: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}
	if index == nil {
		out.Error("search index is disabled", ErrCodeNotFound)
		os.Exit(1)
	}
	defer index.Close()

	// Wait for the background initialLoad to finish. The fs.Size-based tier
	// detector returns immediately; content population is async. Poll up to
	// ~3s — enough for most ~.claude/projects but bounded for CLI latency.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !index.IsLoading() {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	results := index.Search(query)
	if *limit > 0 && len(results) > *limit {
		results = results[:*limit]
	}

	type hitJSON struct {
		SessionID string `json:"session_id"`
		Snippet   string `json:"snippet"`
		CWD       string `json:"cwd"`
		Summary   string `json:"summary,omitempty"`
		FilePath  string `json:"file_path,omitempty"`
	}

	hits := make([]hitJSON, 0, len(results))
	for _, r := range results {
		if r == nil || r.Entry == nil {
			continue
		}
		hits = append(hits, hitJSON{
			SessionID: r.Entry.SessionID,
			Snippet:   r.Snippet,
			CWD:       r.Entry.CWD,
			Summary:   r.Entry.Summary,
			FilePath:  r.Entry.FilePath,
		})
	}

	if *jsonOutput {
		out.Success("", map[string]interface{}{
			"query":   query,
			"results": hits,
			"count":   len(hits),
		})
		return
	}

	if len(hits) == 0 {
		fmt.Printf("No sessions matched %q\n", query)
		return
	}
	fmt.Printf("Found %d match(es) for %q:\n", len(hits), query)
	for i, h := range hits {
		fmt.Printf("%d. %s\n", i+1, h.SessionID)
		if h.CWD != "" {
			fmt.Printf("   cwd: %s\n", h.CWD)
		}
		if h.Snippet != "" {
			fmt.Printf("   %s\n", h.Snippet)
		}
	}
}
