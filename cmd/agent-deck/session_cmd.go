package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/profile"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/ui"
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
	case "restart":
		handleSessionRestart(profile, args[1:])
	case "revive":
		handleSessionRevive(profile, args[1:])
	case "fork":
		handleSessionFork(profile, args[1:])
	case "attach":
		handleSessionAttach(profile, args[1:])
	case "show":
		handleSessionShow(profile, args[1:])
	case "current":
		handleSessionCurrent(profile, args[1:])
	case "set-parent":
		handleSessionSetParent(profile, args[1:])
	case "unset-parent":
		handleSessionUnsetParent(profile, args[1:])
	case "set-transition-notify":
		handleSessionSetTransitionNotify(profile, args[1:])
	case "set-title-lock":
		handleSessionSetTitleLock(profile, args[1:])
	case "set":
		handleSessionSet(profile, args[1:])
	case "move", "mv":
		handleSessionMove(profile, args[1:])
	case "send":
		handleSessionSend(profile, args[1:])
	case "output":
		handleSessionOutput(profile, args[1:])
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
	fmt.Println("  restart [id] [--all]    Restart session (Claude: reload MCPs)")
	fmt.Println("  revive [--all|--name]   Rebuild dead control pipes for errored sessions")
	fmt.Println("  fork <id>               Fork Claude session with context")
	fmt.Println("  attach <id>             Attach to session interactively")
	fmt.Println("  show [id]               Show session details (auto-detect current if no id)")
	fmt.Println("  current                 Show current session and profile (auto-detect)")
	fmt.Println("  set <id> <field> <value>  Update session property")
	fmt.Println("  move <id> <path>        Move session to a new path (migrates Claude history)")
	fmt.Println("  send <id> <message>     Send a message to a running session")
	fmt.Println("  output <id>             Get the last response from a session")
	fmt.Println("  search <query>          Search message content across Claude sessions")
	fmt.Println("  set-parent <id> <parent>  Link session as sub-session of parent")
	fmt.Println("  unset-parent <id>       Remove sub-session link")
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
	yoloMode := fs.Bool("yolo", false, "Enable YOLO mode when starting Gemini or Codex sessions")

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
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Merge message flags
	initialMessage := mergeFlags(*message, *messageShort)

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

	// Save updated state
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Output success
	out.Success(fmt.Sprintf("Stopped session: %s", inst.Title), map[string]interface{}{
		"success": true,
		"id":      inst.ID,
		"title":   inst.Title,
	})
}

