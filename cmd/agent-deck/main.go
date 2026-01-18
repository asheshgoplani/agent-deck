package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/ui"
	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

const Version = "0.8.42"

// Table column widths for list command output
const (
	tableColTitle     = 20
	tableColGroup     = 15
	tableColPath      = 40
	tableColIDDisplay = 12
)

// init sets up color profile for consistent terminal colors across environments
func init() {
	initColorProfile()
	initUpdateSettings()
}

// initUpdateSettings configures update checking from user config
func initUpdateSettings() {
	settings := session.GetUpdateSettings()
	update.SetCheckInterval(settings.CheckIntervalHours)
}

// printUpdateNotice checks for updates and prints a one-liner if available
// Uses cache to avoid API calls - only prints if update was already detected
func printUpdateNotice() {
	settings := session.GetUpdateSettings()
	if !settings.CheckEnabled || !settings.NotifyInCLI {
		return
	}

	info, err := update.CheckForUpdate(Version, false)
	if err != nil || info == nil || !info.Available {
		return
	}

	// Print update notice to stderr so it doesn't interfere with JSON output
	fmt.Fprintf(os.Stderr, "\n💡 Update available: v%s → v%s (run: agent-deck update)\n",
		info.CurrentVersion, info.LatestVersion)
}

// promptForUpdate checks for updates and prompts user if auto_update is enabled
func promptForUpdate() bool {
	settings := session.GetUpdateSettings()
	if !settings.CheckEnabled {
		return false
	}

	info, err := update.CheckForUpdate(Version, false)
	if err != nil || info == nil || !info.Available {
		return false
	}

	// If auto_update is disabled, just show notification (don't prompt)
	if !settings.AutoUpdate {
		fmt.Fprintf(os.Stderr, "\n💡 Update available: v%s → v%s (run: agent-deck update)\n",
			info.CurrentVersion, info.LatestVersion)
		return false
	}

	// auto_update is enabled - prompt user
	fmt.Printf("\n⬆ Update available: v%s → v%s\n", info.CurrentVersion, info.LatestVersion)
	fmt.Print("Update now? [Y/n]: ")

	var response string
	_, _ = fmt.Scanln(&response)
	response = strings.TrimSpace(strings.ToLower(response))

	// Default to yes (empty or "y" or "yes")
	if response != "" && response != "y" && response != "yes" {
		fmt.Println("Skipped. Run 'agent-deck update' later.")
		return false
	}

	fmt.Println()
	if err := update.PerformUpdate(info.DownloadURL); err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		return false
	}

	fmt.Println("Restart agent-deck to use the new version.")
	return true
}

// initColorProfile configures lipgloss color profile based on terminal capabilities.
// Prefers TrueColor for best visuals, falls back to ANSI256 for compatibility.
func initColorProfile() {
	// Allow user override via environment variable
	// AGENTDECK_COLOR: truecolor, 256, 16, none
	if colorEnv := os.Getenv("AGENTDECK_COLOR"); colorEnv != "" {
		switch strings.ToLower(colorEnv) {
		case "truecolor", "true", "24bit":
			lipgloss.SetColorProfile(termenv.TrueColor)
			return
		case "256", "ansi256":
			lipgloss.SetColorProfile(termenv.ANSI256)
			return
		case "16", "ansi", "basic":
			lipgloss.SetColorProfile(termenv.ANSI)
			return
		case "none", "off", "ascii":
			lipgloss.SetColorProfile(termenv.Ascii)
			return
		}
	}

	// Auto-detect with TrueColor preference
	// Most modern terminals support TrueColor even if not advertised

	// Explicit TrueColor support
	colorTerm := os.Getenv("COLORTERM")
	if colorTerm == "truecolor" || colorTerm == "24bit" {
		lipgloss.SetColorProfile(termenv.TrueColor)
		return
	}

	// Check TERM for capability hints
	term := os.Getenv("TERM")

	// Known TrueColor-capable terminals
	trueColorTerms := []string{
		"xterm-256color",
		"screen-256color",
		"tmux-256color",
		"xterm-direct",
		"alacritty",
		"kitty",
		"wezterm",
	}
	for _, t := range trueColorTerms {
		if strings.Contains(term, t) || term == t {
			// These terminals typically support TrueColor
			lipgloss.SetColorProfile(termenv.TrueColor)
			return
		}
	}

	// Check for common terminal emulators via env vars
	// Windows Terminal, iTerm2, etc. set these
	if os.Getenv("WT_SESSION") != "" || // Windows Terminal
		os.Getenv("ITERM_SESSION_ID") != "" || // iTerm2
		os.Getenv("TERMINAL_EMULATOR") != "" || // JetBrains terminals
		os.Getenv("KONSOLE_VERSION") != "" { // Konsole
		lipgloss.SetColorProfile(termenv.TrueColor)
		return
	}

	// Fallback: Use ANSI256 for maximum compatibility
	// Works in SSH, basic terminals, and older emulators
	lipgloss.SetColorProfile(termenv.ANSI256)
}

