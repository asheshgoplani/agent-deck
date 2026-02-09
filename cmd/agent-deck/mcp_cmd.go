package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleMCP handles all mcp subcommands
func handleMCP(profile string, args []string) {
	if len(args) == 0 {
		printMCPHelp()
		os.Exit(1)
	}

	switch args[0] {
	case "list", "ls":
		handleMCPList(args[1:])
	case "attached":
		handleMCPAttached(profile, args[1:])
	case "attach":
		handleMCPAttach(profile, args[1:])
	case "detach":
		handleMCPDetach(profile, args[1:])
	case "server":
		handleMCPServer(args[1:])
	case "help", "-h", "--help":
		printMCPHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown mcp command '%s'\n", args[0])
		printMCPHelp()
		os.Exit(1)
	}
}

// printMCPHelp prints help for mcp commands
func printMCPHelp() {
	fmt.Println("Usage: agent-deck mcp <command> [options]")
	fmt.Println()
	fmt.Println("Manage MCP (Model Context Protocol) servers for sessions.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list                List all available MCPs from config.toml")
	fmt.Println("  attached [id]       Show MCPs attached to a session")
	fmt.Println("  attach <id> <mcp>   Attach an MCP to a session")
	fmt.Println("  detach <id> <mcp>   Detach an MCP from a session")
	fmt.Println("  server <cmd>        Manage HTTP MCP servers (start/stop/status)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck mcp list                        # List available MCPs")
	fmt.Println("  agent-deck mcp attached                    # Show MCPs for current session")
	fmt.Println("  agent-deck mcp attached my-project         # Show MCPs for specific session")
	fmt.Println("  agent-deck mcp attach my-project exa       # Attach exa to my-project (local)")
	fmt.Println("  agent-deck mcp attach my-project exa --global     # Attach globally")
	fmt.Println("  agent-deck mcp detach my-project exa       # Detach exa from my-project")
	fmt.Println("  agent-deck mcp server status               # Show HTTP server status")
	fmt.Println("  agent-deck mcp server start slack          # Start HTTP server for slack MCP")
}

