package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleConductor dispatches conductor subcommands
func handleConductor(profile string, args []string) {
	if len(args) == 0 {
		printConductorHelp()
		return
	}

	switch args[0] {
	case "setup":
		handleConductorSetup(profile, args[1:])
	case "teardown":
		handleConductorTeardown(profile, args[1:])
	case "status":
		handleConductorStatus(profile, args[1:])
	case "list":
		handleConductorList(profile, args[1:])
	case "help", "--help", "-h":
		printConductorHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown conductor command: %s\n", args[0])
		fmt.Fprintln(os.Stderr)
		printConductorHelp()
		os.Exit(1)
	}
}

// runAutoMigration runs legacy conductor migration and prints results
func runAutoMigration(jsonOutput bool) {
	migrated, err := session.MigrateLegacyConductors()
	if err != nil && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Warning: migration check failed: %v\n", err)
	}
	if !jsonOutput {
		for _, name := range migrated {
			fmt.Printf("  [migrated] Legacy conductor: %s\n", name)
		}
	}
}

// handleConductorSetup sets up a named conductor with directories, sessions, and optionally the Telegram bridge
func handleConductorSetup(profile string, args []string) {
	fs := flag.NewFlagSet("conductor setup", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	noHeartbeat := fs.Bool("no-heartbeat", false, "Disable heartbeat for this conductor")
	heartbeat := fs.Bool("heartbeat", false, "Enable heartbeat for this conductor (default)")
	description := fs.String("description", "", "Description for this conductor")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck [-p profile] conductor setup <name> [options]")
		fmt.Println()
		fmt.Println("Set up a named conductor: creates directory, CLAUDE.md, meta.json, and registers session.")
		fmt.Println("Multiple conductors can exist per profile.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  <name>    Conductor name (e.g., ryan, infra, monitor)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	// Extract positional arg before flags
	var name string
	var flagArgs []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
		} else if name == "" {
			name = arg
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}

	if err := fs.Parse(normalizeArgs(fs, flagArgs)); err != nil {
		os.Exit(1)
	}

	if name == "" {
		fmt.Fprintln(os.Stderr, "Error: conductor name is required")
		fmt.Fprintln(os.Stderr, "Usage: agent-deck [-p profile] conductor setup <name>")
		os.Exit(1)
	}

	if err := session.ValidateConductorName(name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Auto-migrate legacy conductors
	runAutoMigration(*jsonOutput)

	// Determine heartbeat setting
	heartbeatEnabled := true
	if *noHeartbeat {
		heartbeatEnabled = false
	} else if *heartbeat {
		heartbeatEnabled = true
	}

	// Step 1: Load config and check if conductor system is enabled
	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	settings := config.Conductor
	telegramConfigured := settings.Telegram.Token != ""

	// Step 2: If conductor system not enabled, run first-time setup
	if !settings.Enabled {
		fmt.Println("Conductor Setup")
		fmt.Println("===============")
		fmt.Println()
		fmt.Println("The conductor system lets you create named persistent Claude sessions that")
		fmt.Println("monitor and orchestrate all your agent-deck sessions.")
		fmt.Println()

		reader := bufio.NewReader(os.Stdin)

		// Ask about Telegram
		fmt.Print("Connect Telegram bot for mobile control? (y/N): ")
		tgAnswer, _ := reader.ReadString('\n')
		tgAnswer = strings.TrimSpace(strings.ToLower(tgAnswer))

		var telegram session.TelegramSettings
		if tgAnswer == "y" || tgAnswer == "yes" {
			fmt.Println()
			fmt.Println("  1. Message @BotFather on Telegram -> /newbot -> copy the token")
			fmt.Println("  2. Message @userinfobot on Telegram -> copy your user ID")
			fmt.Println()

			fmt.Print("Telegram bot token: ")
			token, _ := reader.ReadString('\n')
			token = strings.TrimSpace(token)
			if token == "" {
				fmt.Fprintln(os.Stderr, "Error: token is required")
				os.Exit(1)
			}

			fmt.Print("Your Telegram user ID: ")
			userIDStr, _ := reader.ReadString('\n')
			userIDStr = strings.TrimSpace(userIDStr)
			userID, err := strconv.ParseInt(userIDStr, 10, 64)
			if err != nil || userID == 0 {
				fmt.Fprintln(os.Stderr, "Error: valid user ID is required")
				os.Exit(1)
			}

			telegram = session.TelegramSettings{Token: token, UserID: userID}
			telegramConfigured = true
		}

		// Update config (no longer stores profiles list, conductors are on disk)
		settings = session.ConductorSettings{
			Enabled:           true,
			HeartbeatInterval: 15,
			Telegram:          telegram,
		}
		config.Conductor = settings

		if err := session.SaveUserConfig(config); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println()
		fmt.Println("[ok] Conductor config saved to config.toml")
	}

	// Step 3: Install/update shared CLAUDE.md
	if err := session.InstallSharedClaudeMD(); err != nil {
		fmt.Fprintf(os.Stderr, "Error installing shared CLAUDE.md: %v\n", err)
		os.Exit(1)
	}
	if !*jsonOutput {
		fmt.Println("[ok] Shared CLAUDE.md installed/updated")
	}

	// Step 4: Set up the named conductor
	if !*jsonOutput {
		fmt.Printf("\nSetting up conductor: %s (profile: %s)\n", name, profile)
	}

	if err := session.SetupConductor(name, profile, heartbeatEnabled, *description); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up conductor %s: %v\n", name, err)
		os.Exit(1)
	}
	if !*jsonOutput {
		fmt.Printf("  [ok] Directory, CLAUDE.md, and meta.json created\n")
	}

	// Step 5: Register session in the profile's storage
	sessionTitle := session.ConductorSessionTitle(name)
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading storage for %s: %v\n", profile, err)
		os.Exit(1)
	}

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading sessions for %s: %v\n", profile, err)
		os.Exit(1)
	}

	// Check if session already exists
	var existingID string
	for _, inst := range instances {
		if inst.Title == sessionTitle {
			existingID = inst.ID
			break
		}
	}

	var sessionID string
	existed := false
	if existingID != "" {
		sessionID = existingID
		existed = true
		if !*jsonOutput {
			fmt.Printf("  [ok] Session '%s' already registered (ID: %s)\n", sessionTitle, existingID[:8])
		}
	} else {
		dir, _ := session.ConductorNameDir(name)
		newInst := session.NewInstanceWithGroupAndTool(sessionTitle, dir, "conductor", "claude")
		newInst.Command = "claude"
		instances = append(instances, newInst)

		sessionID = newInst.ID
		if !*jsonOutput {
			fmt.Printf("  [ok] Session '%s' registered (ID: %s)\n", sessionTitle, newInst.ID[:8])
		}
	}

	// Always ensure conductor group is pinned to top
	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	conductorGroup := groupTree.CreateGroup("conductor")
	conductorGroup.Order = -1

	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving session for %s: %v\n", profile, err)
		os.Exit(1)
	}

	// Step 6: Install heartbeat timer (if heartbeat enabled)
	if heartbeatEnabled {
		interval := settings.GetHeartbeatInterval()
		if err := session.InstallHeartbeatScript(name, profile); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to install heartbeat script: %v\n", err)
		} else {
			plistContent, err := session.GenerateHeartbeatPlist(name, interval)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to generate heartbeat plist: %v\n", err)
			} else {
				hbPlistPath, err := session.HeartbeatPlistPath(name)
				if err == nil {
					homeDir, _ := os.UserHomeDir()
					_ = os.MkdirAll(filepath.Join(homeDir, "Library", "LaunchAgents"), 0o755)
					_ = exec.Command("launchctl", "unload", hbPlistPath).Run()
					if err := os.WriteFile(hbPlistPath, []byte(plistContent), 0o644); err == nil {
						if err := exec.Command("launchctl", "load", hbPlistPath).Run(); err == nil {
							if !*jsonOutput {
								fmt.Printf("  [ok] Heartbeat timer installed (every %d min)\n", interval)
							}
						}
					}
				}
			}
		}
	}

	// Step 7: Install Telegram bridge (only if Telegram is configured)
	var plistPath string
	if telegramConfigured {
		if !*jsonOutput {
			fmt.Println()
			fmt.Println("Installing Telegram bridge...")
		}

		installPythonDeps()

		if err := session.InstallBridgeScript(); err != nil {
			fmt.Fprintf(os.Stderr, "Error installing bridge.py: %v\n", err)
			os.Exit(1)
		}
		if !*jsonOutput {
			fmt.Println("[ok] bridge.py installed")
		}

		// Install launchd plist
		plistContent, err := session.GenerateLaunchdPlist()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating plist: %v\n", err)
			os.Exit(1)
		}

		plistPath, err = session.LaunchdPlistPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting plist path: %v\n", err)
			os.Exit(1)
		}

		homeDir, _ := os.UserHomeDir()
		if err := os.MkdirAll(filepath.Join(homeDir, "Library", "LaunchAgents"), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating LaunchAgents dir: %v\n", err)
			os.Exit(1)
		}

		// Unload existing daemon if any
		_ = exec.Command("launchctl", "unload", plistPath).Run()

		if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing plist: %v\n", err)
			os.Exit(1)
		}

		if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load daemon: %v\n", err)
			fmt.Fprintln(os.Stderr, "You may need to load it manually: launchctl load", plistPath)
		} else if !*jsonOutput {
			fmt.Println("[ok] Bridge daemon loaded")
		}
	}

	// Output summary
	if *jsonOutput {
		data := map[string]any{
			"success":   true,
			"name":      name,
			"profile":   profile,
			"session":   sessionID,
			"existed":   existed,
			"heartbeat": heartbeatEnabled,
			"telegram":  telegramConfigured,
		}
		if plistPath != "" {
			data["daemon"] = plistPath
		}
		output, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(output))
		return
	}

	fmt.Println()
	fmt.Println("Conductor setup complete!")
	fmt.Println()
	fmt.Printf("  Name:      %s\n", name)
	fmt.Printf("  Profile:   %s\n", profile)
	fmt.Printf("  Heartbeat: %v\n", heartbeatEnabled)
	if *description != "" {
		fmt.Printf("  Desc:      %s\n", *description)
	}
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Printf("  agent-deck -p %s session start %s\n", profile, sessionTitle)
	if telegramConfigured {
		fmt.Println()
		fmt.Println("  Test from Telegram: send /status to your bot")
		condDir, _ := session.ConductorDir()
		fmt.Printf("  View bridge logs:   tail -f %s/bridge.log\n", condDir)
	} else {
		fmt.Println()
		fmt.Println("  To add Telegram later: re-run setup after adding [conductor.telegram] to config.toml")
	}
}