func main() {
	// Extract global -p/--profile flag before subcommand dispatch
	profile, args := extractProfileFlag(os.Args[1:])

	// Handle subcommands
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Printf("Agent Deck v%s\n", Version)
			return
		case "help", "--help", "-h":
			printHelp()
			return
		case "add":
			handleAdd(profile, args[1:])
			return
		case "list", "ls":
			handleList(profile, args[1:])
			return
		case "remove", "rm":
			handleRemove(profile, args[1:])
			return
		case "status":
			handleStatus(profile, args[1:])
			return
		case "profile":
			handleProfile(args[1:])
			return
		case "update":
			handleUpdate(args[1:])
			return
		case "session":
			handleSession(profile, args[1:])
			return
		case "mcp":
			handleMCP(profile, args[1:])
			return
		case "group":
			handleGroup(profile, args[1:])
			return
		case "uninstall":
			handleUninstall(args[1:])
			return
		}
	}

	// Set version for UI update checking
	ui.SetVersion(Version)

	// Initialize theme from config
	theme := session.GetTheme()
	ui.InitTheme(theme)

	// Check for updates and prompt user before launching TUI
	if promptForUpdate() {
		// Update was performed, exit so user can restart with new version
		return
	}

	// Check if tmux is available
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Println("Error: tmux not found in PATH")
		fmt.Println("\nAgent Deck requires tmux. Install with:")
		fmt.Println("  brew install tmux")
		os.Exit(1)
	}

	// Acquire lock to prevent duplicate instances
	if err := acquireLock(profile); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	defer releaseLock(profile)

	// Set up signal handling for graceful lock cleanup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		releaseLock(profile)
		os.Exit(0)
	}()

	// Set up debug logging if AGENTDECK_DEBUG is set
	// Logs go to ~/.agent-deck/debug.log to avoid interfering with TUI
	if os.Getenv("AGENTDECK_DEBUG") != "" {
		if baseDir, err := session.GetAgentDeckDir(); err == nil {
			logPath := filepath.Join(baseDir, "debug.log")
			logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err == nil {
				log.SetOutput(logFile)
				log.SetFlags(log.Ltime | log.Lmicroseconds)
				log.Printf("=== Agent Deck Debug Log Started ===")
				defer logFile.Close()
			}
		}
	} else {
		// Disable logging when not in debug mode to avoid TUI interference
		log.SetOutput(io.Discard)
	}

	// Start TUI with the specified profile
	p := tea.NewProgram(
		ui.NewHomeWithProfile(profile),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

// extractProfileFlag extracts -p or --profile from args, returning the profile and remaining args
func extractProfileFlag(args []string) (string, []string) {
	var profile string
	var remaining []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Check for -p=value or --profile=value
		if strings.HasPrefix(arg, "-p=") {
			profile = strings.TrimPrefix(arg, "-p=")
			continue
		}
		if strings.HasPrefix(arg, "--profile=") {
			profile = strings.TrimPrefix(arg, "--profile=")
			continue
		}

		// Check for -p value or --profile value
		if arg == "-p" || arg == "--profile" {
			if i+1 < len(args) {
				profile = args[i+1]
				i++ // Skip the value
				continue
			}
		}

		remaining = append(remaining, arg)
	}

	return profile, remaining
}

// reorderArgsForFlagParsing moves the path argument to the end of args
// so Go's flag package can parse all flags correctly.
// Go's flag package stops parsing at the first non-flag argument,
// so "add . -c claude" would fail to parse -c without this fix.
// This reorders to "add -c claude ." which parses correctly.
func reorderArgsForFlagParsing(args []string) []string {
	if len(args) == 0 {
		return args
	}

	// Known flags that take a value (need to skip their values)
	valueFlags := map[string]bool{
		"-t": true, "--title": true,
		"-g": true, "--group": true,
		"-c": true, "--cmd": true,
		"-p": true, "--parent": true,
		"--mcp": true,
	}

	var flags []string
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Check if it's a flag
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)

			// Check if this flag takes a value (and value is separate)
			// Handle both "-c value" and "-c=value" formats
			if !strings.Contains(arg, "=") && valueFlags[arg] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			// Non-flag argument (path)
			positional = append(positional, arg)
		}
	}

	// Return flags first, then positional args
	return append(flags, positional...)
}

// isDuplicateSession checks if a session with the same title AND path already exists.
// Returns (isDuplicate, existingInstance)
// Paths are normalized by removing trailing slashes for comparison.
func isDuplicateSession(instances []*session.Instance, title, path string) (bool, *session.Instance) {
	// Normalize path by removing trailing slash (except for root "/")
	normalizedPath := strings.TrimSuffix(path, "/")
	if normalizedPath == "" {
		normalizedPath = "/"
	}

	for _, inst := range instances {
		// Normalize existing path for comparison
		existingPath := strings.TrimSuffix(inst.ProjectPath, "/")
		if existingPath == "" {
			existingPath = "/"
		}

		if existingPath == normalizedPath && inst.Title == title {
			return true, inst
		}
	}
	return false, nil
}

// generateUniqueTitle generates a unique title for sessions at the same path.
// If "project" exists at path, returns "project (2)", then "project (3)", etc.
func generateUniqueTitle(instances []*session.Instance, baseTitle, path string) string {
	// Check if base title is available at this path
	titleExists := func(title string) bool {
		for _, inst := range instances {
			if inst.ProjectPath == path && inst.Title == title {
				return true
			}
		}
		return false
	}

	if !titleExists(baseTitle) {
		return baseTitle
	}

	// Find next available number
	for i := 2; i <= 100; i++ { // Cap at 100 to prevent infinite loop
		candidate := fmt.Sprintf("%s (%d)", baseTitle, i)
		if !titleExists(candidate) {
			return candidate
		}
	}

	// Fallback: use timestamp
	return fmt.Sprintf("%s (%d)", baseTitle, time.Now().Unix())
}