// handleMCPList lists all available MCPs from config.toml
func handleMCPList(args []string) {
	fs := flag.NewFlagSet("mcp list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck mcp list [options]")
		fmt.Println()
		fmt.Println("List all available MCPs from config.toml.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Get available MCPs from config.toml
	mcps := session.GetAvailableMCPs()

	if len(mcps) == 0 {
		if *jsonOutput {
			out.Print("", map[string]interface{}{
				"mcps": []interface{}{},
			})
		} else if !quietMode {
			fmt.Println("No MCPs configured.")
			fmt.Println()
			fmt.Println("Define MCPs in ~/.agent-deck/config.toml:")
			fmt.Println()
			fmt.Println("  [mcps.exa]")
			fmt.Println("  command = \"npx\"")
			fmt.Println("  args = [\"-y\", \"exa-mcp-server\"]")
			fmt.Println("  description = \"Web search via Exa AI\"")
		}
		return
	}

	if *jsonOutput {
		// Build JSON output
		type mcpJSON struct {
			Name        string            `json:"name"`
			Transport   string            `json:"transport"`
			Command     string            `json:"command,omitempty"`
			Args        []string          `json:"args,omitempty"`
			URL         string            `json:"url,omitempty"`
			Env         map[string]string `json:"env,omitempty"`
			Description string            `json:"description,omitempty"`
			HasServer   bool              `json:"has_server_config,omitempty"`
		}

		mcpList := make([]mcpJSON, 0, len(mcps))
		for name, def := range mcps {
			mcpList = append(mcpList, mcpJSON{
				Name:        name,
				Transport:   def.GetTransport(),
				Command:     def.Command,
				Args:        def.Args,
				URL:         def.URL,
				Env:         def.Env,
				Description: def.Description,
				HasServer:   def.HasAutoStartServer(),
			})
		}

		out.Print("", map[string]interface{}{
			"mcps": mcpList,
		})
		return
	}

	if quietMode {
		// Just list names
		names := session.GetAvailableMCPNames()
		for _, name := range names {
			fmt.Println(name)
		}
		return
	}

	// Human-readable table output
	configPath, _ := session.GetUserConfigPath()
	fmt.Printf("Available MCPs (from %s):\n\n", FormatPath(configPath))

	// Calculate column widths
	maxName := 12
	maxCmd := 45
	for name := range mcps {
		if len(name) > maxName {
			maxName = len(name)
		}
	}
	if maxName > 18 {
		maxName = 18
	}

	fmt.Printf("%-*s %-7s %-*s %s\n", maxName, "NAME", "TYPE", maxCmd, "COMMAND/URL", "DESCRIPTION")
	fmt.Println(strings.Repeat("-", maxName+maxCmd+35))

	names := session.GetAvailableMCPNames()
	for _, name := range names {
		def := mcps[name]

		// Determine transport type indicator
		transport := def.GetTransport()
		transportDisplay := "[S]" // stdio
		if transport == "http" {
			transportDisplay = "[H]"
		} else if transport == "sse" {
			transportDisplay = "[E]"
		}

		// Build command/URL display
		var cmdDisplay string
		if def.URL != "" {
			cmdDisplay = def.URL
		} else {
			cmdDisplay = def.Command
			if len(def.Args) > 0 {
				cmdDisplay += " " + strings.Join(def.Args, " ")
			}
		}
		if len(cmdDisplay) > maxCmd {
			cmdDisplay = cmdDisplay[:maxCmd-3] + "..."
		}

		nameDisplay := name
		if len(nameDisplay) > maxName {
			nameDisplay = nameDisplay[:maxName-3] + "..."
		}

		fmt.Printf("%-*s %-7s %-*s %s\n", maxName, nameDisplay, transportDisplay, maxCmd, cmdDisplay, def.Description)
	}

	fmt.Printf("\nTotal: %d MCPs\n", len(mcps))
}

// handleMCPAttached shows MCPs attached to a session
func handleMCPAttached(profile string, args []string) {
	fs := flag.NewFlagSet("mcp attached", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck mcp attached [session-id] [options]")
		fmt.Println()
		fmt.Println("Show MCPs attached to a session.")
		fmt.Println("If no session ID is provided, uses the current session (if in tmux).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session
	identifier := fs.Arg(0)
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Get MCP info for this session
	mcpInfo := session.GetMCPInfo(inst.ProjectPath)
	globalMCPs := mcpInfo.Global
	projectMCPs := mcpInfo.Project
	localMCPs := mcpInfo.Local() // Call method for backward compatibility

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"session":    inst.Title,
			"session_id": TruncateID(inst.ID),
			"local":      localMCPs,
			"global":     globalMCPs,
			"project":    projectMCPs,
		})
		return
	}

	if quietMode {
		// Just list all MCP names
		seen := make(map[string]bool)
		for _, name := range localMCPs {
			if !seen[name] {
				fmt.Println(name)
				seen[name] = true
			}
		}
		for _, name := range globalMCPs {
			if !seen[name] {
				fmt.Println(name)
				seen[name] = true
			}
		}
		for _, name := range projectMCPs {
			if !seen[name] {
				fmt.Println(name)
				seen[name] = true
			}
		}
		return
	}

	// Human-readable output
	fmt.Printf("Session: %s\n\n", inst.Title)

	hasAny := false

	if len(localMCPs) > 0 {
		hasAny = true
		mcpPath := filepath.Join(inst.ProjectPath, ".mcp.json")
		fmt.Printf("LOCAL (%s):\n", FormatPath(mcpPath))
		for _, name := range localMCPs {
			fmt.Printf("  %s %s\n", bulletSymbol, name)
		}
		fmt.Println()
	}

	if len(globalMCPs) > 0 {
		hasAny = true
		configDir := session.GetClaudeConfigDir()
		configPath := filepath.Join(configDir, ".claude.json")
		fmt.Printf("GLOBAL (%s):\n", FormatPath(configPath))
		for _, name := range globalMCPs {
			fmt.Printf("  %s %s\n", bulletSymbol, name)
		}
		fmt.Println()
	}

	if len(projectMCPs) > 0 {
		hasAny = true
		fmt.Printf("PROJECT (Claude project-specific):\n")
		for _, name := range projectMCPs {
			fmt.Printf("  %s %s\n", bulletSymbol, name)
		}
		fmt.Println()
	}

	if !hasAny {
		fmt.Println("No MCPs attached to this session.")
	}
}

