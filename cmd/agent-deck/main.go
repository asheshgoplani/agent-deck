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

const Version = "0.5.8"

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
		}
	}

	// Set version for UI update checking
	ui.SetVersion(Version)

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

// handleAdd adds a new session from CLI
func handleAdd(profile string, args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	title := fs.String("title", "", "Session title (defaults to folder name)")
	titleShort := fs.String("t", "", "Session title (short)")
	group := fs.String("group", "", "Group path (defaults to parent folder)")
	groupShort := fs.String("g", "", "Group path (short)")
	command := fs.String("cmd", "", "Command to run (e.g., 'claude', 'aider')")
	commandShort := fs.String("c", "", "Command to run (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck add <path> [options]")
		fmt.Println()
		fmt.Println("Add a new session to Agent Deck.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  <path>    Project directory (use '.' for current directory)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck add .")
		fmt.Println("  agent-deck add /path/to/project")
		fmt.Println("  agent-deck add -t \"My Project\" -g \"work\" .")
		fmt.Println("  agent-deck add -c claude .")
		fmt.Println("  agent-deck -p work add .             # Add to 'work' profile")
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Get path argument
	path := fs.Arg(0)
	if path == "" {
		fmt.Println("Error: path is required")
		fmt.Println("Usage: agent-deck add <path> [options]")
		os.Exit(1)
	}

	// Resolve path
	if path == "." {
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

	// Check for duplicate (same path)
	for _, inst := range instances {
		if inst.ProjectPath == path {
			fmt.Printf("Session already exists: %s (%s)\n", inst.Title, inst.ID)
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

	fmt.Printf("✓ Added session: %s\n", sessionTitle)
	fmt.Printf("  Profile: %s\n", storage.Profile())
	fmt.Printf("  Path:    %s\n", path)
	fmt.Printf("  Group:   %s\n", newInstance.GroupPath)
	fmt.Printf("  ID:      %s\n", newInstance.ID)
	if sessionCommand != "" {
		fmt.Printf("  Cmd:     %s\n", sessionCommand)
	}
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
	forceCheck := fs.Bool("force", false, "Force check (ignore cache)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck update [options]")
		fmt.Println()
		fmt.Println("Check for and install updates.")
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

	info, err := update.CheckForUpdate(Version, *forceCheck)
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
	fmt.Println("  profile          Manage profiles")
	fmt.Println("  update           Check for and install updates")
	fmt.Println("  version          Show version")
	fmt.Println("  help             Show this help")
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
	fmt.Println("  agent-deck -p work add .              # Add to 'work' profile")
	fmt.Println("  agent-deck add -t \"My App\" -g dev .   # With title and group")
	fmt.Println("  agent-deck list                       # List sessions")
	fmt.Println("  agent-deck list --all                 # List sessions from all profiles")
	fmt.Println("  agent-deck profile create work        # Create 'work' profile")
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
	case strings.Contains(cmd, "aider"):
		return "aider"
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