// handleAdd adds a new session from CLI
func handleAdd(profile string, args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	title := fs.String("title", "", "Session title (defaults to folder name)")
	titleShort := fs.String("t", "", "Session title (short)")
	group := fs.String("group", "", "Group path (defaults to parent folder)")
	groupShort := fs.String("g", "", "Group path (short)")
	command := fs.String("cmd", "", "Command to run (e.g., 'claude', 'opencode')")
	commandShort := fs.String("c", "", "Command to run (short)")
	parent := fs.String("parent", "", "Parent session (creates sub-session, inherits group)")
	parentShort := fs.String("p", "", "Parent session (short)")

	// MCP flag - can be specified multiple times
	var mcpFlags []string
	fs.Func("mcp", "MCP to attach (can specify multiple times)", func(s string) error {
		mcpFlags = append(mcpFlags, s)
		return nil
	})

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck add [path] [options]")
		fmt.Println()
		fmt.Println("Add a new session to Agent Deck.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  [path]    Project directory (defaults to current directory)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck add                       # Use current directory")
		fmt.Println("  agent-deck add /path/to/project")
		fmt.Println("  agent-deck add -t \"My Project\" -g \"work\"")
		fmt.Println("  agent-deck add -c claude .")
		fmt.Println("  agent-deck -p work add               # Add to 'work' profile")
		fmt.Println("  agent-deck add -t \"Sub-task\" --parent \"Main Project\"  # Create sub-session")
		fmt.Println("  agent-deck add -t \"Research\" -c claude --mcp memory --mcp sequential-thinking /tmp/x")
	}

	// Reorder args: move path to end so flags are parsed correctly
	// Go's flag package stops parsing at first non-flag argument
	// This allows: "add . -c claude" to work same as "add -c claude ."
	args = reorderArgsForFlagParsing(args)

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Get path argument (defaults to current directory)
	// Fix: sanitize input to remove surrounding quotes that cause issues
	// Users sometimes pass paths like '"/path/with spaces"' which stores literal quotes
	path := strings.Trim(fs.Arg(0), "'\"")
	if path == "" || path == "." {
		var err error
		path, err = os.Getwd()
		if err != nil {
			fmt.Printf("Error: failed to get current directory: %v\n", err)
			os.Exit(1)
		}
	} else {
		var err error
		path, err = filepath.Abs(path)
		if err != nil {
			fmt.Printf("Error: failed to resolve path: %v\n", err)
			os.Exit(1)
		}
	}

	// Verify path exists and is a directory
	info, err := os.Stat(path)
	if err != nil {
		fmt.Printf("Error: path does not exist: %s\n", path)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Printf("Error: path is not a directory: %s\n", path)
		os.Exit(1)
	}

	// Merge short and long flags
	sessionTitle := mergeFlags(*title, *titleShort)
	sessionGroup := mergeFlags(*group, *groupShort)
	sessionCommand := mergeFlags(*command, *commandShort)
	sessionParent := mergeFlags(*parent, *parentShort)

	// Default title to folder name
	if sessionTitle == "" {
		sessionTitle = filepath.Base(path)
	}

	// Load existing sessions with profile
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Printf("Error: failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Printf("Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	// Resolve parent session if specified
	var parentInstance *session.Instance
	if sessionParent != "" {
		var errMsg string
		parentInstance, errMsg, _ = ResolveSession(sessionParent, instances)
		if parentInstance == nil {
			fmt.Printf("Error: %s\n", errMsg)
			os.Exit(1)
		}
		// Sub-sessions cannot have sub-sessions (single level only)
		if parentInstance.IsSubSession() {
			fmt.Printf("Error: cannot create sub-session of a sub-session (single level only)\n")
			os.Exit(1)
		}
		// Inherit group from parent
		sessionGroup = parentInstance.GroupPath
	}

	// Track if user provided explicit title or we auto-generated from folder name
	userProvidedTitle := (mergeFlags(*title, *titleShort) != "")

	if !userProvidedTitle {
		// User didn't provide title - auto-generate unique title for this path
		sessionTitle = generateUniqueTitle(instances, sessionTitle, path)
	} else {
		// User provided explicit title - check for exact duplicate (same title AND path)
		if isDupe, existingInst := isDuplicateSession(instances, sessionTitle, path); isDupe {
			fmt.Printf("Session already exists with same title and path: %s (%s)\n", existingInst.Title, existingInst.ID)
			os.Exit(0)
		}
	}

	// Create new instance (without starting tmux)
	var newInstance *session.Instance
	if sessionGroup != "" {
		newInstance = session.NewInstanceWithGroup(sessionTitle, path, sessionGroup)
	} else {
		newInstance = session.NewInstance(sessionTitle, path)
	}

	// Set parent if specified (includes parent's project path for --add-dir access)
	if parentInstance != nil {
		newInstance.SetParentWithPath(parentInstance.ID, parentInstance.ProjectPath)
	}

	// Set command if provided
	if sessionCommand != "" {
		newInstance.Command = sessionCommand
		// Detect tool from command
		newInstance.Tool = detectTool(sessionCommand)
	}

	// Add to instances
	instances = append(instances, newInstance)

	// Rebuild group tree and save
	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	// Ensure the session's group exists
	if newInstance.GroupPath != "" {
		groupTree.CreateGroup(newInstance.GroupPath)
	}

	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		fmt.Printf("Error: failed to save session: %v\n", err)
		os.Exit(1)
	}

	// Attach MCPs if specified
	if len(mcpFlags) > 0 {
		// Validate MCPs exist in config.toml
		availableMCPs := session.GetAvailableMCPs()
		for _, mcpName := range mcpFlags {
			if _, exists := availableMCPs[mcpName]; !exists {
				fmt.Printf("Error: MCP '%s' not found in config.toml\n", mcpName)
				fmt.Println("\nAvailable MCPs:")
				for name := range availableMCPs {
					fmt.Printf("  • %s\n", name)
				}
				os.Exit(1)
			}
		}

		// Write MCPs to .mcp.json
		if err := session.WriteMCPJsonFromConfig(path, mcpFlags); err != nil {
			fmt.Printf("Error: failed to write MCPs: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("✓ Added session: %s\n", sessionTitle)
	fmt.Printf("  Profile: %s\n", storage.Profile())
	fmt.Printf("  Path:    %s\n", path)
	fmt.Printf("  Group:   %s\n", newInstance.GroupPath)
	fmt.Printf("  ID:      %s\n", newInstance.ID)
	if sessionCommand != "" {
		fmt.Printf("  Cmd:     %s\n", sessionCommand)
	}
	if len(mcpFlags) > 0 {
		fmt.Printf("  MCPs:    %s\n", strings.Join(mcpFlags, ", "))
	}
	if parentInstance != nil {
		fmt.Printf("  Parent:  %s (%s)\n", parentInstance.Title, parentInstance.ID[:8])
	}

	// Show helpful next steps
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Printf("  agent-deck session start %s   # Start the session\n", sessionTitle)
	fmt.Printf("  agent-deck                         # Open TUI and press Enter to attach\n")
}

// handleList lists all sessions
func handleList(profile string, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	allProfiles := fs.Bool("all", false, "List sessions from all profiles")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck list [options]")
		fmt.Println()
		fmt.Println("List all sessions.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck list                    # List from default profile")
		fmt.Println("  agent-deck -p work list            # List from 'work' profile")
		fmt.Println("  agent-deck list --all              # List from all profiles")
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *allProfiles {
		handleListAllProfiles(*jsonOutput)
		return
	}

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Printf("Error: failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Printf("Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	if len(instances) == 0 {
		fmt.Printf("No sessions found in profile '%s'.\n", storage.Profile())
		return
	}

	if *jsonOutput {
		// JSON output for scripting
		type sessionJSON struct {
			ID        string    `json:"id"`
			Title     string    `json:"title"`
			Path      string    `json:"path"`
			Group     string    `json:"group"`
			Tool      string    `json:"tool"`
			Command   string    `json:"command,omitempty"`
			Profile   string    `json:"profile"`
			CreatedAt time.Time `json:"created_at"`
		}
		sessions := make([]sessionJSON, len(instances))
		for i, inst := range instances {
			sessions[i] = sessionJSON{
				ID:        inst.ID,
				Title:     inst.Title,
				Path:      inst.ProjectPath,
				Group:     inst.GroupPath,
				Tool:      inst.Tool,
				Command:   inst.Command,
				Profile:   storage.Profile(),
				CreatedAt: inst.CreatedAt,
			}
		}
		output, err := json.MarshalIndent(sessions, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON output: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(output))
		return
	}

	// Table output
	fmt.Printf("Profile: %s\n\n", storage.Profile())
	fmt.Printf("%-*s %-*s %-*s %s\n", tableColTitle, "TITLE", tableColGroup, "GROUP", tableColPath, "PATH", "ID")
	fmt.Println(strings.Repeat("-", tableColTitle+tableColGroup+tableColPath+tableColIDDisplay+5))
	for _, inst := range instances {
		title := truncate(inst.Title, tableColTitle)
		group := truncate(inst.GroupPath, tableColGroup)
		path := truncate(inst.ProjectPath, tableColPath)
		// Safe ID display with bounds check to prevent panic
		idDisplay := inst.ID
		if len(idDisplay) > tableColIDDisplay {
			idDisplay = idDisplay[:tableColIDDisplay]
		}
		fmt.Printf("%-*s %-*s %-*s %s\n", tableColTitle, title, tableColGroup, group, tableColPath, path, idDisplay)
	}
	fmt.Printf("\nTotal: %d sessions\n", len(instances))

	// Show update notice if available
	printUpdateNotice()
}

// handleListAllProfiles lists sessions from all profiles
func handleListAllProfiles(jsonOutput bool) {
	profiles, err := session.ListProfiles()
	if err != nil {
		fmt.Printf("Error: failed to list profiles: %v\n", err)
		os.Exit(1)
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles found.")
		return
	}

	if jsonOutput {
		type sessionJSON struct {
			ID        string    `json:"id"`
			Title     string    `json:"title"`
			Path      string    `json:"path"`
			Group     string    `json:"group"`
			Tool      string    `json:"tool"`
			Command   string    `json:"command,omitempty"`
			Profile   string    `json:"profile"`
			CreatedAt time.Time `json:"created_at"`
		}
		var allSessions []sessionJSON

		for _, profileName := range profiles {
			storage, err := session.NewStorageWithProfile(profileName)
			if err != nil {
				continue
			}
			instances, _, err := storage.LoadWithGroups()
			if err != nil {
				continue
			}
			for _, inst := range instances {
				allSessions = append(allSessions, sessionJSON{
					ID:        inst.ID,
					Title:     inst.Title,
					Path:      inst.ProjectPath,
					Group:     inst.GroupPath,
					Tool:      inst.Tool,
					Command:   inst.Command,
					Profile:   profileName,
					CreatedAt: inst.CreatedAt,
				})
			}
		}

		output, err := json.MarshalIndent(allSessions, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON output: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(output))
		return
	}

	// Table output grouped by profile
	totalSessions := 0
	for _, profileName := range profiles {
		storage, err := session.NewStorageWithProfile(profileName)
		if err != nil {
			continue
		}
		instances, _, err := storage.LoadWithGroups()
		if err != nil {
			continue
		}

		if len(instances) == 0 {
			continue
		}

		fmt.Printf("\n═══ Profile: %s ═══\n\n", profileName)
		fmt.Printf("%-*s %-*s %-*s %s\n", tableColTitle, "TITLE", tableColGroup, "GROUP", tableColPath, "PATH", "ID")
		fmt.Println(strings.Repeat("-", tableColTitle+tableColGroup+tableColPath+tableColIDDisplay+5))

		for _, inst := range instances {
			title := truncate(inst.Title, tableColTitle)
			group := truncate(inst.GroupPath, tableColGroup)
			path := truncate(inst.ProjectPath, tableColPath)
			idDisplay := inst.ID
			if len(idDisplay) > tableColIDDisplay {
				idDisplay = idDisplay[:tableColIDDisplay]
			}
			fmt.Printf("%-*s %-*s %-*s %s\n", tableColTitle, title, tableColGroup, group, tableColPath, path, idDisplay)
		}
		fmt.Printf("(%d sessions)\n", len(instances))
		totalSessions += len(instances)
	}

	fmt.Printf("\n═══════════════════════════════════════\n")
	fmt.Printf("Total: %d sessions across %d profiles\n", totalSessions, len(profiles))
}

// handleRemove removes a session by ID or title
func handleRemove(profile string, args []string) {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck remove <id|title>")
		fmt.Println()
		fmt.Println("Remove a session by ID or title.")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck remove abc12345")
		fmt.Println("  agent-deck remove \"My Project\"")
		fmt.Println("  agent-deck -p work remove abc12345   # Remove from 'work' profile")
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	if identifier == "" {
		fmt.Println("Error: session ID or title is required")
		fs.Usage()
		os.Exit(1)
	}

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Printf("Error: failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Printf("Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	// Find and remove the session
	found := false
	var removedTitle string
	newInstances := make([]*session.Instance, 0, len(instances))
	for _, inst := range instances {
		if inst.ID == identifier || strings.HasPrefix(inst.ID, identifier) || inst.Title == identifier {
			found = true
			removedTitle = inst.Title
			// Kill tmux session if it exists
			if inst.Exists() {
				if err := inst.Kill(); err != nil {
					fmt.Printf("Warning: failed to kill tmux session: %v\n", err)
					fmt.Println("Session removed from Agent Deck but may still be running in tmux")
				}
			}
		} else {
			newInstances = append(newInstances, inst)
		}
	}

	if !found {
		fmt.Printf("Error: session not found in profile '%s': %s\n", storage.Profile(), identifier)
		os.Exit(1)
	}

	// Rebuild group tree and save
	groupTree := session.NewGroupTreeWithGroups(newInstances, groups)

	if err := storage.SaveWithGroups(newInstances, groupTree); err != nil {
		fmt.Printf("Error: failed to save: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Removed session: %s (from profile '%s')\n", removedTitle, storage.Profile())
}

// statusCounts holds session counts by status
type statusCounts struct {
	running int
	waiting int
	idle    int
	err     int
	total   int
}

// countByStatus counts sessions by their status
func countByStatus(instances []*session.Instance) statusCounts {
	var counts statusCounts
	for _, inst := range instances {
		_ = inst.UpdateStatus() // Refresh status from tmux
		switch inst.Status {
		case session.StatusRunning:
			counts.running++
		case session.StatusWaiting:
			counts.waiting++
		case session.StatusIdle:
			counts.idle++
		case session.StatusError:
			counts.err++
		}
		counts.total++
	}
	return counts
}

// handleStatus shows session status summary
func handleStatus(profile string, args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	verbose := fs.Bool("verbose", false, "Show detailed session list")
	verboseShort := fs.Bool("v", false, "Show detailed session list (short)")
	quiet := fs.Bool("quiet", false, "Only output waiting count (for scripts)")
	quietShort := fs.Bool("q", false, "Only output waiting count (short)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck status [options]")
		fmt.Println()
		fmt.Println("Show a summary of session statuses.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck status              # Quick summary")
		fmt.Println("  agent-deck status -v           # Detailed list")
		fmt.Println("  agent-deck status -q           # Just waiting count")
		fmt.Println("  agent-deck -p work status      # Status for 'work' profile")
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Load sessions
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Printf("Error: failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Printf("Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	if len(instances) == 0 {
		if *jsonOutput {
			fmt.Println(`{"waiting": 0, "running": 0, "idle": 0, "error": 0, "total": 0}`)
		} else if *quiet || *quietShort {
			fmt.Println("0")
		} else {
			fmt.Printf("No sessions in profile '%s'.\n", storage.Profile())
		}
		return
	}

	// Count by status
	counts := countByStatus(instances)

	// Output based on flags
	if *jsonOutput {
		type statusJSON struct {
			Waiting int `json:"waiting"`
			Running int `json:"running"`
			Idle    int `json:"idle"`
			Error   int `json:"error"`
			Total   int `json:"total"`
		}
		output, _ := json.Marshal(statusJSON{
			Waiting: counts.waiting,
			Running: counts.running,
			Idle:    counts.idle,
			Error:   counts.err,
			Total:   counts.total,
		})
		fmt.Println(string(output))
	} else if *quiet || *quietShort {
		fmt.Println(counts.waiting)
	} else if *verbose || *verboseShort {
		// Detailed output grouped by status
		printStatusGroup := func(label, symbol string, status session.Status) {
			var matching []*session.Instance
			for _, inst := range instances {
				if inst.Status == status {
					matching = append(matching, inst)
				}
			}
			if len(matching) == 0 {
				return
			}
			fmt.Printf("%s (%d):\n", label, len(matching))
			for _, inst := range matching {
				path := inst.ProjectPath
				home, _ := os.UserHomeDir()
				if strings.HasPrefix(path, home) {
					path = "~" + path[len(home):]
				}
				fmt.Printf("  %s %-16s %-10s %s\n", symbol, inst.Title, inst.Tool, path)
			}
			fmt.Println()
		}

		printStatusGroup("WAITING", "◐", session.StatusWaiting)
		printStatusGroup("RUNNING", "●", session.StatusRunning)
		printStatusGroup("IDLE", "○", session.StatusIdle)
		printStatusGroup("ERROR", "✕", session.StatusError)

		fmt.Printf("Total: %d sessions in profile '%s'\n", counts.total, storage.Profile())
	} else {
		// Compact output
		fmt.Printf("%d waiting • %d running • %d idle\n",
			counts.waiting, counts.running, counts.idle)
	}

	// Show update notice if available (skip for JSON/quiet output)
	if !*jsonOutput && !*quiet && !*quietShort {
		printUpdateNotice()
	}
}

// handleProfile manages profiles (list, create, delete, default)
func handleProfile(args []string) {
	if len(args) == 0 {
		// Default to list
		handleProfileList()
		return
	}

	switch args[0] {
	case "list", "ls":
		handleProfileList()
	case "create", "new":
		if len(args) < 2 {
			fmt.Println("Error: profile name is required")
			fmt.Println("Usage: agent-deck profile create <name>")
			os.Exit(1)
		}
		handleProfileCreate(args[1])
	case "delete", "rm":
		if len(args) < 2 {
			fmt.Println("Error: profile name is required")
			fmt.Println("Usage: agent-deck profile delete <name>")
			os.Exit(1)
		}
		handleProfileDelete(args[1])
	case "default":
		if len(args) < 2 {
			// Show current default
			config, err := session.LoadConfig()
			if err != nil {
				fmt.Printf("Error: failed to load config: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Default profile: %s\n", config.DefaultProfile)
			return
		}
		handleProfileSetDefault(args[1])
	default:
		fmt.Printf("Unknown profile command: %s\n", args[0])
		fmt.Println()
		fmt.Println("Usage: agent-deck profile <command>")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  list              List all profiles")
		fmt.Println("  create <name>     Create a new profile")
		fmt.Println("  delete <name>     Delete a profile")
		fmt.Println("  default [name]    Show or set default profile")
		os.Exit(1)
	}
}

func handleProfileList() {
	profiles, err := session.ListProfiles()
	if err != nil {
		fmt.Printf("Error: failed to list profiles: %v\n", err)
		os.Exit(1)
	}

	config, _ := session.LoadConfig()
	defaultProfile := session.DefaultProfile
	if config != nil {
		defaultProfile = config.DefaultProfile
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles found.")
		fmt.Println("Run 'agent-deck' to create the default profile automatically.")
		return
	}

	fmt.Println("Profiles:")
	for _, p := range profiles {
		if p == defaultProfile {
			fmt.Printf("  * %s (default)\n", p)
		} else {
			fmt.Printf("    %s\n", p)
		}
	}
	fmt.Printf("\nTotal: %d profiles\n", len(profiles))
}

func handleProfileCreate(name string) {
	if err := session.CreateProfile(name); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Created profile: %s\n", name)
	fmt.Printf("  Use with: agent-deck -p %s\n", name)
}

func handleProfileDelete(name string) {
	// Confirm deletion
	fmt.Printf("Are you sure you want to delete profile '%s'? This will remove all sessions in this profile. [y/N] ", name)
	var response string
	_, _ = fmt.Scanln(&response)
	if response != "y" && response != "Y" {
		fmt.Println("Cancelled.")
		return
	}

	if err := session.DeleteProfile(name); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Deleted profile: %s\n", name)
}

func handleProfileSetDefault(name string) {
	if err := session.SetDefaultProfile(name); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Default profile set to: %s\n", name)
}

// handleUpdate checks for and performs updates
func handleUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "Only check for updates, don't install")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck update [options]")
		fmt.Println()
		fmt.Println("Check for and install updates (always checks GitHub for latest).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck update           # Check and install if available")
		fmt.Println("  agent-deck update --check   # Only check, don't install")
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	fmt.Printf("Agent Deck v%s\n", Version)
	fmt.Println("Checking for updates...")

	// Always force check when user explicitly runs 'update' command
	// Cache is only useful for background checks (TUI startup), not explicit requests
	info, err := update.CheckForUpdate(Version, true)
	if err != nil {
		fmt.Printf("Error checking for updates: %v\n", err)
		os.Exit(1)
	}

	if !info.Available {
		fmt.Println("✓ You're running the latest version!")
		return
	}

	fmt.Printf("\n⬆ Update available: v%s → v%s\n", info.CurrentVersion, info.LatestVersion)
	fmt.Printf("  Release: %s\n", info.ReleaseURL)

	if *checkOnly {
		fmt.Println("\nRun 'agent-deck update' to install.")
		return
	}

	// Confirm update
	fmt.Print("\nInstall update? [Y/n] ")
	var response string
	_, _ = fmt.Scanln(&response)
	if response != "" && response != "y" && response != "Y" {
		fmt.Println("Update cancelled.")
		return
	}

	// Perform update
	fmt.Println()
	if err := update.PerformUpdate(info.DownloadURL); err != nil {
		fmt.Printf("Error installing update: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n✓ Updated to v%s\n", info.LatestVersion)
	fmt.Println("  Restart agent-deck to use the new version.")
}

func printHelp() {
	fmt.Printf("Agent Deck v%s\n", Version)
	fmt.Println("Terminal session manager for AI coding agents")
	fmt.Println()
	fmt.Println("Usage: agent-deck [-p profile] [command]")
	fmt.Println()
	fmt.Println("Global Options:")
	fmt.Println("  -p, --profile <name>   Use specific profile (default: 'default')")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)           Start the TUI")
	fmt.Println("  add <path>       Add a new session")
	fmt.Println("  list, ls         List all sessions")
	fmt.Println("  remove, rm       Remove a session")
	fmt.Println("  status           Show session status summary")
	fmt.Println("  session          Manage session lifecycle")
	fmt.Println("  mcp              Manage MCP servers")
	fmt.Println("  group            Manage groups")
	fmt.Println("  profile          Manage profiles")
	fmt.Println("  update           Check for and install updates")
	fmt.Println("  uninstall        Uninstall Agent Deck")
	fmt.Println("  version          Show version")
	fmt.Println("  help             Show this help")
	fmt.Println()
	fmt.Println("Session Commands:")
	fmt.Println("  session start <id>        Start a session's tmux process")
	fmt.Println("  session stop <id>         Stop session process")
	fmt.Println("  session restart <id>      Restart session (reload MCPs)")
	fmt.Println("  session fork <id>         Fork Claude session with context")
	fmt.Println("  session attach <id>       Attach to session interactively")
	fmt.Println("  session show [id]         Show session details")
	fmt.Println()
	fmt.Println("MCP Commands:")
	fmt.Println("  mcp list                  List available MCPs from config.toml")
	fmt.Println("  mcp attached [id]         Show MCPs attached to a session")
	fmt.Println("  mcp attach <id> <mcp>     Attach MCP to session")
	fmt.Println("  mcp detach <id> <mcp>     Detach MCP from session")
	fmt.Println()
	fmt.Println("Group Commands:")
	fmt.Println("  group list                List all groups")
	fmt.Println("  group create <name>       Create a new group")
	fmt.Println("  group delete <name>       Delete a group")
	fmt.Println("  group move <id> <group>   Move session to group")
	fmt.Println()
	fmt.Println("Profile Commands:")
	fmt.Println("  profile list              List all profiles")
	fmt.Println("  profile create <name>     Create a new profile")
	fmt.Println("  profile delete <name>     Delete a profile")
	fmt.Println("  profile default [name]    Show or set default profile")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck                            # Start TUI with default profile")
	fmt.Println("  agent-deck -p work                    # Start TUI with 'work' profile")
	fmt.Println("  agent-deck add .                      # Add current directory")
	fmt.Println("  agent-deck add -t \"My App\" -g dev .   # With title and group")
	fmt.Println("  agent-deck session start my-project   # Start a session")
	fmt.Println("  agent-deck session show               # Show current session (in tmux)")
	fmt.Println("  agent-deck mcp list --json            # List MCPs as JSON")
	fmt.Println("  agent-deck mcp attach my-app exa      # Attach MCP to session")
	fmt.Println("  agent-deck group move my-app work     # Move session to group")
	fmt.Println()
	fmt.Println("Environment Variables:")
	fmt.Println("  AGENTDECK_PROFILE    Default profile to use")
	fmt.Println("  AGENTDECK_COLOR      Color mode: truecolor, 256, 16, none")
	fmt.Println()
	fmt.Println("Keyboard shortcuts (in TUI):")
	fmt.Println("  n          New session")
	fmt.Println("  g          New group")
	fmt.Println("  Enter      Attach to session")
	fmt.Println("  d          Delete session/group")
	fmt.Println("  m          Move session to group")
	fmt.Println("  R          Rename session/group")
	fmt.Println("  /          Search")
	fmt.Println("  Ctrl+Q     Detach from session")
	fmt.Println("  q          Quit")
}

// mergeFlags returns the non-empty value, preferring the first
func mergeFlags(long, short string) string {
	if long != "" {
		return long
	}
	return short
}

// truncate shortens a string to max length with ellipsis
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// detectTool determines the tool type from command
func detectTool(cmd string) string {
	cmd = strings.ToLower(cmd)
	switch {
	case strings.Contains(cmd, "claude"):
		return "claude"
	case strings.Contains(cmd, "opencode") || strings.Contains(cmd, "open-code"):
		return "opencode"
	case strings.Contains(cmd, "gemini"):
		return "gemini"
	case strings.Contains(cmd, "codex"):
		return "codex"
	case strings.Contains(cmd, "cursor"):
		return "cursor"
	default:
		return "shell"
	}
}

// getLockFilePath returns the path to the lock file for a profile
func getLockFilePath(profile string) string {
	if profile == "" {
		profile = session.DefaultProfile
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".agent-deck", "profiles", profile, ".lock")
}

// isProcessRunning checks if a process with the given PID is still running
func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds, so we need to send signal 0 to check
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// acquireLock attempts to acquire an exclusive lock for the profile
// Uses O_EXCL for atomic file creation to prevent race conditions
func acquireLock(profile string) error {
	lockPath := getLockFilePath(profile)

	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return fmt.Errorf("failed to create lock directory: %w", err)
	}

	// Attempt atomic lock file creation (up to 2 attempts for stale lock cleanup)
	for attempt := 0; attempt < 2; attempt++ {
		// O_EXCL ensures atomic creation - fails if file exists
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			// Successfully created lock file atomically
			defer f.Close()
			if _, writeErr := f.WriteString(strconv.Itoa(os.Getpid())); writeErr != nil {
				os.Remove(lockPath)
				return fmt.Errorf("failed to write PID to lock file: %w", writeErr)
			}
			return nil
		}

		if !os.IsExist(err) {
			return fmt.Errorf("failed to create lock file: %w", err)
		}

		// Lock file exists - check if stale
		data, readErr := os.ReadFile(lockPath)
		if readErr != nil {
			// Cannot read lock file, try removing it
			os.Remove(lockPath)
			continue
		}

		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && isProcessRunning(pid) {
			// Another instance is running
			effectiveProfile := profile
			if effectiveProfile == "" {
				effectiveProfile = session.DefaultProfile
			}
			return fmt.Errorf("agent-deck is already running for profile '%s' (PID %d)\n\nIf this is incorrect, remove the lock file:\n  rm %s", effectiveProfile, pid, lockPath)
		}

		// Stale lock - remove and retry
		os.Remove(lockPath)
	}

	return fmt.Errorf("failed to acquire lock after multiple attempts")
}

// releaseLock removes the lock file for the profile
func releaseLock(profile string) {
	lockPath := getLockFilePath(profile)
	// Only remove if it's our lock (contains our PID)
	if data, err := os.ReadFile(lockPath); err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid == os.Getpid() {
			os.Remove(lockPath)
		}
	}
}

// handleUninstall removes agent-deck from the system
func handleUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	keepData := fs.Bool("keep-data", false, "Keep ~/.agent-deck/ (sessions, config, logs)")
	keepTmuxConfig := fs.Bool("keep-tmux-config", false, "Keep tmux configuration")
	dryRun := fs.Bool("dry-run", false, "Show what would be removed without removing")
	yes := fs.Bool("y", false, "Skip confirmation prompts")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck uninstall [options]")
		fmt.Println()
		fmt.Println("Uninstall Agent Deck from your system.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --dry-run           Show what would be removed without removing")
		fmt.Println("  --keep-data         Keep ~/.agent-deck/ (sessions, config, logs)")
		fmt.Println("  --keep-tmux-config  Keep tmux configuration")
		fmt.Println("  -y                  Skip confirmation prompts")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck uninstall              # Interactive uninstall")
		fmt.Println("  agent-deck uninstall --dry-run    # Preview what would be removed")
		fmt.Println("  agent-deck uninstall --keep-data  # Remove binary only, keep sessions")
		fmt.Println("  agent-deck uninstall -y           # Uninstall without prompts")
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	fmt.Println("╔════════════════════════════════════════╗")
	fmt.Println("║       Agent Deck Uninstaller           ║")
	fmt.Println("╚════════════════════════════════════════╝")
	fmt.Println()

	if *dryRun {
		fmt.Println("DRY RUN MODE - Nothing will be removed")
		fmt.Println()
	}

	homeDir, _ := os.UserHomeDir()
	dataDir := filepath.Join(homeDir, ".agent-deck")

	// Track what we find
	type foundItem struct {
		itemType    string
		path        string
		description string
	}
	var foundItems []foundItem

	// Check for Homebrew installation
	homebrewInstalled := false
	if _, err := exec.LookPath("brew"); err == nil {
		cmd := exec.Command("brew", "list", "agent-deck")
		if cmd.Run() == nil {
			homebrewInstalled = true
			foundItems = append(foundItems, foundItem{"homebrew", "", "Homebrew package: agent-deck"})
			fmt.Println("Found: Homebrew installation")
		}
	}

	// Check common binary locations
	binaryLocations := []string{
		filepath.Join(homeDir, ".local", "bin", "agent-deck"),
		"/usr/local/bin/agent-deck",
		filepath.Join(homeDir, "bin", "agent-deck"),
	}

	for _, loc := range binaryLocations {
		info, err := os.Lstat(loc)
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(loc)
			foundItems = append(foundItems, foundItem{"binary-symlink", loc, fmt.Sprintf("Binary (symlink) → %s", target)})
			fmt.Printf("Found: Binary (symlink) at %s\n", loc)
			fmt.Printf("       → %s\n", target)
		} else {
			foundItems = append(foundItems, foundItem{"binary", loc, "Binary"})
			fmt.Printf("Found: Binary at %s\n", loc)
		}
	}

	// Check for data directory
	if info, err := os.Stat(dataDir); err == nil && info.IsDir() {
		// Count sessions and profiles
		sessionCount := 0
		profileCount := 0
		profilesDir := filepath.Join(dataDir, "profiles")
		if entries, err := os.ReadDir(profilesDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					profileCount++
					sessionsFile := filepath.Join(profilesDir, entry.Name(), "sessions.json")
					if data, err := os.ReadFile(sessionsFile); err == nil {
						sessionCount += strings.Count(string(data), `"id"`)
					}
				}
			}
		}

		// Get total size
		var totalSize int64
		_ = filepath.Walk(dataDir, func(_ string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				totalSize += info.Size()
			}
			return nil
		})
		sizeStr := formatSize(totalSize)

		foundItems = append(foundItems, foundItem{"data", dataDir, fmt.Sprintf("%d profiles, %d sessions, %s", profileCount, sessionCount, sizeStr)})
		fmt.Printf("Found: Data directory at %s\n", dataDir)
		fmt.Printf("       %d profiles, %d sessions, %s\n", profileCount, sessionCount, sizeStr)
	}

	// Check for tmux config
	tmuxConf := filepath.Join(homeDir, ".tmux.conf")
	if data, err := os.ReadFile(tmuxConf); err == nil {
		if strings.Contains(string(data), "# agent-deck configuration") {
			foundItems = append(foundItems, foundItem{"tmux", tmuxConf, "tmux configuration block"})
			fmt.Println("Found: tmux configuration in ~/.tmux.conf")
		}
	}

	fmt.Println()

	// Nothing found?
	if len(foundItems) == 0 {
		fmt.Println("Agent Deck does not appear to be installed.")
		fmt.Println()
		fmt.Println("Checked locations:")
		for _, loc := range binaryLocations {
			fmt.Printf("  - %s\n", loc)
		}
		fmt.Printf("  - %s\n", dataDir)
		fmt.Printf("  - %s (for agent-deck config)\n", tmuxConf)
		return
	}

	// Summary of what will be removed
	fmt.Println("The following will be removed:")
	fmt.Println()

	for _, item := range foundItems {
		switch item.itemType {
		case "homebrew":
			fmt.Println("  • Homebrew package: agent-deck")
		case "binary", "binary-symlink":
			fmt.Printf("  • Binary: %s\n", item.path)
		case "data":
			if *keepData {
				fmt.Printf("  ○ Data directory: %s (keeping)\n", item.path)
			} else {
				fmt.Printf("  • Data directory: %s\n", item.path)
				fmt.Println("    Including: sessions, logs, config")
			}
		case "tmux":
			if *keepTmuxConfig {
				fmt.Println("  ○ tmux config: ~/.tmux.conf (keeping)")
			} else {
				fmt.Println("  • tmux config block in ~/.tmux.conf")
			}
		}
	}

	fmt.Println()

	// Confirm unless -y flag
	if !*yes && !*dryRun {
		fmt.Print("Proceed with uninstall? [y/N] ")
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Uninstall cancelled.")
			return
		}
		fmt.Println()
	}

	// Dry run stops here
	if *dryRun {
		fmt.Println("Dry run complete. No changes made.")
		return
	}

	fmt.Println("Uninstalling...")
	fmt.Println()

	// Track the current binary path for self-deletion at the end
	currentBinary, _ := os.Executable()
	currentBinary, _ = filepath.EvalSymlinks(currentBinary)

	// 1. Homebrew
	if homebrewInstalled {
		fmt.Println("Removing Homebrew package...")
		cmd := exec.Command("brew", "uninstall", "agent-deck")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("Warning: failed to uninstall via Homebrew: %v\n", err)
		} else {
			fmt.Println("✓ Homebrew package removed")
		}
	}

	// 2. Binary files
	for _, item := range foundItems {
		if item.itemType != "binary" && item.itemType != "binary-symlink" {
			continue
		}

		fmt.Printf("Removing binary at %s...\n", item.path)

		// Resolve symlink to check if it points to current binary
		realPath, _ := filepath.EvalSymlinks(item.path)

		// Check if we need sudo
		dir := filepath.Dir(item.path)
		testFile := filepath.Join(dir, ".agent-deck-write-test")
		if f, err := os.Create(testFile); err != nil {
			// Need elevated permissions
			fmt.Printf("Requires sudo to remove %s\n", item.path)
			cmd := exec.Command("sudo", "rm", "-f", item.path)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Printf("Warning: failed to remove %s: %v\n", item.path, err)
			} else {
				fmt.Printf("✓ Binary removed: %s\n", item.path)
			}
		} else {
			f.Close()
			os.Remove(testFile)

			// Skip if this is our own binary (delete last)
			if realPath == currentBinary {
				continue
			}

			if err := os.Remove(item.path); err != nil {
				fmt.Printf("Warning: failed to remove %s: %v\n", item.path, err)
			} else {
				fmt.Printf("✓ Binary removed: %s\n", item.path)
			}
		}
	}

	// 3. tmux config
	if !*keepTmuxConfig {
		for _, item := range foundItems {
			if item.itemType != "tmux" {
				continue
			}

			fmt.Println("Removing tmux configuration...")

			data, err := os.ReadFile(tmuxConf)
			if err != nil {
				fmt.Printf("Warning: failed to read tmux config: %v\n", err)
				continue
			}

			// Create backup
			backupPath := tmuxConf + ".bak.agentdeck-uninstall"
			if err := os.WriteFile(backupPath, data, 0644); err != nil {
				fmt.Printf("Warning: failed to create backup: %v\n", err)
			}

			// Remove the agent-deck config block
			content := string(data)
			startMarker := "# agent-deck configuration"
			endMarker := "# End agent-deck configuration"

			startIdx := strings.Index(content, startMarker)
			endIdx := strings.Index(content, endMarker)

			if startIdx != -1 && endIdx != -1 {
				// Include the end marker line in removal
				endIdx += len(endMarker)
				// Also remove trailing newline
				if endIdx < len(content) && content[endIdx] == '\n' {
					endIdx++
				}

				newContent := content[:startIdx] + content[endIdx:]
				// Clean up multiple blank lines
				for strings.Contains(newContent, "\n\n\n") {
					newContent = strings.ReplaceAll(newContent, "\n\n\n", "\n\n")
				}
				newContent = strings.TrimRight(newContent, "\n") + "\n"

				if err := os.WriteFile(tmuxConf, []byte(newContent), 0644); err != nil {
					fmt.Printf("Warning: failed to update tmux config: %v\n", err)
				} else {
					fmt.Printf("✓ tmux configuration removed (backup: %s)\n", backupPath)
				}
			}
		}
	}

	// 4. Data directory
	if !*keepData {
		for _, item := range foundItems {
			if item.itemType != "data" {
				continue
			}

			// Offer backup unless -y flag
			if !*yes {
				fmt.Print("Create backup of data before removing? [Y/n] ")
				var response string
				_, _ = fmt.Scanln(&response)
				if strings.ToLower(response) != "n" {
					backupFile := filepath.Join(homeDir, fmt.Sprintf("agent-deck-backup-%s.tar.gz", time.Now().Format("20060102-150405")))
					fmt.Printf("Creating backup at %s...\n", backupFile)

					cmd := exec.Command("tar", "-czf", backupFile, "-C", homeDir, ".agent-deck")
					if err := cmd.Run(); err != nil {
						fmt.Printf("Warning: failed to create backup: %v\n", err)
					} else {
						fmt.Printf("✓ Backup created: %s\n", backupFile)
					}
				}
			}

			fmt.Println("Removing data directory...")
			if err := os.RemoveAll(dataDir); err != nil {
				fmt.Printf("Warning: failed to remove data directory: %v\n", err)
			} else {
				fmt.Printf("✓ Data directory removed: %s\n", dataDir)
			}
		}
	}

	fmt.Println()
	fmt.Println("╔════════════════════════════════════════╗")
	fmt.Println("║     Uninstall complete!                ║")
	fmt.Println("╚════════════════════════════════════════╝")
	fmt.Println()

	if *keepData {
		fmt.Printf("Note: Data directory preserved at %s\n", dataDir)
		fmt.Println("      Remove manually with: rm -rf ~/.agent-deck")
	}

	if *keepTmuxConfig {
		fmt.Println("Note: tmux config preserved in ~/.tmux.conf")
		fmt.Println("      Remove the '# agent-deck configuration' block manually if desired")
	}

	fmt.Println()
	fmt.Println("Thank you for using Agent Deck!")
	fmt.Println("Feedback: https://github.com/asheshgoplani/agent-deck/issues")
}

// formatSize formats bytes into human-readable size
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
