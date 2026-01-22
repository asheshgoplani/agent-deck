package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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
	fmt.Println("  claude-session-id  Claude conversation ID (for fork/resume)")
	fmt.Println("  gemini-session-id  Gemini conversation ID (for resume)")
	fmt.Println()
	fmt.Println("Set examples:")
	fmt.Println("  agent-deck session set my-project title \"New Title\"")
	fmt.Println("  agent-deck session set my-project claude-session-id \"abc123-def456\"")
	fmt.Println("  agent-deck session set my-project tool claude")
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

	if err := fs.Parse(args); err != nil {
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

	if err := fs.Parse(args); err != nil {
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

	if err := fs.Parse(args); err != nil {
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
	}

	// Restart the session
	if err := inst.Restart(); err != nil {
		out.Error(fmt.Sprintf("failed to restart session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
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
	}

	if err := fs.Parse(args); err != nil {
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
	}

	// Verify it's a Claude session
	if inst.Tool != "claude" {
		out.Error(fmt.Sprintf("session '%s' is not a Claude session (tool: %s)", inst.Title, inst.Tool), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Verify it can be forked
	if !inst.CanFork() {
		out.Error(fmt.Sprintf("session '%s' cannot be forked: no active Claude session ID", inst.Title), ErrCodeInvalidOperation)
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

	// Create the forked instance
	forkedInst, _, err := inst.CreateForkedInstance(forkTitle, forkGroup)
	if err != nil {
		out.Error(fmt.Sprintf("failed to create fork: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Start the forked session
	if err := forkedInst.Start(); err != nil {
		out.Error(fmt.Sprintf("failed to start forked session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

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
	out.Success(fmt.Sprintf("Forked session: %s -> %s (%s)", inst.Title, forkedInst.Title, TruncateID(forkedInst.ID)), map[string]interface{}{
		"success":   true,
		"parent_id": inst.ID,
		"new_id":    forkedInst.ID,
		"new_title": forkedInst.Title,
	})
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

	if err := fs.Parse(args); err != nil {
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

	if err := fs.Parse(args); err != nil {
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
	}

	if err := fs.Parse(args); err != nil {
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
		"claude-session-id": true,
		"gemini-session-id": true,
	}

	if !validFields[field] {
		out.Error(fmt.Sprintf("invalid field: %s\nValid fields: title, path, command, tool, claude-session-id, gemini-session-id", field), ErrCodeInvalidOperation)
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
	}

	// Store old value for output
	var oldValue string

	// Apply the update
	switch field {
	case "title":
		oldValue = inst.Title
		inst.Title = value
	case "path":
		oldValue = inst.ProjectPath
		inst.ProjectPath = value
	case "command":
		oldValue = inst.Command
		inst.Command = value
	case "tool":
		oldValue = inst.Tool
		inst.Tool = value
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

	// LoadWithGroups already reconnects tmux sessions and updates status

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

	if err := fs.Parse(args); err != nil {
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
	}

	// Resolve the parent session
	parentInst, errMsg, errCode := ResolveSession(parentID, instances)
	if parentInst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
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
			out.Error(fmt.Sprintf("session '%s' already has sub-sessions, cannot become a sub-session", inst.Title), ErrCodeInvalidOperation)
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

	if err := fs.Parse(args); err != nil {
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

	out.Success(fmt.Sprintf("Removed sub-session link from '%s' (was linked to '%s')", inst.Title, parentTitle), map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"former_parent": parentTitle,
	})
}

// handleSessionSend sends a message to a running session
// Waits for the agent to be ready before sending (Claude, Gemini, etc.)
func handleSessionSend(profile string, args []string) {
	fs := flag.NewFlagSet("session send", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")
	noWait := fs.Bool("no-wait", false, "Don't wait for agent to be ready (send immediately)")
	if err := fs.Parse(args); err != nil {
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

	// Send message via tmux
	tmuxName := tmuxSess.Name
	cmd := exec.Command("tmux", "send-keys", "-l", "-t", tmuxName, message)
	if err := cmd.Run(); err != nil {
		out.Error(fmt.Sprintf("failed to send message: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Send Enter
	cmd = exec.Command("tmux", "send-keys", "-t", tmuxName, "Enter")
	if err := cmd.Run(); err != nil {
		out.Error(fmt.Sprintf("failed to send Enter: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Sent message to '%s'", inst.Title), map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"message":       message,
	})
}

// waitForAgentReady waits for Claude/Gemini/other agents to be ready for input
// Uses status detection: waits for "active" → "waiting" transition
func waitForAgentReady(tmuxSess *tmux.Session, tool string) error {
	sawActive := false
	waitingCount := 0
	maxAttempts := 300 // 60 seconds max (300 * 200ms)

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

	return fmt.Errorf("agent not ready after 60 seconds")
}

// handleSessionOutput gets the last response from a session
func handleSessionOutput(profile string, args []string) {
	fs := flag.NewFlagSet("session output", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session output [id|title] [options]")
		fmt.Println()
		fmt.Println("Get the last response from a session. If no ID is provided, auto-detects current session.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
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
	}

	// Get the last response
	response, err := inst.GetLastResponse()
	if err != nil {
		out.Error(fmt.Sprintf("failed to get response: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
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

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Check if we're in a tmux session
	if os.Getenv("TMUX") == "" {
		out.Error("not in a tmux session", ErrCodeNotFound)
		os.Exit(1)
	}

	// Detect profile: use explicit arg if provided, otherwise auto-detect
	detectedProfile := profileArg
	if detectedProfile == "" || detectedProfile == session.DefaultProfile {
		detectedProfile = profile.DetectCurrentProfile()
	}

	// Load sessions for detected profile first
	_, instances, _, err := loadSessionData(detectedProfile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Try to find session in detected profile
	inst := findSessionByTmux(instances)

	// If not found in detected profile, search all profiles
	if inst == nil {
		var foundProfile string
		inst, foundProfile = findSessionByTmuxAcrossProfiles()
		if inst != nil && foundProfile != "" {
			detectedProfile = foundProfile
		}
	}

	if inst == nil {
		out.Error("current tmux session is not an agent-deck session\nHint: Run 'agent-deck list' to see available sessions", ErrCodeNotFound)
		os.Exit(1)
	}

	// Update status
	_ = inst.UpdateStatus()

	// Quiet mode: just print session name
	if quietMode {
		fmt.Println(inst.Title)
		return
	}

	// Prepare JSON output
	jsonData := map[string]interface{}{
		"session": inst.Title,
		"profile": detectedProfile,
		"id":      inst.ID,
		"path":    inst.ProjectPath,
		"status":  StatusString(inst.Status),
	}

	if inst.GroupPath != "" {
		jsonData["group"] = inst.GroupPath
	}

	// Build human-readable output
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session: %s\n", inst.Title))
	sb.WriteString(fmt.Sprintf("Profile: %s\n", detectedProfile))
	sb.WriteString(fmt.Sprintf("ID:      %s\n", inst.ID))
	sb.WriteString(fmt.Sprintf("Status:  %s %s\n", StatusSymbol(inst.Status), StatusString(inst.Status)))
	sb.WriteString(fmt.Sprintf("Path:    %s\n", FormatPath(inst.ProjectPath)))
	if inst.GroupPath != "" {
		sb.WriteString(fmt.Sprintf("Group:   %s\n", inst.GroupPath))
	}

	out.Print(sb.String(), jsonData)
}