// handleConductorTeardown stops conductors and optionally removes directories
func handleConductorTeardown(_ string, args []string) {
	fs := flag.NewFlagSet("conductor teardown", flag.ExitOnError)
	removeAll := fs.Bool("remove", false, "Remove conductor directories and sessions")
	allConductors := fs.Bool("all", false, "Teardown all conductors")
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck conductor teardown <name> [options]")
		fmt.Println("       agent-deck conductor teardown --all [options]")
		fmt.Println()
		fmt.Println("Stop a conductor session and optionally remove its directory.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  <name>    Conductor name to tear down")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	// Extract positional arg before flags
	var name string
	var flagArgs []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
		} else if name == "" {
			name = arg
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}

	if err := fs.Parse(normalizeArgs(fs, flagArgs)); err != nil {
		os.Exit(1)
	}

	if !*allConductors && name == "" {
		fmt.Fprintln(os.Stderr, "Error: conductor name or --all is required")
		fmt.Fprintln(os.Stderr, "Usage: agent-deck conductor teardown <name> or --all")
		os.Exit(1)
	}

	// Auto-migrate before teardown so we can find legacy conductors
	runAutoMigration(*jsonOutput)

	// Determine which conductors to tear down
	var targets []session.ConductorMeta
	if *allConductors {
		var err error
		targets, err = session.ListConductors()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing conductors: %v\n", err)
			os.Exit(1)
		}
		if len(targets) == 0 {
			if *jsonOutput {
				fmt.Println(`{"success": true, "removed": 0}`)
			} else {
				fmt.Println("No conductors found.")
			}
			return
		}
	} else {
		meta, err := session.LoadConductorMeta(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: conductor %q not found: %v\n", name, err)
			os.Exit(1)
		}
		targets = []session.ConductorMeta{*meta}
	}

	// Step 1: Stop launchd daemon (only when tearing down all)
	if *allConductors {
		plistPath, _ := session.LaunchdPlistPath()
		if plistPath != "" {
			if _, err := os.Stat(plistPath); err == nil {
				if !*jsonOutput {
					fmt.Println("Stopping bridge daemon...")
				}
				_ = exec.Command("launchctl", "unload", plistPath).Run()
				_ = os.Remove(plistPath)
				if !*jsonOutput {
					fmt.Println("[ok] Daemon stopped and plist removed")
				}
			}
		}
	}

	// Step 2: Stop and optionally remove each conductor
	var removed []string
	for _, meta := range targets {
		sessionTitle := session.ConductorSessionTitle(meta.Name)
		if !*jsonOutput {
			fmt.Printf("Stopping conductor: %s (profile: %s)\n", meta.Name, meta.Profile)
		}

		// Stop the session
		storage, err := session.NewStorageWithProfile(meta.Profile)
		if err == nil {
			instances, _, err := storage.LoadWithGroups()
			if err == nil {
				for _, inst := range instances {
					if inst.Title == sessionTitle {
						if inst.Exists() {
							_ = inst.Kill()
						}
						if !*jsonOutput {
							fmt.Printf("  [ok] %s stopped\n", sessionTitle)
						}
						break
					}
				}
			}
		}

		// Remove heartbeat timer
		hbPlistPath, err := session.HeartbeatPlistPath(meta.Name)
		if err == nil {
			_ = exec.Command("launchctl", "unload", hbPlistPath).Run()
			_ = session.RemoveHeartbeatPlist(meta.Name)
		}

		// Optionally remove directory and session
		if *removeAll {
			if err := session.TeardownConductor(meta.Name); err != nil {
				if !*jsonOutput {
					fmt.Fprintf(os.Stderr, "  Warning: failed to remove dir for %s: %v\n", meta.Name, err)
				}
			} else if !*jsonOutput {
				fmt.Printf("  [ok] Removed directory for %s\n", meta.Name)
			}

			// Remove session from storage
			if storage != nil {
				instances, groups, err := storage.LoadWithGroups()
				if err == nil {
					var filtered []*session.Instance
					sessionRemoved := false
					for _, inst := range instances {
						if inst.Title == sessionTitle {
							sessionRemoved = true
							continue
						}
						filtered = append(filtered, inst)
					}
					if sessionRemoved {
						groupTree := session.NewGroupTreeWithGroups(filtered, groups)
						_ = storage.SaveWithGroups(filtered, groupTree)
						if !*jsonOutput {
							fmt.Printf("  [ok] Removed session '%s' from %s\n", sessionTitle, meta.Profile)
						}
					}
				}
			}
		}

		removed = append(removed, meta.Name)
	}

	// Clean up shared files if removing all
	if *allConductors && *removeAll {
		condDir, _ := session.ConductorDir()
		if condDir != "" {
			_ = os.Remove(filepath.Join(condDir, "bridge.py"))
			_ = os.Remove(filepath.Join(condDir, "bridge.log"))
			_ = os.Remove(filepath.Join(condDir, "CLAUDE.md"))
			_ = os.Remove(condDir) // Remove dir if empty
		}
	}

	if *jsonOutput {
		output, _ := json.MarshalIndent(map[string]any{
			"success":  true,
			"removed":  *removeAll,
			"teardown": removed,
		}, "", "  ")
		fmt.Println(string(output))
		return
	}

	fmt.Println()
	fmt.Println("Teardown complete.")
	if !*removeAll {
		fmt.Println()
		fmt.Println("Conductor directories were kept. To remove them:")
		if *allConductors {
			fmt.Println("  agent-deck conductor teardown --all --remove")
		} else {
			fmt.Printf("  agent-deck conductor teardown %s --remove\n", name)
		}
	}
}