// handleSessionRestart restarts a session (or all active sessions with --all)
func handleSessionRestart(profile string, args []string) {
	fs := flag.NewFlagSet("session restart", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	force := fs.Bool("force", false, "Restart even if the session is already healthy and fresh (bypasses issue #30 guard)")
	all := fs.Bool("all", false, "Restart all active sessions")

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
		restartAllSessions(out, storage, instances, groups)
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
	if skip, reason := session.ShouldSkipRestart(inst, time.Now(), *force); skip {
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
	if err := inst.Restart(); err != nil {
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
func restartAllSessions(out *CLIOutput, storage *session.Storage, instances []*session.Instance, groups []*session.GroupData) {
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

		if err := inst.Restart(); err != nil {
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

// handleSessionFork forks a Claude session
func handleSessionFork(profile string, args []string) {
	fs := flag.NewFlagSet("session fork", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	title := fs.String("title", "", "Title for forked session")
	titleShort := fs.String("t", "", "Title for forked session (short)")
	group := fs.String("group", "", "Group for forked session")
	groupShort := fs.String("g", "", "Group for forked session (short)")
	worktreeBranch := fs.String("w", "", "Create fork in git worktree for branch")
	worktreeBranchLong := fs.String("worktree", "", "Create fork in git worktree for branch")
	newBranch := fs.Bool("b", false, "Create new branch (use with --worktree)")
	newBranchLong := fs.Bool("new-branch", false, "Create new branch")
	sandbox := fs.Bool("sandbox", false, "Run forked session in Docker sandbox")
	sandboxImage := fs.String("sandbox-image", "", "Docker image for sandbox (overrides config default)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session fork <id|title> [options]")
		fmt.Println()
		fmt.Println("Fork a Claude session with conversation context.")
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

	// Verify it's a Claude session
	if !session.IsClaudeCompatible(inst.Tool) {
		out.Error(
			fmt.Sprintf("session '%s' is not a Claude session (tool: %s)", inst.Title, inst.Tool),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}

	// Try to capture session ID from tmux if missing (handles pre-fix sessions)
	if inst.ClaudeSessionID == "" && inst.Exists() {
		inst.PostStartSync(2 * time.Second)
	}

	// Verify it can be forked
	if !inst.CanFork() {
		out.Error(
			fmt.Sprintf("session '%s' cannot be forked: no active Claude session ID", inst.Title),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}

	// Default title if not provided
	if forkTitle == "" {
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

	// Handle worktree creation
	var opts *session.ClaudeOptions
	if wtBranch != "" {
		if !git.IsGitRepoOrBareProjectRoot(inst.ProjectPath) {
			out.Error("session path is not a git repository", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		repoRoot, err := git.GetWorktreeBaseRoot(inst.ProjectPath)
		if err != nil {
			out.Error(fmt.Sprintf("failed to get repo root: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		// Apply configured branch prefix before validation/existence checks
		wtSettings := session.GetWorktreeSettings()
		wtBranch = wtSettings.ApplyBranchPrefix(wtBranch)

		if !createNewBranch && !git.BranchExists(repoRoot, wtBranch) {
			out.Error(fmt.Sprintf("branch '%s' does not exist (use -b to create)", wtBranch), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		worktreePath := git.WorktreePath(git.WorktreePathOptions{
			Branch:    wtBranch,
			Location:  wtSettings.DefaultLocation,
			RepoDir:   repoRoot,
			SessionID: git.GeneratePathID(),
			Template:  wtSettings.Template(),
		})

		// Check for an existing worktree for this branch before creating a new one
		if existingPath, err := git.GetWorktreeForBranch(repoRoot, wtBranch); err == nil && existingPath != "" {
			fmt.Fprintf(os.Stderr, "Reusing existing worktree at %s for branch %s\n", existingPath, wtBranch)
			worktreePath = existingPath
		} else {
			if _, statErr := os.Stat(worktreePath); statErr == nil {
				out.Error(fmt.Sprintf("worktree path already exists: %s", worktreePath), ErrCodeInvalidOperation)
				os.Exit(1)
			}

			if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
				out.Error(fmt.Sprintf("failed to create directory: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}

			setupErr, err := git.CreateWorktreeWithSetup(repoRoot, worktreePath, wtBranch, os.Stdout, os.Stderr)
			if err != nil {
				out.Error(fmt.Sprintf("worktree creation failed: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
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
	forkedInst, _, err := inst.CreateForkedInstanceWithOptions(forkTitle, forkGroup, opts)
	if err != nil {
		out.Error(fmt.Sprintf("failed to create fork: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Apply sandbox config if requested.
	if *sandbox {
		forkedInst.Sandbox = session.NewSandboxConfig(*sandboxImage)
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
	if forkedInst.GroupPath != "" {
		groupTree.CreateGroup(forkedInst.GroupPath)
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
	}

	if tmuxSession := inst.GetTmuxSession(); tmuxSession != nil {
		jsonData["tmux_session"] = tmuxSession.Name
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
		fmt.Println("  extra-args         Extra claude CLI tokens (claude only; use `-- --flag value` for tokens starting with -; persisted plaintext — no secrets)")
		fmt.Println("  color              Optional TUI row tint: '#RRGGBB' or ANSI '0'..'255' or '' (issue #391)")
		fmt.Println("  claude-session-id  Claude conversation ID")
		fmt.Println("  gemini-session-id  Gemini conversation ID")
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

	// Validate field name
	validFields := map[string]bool{
		"title":             true,
		"path":              true,
		"command":           true,
		"tool":              true,
		"wrapper":           true,
		"channels":          true,
		"extra-args":        true,
		"color":             true,
		"claude-session-id": true,
		"gemini-session-id": true,
	}

	if !validFields[field] {
		out.Error(
			fmt.Sprintf(
				"invalid field: %s\nValid fields: title, path, command, tool, wrapper, channels, extra-args, color, claude-session-id, gemini-session-id",
				field,
			),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}

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

	// Store old value for output
	var oldValue string

	// Apply the update
	switch field {
	case "title":
		oldValue = inst.Title
		inst.Title = value
		inst.SyncTmuxDisplayName()
	case "path":
		oldValue = inst.ProjectPath
		inst.ProjectPath = value
	case "command":
		oldValue = inst.Command
		inst.Command = value
	case "tool":
		oldValue = inst.Tool
		inst.Tool = value
	case "wrapper":
		oldValue = inst.Wrapper
		inst.Wrapper = value
	case "channels":
		// channels is a Claude Code CLI flag; only meaningful for claude sessions.
		if inst.Tool != "claude" {
			out.Error(
				fmt.Sprintf("channels only supported for claude sessions (this session's tool is %q); requires --channels on the claude binary", inst.Tool),
				ErrCodeInvalidOperation,
			)
			os.Exit(1)
		}
		oldValue = strings.Join(inst.Channels, ",")
		// Parse CSV value: trim whitespace, drop empties.
		parsed := []string{}
		for _, raw := range strings.Split(value, ",") {
			if s := strings.TrimSpace(raw); s != "" {
				parsed = append(parsed, s)
			}
		}
		inst.Channels = parsed
	case "color":
		// Per-session color tint (issue #391). Opt-in; empty clears.
		oldValue = inst.Color
		trimmed := strings.TrimSpace(value)
		if !isValidSessionColor(trimmed) {
			out.Error(
				fmt.Sprintf("invalid color %q — expected '#RRGGBB', ANSI '0'..'255', or '' to clear", trimmed),
				ErrCodeInvalidOperation,
			)
			os.Exit(1)
		}
		inst.Color = trimmed
	case "extra-args":
		// extra-args are passed to the claude binary by buildClaudeExtraFlags;
		// only meaningful for claude sessions. Every positional arg after the
		// field name is treated as one already-tokenised extra arg. Use `--`
		// so Go's flag package leaves tokens starting with `-` alone:
		//   agent-deck session set <id> extra-args -- --model opus
		// Empty string clears all extra args. Empty tokens mixed with real
		// ones are dropped (avoid emitting literal `''` to claude).
		//
		// Note: extra-args persist in state.db as plaintext. Do NOT pass
		// secrets like API keys via --extra-arg.
		if inst.Tool != "claude" {
			out.Error(
				fmt.Sprintf("extra-args only supported for claude sessions (this session's tool is %q); claude is the only tool whose builder appends user extra args", inst.Tool),
				ErrCodeInvalidOperation,
			)
			os.Exit(1)
		}
		oldValue = strings.Join(inst.ExtraArgs, " ")
		cleaned := make([]string, 0, len(extraArgTokens))
		for _, tok := range extraArgTokens {
			if tok != "" {
				cleaned = append(cleaned, tok)
			}
		}
		if len(cleaned) == 0 {
			inst.ExtraArgs = nil
		} else {
			inst.ExtraArgs = cleaned
		}
	case "claude-session-id":
		oldValue = inst.ClaudeSessionID
		inst.ClaudeSessionID = value
		inst.ClaudeDetectedAt = time.Now()
		// Also update tmux environment if session is running
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
			_ = tmux.Exec(inst.TmuxSocketName, "set-environment", "-t", tmuxSess.Name, "CLAUDE_SESSION_ID", value).Run()
		}
	case "gemini-session-id":
		oldValue = inst.GeminiSessionID
		inst.GeminiSessionID = value
		inst.GeminiDetectedAt = time.Now()
		// Also update tmux environment if session is running
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
			_ = tmux.Exec(inst.TmuxSocketName, "set-environment", "-t", tmuxSess.Name, "GEMINI_SESSION_ID", value).Run()
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

	// v1.7.22: emit telegram topology warnings whenever the signals the
	// validator cares about change (wrapper or channels). The silent-path
	// case writes nothing — see ValidateTelegramTopology (#658).
	if field == "wrapper" || field == "channels" {
		cfgDir := session.GetClaudeConfigDirForGroup(inst.GroupPath)
		globalTelegramEnabled, _ := readTelegramGloballyEnabled(cfgDir)
		emitTelegramWarnings(os.Stderr, session.TelegramValidatorInput{
			GlobalEnabled:   globalTelegramEnabled,
			SessionChannels: inst.Channels,
			SessionWrapper:  inst.Wrapper,
		})
	}
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

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session set-parent <session> <parent>")
		fmt.Println()
		fmt.Println("Link a session as a sub-session of another session.")
		fmt.Println("The session will inherit the parent's group.")
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

	// Set parent (with project path for --add-dir access) and inherit group
	inst.SetParentWithPath(parentInst.ID, parentInst.ProjectPath)
	inst.GroupPath = parentInst.GroupPath

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
		"inherited_group": inst.GroupPath,
	})
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
	timeout := fs.Duration("timeout", 10*time.Minute, "Max time to wait for completion (used with --wait)")
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
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	remaining := fs.Args()

	out := NewCLIOutput(*jsonOutput, *quiet)

	if len(remaining) < 2 {
		fs.Usage()
		out.Error("session and message are required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if *stream && *wait {
		out.Error("--stream and --wait are mutually exclusive", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	sessionRef := remaining[0]
	message := strings.Join(remaining[1:], " ")

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

	// Get tmux session
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		out.Error("could not determine tmux session", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Wait for agent to be ready (unless --no-wait is specified)
	if !*noWait {
		if err := waitForAgentReady(tmuxSess, inst.Tool); err != nil {
			out.Error(fmt.Sprintf("timeout waiting for agent: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Record send time before the actual send so we can verify output freshness.
	// Captured early to avoid false negatives from clock skew.
	sentAt := time.Now()

	// Send message atomically (text + Enter in single tmux invocation).
	// --no-wait: skip full readiness waiting, but run a capped preflight
	// barrier + extended verification loop to avoid the #616 race where
	// Claude's composer renders after the loop has already returned
	// success on startup "active" status, leaving the message unsubmitted.
	// default mode: full retry budget after readiness check.
	if *noWait {
		if err := sendNoWait(tmuxSess, inst.Tool, message); err != nil {
			out.Error(fmt.Sprintf("failed to send message: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	} else {
		if err := sendWithRetry(tmuxSess, message, false); err != nil {
			out.Error(fmt.Sprintf("failed to send message: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	if !*stream {
		out.Success(fmt.Sprintf("Sent message to '%s'", inst.Title), map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"message":       message,
		})
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
		response, err := waitForFreshOutput(inst, sentAt)
		if err != nil {
			// Fallback: reload session from DB in case tmux env was also stale
			// (e.g., /clear created a new session that TUI or hooks detected)
			if _, freshInstances, _, loadErr := loadSessionData(profile); loadErr == nil {
				if freshInst, _, _ := ResolveSession(sessionRef, freshInstances); freshInst != nil {
					response, err = waitForFreshOutput(freshInst, sentAt)
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

// sendWithRetry sends a message atomically and retries Enter if the agent
// doesn't start processing within a reasonable time.
func sendWithRetry(tmuxSess *tmux.Session, message string, skipVerify bool) error {
	return sendWithRetryTarget(tmuxSess, message, skipVerify, sendRetryOptions{
		maxRetries: 50,
		checkDelay: 300 * time.Millisecond,
	})
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

// sendNoWait implements `session send --no-wait` semantics for the CLI.
//
// Issue #616 fix has three layers, applied in order:
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
// own readiness shapes and upstream gating.
func sendNoWait(target sendRetryTarget, tool, message string) error {
	if session.IsClaudeCompatible(tool) {
		if awaitComposerReadyBestEffort(target, 5*time.Second, 100*time.Millisecond) {
			// Post-composer settle: React mount can lag behind the
			// composer glyph by a few hundred ms on cold starts.
			time.Sleep(500 * time.Millisecond)
		}
	}
	return sendWithRetryTarget(target, message, false, noWaitSendOptions())
}

type sendRetryTarget interface {
	SendKeysAndEnter(string) error
	GetStatus() (string, error)
	SendEnter() error
	SendCtrlC() error
	CapturePaneFresh() (string, error)
}

type sendRetryOptions struct {
	maxRetries     int
	checkDelay     time.Duration
	maxFullResends int // >0 overrides default (3); <0 disables Ctrl+C-then-resend; 0 uses default
}

func sendWithRetryTarget(target sendRetryTarget, message string, skipVerify bool, opts sendRetryOptions) error {
	if opts.maxRetries <= 0 {
		opts.maxRetries = 1
	}
	if opts.checkDelay < 0 {
		opts.checkDelay = 0
	}

	if err := target.SendKeysAndEnter(message); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	if skipVerify {
		return nil
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
	for retry := 0; retry < opts.maxRetries; retry++ {
		time.Sleep(opts.checkDelay)

		unsentPromptDetected := false
		if rawContent, captureErr := target.CapturePaneFresh(); captureErr == nil {
			content := tmux.StripANSI(rawContent)
			unsentPromptDetected = send.HasUnsentPastedPrompt(content) || send.HasUnsentComposerPrompt(content, message)
		}
		status, err := target.GetStatus()

		if unsentPromptDetected {
			waitingNoMarkerChecks = 0
			waitingNoActivityChecks = 0
			activeChecks = 0
			_ = target.SendEnter()
			continue
		}

		if err == nil && status == "active" {
			sawActiveAfterSend = true
			waitingNoMarkerChecks = 0
			waitingNoActivityChecks = 0
			activeChecks++
			if activeChecks >= activeSuccessThreshold {
				return nil
			}
			continue
		}
		activeChecks = 0

		if err == nil && (status == "waiting" || status == "idle") {
			if sawActiveAfterSend {
				waitingNoMarkerChecks++
				waitingNoActivityChecks = 0
				if waitingNoMarkerChecks >= waitingAfterActiveThreshold {
					return nil
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

	// Best effort: don't fail even if verification is inconclusive.
	return nil
}

// waitForAgentReady waits for Claude/Gemini/other agents to be ready for input
// Uses status detection: waits for "active" → "waiting" transition
func waitForAgentReady(tmuxSess *tmux.Session, tool string) error {
	sawActive := false
	readyCount := 0
	maxAttempts := 400 // 80 seconds max (400 * 200ms)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(200 * time.Millisecond)

		status, err := tmuxSess.GetStatus()
		if err != nil {
			readyCount = 0
			continue
		}

		if status == "active" {
			sawActive = true
			readyCount = 0
			continue
		}

		if status == "waiting" || status == "idle" {
			readyCount++
		} else {
			readyCount = 0
		}

		// Agent is ready when:
		// 1. We've seen "active" (loading) and now see "waiting" (ready)
		// 2. We've seen stable waiting/idle 10+ times (already ready)
		alreadyReady := readyCount >= 10 && attempt >= 15 // At least 3s elapsed
		if (sawActive && (status == "waiting" || status == "idle")) || alreadyReady {
			if tool == "claude" {
				if rawContent, captureErr := tmuxSess.CapturePaneFresh(); captureErr == nil && !send.HasCurrentComposerPrompt(tmux.StripANSI(rawContent)) {
					// Claude can report waiting before the interactive prompt is visible.
					// Keep polling until the prompt line is present.
					continue
				}
			}
			// Gate Codex sends on prompt readiness: wait for "codex>" or
			// "Continue?" to be visible before considering the agent ready.
			if tool == "codex" {
				if rawContent, captureErr := tmuxSess.CapturePaneFresh(); captureErr == nil {
					content := tmux.StripANSI(rawContent)
					detector := tmux.NewPromptDetector("codex")
					if !detector.HasPrompt(content) {
						// Codex hasn't shown its prompt yet; keep polling.
						continue
					}
				}
			}
			time.Sleep(300 * time.Millisecond) // Small delay for UI to render
			return nil
		}
	}

	return fmt.Errorf("agent not ready after 80 seconds")
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
func waitForFreshOutput(inst *session.Instance, sentAt time.Time) (*session.ResponseOutput, error) {
	// Non-Claude tools don't use JSONL timestamps — skip the freshness loop.
	if !session.IsClaudeCompatible(inst.Tool) {
		return inst.GetLastResponseBestEffort()
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
	deadline := time.Now().Add(opts.timeout)
	for time.Now().Before(deadline) {
		jsonlPath = resolvedInst.GetJSONLPath()
		if jsonlPath != "" {
			break
		}
		// Refresh from DB in case the session was just created.
		if _, freshInstances, _, loadErr := loadSessionData(profile); loadErr == nil {
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

	// Get the last response (best-effort fallback for smoother CLI reads)
	response, err := inst.GetLastResponseBestEffort()
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

// isValidSessionColor validates a per-session color tint (issue #391).
// Accepts:
//   - "" (empty / opt-out)
//   - "#RRGGBB" or "#rrggbb" (24-bit truecolor hex, exactly 6 hex digits)
//   - "0".."255" (ANSI 256-palette index, decimal)
//
// Rejects everything else so typos like "red" or "#12" don't quietly persist
// into the TUI render layer where they'd fall through to lipgloss defaults
// with surprising results. Kept as a pure function so the test table stays
// CLI-free (see TestIsValidSessionColor).
func isValidSessionColor(v string) bool {
	if v == "" {
		return true
	}
	// Truecolor hex: '#' + 6 hex digits exactly.
	if len(v) == 7 && v[0] == '#' {
		for i := 1; i < 7; i++ {
			c := v[i]
			ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !ok {
				return false
			}
		}
		return true
	}
	// ANSI 256-palette index: 0..255 as decimal.
	n := 0
	if len(v) == 0 || len(v) > 3 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n >= 0 && n <= 255
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
	cfg := session.GlobalSearchSettings{
		Enabled:        true,
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
