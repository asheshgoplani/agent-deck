package main

import (
	"context"
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
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
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
	case "restart":
		handleSessionRestart(profile, args[1:])
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
	case "set":
		handleSessionSet(profile, args[1:])
	case "send":
		handleSessionSend(profile, args[1:])
	case "output":
		handleSessionOutput(profile, args[1:])
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
	fmt.Println("  restart <id>            Restart session (Claude: reload MCPs)")
	fmt.Println("  fork <id>               Fork Claude session with context")
	fmt.Println("  attach <id>             Attach to session interactively")
	fmt.Println("  show [id]               Show session details (auto-detect current if no id)")
	fmt.Println("  current                 Show current session and profile (auto-detect)")
	fmt.Println("  set <id> <field> <value>  Update session property")
	fmt.Println("  send <id> <message>     Send a message to a running session")
	fmt.Println("  output <id>             Get the last response from a session")
	fmt.Println("  set-parent <id> <parent>  Link session as sub-session of parent")
	fmt.Println("  unset-parent <id>       Remove sub-session link")
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
	fmt.Println("  agent-deck session fork my-project -t \"my-project-fork\"")
	fmt.Println("  agent-deck session attach my-project")
	fmt.Println("  agent-deck session show                  # Auto-detect current session")
	fmt.Println("  agent-deck session show my-project --json")
	fmt.Println("  agent-deck session set-parent sub-task main-project  # Make sub-task a sub-session")
	fmt.Println("  agent-deck session unset-parent sub-task             # Remove sub-session link")
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
	storage, instances, _, err := loadSessionData(profile)
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
	if err := saveSessionData(storage, instances); err != nil {
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
		jsonData["message_pending"] = true
		out.Success(fmt.Sprintf("Started session: %s (message will be sent when ready)", inst.Title), jsonData)
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
	storage, instances, _, err := loadSessionData(profile)
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

	// Stop the session by killing the tmux session
	if err := inst.Kill(); err != nil {
		out.Error(fmt.Sprintf("failed to stop session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Save updated state
	if err := saveSessionData(storage, instances); err != nil {
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

// handleSessionRestart restarts a session
func handleSessionRestart(profile string, args []string) {
	fs := flag.NewFlagSet("session restart", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session restart <id|title> [options]")
		fmt.Println()
		fmt.Println("Restart a session. For Claude sessions, this reloads MCPs.")
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
	storage, instances, _, err := loadSessionData(profile)
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

	// Restart the session
	if err := inst.Restart(); err != nil {
		out.Error(fmt.Sprintf("failed to restart session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// If restart created a fresh session (no prior ID), capture the new ID
	if inst.Tool == "claude" && inst.ClaudeSessionID == "" {
		inst.PostStartSync(3 * time.Second)
	}

	// Save updated state
	if err := saveSessionData(storage, instances); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Output success
	out.Success(fmt.Sprintf("Restarted session: %s", inst.Title), map[string]interface{}{
		"success": true,
		"id":      inst.ID,
		"title":   inst.Title,
	})
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
	if inst.Tool != "claude" {
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
		if !git.IsGitRepo(inst.ProjectPath) {
			out.Error("session path is not a git repository", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		repoRoot, err := git.GetWorktreeBaseRoot(inst.ProjectPath)
		if err != nil {
			out.Error(fmt.Sprintf("failed to get repo root: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		if !createNewBranch && !git.BranchExists(repoRoot, wtBranch) {
			out.Error(fmt.Sprintf("branch '%s' does not exist (use -b to create)", wtBranch), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		wtSettings := session.GetWorktreeSettings()
		worktreePath := git.WorktreePath(git.WorktreePathOptions{
			Branch:    wtBranch,
			Location:  wtSettings.DefaultLocation,
			RepoDir:   repoRoot,
			SessionID: git.GeneratePathID(),
			Template:  wtSettings.Template(),
		})

		if _, statErr := os.Stat(worktreePath); statErr == nil {
			out.Error(fmt.Sprintf("worktree path already exists: %s", worktreePath), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
			out.Error(fmt.Sprintf("failed to create directory: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		if err := git.CreateWorktree(repoRoot, worktreePath, wtBranch); err != nil {
			out.Error(fmt.Sprintf("worktree creation failed: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
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

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session attach <id|title>")
		fmt.Println()
		fmt.Println("Attach to a session interactively.")
		fmt.Println("Press Ctrl+Q to detach.")
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

	if err := tmuxSession.Attach(ctx); err != nil {
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

	// Update status
	_ = inst.UpdateStatus()

	// Get MCP info if Claude session
	var mcpInfo *session.MCPInfo
	if inst.Tool == "claude" {
		mcpInfo = inst.GetMCPInfo()
	}

	// Prepare JSON output
	jsonData := map[string]interface{}{
		"id":         inst.ID,
		"title":      inst.Title,
		"profile":    profile,
		"status":     StatusString(inst.Status),
		"path":       inst.ProjectPath,
		"group":      inst.GroupPath,
		"tool":       inst.Tool,
		"created_at": inst.CreatedAt.Format(time.RFC3339),
	}

	if inst.Command != "" {
		jsonData["command"] = inst.Command
	}

	if inst.Tool == "claude" {
		jsonData["claude_session_id"] = inst.ClaudeSessionID
		jsonData["can_fork"] = inst.CanFork()
		jsonData["can_restart"] = inst.CanRestart()

		if mcpInfo != nil && mcpInfo.HasAny() {
			jsonData["mcps"] = map[string]interface{}{
				"local":   mcpInfo.Local,
				"global":  mcpInfo.Global,
				"project": mcpInfo.Project,
			}
		}
	}

	if inst.Exists() {
		tmuxSession := inst.GetTmuxSession()
		if tmuxSession != nil {
			jsonData["tmux_session"] = tmuxSession.Name
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

	if inst.Command != "" {
		sb.WriteString(fmt.Sprintf("Command: %s\n", inst.Command))
	}

	if inst.Tool == "claude" {
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
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Validate field name
	validFields := map[string]bool{
		"title":             true,
		"path":              true,
		"command":           true,
		"tool":              true,
		"wrapper":           true,
		"claude-session-id": true,
		"gemini-session-id": true,
	}

	if !validFields[field] {
		out.Error(
			fmt.Sprintf(
				"invalid field: %s\nValid fields: title, path, command, tool, wrapper, claude-session-id, gemini-session-id",
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
	case "claude-session-id":
		oldValue = inst.ClaudeSessionID
		inst.ClaudeSessionID = value
		inst.ClaudeDetectedAt = time.Now()
		// Also update tmux environment if session is running
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
			_ = exec.Command("tmux", "set-environment", "-t", tmuxSess.Name, "CLAUDE_SESSION_ID", value).Run()
		}
	case "gemini-session-id":
		oldValue = inst.GeminiSessionID
		inst.GeminiSessionID = value
		inst.GeminiDetectedAt = time.Now()
		// Also update tmux environment if session is running
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
			_ = exec.Command("tmux", "set-environment", "-t", tmuxSess.Name, "GEMINI_SESSION_ID", value).Run()
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

// saveSessionData saves session data with groups
func saveSessionData(storage *session.Storage, instances []*session.Instance) error {
	// Rebuild group tree from instances
	groupTree := session.NewGroupTree(instances)
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

// handleSessionSend sends a message to a running session
// Waits for the agent to be ready before sending (Claude, Gemini, etc.)
func handleSessionSend(profile string, args []string) {
	fs := flag.NewFlagSet("session send", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")
	noWait := fs.Bool("no-wait", false, "Don't wait for agent to be ready (send immediately)")
	wait := fs.Bool("wait", false, "Block until agent finishes processing, then print output")
	timeout := fs.Duration("timeout", 10*time.Minute, "Max time to wait for completion (used with --wait)")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	remaining := fs.Args()

	out := NewCLIOutput(*jsonOutput, *quiet)

	if len(remaining) < 2 {
		out.Error("usage: agent-deck session send <id> <message>", ErrCodeInvalidOperation)
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

	// Send message atomically (text + Enter in single tmux invocation)
	// with retry to handle rare cases where Enter is still dropped
	if err := sendWithRetry(tmuxSess, message); err != nil {
		out.Error(fmt.Sprintf("failed to send message: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Sent message to '%s'", inst.Title), map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"message":       message,
	})

	// If --wait, block until the agent finishes processing, then print output
	if *wait {
		finalStatus, err := waitForCompletion(tmuxSess, *timeout)
		if err != nil {
			out.Error(fmt.Sprintf("timeout waiting for completion: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		// Fetch and print last response (like session output -q)
		response, err := inst.GetLastResponse()
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
func sendWithRetry(tmuxSess *tmux.Session, message string) error {
	if err := tmuxSess.SendKeysAndEnter(message); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	// Verify: check if agent transitions to "active" (processing).
	// If not, the Enter may have been lost; retry just the Enter key.
	const maxRetries = 2
	const checkDelay = 500 * time.Millisecond

	for retry := 0; retry < maxRetries; retry++ {
		time.Sleep(checkDelay)
		status, err := tmuxSess.GetStatus()
		if err == nil && status == "active" {
			return nil // Agent is processing
		}
		// Retry just the Enter key
		_ = tmuxSess.SendEnter()
	}
	// Best effort: don't fail even if verification is inconclusive
	// (agent might process fast enough that we miss the "active" window)
	return nil
}

// waitForAgentReady waits for Claude/Gemini/other agents to be ready for input
// Uses status detection: waits for "active" → "waiting" transition
func waitForAgentReady(tmuxSess *tmux.Session, tool string) error {
	sawActive := false
	waitingCount := 0
	maxAttempts := 400 // 80 seconds max (400 * 200ms)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(200 * time.Millisecond)

		status, err := tmuxSess.GetStatus()
		if err != nil {
			waitingCount = 0
			continue
		}

		if status == "active" {
			sawActive = true
			waitingCount = 0
			continue
		}

		if status == "waiting" {
			waitingCount++
		} else {
			waitingCount = 0
		}

		// Agent is ready when:
		// 1. We've seen "active" (loading) and now see "waiting" (ready)
		// 2. We've seen "waiting" 10+ times (already ready)
		alreadyReady := waitingCount >= 10 && attempt >= 15 // At least 3s elapsed
		if (sawActive && status == "waiting") || alreadyReady {
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

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("agent still running after %s", timeout)
		default:
		}

		status, err := checker.GetStatus()
		if err != nil {
			// Transient tmux error, keep polling
			time.Sleep(pollInterval)
			continue
		}

		// "active" means still processing, keep waiting
		if status == "active" {
			time.Sleep(pollInterval)
			continue
		}

		// Any non-active status means the agent is done
		return status, nil
	}
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

	// Get the last response
	response, err := inst.GetLastResponse()
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