// handleConductorStatus shows conductor health
func handleConductorStatus(_ string, args []string) {
	fs := flag.NewFlagSet("conductor status", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck conductor status [name] [options]")
		fmt.Println()
		fmt.Println("Show conductor health status. If name is given, show that conductor only.")
		fmt.Println("Otherwise show all conductors.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	// Extract positional arg before flags
	var name string
	var flagArgs []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
		} else if name == "" {
			name = arg
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}

	if err := fs.Parse(normalizeArgs(fs, flagArgs)); err != nil {
		os.Exit(1)
	}

	settings := session.GetConductorSettings()
	if !settings.Enabled {
		if *jsonOutput {
			fmt.Println(`{"enabled": false}`)
		} else {
			fmt.Println("Conductor is not enabled.")
			fmt.Println("Run 'agent-deck conductor setup <name>' to configure it.")
		}
		return
	}

	// Auto-migrate before status check
	runAutoMigration(*jsonOutput)

	// Get conductors to display
	var conductors []session.ConductorMeta
	if name != "" {
		meta, err := session.LoadConductorMeta(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: conductor %q not found: %v\n", name, err)
			os.Exit(1)
		}
		conductors = []session.ConductorMeta{*meta}
	} else {
		var err error
		conductors, err = session.ListConductors()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing conductors: %v\n", err)
			os.Exit(1)
		}
	}

	type conductorStatus struct {
		Name        string `json:"name"`
		Profile     string `json:"profile"`
		DirExists   bool   `json:"dir_exists"`
		SessionID   string `json:"session_id,omitempty"`
		SessionDone bool   `json:"session_registered"`
		Running     bool   `json:"running"`
		Heartbeat   bool   `json:"heartbeat"`
		Description string `json:"description,omitempty"`
	}
	var statuses []conductorStatus

	for _, meta := range conductors {
		cs := conductorStatus{
			Name:        meta.Name,
			Profile:     meta.Profile,
			DirExists:   session.IsConductorSetup(meta.Name),
			Heartbeat:   meta.HeartbeatEnabled,
			Description: meta.Description,
		}

		// Check session
		sessionTitle := session.ConductorSessionTitle(meta.Name)
		storage, err := session.NewStorageWithProfile(meta.Profile)
		if err == nil {
			instances, _, err := storage.LoadWithGroups()
			if err == nil {
				for _, inst := range instances {
					if inst.Title == sessionTitle {
						cs.SessionID = inst.ID
						cs.SessionDone = true
						_ = inst.UpdateStatus()
						cs.Running = inst.Status == session.StatusRunning || inst.Status == session.StatusWaiting
						break
					}
				}
			}
		}

		statuses = append(statuses, cs)
	}

	// Check bridge daemon
	daemonRunning := false
	if out, err := exec.Command("launchctl", "list", session.LaunchdPlistName).Output(); err == nil {
		daemonRunning = len(out) > 0
	}

	if *jsonOutput {
		output, _ := json.MarshalIndent(map[string]any{
			"enabled":        true,
			"conductors":     statuses,
			"daemon_running": daemonRunning,
		}, "", "  ")
		fmt.Println(string(output))
		return
	}

	// Human-readable output
	fmt.Println("Conductor Status")
	fmt.Println("================")
	fmt.Println()

	if daemonRunning {
		fmt.Println("Bridge daemon: RUNNING")
	} else {
		fmt.Println("Bridge daemon: STOPPED")
	}
	fmt.Println()

	if len(statuses) == 0 {
		fmt.Println("  No conductors configured.")
		fmt.Println("  Run 'agent-deck conductor setup <name>' to create one.")
	}

	for _, cs := range statuses {
		var statusIcon, statusText string

		switch {
		case !cs.DirExists:
			statusIcon = "!"
			statusText = "not setup"
		case !cs.SessionDone:
			statusIcon = "!"
			statusText = "no session"
		case cs.Running:
			statusIcon = "●"
			statusText = "running"
		default:
			statusIcon = "○"
			statusText = "stopped"
		}

		hb := "on"
		if !cs.Heartbeat {
			hb = "off"
		}

		desc := ""
		if cs.Description != "" {
			desc = fmt.Sprintf("  %q", cs.Description)
		}

		fmt.Printf("  %s %s [%s] heartbeat:%s  (%s)%s\n", statusIcon, cs.Name, cs.Profile, hb, statusText, desc)
	}
	fmt.Println()

	// Hints
	if !daemonRunning {
		plistPath, _ := session.LaunchdPlistPath()
		if _, err := os.Stat(plistPath); err == nil {
			fmt.Println("Tip: Start daemon with: launchctl load", plistPath)
		} else {
			fmt.Println("Tip: Run 'agent-deck conductor setup <name>' to install the daemon")
		}
	}
}