// handleMCPAttach attaches an MCP to a session
func handleMCPAttach(profile string, args []string) {
	fs := flag.NewFlagSet("mcp attach", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	global := fs.Bool("global", false, "Attach to global config instead of local .mcp.json")
	restart := fs.Bool("restart", false, "Restart session to load MCP immediately")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck mcp attach <session-id> <mcp-name> [options]")
		fmt.Println()
		fmt.Println("Attach an MCP to a session.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck mcp attach my-project exa           # Attach locally")
		fmt.Println("  agent-deck mcp attach my-project exa --global  # Attach globally")
		fmt.Println("  agent-deck mcp attach my-project exa --restart # Attach and restart")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Need both session ID and MCP name
	if fs.NArg() < 2 {
		out.Error("session ID and MCP name are required", ErrCodeInvalidOperation)
		if !*jsonOutput {
			fmt.Println("\nUsage: agent-deck mcp attach <session-id> <mcp-name> [options]")
		}
		os.Exit(1)
	}

	sessionID := fs.Arg(0)
	mcpName := fs.Arg(1)

	// Load sessions
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Verify MCP exists in config.toml
	availableMCPs := session.GetAvailableMCPs()
	if _, exists := availableMCPs[mcpName]; !exists {
		out.Error(fmt.Sprintf("MCP '%s' not found in config.toml", mcpName), ErrCodeMCPNotAvailable)
		if !*jsonOutput && !quietMode {
			fmt.Println("\nAvailable MCPs:")
			for name := range availableMCPs {
				fmt.Printf("  %s %s\n", bulletSymbol, name)
			}
		}
		os.Exit(2)
	}

	scope := session.GetMCPDefaultScope()
	if *global {
		scope = "global"
	}

	// Attach the MCP
	if *global {
		// Add to global config
		currentGlobal := session.GetGlobalMCPNames()
		// Check if already attached
		for _, name := range currentGlobal {
			if name == mcpName {
				out.Error(fmt.Sprintf("MCP '%s' is already attached globally", mcpName), ErrCodeAlreadyExists)
				os.Exit(1)
			}
		}
		// Add to list
		newGlobal := append(currentGlobal, mcpName)
		if err := session.WriteGlobalMCP(newGlobal); err != nil {
			out.Error(fmt.Sprintf("failed to write global config: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	} else {
		// Add to local .mcp.json
		mcpInfo := session.GetMCPInfo(inst.ProjectPath)
		// Check if already attached locally
		for _, name := range mcpInfo.Local() {
			if name == mcpName {
				out.Error(fmt.Sprintf("MCP '%s' is already attached locally", mcpName), ErrCodeAlreadyExists)
				os.Exit(1)
			}
		}
		// Add to local MCPs
		newLocal := append(mcpInfo.Local(), mcpName)
		if err := session.WriteMCPJsonFromConfig(inst.ProjectPath, newLocal); err != nil {
			out.Error(fmt.Sprintf("failed to write .mcp.json: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Clear MCP cache for this project
	session.ClearMCPCache(inst.ProjectPath)

	// Restart if requested
	restarted := false
	if *restart && (inst.Tool == "claude" || inst.Tool == "gemini") {
		if err := inst.Restart(); err != nil {
			// Don't fail the whole operation, just warn
			if !*jsonOutput && !quietMode {
				fmt.Fprintf(os.Stderr, "Warning: failed to restart session: %v\n", err)
			}
		} else {
			restarted = true
			// Auto-continue: wait for Claude/Gemini to initialize, then send continue message
			time.Sleep(2 * time.Second)
			if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
				// Send "continue" and Enter to resume the conversation
				_ = exec.Command("tmux", "send-keys", "-l", "-t", tmuxSess.Name, "continue").Run()
				_ = exec.Command("tmux", "send-keys", "-t", tmuxSess.Name, "Enter").Run()
			}
		}
	}

	// Output result
	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"success":   true,
			"session":   inst.Title,
			"mcp":       mcpName,
			"scope":     scope,
			"restarted": restarted,
		})
	} else {
		message := fmt.Sprintf("Attached %s to %s (%s)", mcpName, inst.Title, scope)
		if restarted {
			message += " - session restarted"
		}
		out.Success(message, nil)
	}
}

// handleMCPDetach detaches an MCP from a session
func handleMCPDetach(profile string, args []string) {
	fs := flag.NewFlagSet("mcp detach", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	global := fs.Bool("global", false, "Remove from global config instead of local .mcp.json")
	restart := fs.Bool("restart", false, "Restart session to unload MCP immediately")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck mcp detach <session-id> <mcp-name> [options]")
		fmt.Println()
		fmt.Println("Detach an MCP from a session.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck mcp detach my-project exa           # Detach from local")
		fmt.Println("  agent-deck mcp detach my-project exa --global  # Detach from global")
		fmt.Println("  agent-deck mcp detach my-project exa --restart # Detach and restart")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Need both session ID and MCP name
	if fs.NArg() < 2 {
		out.Error("session ID and MCP name are required", ErrCodeInvalidOperation)
		if !*jsonOutput {
			fmt.Println("\nUsage: agent-deck mcp detach <session-id> <mcp-name> [options]")
		}
		os.Exit(1)
	}

	sessionID := fs.Arg(0)
	mcpName := fs.Arg(1)

	// Load sessions
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return // unreachable, satisfies staticcheck SA5011
	}

	scope := session.GetMCPDefaultScope()
	if *global {
		scope = "global"
	}

	// Detach the MCP
	if *global {
		// Remove from global config
		currentGlobal := session.GetGlobalMCPNames()
		found := false
		newGlobal := make([]string, 0, len(currentGlobal))
		for _, name := range currentGlobal {
			if name == mcpName {
				found = true
			} else {
				newGlobal = append(newGlobal, name)
			}
		}
		if !found {
			out.Error(fmt.Sprintf("MCP '%s' is not attached globally", mcpName), ErrCodeNotFound)
			os.Exit(2)
		}
		if err := session.WriteGlobalMCP(newGlobal); err != nil {
			out.Error(fmt.Sprintf("failed to write global config: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	} else {
		// Remove from local .mcp.json
		mcpInfo := session.GetMCPInfo(inst.ProjectPath)
		found := false
		localMCPs := mcpInfo.Local()
		newLocal := make([]string, 0, len(localMCPs))
		for _, name := range localMCPs {
			if name == mcpName {
				found = true
			} else {
				newLocal = append(newLocal, name)
			}
		}
		if !found {
			out.Error(fmt.Sprintf("MCP '%s' is not attached locally", mcpName), ErrCodeNotFound)
			os.Exit(2)
		}
		if err := session.WriteMCPJsonFromConfig(inst.ProjectPath, newLocal); err != nil {
			out.Error(fmt.Sprintf("failed to write .mcp.json: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Clear MCP cache for this project
	session.ClearMCPCache(inst.ProjectPath)

	// Restart if requested
	restarted := false
	if *restart && (inst.Tool == "claude" || inst.Tool == "gemini") {
		if err := inst.Restart(); err != nil {
			// Don't fail the whole operation, just warn
			if !*jsonOutput && !quietMode {
				fmt.Fprintf(os.Stderr, "Warning: failed to restart session: %v\n", err)
			}
		} else {
			restarted = true
			// Auto-continue: wait for Claude/Gemini to initialize, then send continue message
			time.Sleep(2 * time.Second)
			if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
				// Send "continue" and Enter to resume the conversation
				_ = exec.Command("tmux", "send-keys", "-l", "-t", tmuxSess.Name, "continue").Run()
				_ = exec.Command("tmux", "send-keys", "-t", tmuxSess.Name, "Enter").Run()
			}
		}
	}

	// Output result
	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"success":   true,
			"session":   inst.Title,
			"mcp":       mcpName,
			"scope":     scope,
			"restarted": restarted,
		})
	} else {
		message := fmt.Sprintf("Detached %s from %s (%s)", mcpName, inst.Title, scope)
		if restarted {
			message += " - session restarted"
		}
		out.Success(message, nil)
	}
}

// handleMCPServer handles mcp server subcommands (start/stop/status)
func handleMCPServer(args []string) {
	if len(args) == 0 {
		printMCPServerHelp()
		os.Exit(1)
	}

	switch args[0] {
	case "start":
		handleMCPServerStart(args[1:])
	case "stop":
		handleMCPServerStop(args[1:])
	case "status":
		handleMCPServerStatus(args[1:])
	case "help", "-h", "--help":
		printMCPServerHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown mcp server command '%s'\n", args[0])
		printMCPServerHelp()
		os.Exit(1)
	}
}

// printMCPServerHelp prints help for mcp server commands
func printMCPServerHelp() {
	fmt.Println("Usage: agent-deck mcp server <command> [options]")
	fmt.Println()
	fmt.Println("Manage HTTP MCP servers.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start <mcp-name>    Start HTTP server for an MCP")
	fmt.Println("  stop <mcp-name>     Stop HTTP server for an MCP")
	fmt.Println("  status [mcp-name]   Show server status (all or specific)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck mcp server status              # Show all HTTP server status")
	fmt.Println("  agent-deck mcp server status slack        # Show slack server status")
	fmt.Println("  agent-deck mcp server start slack         # Start slack HTTP server")
	fmt.Println("  agent-deck mcp server stop slack          # Stop slack HTTP server")
}

// handleMCPServerStart starts an HTTP MCP server
func handleMCPServerStart(args []string) {
	fs := flag.NewFlagSet("mcp server start", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck mcp server start <mcp-name>")
		fmt.Println()
		fmt.Println("Start an HTTP MCP server.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	if fs.NArg() < 1 {
		out.Error("MCP name is required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	mcpName := fs.Arg(0)

	// Get MCP definition
	def := session.GetMCPDef(mcpName)
	if def == nil {
		out.Error(fmt.Sprintf("MCP '%s' not found in config.toml", mcpName), ErrCodeMCPNotAvailable)
		os.Exit(2)
	}

	// Check if it's an HTTP MCP with server config
	if !def.HasAutoStartServer() {
		out.Error(fmt.Sprintf("MCP '%s' is not an HTTP MCP with server config", mcpName), ErrCodeInvalidOperation)
		if !*jsonOutput && !quietMode {
			if def.URL == "" {
				fmt.Println("\nThis is a stdio MCP (no URL configured).")
			} else {
				fmt.Println("\nThis HTTP MCP has no [mcps." + mcpName + ".server] block configured.")
				fmt.Println("Add a server block to enable auto-start:")
				fmt.Println()
				fmt.Println("  [mcps." + mcpName + ".server]")
				fmt.Println("  command = \"uvx\"")
				fmt.Println("  args = [\"your-server-package\"]")
			}
		}
		os.Exit(2)
	}

	// Start the server
	if err := session.StartHTTPServer(mcpName, def); err != nil {
		out.Error(fmt.Sprintf("failed to start HTTP server: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Output result
	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"success": true,
			"mcp":     mcpName,
			"url":     def.URL,
			"status":  "running",
		})
	} else {
		out.Success(fmt.Sprintf("Started HTTP server for %s at %s", mcpName, def.URL), nil)
	}
}

// handleMCPServerStop stops an HTTP MCP server
func handleMCPServerStop(args []string) {
	fs := flag.NewFlagSet("mcp server stop", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck mcp server stop <mcp-name>")
		fmt.Println()
		fmt.Println("Stop an HTTP MCP server.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	if fs.NArg() < 1 {
		out.Error("MCP name is required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	mcpName := fs.Arg(0)

	// Get HTTP pool
	httpPool := session.GetGlobalHTTPPool()
	if httpPool == nil {
		out.Error("HTTP pool not initialized (run TUI first)", ErrCodeNotFound)
		os.Exit(2)
	}

	// Check if server is running
	if !httpPool.IsRunning(mcpName) {
		out.Error(fmt.Sprintf("HTTP server '%s' is not running", mcpName), ErrCodeNotFound)
		os.Exit(2)
	}

	// Stop the server
	if err := httpPool.Stop(mcpName); err != nil {
		out.Error(fmt.Sprintf("failed to stop HTTP server: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Output result
	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"success": true,
			"mcp":     mcpName,
			"status":  "stopped",
		})
	} else {
		out.Success(fmt.Sprintf("Stopped HTTP server for %s", mcpName), nil)
	}
}

// handleMCPServerStatus shows HTTP MCP server status
func handleMCPServerStatus(args []string) {
	fs := flag.NewFlagSet("mcp server status", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck mcp server status [mcp-name]")
		fmt.Println()
		fmt.Println("Show HTTP MCP server status.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	mcpName := fs.Arg(0) // Optional specific MCP

	// Get all HTTP MCPs from config
	availableMCPs := session.GetAvailableMCPs()
	httpPool := session.GetGlobalHTTPPool()

	type serverInfo struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		Transport   string `json:"transport"`
		Status      string `json:"status"`
		StartedByUs bool   `json:"started_by_us"`
		HasServer   bool   `json:"has_server_config"`
	}

	var servers []serverInfo

	for name, def := range availableMCPs {
		// Filter by specific MCP if provided
		if mcpName != "" && name != mcpName {
			continue
		}

		// Only show HTTP MCPs
		if !def.IsHTTP() {
			continue
		}

		info := serverInfo{
			Name:      name,
			URL:       def.URL,
			Transport: def.GetTransport(),
			HasServer: def.HasAutoStartServer(),
		}

		// Get status from pool
		if httpPool != nil {
			if httpPool.IsRunning(name) {
				info.Status = "running"
				server := httpPool.GetServer(name)
				if server != nil {
					info.StartedByUs = server.StartedByUs()
				}
			} else {
				status := session.GetHTTPServerStatus(name)
				if status == "not_found" {
					info.Status = "not_started"
				} else {
					info.Status = status
				}
			}
		} else {
			info.Status = "pool_not_initialized"
		}

		servers = append(servers, info)
	}

	if mcpName != "" && len(servers) == 0 {
		out.Error(fmt.Sprintf("HTTP MCP '%s' not found", mcpName), ErrCodeNotFound)
		os.Exit(2)
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"servers": servers,
		})
		return
	}

	if len(servers) == 0 {
		if !quietMode {
			fmt.Println("No HTTP MCPs configured.")
			fmt.Println()
			fmt.Println("To add an HTTP MCP, add to config.toml:")
			fmt.Println()
			fmt.Println("  [mcps.my-http-mcp]")
			fmt.Println("  url = \"http://localhost:8000/mcp\"")
			fmt.Println("  transport = \"http\"")
		}
		return
	}

	if quietMode {
		for _, s := range servers {
			fmt.Printf("%s\t%s\n", s.Name, s.Status)
		}
		return
	}

	// Human-readable table output
	fmt.Println("HTTP MCP Servers:")
	fmt.Println()
	fmt.Printf("%-15s %-10s %-12s %-35s %s\n", "NAME", "TRANSPORT", "STATUS", "URL", "SERVER CONFIG")
	fmt.Println(strings.Repeat("-", 90))

	for _, s := range servers {
		statusDisplay := s.Status
		if s.Status == "running" && s.StartedByUs {
			statusDisplay = "running (ours)"
		} else if s.Status == "running" {
			statusDisplay = "running (ext)"
		}

		serverConfig := "no"
		if s.HasServer {
			serverConfig = "yes"
		}

		fmt.Printf("%-15s %-10s %-12s %-35s %s\n",
			truncateString(s.Name, 15),
			s.Transport,
			statusDisplay,
			truncateString(s.URL, 35),
			serverConfig,
		)
	}

	fmt.Printf("\nTotal: %d HTTP MCPs\n", len(servers))
}
