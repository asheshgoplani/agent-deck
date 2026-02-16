package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/ui"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

const Version = "0.16.0"

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
		case "rename", "mv":
			handleRename(profile, args[1:])
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
		case "mcp-proxy":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "Usage: agent-deck mcp-proxy <socket-path>")
				os.Exit(1)
			}
			runMCPProxy(args[1])
			return
		case "group":
			handleGroup(profile, args[1:])
			return
		case "try":
			handleTry(profile, args[1:])
			return
		case "conductor":
			handleConductor(profile, args[1:])
			return
		case "worktree", "wt":
			handleWorktree(profile, args[1:])
			return
		case "uninstall":
			handleUninstall(args[1:])
			return
		case "hook-handler":
			handleHookHandler()
			return
		case "hooks":
			handleHooks(args[1:])
			return
		}
	}

	// Block TUI launch inside a managed session to prevent infinite nesting.
	// CLI commands (add, session start/stop, mcp attach, etc.) still work fine.
	if isNestedSession() {
		fmt.Fprintln(os.Stderr, "Error: Cannot launch the agent-deck TUI inside an agent-deck session.")
		fmt.Fprintln(os.Stderr, "This would create a recursive nested session.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "CLI commands work inside sessions. For example:")
		fmt.Fprintln(os.Stderr, "  agent-deck add /path -t \"Title\"    # Add a new session")
		fmt.Fprintln(os.Stderr, "  agent-deck session start <id>      # Start a session")
		fmt.Fprintln(os.Stderr, "  agent-deck mcp attach <id> <mcp>   # Attach MCP")
		fmt.Fprintln(os.Stderr, "  agent-deck list                    # List sessions")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "To open the TUI, detach first with Ctrl+Q.")
		os.Exit(1)
	}

	// Set version for UI update checking
	ui.SetVersion(Version)

	// Initialize theme from config (resolves "system" to actual dark/light)
	theme := session.ResolveTheme()
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

	// Create storage early to register instance via SQLite
	earlyStorage, err := session.NewStorageWithProfile(profile)
	if err == nil {
		if db := earlyStorage.GetDB(); db != nil {
			statedb.SetGlobal(db)
			_ = db.RegisterInstance(false)
		}
	}

	// Check if multiple instances are allowed (uses primary election as single-instance gate)
	instanceSettings := session.GetInstanceSettings()
	if !instanceSettings.GetAllowMultiple() {
		if db := statedb.GetGlobal(); db != nil {
			isFirst, electErr := db.ElectPrimary(30 * time.Second)
			if electErr == nil && !isFirst {
				fmt.Println("Error: agent-deck is already running for this profile")
				fmt.Println("Set [instances] allow_multiple = true in config.toml to allow multiple instances")
				os.Exit(1)
			}
		}
	}

	// Set up signal handling for graceful shutdown and crash dumps
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		if db := statedb.GetGlobal(); db != nil {
			_ = db.ResignPrimary()
			_ = db.UnregisterInstance()
		}
		os.Exit(0)
	}()

	// Set up structured logging (JSONL format with rotation)
	// When AGENTDECK_DEBUG is set, logs go to ~/.agent-deck/debug.log
	// When not set, logs are discarded to avoid TUI interference
	debugMode := os.Getenv("AGENTDECK_DEBUG") != ""
	if baseDir, err := session.GetAgentDeckDir(); err == nil {
		logCfg := logging.Config{
			Debug:                 debugMode,
			LogDir:                baseDir,
			Level:                 "debug",
			Format:                "json",
			MaxSizeMB:             10,
			MaxBackups:            5,
			MaxAgeDays:            10,
			Compress:              true,
			RingBufferSize:        10 * 1024 * 1024,
			AggregateIntervalSecs: 30,
		}

		// Override defaults from user config if available
		if userCfg, err := session.LoadUserConfig(); err == nil {
			ls := userCfg.Logs
			if ls.DebugLevel != "" {
				logCfg.Level = ls.DebugLevel
			}
			if ls.DebugFormat != "" {
				logCfg.Format = ls.DebugFormat
			}
			if ls.DebugMaxMB > 0 {
				logCfg.MaxSizeMB = ls.DebugMaxMB
			}
			if ls.DebugBackups > 0 {
				logCfg.MaxBackups = ls.DebugBackups
			}
			if ls.DebugRetentionDays > 0 {
				logCfg.MaxAgeDays = ls.DebugRetentionDays
			}
			if ls.DebugCompress {
				logCfg.Compress = ls.DebugCompress
			}
			if ls.RingBufferMB > 0 {
				logCfg.RingBufferSize = ls.RingBufferMB * 1024 * 1024
			}
			if ls.PprofEnabled {
				logCfg.PprofEnabled = ls.PprofEnabled
			}
			if ls.AggregateIntervalS > 0 {
				logCfg.AggregateIntervalSecs = ls.AggregateIntervalS
			}
		}

		logging.Init(logCfg)
		defer logging.Shutdown()

		if debugMode {
			logging.ForComponent(logging.CompUI).Info("instance_started",
				slog.Int("pid", os.Getpid()))
		}

		// SIGUSR1 dumps the ring buffer for post-mortem debugging
		usr1Chan := make(chan os.Signal, 1)
		signal.Notify(usr1Chan, syscall.SIGUSR1)
		go func() {
			for range usr1Chan {
				dumpPath := filepath.Join(baseDir, fmt.Sprintf("crash-dump-%d.jsonl", time.Now().Unix()))
				if err := logging.DumpRingBuffer(dumpPath); err != nil {
					logging.ForComponent(logging.CompUI).Error("crash_dump_failed",
						slog.String("error", err.Error()))
				} else {
					logging.ForComponent(logging.CompUI).Info("crash_dump_written",
						slog.String("path", dumpPath))
				}
			}
		}()
	}

	// Start TUI with the specified profile
	homeModel := ui.NewHomeWithProfileAndMode(profile)
	p := tea.NewProgram(
		homeModel,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// Start maintenance worker (background goroutine, respects config toggle)
	maintenanceCtx, maintenanceCancel := context.WithCancel(context.Background())
	defer maintenanceCancel()
	session.StartMaintenanceWorker(maintenanceCtx, func(result session.MaintenanceResult) {
		p.Send(ui.MaintenanceCompleteMsg{Result: result})
	})

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
	// Note: -b/--new-branch are boolean flags (no value), so not included here
	valueFlags := map[string]bool{
		"-t": true, "--title": true,
		"-g": true, "--group": true,
		"-c": true, "--cmd": true,
		"-p": true, "--parent": true,
		"--mcp": true,
		"-w":    true, "--worktree": true,
		"--location":       true,
		"--resume-session": true,
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
	wrapper := fs.String("wrapper", "", "Wrapper command (use {command} to include tool command, e.g., 'nvim +\"terminal {command}\"')")
	parent := fs.String("parent", "", "Parent session (creates sub-session, inherits group)")
	parentShort := fs.String("p", "", "Parent session (short)")
	quickCreate := fs.Bool("quick", false, "Auto-generate session name (adjective-noun)")
	quickCreateShort := fs.Bool("Q", false, "Auto-generate session name (short)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	// Worktree flags
	worktreeBranch := fs.String("w", "", "Create session in git worktree for branch")
	worktreeBranchLong := fs.String("worktree", "", "Create session in git worktree for branch")
	newBranch := fs.Bool("b", false, "Create new branch (use with --worktree)")
	newBranchLong := fs.Bool("new-branch", false, "Create new branch")
	worktreeLocation := fs.String("location", "", "Worktree location: sibling, subdirectory, or custom path")

	// MCP flag - can be specified multiple times
	var mcpFlags []string
	fs.Func("mcp", "MCP to attach (can specify multiple times)", func(s string) error {
		mcpFlags = append(mcpFlags, s)
		return nil
	})

	// Resume session flag
	resumeSession := fs.String("resume-session", "", "Claude session ID to resume (skips new session creation)")

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
		fmt.Println("  agent-deck add -c opencode --wrapper \"nvim +'terminal {command}' +'startinsert'\" .")
		fmt.Println("  agent-deck add --quick -c claude .   # Auto-generated name")
		fmt.Println()
		fmt.Println("Worktree Examples:")
		fmt.Println("  agent-deck add -w feature/login .    # Create worktree for existing branch")
		fmt.Println("  agent-deck add -w feature/new -b .   # Create worktree with new branch")
		fmt.Println("  agent-deck add --worktree fix/bug-123 --new-branch /path/to/repo")
	}

	// Reorder args: move path to end so flags are parsed correctly
	// Go's flag package stops parsing at first non-flag argument
	// This allows: "add . -c claude" to work same as "add -c claude ."
	args = reorderArgsForFlagParsing(args)

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
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

	// Resolve worktree flags
	wtBranch := *worktreeBranch
	if *worktreeBranchLong != "" {
		wtBranch = *worktreeBranchLong
	}
	createNewBranch := *newBranch || *newBranchLong

	// Handle worktree creation
	var worktreePath, worktreeRepoRoot string
	if wtBranch != "" {
		// Validate path is a git repo
		if !git.IsGitRepo(path) {
			fmt.Fprintf(os.Stderr, "Error: %s is not a git repository\n", path)
			os.Exit(1)
		}

		// Get repo root
		repoRoot, err := git.GetRepoRoot(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get repo root: %v\n", err)
			os.Exit(1)
		}

		// Pre-validate branch name for better error messages
		if err := git.ValidateBranchName(wtBranch); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid branch name: %v\n", err)
			os.Exit(1)
		}

		// Check -b flag logic: if -b is passed, branch must NOT exist (user wants new branch)
		branchExists := git.BranchExists(repoRoot, wtBranch)
		if createNewBranch && branchExists {
			fmt.Fprintf(
				os.Stderr,
				"Error: branch '%s' already exists (remove -b flag to use existing branch)\n",
				wtBranch,
			)
			os.Exit(1)
		}

		// Determine worktree location: CLI flag overrides config
		wtSettings := session.GetWorktreeSettings()
		location := wtSettings.DefaultLocation
		if *worktreeLocation != "" {
			location = *worktreeLocation
		}

		// Generate worktree path
		worktreePath = git.WorktreePath(git.WorktreePathOptions{
			Branch:    wtBranch,
			Location:  location,
			RepoDir:   repoRoot,
			SessionID: git.GeneratePathID(),
			Template:  wtSettings.Template(),
		})

		// Ensure parent directory exists (needed for subdirectory mode)
		if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create parent directory: %v\n", err)
			os.Exit(1)
		}

		// Check if worktree already exists
		if _, err := os.Stat(worktreePath); err == nil {
			fmt.Fprintf(os.Stderr, "Error: worktree already exists at %s\n", worktreePath)
			fmt.Fprintf(os.Stderr, "Tip: Use 'agent-deck add %s' to add the existing worktree\n", worktreePath)
			os.Exit(1)
		}

		// Create worktree
		if err := git.CreateWorktree(repoRoot, worktreePath, wtBranch); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create worktree: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Created worktree at: %s\n", worktreePath)
		worktreeRepoRoot = repoRoot
		// Update path to point to worktree so session uses worktree as working directory
		path = worktreePath
	}

	// Merge short and long flags
	sessionTitle := mergeFlags(*title, *titleShort)
	sessionGroup := mergeFlags(*group, *groupShort)
	sessionCommand := mergeFlags(*command, *commandShort)
	sessionParent := mergeFlags(*parent, *parentShort)

	// Validate --resume-session requires Claude
	if *resumeSession != "" {
		tool := detectTool(sessionCommand)
		if tool != "claude" {
			fmt.Println("Error: --resume-session only works with Claude sessions (-c claude)")
			os.Exit(1)
		}
	}

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
			return // unreachable, satisfies staticcheck SA5011
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
	isQuick := *quickCreate || *quickCreateShort

	if isQuick && !userProvidedTitle {
		// Quick mode: use auto-generated adjective-noun name
		sessionTitle = session.GenerateUniqueSessionName(instances, sessionGroup)
	} else if !userProvidedTitle {
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
		newInstance.Tool = detectTool(sessionCommand)
		// For custom tools, resolve the actual shell command (e.g. "glm" → "claude")
		if toolDef := session.GetToolDef(newInstance.Tool); toolDef != nil {
			newInstance.Command = toolDef.Command
		} else {
			newInstance.Command = sessionCommand
		}
	}

	// Set wrapper if provided
	if *wrapper != "" {
		newInstance.Wrapper = *wrapper
	}

	// Set worktree fields if created
	if worktreePath != "" {
		newInstance.WorktreePath = worktreePath
		newInstance.WorktreeRepoRoot = worktreeRepoRoot
		newInstance.WorktreeBranch = wtBranch
	}

	// Handle --resume-session: set Claude session ID and resume mode
	if *resumeSession != "" {
		newInstance.ClaudeSessionID = *resumeSession
		newInstance.ClaudeDetectedAt = time.Now()

		opts := newInstance.GetClaudeOptions()
		if opts == nil {
			userConfig, _ := session.LoadUserConfig()
			opts = session.NewClaudeOptions(userConfig)
		}
		opts.SessionMode = "resume"
		opts.ResumeSessionID = *resumeSession
		if err := newInstance.SetClaudeOptions(opts); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to set resume options: %v\n", err)
		}
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

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Build human-readable output
	var humanLines []string
	humanLines = append(humanLines, fmt.Sprintf("Added session: %s", sessionTitle))
	humanLines = append(humanLines, fmt.Sprintf("  Profile: %s", storage.Profile()))
	humanLines = append(humanLines, fmt.Sprintf("  Path:    %s", path))
	humanLines = append(humanLines, fmt.Sprintf("  Group:   %s", newInstance.GroupPath))
	humanLines = append(humanLines, fmt.Sprintf("  ID:      %s", newInstance.ID))
	if sessionCommand != "" {
		humanLines = append(humanLines, fmt.Sprintf("  Cmd:     %s", sessionCommand))
	}
	if len(mcpFlags) > 0 {
		humanLines = append(humanLines, fmt.Sprintf("  MCPs:    %s", strings.Join(mcpFlags, ", ")))
	}
	if parentInstance != nil {
		humanLines = append(humanLines, fmt.Sprintf("  Parent:  %s (%s)", parentInstance.Title, parentInstance.ID[:8]))
	}
	if worktreePath != "" {
		humanLines = append(humanLines, fmt.Sprintf("  Worktree: %s (branch: %s)", worktreePath, wtBranch))
		humanLines = append(humanLines, fmt.Sprintf("  Repo:    %s", worktreeRepoRoot))
	}
	if *resumeSession != "" {
		humanLines = append(humanLines, fmt.Sprintf("  Resume:  %s", *resumeSession))
	}
	humanLines = append(humanLines, "")
	humanLines = append(humanLines, "Next steps:")
	humanLines = append(humanLines, fmt.Sprintf("  agent-deck session start %s   # Start the session", sessionTitle))
	humanLines = append(humanLines, "  agent-deck                         # Open TUI and press Enter to attach")

	// Build JSON data
	jsonData := map[string]interface{}{
		"success": true,
		"id":      newInstance.ID,
		"title":   newInstance.Title,
		"path":    path,
		"tool":    newInstance.Tool,
		"group":   newInstance.GroupPath,
		"profile": storage.Profile(),
	}
	if sessionCommand != "" {
		jsonData["command"] = sessionCommand
	}
	if len(mcpFlags) > 0 {
		jsonData["mcps"] = mcpFlags
	}
	if parentInstance != nil {
		jsonData["parent_id"] = parentInstance.ID
		jsonData["parent_title"] = parentInstance.Title
	}
	if worktreePath != "" {
		jsonData["worktree_path"] = worktreePath
		jsonData["worktree_branch"] = wtBranch
		jsonData["worktree_repo_root"] = worktreeRepoRoot
	}
	if *resumeSession != "" {
		jsonData["resume_session"] = *resumeSession
	}

	out.Success(humanLines[0], jsonData)
	if !*jsonOutput && !quietMode {
		for _, line := range humanLines[1:] {
			fmt.Println(line)
		}
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

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
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
			Status    string    `json:"status"`
			Profile   string    `json:"profile"`
			CreatedAt time.Time `json:"created_at"`
		}
		sessions := make([]sessionJSON, len(instances))
		for i, inst := range instances {
			_ = inst.UpdateStatus()
			sessions[i] = sessionJSON{
				ID:        inst.ID,
				Title:     inst.Title,
				Path:      inst.ProjectPath,
				Group:     inst.GroupPath,
				Tool:      inst.Tool,
				Command:   inst.Command,
				Status:    StatusString(inst.Status),
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
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

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

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	identifier := fs.Arg(0)
	if identifier == "" {
		out.Error("session ID or title is required", ErrCodeNotFound)
		if !*jsonOutput {
			fs.Usage()
		}
		os.Exit(1)
	}

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Use shared ResolveSession for consistent matching (ambiguity detection, min prefix length)
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(fmt.Sprintf("%s (profile '%s')", errMsg, storage.Profile()), errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
	}

	removedID := inst.ID
	removedTitle := inst.Title

	// Always attempt to kill the tmux session, even if Exists() returns false.
	// The saved status may be stale (e.g., "error" in DB but tmux session still alive).
	// Kill() is safe to call on non-existent sessions (returns error which we handle).
	if err := inst.Kill(); err != nil {
		// Only warn if the session actually existed (ignore "not found" errors)
		if inst.Exists() && !*jsonOutput {
			fmt.Printf("Warning: failed to kill tmux session: %v\n", err)
			fmt.Println("Session removed from Agent Deck but may still be running in tmux")
		}
	}

	// Clean up worktree directory if this is a worktree session
	if inst.IsWorktree() {
		if err := git.RemoveWorktree(inst.WorktreeRepoRoot, inst.WorktreePath, false); err != nil {
			if !*jsonOutput {
				fmt.Printf("Warning: failed to remove worktree: %v\n", err)
			}
		}
		_ = git.PruneWorktrees(inst.WorktreeRepoRoot)
	}

	// Direct SQL DELETE first to prevent resurrection by concurrent TUI force saves.
	// The TUI's forceSaveInstances() can race with CLI deletion and re-insert the session.
	// By deleting the row directly, we ensure it's gone even if SaveWithGroups races.
	if err := storage.DeleteInstance(removedID); err != nil {
		if !*jsonOutput {
			fmt.Printf("Warning: direct delete failed: %v\n", err)
		}
	}

	// Rebuild instance list without the deleted session and save with groups
	newInstances := make([]*session.Instance, 0, len(instances)-1)
	for _, s := range instances {
		if s.ID != removedID {
			newInstances = append(newInstances, s)
		}
	}
	groupTree := session.NewGroupTreeWithGroups(newInstances, groups)

	if err := storage.SaveWithGroups(newInstances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(
		fmt.Sprintf("Removed session: %s (from profile '%s')", removedTitle, storage.Profile()),
		map[string]interface{}{
			"success": true,
			"id":      removedID,
			"title":   removedTitle,
			"removed": true,
			"profile": storage.Profile(),
		},
	)
}

func handleRename(profile string, args []string) {
	fs := flag.NewFlagSet("rename", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck rename <id|title> <new-title>")
		fmt.Println()
		fmt.Println("Rename a session by ID or title.")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck rename abc12345 \"New Name\"")
		fmt.Println("  agent-deck rename \"Old Name\" \"New Name\"")
		fmt.Println("  agent-deck -p work rename abc12345 \"New Name\"   # Rename in 'work' profile")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	identifier := fs.Arg(0)
	newTitle := fs.Arg(1)
	if identifier == "" || newTitle == "" {
		out.Error("session ID/title and new title are required", ErrCodeInvalidOperation)
		if !*jsonOutput {
			fs.Usage()
		}
		os.Exit(1)
	}

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(fmt.Sprintf("%s (profile '%s')", errMsg, storage.Profile()), errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
	}

	oldTitle := inst.Title

	// Check for duplicate title at the same path (but allow renaming to same title)
	if newTitle != oldTitle {
		if isDup, existing := isDuplicateSession(instances, newTitle, inst.ProjectPath); isDup {
			out.Error(
				fmt.Sprintf("session with title %q already exists at path %q (id: %s)", newTitle, inst.ProjectPath, existing.ID),
				ErrCodeInvalidOperation,
			)
			os.Exit(1)
		}
	}

	inst.Title = newTitle
	inst.SyncTmuxDisplayName()

	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(
		fmt.Sprintf("Renamed session: %q → %q (profile '%s')", oldTitle, newTitle, storage.Profile()),
		map[string]interface{}{
			"success":   true,
			"id":        inst.ID,
			"old_title": oldTitle,
			"new_title": newTitle,
			"profile":   storage.Profile(),
		},
	)
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

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
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
	// Extract --json and -q/--quiet flags from anywhere in args
	var jsonMode, quietMode bool
	var filteredArgs []string
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonMode = true
		case "--quiet", "-q":
			quietMode = true
		default:
			filteredArgs = append(filteredArgs, arg)
		}
	}
	out := NewCLIOutput(jsonMode, quietMode)

	if len(filteredArgs) == 0 {
		// Default to list
		handleProfileList(out, jsonMode)
		return
	}

	switch filteredArgs[0] {
	case "list", "ls":
		handleProfileList(out, jsonMode)
	case "create", "new":
		if len(filteredArgs) < 2 {
			out.Error("profile name is required", ErrCodeInvalidOperation)
			if !jsonMode {
				fmt.Println("Usage: agent-deck profile create <name>")
			}
			os.Exit(1)
		}
		handleProfileCreate(out, filteredArgs[1])
	case "delete", "rm":
		if len(filteredArgs) < 2 {
			out.Error("profile name is required", ErrCodeInvalidOperation)
			if !jsonMode {
				fmt.Println("Usage: agent-deck profile delete <name>")
			}
			os.Exit(1)
		}
		handleProfileDelete(out, jsonMode, filteredArgs[1])
	case "default":
		if len(filteredArgs) < 2 {
			// Show current default
			config, err := session.LoadConfig()
			if err != nil {
				out.Error(fmt.Sprintf("failed to load config: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			out.Success(fmt.Sprintf("Default profile: %s", config.DefaultProfile), map[string]interface{}{
				"success":         true,
				"default_profile": config.DefaultProfile,
			})
			return
		}
		handleProfileSetDefault(out, filteredArgs[1])
	default:
		out.Error(fmt.Sprintf("unknown profile command: %s", filteredArgs[0]), ErrCodeInvalidOperation)
		if !jsonMode {
			fmt.Println()
			fmt.Println("Usage: agent-deck profile <command>")
			fmt.Println()
			fmt.Println("Commands:")
			fmt.Println("  list              List all profiles")
			fmt.Println("  create <name>     Create a new profile")
			fmt.Println("  delete <name>     Delete a profile")
			fmt.Println("  default [name]    Show or set default profile")
		}
		os.Exit(1)
	}
}

func handleProfileList(out *CLIOutput, jsonMode bool) {
	profiles, err := session.ListProfiles()
	if err != nil {
		out.Error(fmt.Sprintf("failed to list profiles: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	config, _ := session.LoadConfig()
	defaultProfile := session.DefaultProfile
	if config != nil {
		defaultProfile = config.DefaultProfile
	}

	if jsonMode {
		var profileList []map[string]interface{}
		for _, p := range profiles {
			profileList = append(profileList, map[string]interface{}{
				"name":       p,
				"is_default": p == defaultProfile,
			})
		}
		out.Success("", map[string]interface{}{
			"success":         true,
			"profiles":        profileList,
			"default_profile": defaultProfile,
			"total":           len(profiles),
		})
		return
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

func handleProfileCreate(out *CLIOutput, name string) {
	if err := session.CreateProfile(name); err != nil {
		out.Error(fmt.Sprintf("%v", err), ErrCodeAlreadyExists)
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("Created profile: %s", name), map[string]interface{}{
		"success": true,
		"name":    name,
		"created": true,
	})
}

func handleProfileDelete(out *CLIOutput, jsonMode bool, name string) {
	// Skip confirmation in JSON mode (for automation)
	if !jsonMode {
		fmt.Printf(
			"Are you sure you want to delete profile '%s'? This will remove all sessions in this profile. [y/N] ",
			name,
		)
		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Cancelled.")
			return
		}
	}

	if err := session.DeleteProfile(name); err != nil {
		out.Error(fmt.Sprintf("%v", err), ErrCodeNotFound)
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("Deleted profile: %s", name), map[string]interface{}{
		"success": true,
		"name":    name,
		"deleted": true,
	})
}

func handleProfileSetDefault(out *CLIOutput, name string) {
	if err := session.SetDefaultProfile(name); err != nil {
		out.Error(fmt.Sprintf("%v", err), ErrCodeNotFound)
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("Default profile set to: %s", name), map[string]interface{}{
		"success":         true,
		"name":            name,
		"default_profile": name,
	})
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

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
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

	// Fetch and display changelog
	displayChangelog(info.CurrentVersion, info.LatestVersion)

	if *checkOnly {
		fmt.Println("\nRun 'agent-deck update' to install.")
		return
	}

	// Confirm update - drain any buffered input first to avoid garbage
	drainStdin()
	fmt.Print("\nInstall update? [Y/n] ")
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(response)
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

// displayChangelog fetches and displays changelog between versions
func displayChangelog(currentVersion, latestVersion string) {
	changelog, err := update.FetchChangelog()
	if err != nil {
		fmt.Println("\n  (Could not fetch changelog. See release notes at the URL above.)")
		return
	}

	entries := update.ParseChangelog(changelog)
	changes := update.GetChangesBetweenVersions(entries, currentVersion, latestVersion)

	if len(changes) > 0 {
		fmt.Print(update.FormatChangelogForDisplay(changes))
	}
}

// drainStdin discards any pending input in stdin to prevent garbage from being read
// This is needed before prompts because ANSI escape sequences or user keypresses
// may have buffered during the changelog display
func drainStdin() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return
	}

	// Use TCIFLUSH via ioctl to flush the terminal input queue
	// This is the proper Unix way to discard pending input
	// TCIFLUSH = 0 (flush input), TCIOFLUSH = 2 (flush both)
	// The syscall is: ioctl(fd, TCFLSH, TCIFLUSH)
	// On macOS/Darwin, TCFLSH = 0x80047410 (from termios.h)
	// On Linux, TCFLSH = 0x540B
	const (
		tcflshDarwin = 0x80047410
		tcflshLinux  = 0x540B
		tciflush     = 0 // flush input queue
	)

	// Try Darwin first, then Linux
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tcflshDarwin, tciflush)
	if errno != 0 {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tcflshLinux, tciflush)
	}
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
	fmt.Println("  try <name>       Quick experiment (create/find dated folder + session)")
	fmt.Println("  list, ls         List all sessions")
	fmt.Println("  remove, rm       Remove a session")
	fmt.Println("  rename, mv       Rename a session")
	fmt.Println("  status           Show session status summary")
	fmt.Println("  session          Manage session lifecycle")
	fmt.Println("  mcp              Manage MCP servers")
	fmt.Println("  group            Manage groups")
	fmt.Println("  worktree, wt     Manage git worktrees")
	fmt.Println("  conductor        Manage conductor meta-agent orchestration")
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
	fmt.Println("Conductor Commands:")
	fmt.Println("  conductor setup           Set up conductor (Telegram bridge + sessions)")
	fmt.Println("  conductor teardown        Stop conductor and remove bridge daemon")
	fmt.Println("  conductor status          Show conductor health across profiles")
	fmt.Println()
	fmt.Println("Worktree Commands:")
	fmt.Println("  worktree list             List worktrees with session associations")
	fmt.Println("  worktree info <session>   Show worktree info for a session")
	fmt.Println("  worktree cleanup          Find and remove orphaned worktrees/sessions")
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
	// Check custom tools first (exact match on original case)
	if session.GetToolDef(cmd) != nil {
		return cmd
	}

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

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
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
			foundItems = append(
				foundItems,
				foundItem{"binary-symlink", loc, fmt.Sprintf("Binary (symlink) → %s", target)},
			)
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
					// Check for state.db (SQLite, v0.11.0+) or sessions.json (legacy)
					dbFile := filepath.Join(profilesDir, entry.Name(), "state.db")
					jsonFile := filepath.Join(profilesDir, entry.Name(), "sessions.json")
					if _, err := os.Stat(dbFile); err == nil {
						profileCount++
						if s, err := session.NewStorageWithProfile(entry.Name()); err == nil {
							if instances, _, err := s.LoadWithGroups(); err == nil {
								sessionCount += len(instances)
							}
						}
					} else if data, err := os.ReadFile(jsonFile); err == nil {
						profileCount++
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

		foundItems = append(
			foundItems,
			foundItem{
				"data",
				dataDir,
				fmt.Sprintf("%d profiles, %d sessions, %s", profileCount, sessionCount, sizeStr),
			},
		)
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
			if err := os.WriteFile(backupPath, data, 0o644); err != nil {
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

				if err := os.WriteFile(tmuxConf, []byte(newContent), 0o644); err != nil {
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
					backupFile := filepath.Join(
						homeDir,
						fmt.Sprintf("agent-deck-backup-%s.tar.gz", time.Now().Format("20060102-150405")),
					)
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

// isNestedSession returns true if we're running inside an agent-deck managed tmux session.
// Uses GetCurrentSessionID() which checks if the current tmux session name matches agentdeck_*.
func isNestedSession() bool {
	return GetCurrentSessionID() != ""
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