// handleConductorList lists all conductors
func handleConductorList(profile string, args []string) {
	fs := flag.NewFlagSet("conductor list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	filterProfile := fs.String("profile", "", "Filter by profile")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck conductor list [options]")
		fmt.Println()
		fmt.Println("List all configured conductors.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	// Auto-migrate
	runAutoMigration(*jsonOutput)

	var conductors []session.ConductorMeta
	var err error

	targetProfile := *filterProfile

	if targetProfile != "" {
		conductors, err = session.ListConductorsForProfile(targetProfile)
	} else {
		conductors, err = session.ListConductors()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing conductors: %v\n", err)
		os.Exit(1)
	}

	if *jsonOutput {
		output, _ := json.MarshalIndent(map[string]any{
			"conductors": conductors,
		}, "", "  ")
		fmt.Println(string(output))
		return
	}

	if len(conductors) == 0 {
		fmt.Println("No conductors configured.")
		fmt.Println("Run 'agent-deck conductor setup <name>' to create one.")
		return
	}

	fmt.Println("Conductors:")
	fmt.Println()

	for _, meta := range conductors {
		// Check session status
		var statusText string
		sessionTitle := session.ConductorSessionTitle(meta.Name)
		storage, err := session.NewStorageWithProfile(meta.Profile)
		if err == nil {
			instances, _, err := storage.LoadWithGroups()
			if err == nil {
				found := false
				for _, inst := range instances {
					if inst.Title == sessionTitle {
						found = true
						_ = inst.UpdateStatus()
						if inst.Status == session.StatusRunning || inst.Status == session.StatusWaiting {
							statusText = "running"
						} else {
							statusText = "stopped"
						}
						break
					}
				}
				if !found {
					statusText = "no session"
				}
			}
		}

		hb := "on"
		if !meta.HeartbeatEnabled {
			hb = "off"
		}

		desc := ""
		if meta.Description != "" {
			desc = fmt.Sprintf("  %q", meta.Description)
		}

		fmt.Printf("  %-12s [%s]  heartbeat:%-3s  %-10s%s\n", meta.Name, meta.Profile, hb, statusText, desc)
	}
	fmt.Println()
}

// installPythonDeps installs Python dependencies for the bridge
func installPythonDeps() {
	// Try pip install --user first
	cmd := exec.Command("python3", "-m", "pip", "install", "--quiet", "--user", "aiogram", "toml")
	if err := cmd.Run(); err != nil {
		// Try without --user
		cmd = exec.Command("python3", "-m", "pip", "install", "--quiet", "aiogram", "toml")
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "Warning: could not install Python dependencies (aiogram, toml)")
			fmt.Fprintln(os.Stderr, "Install manually: pip3 install aiogram toml")
		}
	}
}

// printConductorHelp prints the conductor subcommand help
func printConductorHelp() {
	fmt.Println("Usage: agent-deck [-p profile] conductor <command>")
	fmt.Println()
	fmt.Println("Manage named conductor sessions for meta-agent orchestration.")
	fmt.Println("Multiple conductors can exist per profile.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  setup <name>     Set up a named conductor (directory, session, bridge)")
	fmt.Println("  teardown <name>  Stop and optionally remove a conductor (or --all)")
	fmt.Println("  status [name]    Show conductor health (all or specific)")
	fmt.Println("  list             List all configured conductors")
	fmt.Println("  help             Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck -p work conductor setup ryan --description \"Ryan project\"")
	fmt.Println("  agent-deck -p work conductor setup infra --no-heartbeat")
	fmt.Println("  agent-deck conductor list")
	fmt.Println("  agent-deck conductor status")
	fmt.Println("  agent-deck conductor teardown infra --remove")
	fmt.Println("  agent-deck conductor teardown --all --remove")
}
