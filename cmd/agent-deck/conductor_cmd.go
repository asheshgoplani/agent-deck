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
	migratedLegacy, err := session.MigrateLegacyConductors()
	if err != nil && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Warning: migration check failed: %v\n", err)
	}

	migratedPolicy, err := session.MigrateConductorPolicySplit()
	if err != nil && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Warning: policy migration check failed: %v\n", err)
	}

	if !jsonOutput {
		for _, name := range migratedLegacy {
			fmt.Printf("  [migrated] Legacy conductor: %s\n", name)
		}
		for _, name := range migratedPolicy {
			fmt.Printf("  [migrated] Updated policy split: %s\n", name)
		}
	}
}

// parseConductorSetupArgs parses setup flags and returns the conductor name and any extra positional args.
func parseConductorSetupArgs(fs *flag.FlagSet, args []string) (string, []string, error) {
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return "", nil, err
	}
	remaining := fs.Args()
	if len(remaining) == 0 {
		return "", nil, nil
	}
	return remaining[0], remaining[1:], nil
}

// handleConductorSetup sets up a named conductor with directories, sessions, and optionally the Telegram bridge
func handleConductorSetup(profile string, args []string) {
	fs := flag.NewFlagSet("conductor setup", flag.ExitOnError)
	description := fs.String("description", "", "Description for this conductor")
	heartbeat := fs.Bool("heartbeat", false, "Enable heartbeat for this conductor (default)")
	noHeartbeat := fs.Bool("no-heartbeat", false, "Disable heartbeat for this conductor")
	claudeMD := fs.String("claude-md", "", "Custom CLAUDE.md for this conductor (e.g., ~/docs/conductor-ryan.md)")
	policyMD := fs.String("policy-md", "", "Custom POLICY.md for this conductor (e.g., ~/docs/my-policy.md)")
	sharedClaudeMD := fs.String("shared-claude-md", "", "Custom path for shared CLAUDE.md (e.g., ~/docs/conductor-shared.md)")
	sharedPolicyMD := fs.String("shared-policy-md", "", "Custom path for shared POLICY.md (e.g., ~/docs/conductor-policy.md)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")

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
		fmt.Println("  -description string")
		fmt.Println("        Description for this conductor")
		fmt.Println("  -heartbeat")
		fmt.Println("        Enable heartbeat for this conductor (default)")
		fmt.Println("  -no-heartbeat")
		fmt.Println("        Disable heartbeat for this conductor")
		fmt.Println()
		fmt.Println("Conductor-specific files:")
		fmt.Println("  -claude-md string")
		fmt.Println("        Custom CLAUDE.md for this conductor (e.g., ~/docs/conductor-ryan.md)")
		fmt.Println("  -policy-md string")
		fmt.Println("        Custom POLICY.md for this conductor (e.g., ~/docs/my-policy.md)")
		fmt.Println()
		fmt.Println("Shared files (all conductors):")
		fmt.Println("  -shared-claude-md string")
		fmt.Println("        Custom path for shared CLAUDE.md (e.g., ~/docs/conductor-shared.md)")
		fmt.Println("  -shared-policy-md string")
		fmt.Println("        Custom path for shared POLICY.md (e.g., ~/docs/conductor-policy.md)")
		fmt.Println()
		fmt.Println("Output:")
		fmt.Println("  -json")
		fmt.Println("        Output as JSON")
	}

	name, extras, err := parseConductorSetupArgs(fs, args)
	if err != nil {
		os.Exit(1)
	}

	if name == "" {
		fmt.Fprintln(os.Stderr, "Error: conductor name is required")
		fmt.Fprintln(os.Stderr, "Usage: agent-deck [-p profile] conductor setup <name>")
		os.Exit(1)
	}
	if len(extras) > 0 {
		fmt.Fprintf(os.Stderr, "Error: unexpected arguments: %s\n", strings.Join(extras, " "))
		os.Exit(1)
	}

	if err := session.ValidateConductorName(name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	resolvedProfile := session.GetEffectiveProfile(profile)

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
	slackConfigured := settings.Slack.BotToken != ""

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

		// Ask about Slack
		fmt.Print("Connect Slack bot for channel-based control? (y/N): ")
		slackAnswer, _ := reader.ReadString('\n')
		slackAnswer = strings.TrimSpace(strings.ToLower(slackAnswer))

		var slack session.SlackSettings
		if slackAnswer == "y" || slackAnswer == "yes" {
			fmt.Println()
			fmt.Println("  1. Create a Slack app at https://api.slack.com/apps")
			fmt.Println("  2. Enable Socket Mode -> generate an app-level token (xapp-...)")
			fmt.Println("  3. Add bot scopes: chat:write, channels:history, channels:read, app_mentions:read")
			fmt.Println("  4. Enable Event Subscriptions -> subscribe to bot events: message.channels, app_mention")
			fmt.Println("  5. Install the app to your workspace")
			fmt.Println("  6. Invite the bot to your channel (/invite @botname)")
			fmt.Println()

			fmt.Print("Slack bot token (xoxb-...): ")
			botToken, _ := reader.ReadString('\n')
			botToken = strings.TrimSpace(botToken)
			if botToken == "" {
				fmt.Fprintln(os.Stderr, "Error: bot token is required")
				os.Exit(1)
			}

			fmt.Print("Slack app token (xapp-...): ")
			appToken, _ := reader.ReadString('\n')
			appToken = strings.TrimSpace(appToken)
			if appToken == "" {
				fmt.Fprintln(os.Stderr, "Error: app token is required")
				os.Exit(1)
			}

			fmt.Print("Slack channel ID (C01234...): ")
			channelID, _ := reader.ReadString('\n')
			channelID = strings.TrimSpace(channelID)
			if channelID == "" {
				fmt.Fprintln(os.Stderr, "Error: channel ID is required")
				os.Exit(1)
			}

			slack = session.SlackSettings{BotToken: botToken, AppToken: appToken, ChannelID: channelID}
			slackConfigured = true
		}

		// Update config (no longer stores profiles list, conductors are on disk)
		settings = session.ConductorSettings{
			Enabled:           true,
			HeartbeatInterval: 15,
			Telegram:          telegram,
			Slack:             slack,
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
	if err := session.InstallSharedClaudeMD(*sharedClaudeMD); err != nil {
		fmt.Fprintf(os.Stderr, "Error installing shared CLAUDE.md: %v\n", err)
		os.Exit(1)
	}
	if !*jsonOutput {
		fmt.Println("[ok] Shared CLAUDE.md installed/updated")
	}

	// Step 3b: Install/update shared POLICY.md
	if err := session.InstallPolicyMD(*sharedPolicyMD); err != nil {
		fmt.Fprintf(os.Stderr, "Error installing POLICY.md: %v\n", err)
		os.Exit(1)
	}
	if !*jsonOutput {
		fmt.Println("[ok] Shared POLICY.md installed/updated")
	}

	// Step 4: Set up the named conductor
	if !*jsonOutput {
		fmt.Printf("\nSetting up conductor: %s (profile: %s)\n", name, resolvedProfile)
	}

	if err := session.SetupConductor(name, resolvedProfile, heartbeatEnabled, *description, *claudeMD, *policyMD); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up conductor %s: %v\n", name, err)
		os.Exit(1)
	}
	if !*jsonOutput {
		fmt.Printf("  [ok] Directory, CLAUDE.md, and meta.json created\n")
	}

	// Step 5: Register session in the profile's storage
	sessionTitle := session.ConductorSessionTitle(name)
	storage, err := session.NewStorageWithProfile(resolvedProfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading storage for %s: %v\n", resolvedProfile, err)
		os.Exit(1)
	}

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading sessions for %s: %v\n", resolvedProfile, err)
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
		fmt.Fprintf(os.Stderr, "Error saving session for %s: %v\n", resolvedProfile, err)
		os.Exit(1)
	}

	// Step 6: Install heartbeat timer (if heartbeat enabled)
	if heartbeatEnabled {
		interval := settings.GetHeartbeatInterval()
		if err := session.InstallHeartbeatScript(name, resolvedProfile); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to install heartbeat script: %v\n", err)
		} else if err := session.InstallHeartbeatDaemon(name, resolvedProfile, interval); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to install heartbeat daemon: %v\n", err)
		} else if !*jsonOutput {
			fmt.Printf("  [ok] Heartbeat timer installed (every %d min)\n", interval)
		}
	}

	// Step 7: Install bridge (if Telegram or Slack is configured)
	var plistPath string
	if telegramConfigured || slackConfigured {
		if !*jsonOutput {
			fmt.Println()
			fmt.Println("Installing bridge...")
		}

		installPythonDeps()

		if err := session.InstallBridgeScript(); err != nil {
			fmt.Fprintf(os.Stderr, "Error installing bridge.py: %v\n", err)
			os.Exit(1)
		}
		if !*jsonOutput {
			fmt.Println("[ok] bridge.py installed")
		}

		// Install daemon (platform-aware: launchd on macOS, systemd on Linux)
		daemonPath, err := session.InstallBridgeDaemon()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to install bridge daemon: %v\n", err)
			condDir, _ := session.ConductorDir()
			fmt.Fprintf(os.Stderr, "Run manually: python3 %s/bridge.py\n", condDir)
		} else {
			plistPath = daemonPath
			if !*jsonOutput {
				fmt.Println("[ok] Bridge daemon loaded")
			}
		}
	}

	// Output summary
	if *jsonOutput {
		data := map[string]any{
			"success":   true,
			"name":      name,
			"profile":   resolvedProfile,
			"session":   sessionID,
			"existed":   existed,
			"heartbeat": heartbeatEnabled,
			"telegram":  telegramConfigured,
			"slack":     slackConfigured,
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
	fmt.Printf("  Profile:   %s\n", resolvedProfile)
	fmt.Printf("  Heartbeat: %v\n", heartbeatEnabled)
	if *description != "" {
		fmt.Printf("  Desc:      %s\n", *description)
	}
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Printf("  agent-deck -p %s session start %s\n", resolvedProfile, sessionTitle)
	condDir, _ := session.ConductorDir()
	if telegramConfigured || slackConfigured {
		fmt.Println()
		if telegramConfigured {
			fmt.Println("  Test from Telegram: send /status to your bot")
		}
		if slackConfigured {
			fmt.Println("  Test from Slack: post a message in the configured channel")
		}
		fmt.Printf("  View bridge logs:   tail -f %s/bridge.log\n", condDir)
	} else {
		fmt.Println()
		fmt.Println("  To add Telegram later: re-run setup after adding [conductor.telegram] to config.toml")
		fmt.Println("  To add Slack later: re-run setup after adding [conductor.slack] to config.toml")
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

	// Step 1: Stop bridge daemon (only when tearing down all)
	if *allConductors {
		if session.IsBridgeDaemonRunning() {
			if !*jsonOutput {
				fmt.Println("Stopping bridge daemon...")
			}
			_ = session.UninstallBridgeDaemon()
			if !*jsonOutput {
				fmt.Println("[ok] Daemon stopped and removed")
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
		_ = session.UninstallHeartbeatDaemon(meta.Name)

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
			_ = os.Remove(filepath.Join(condDir, "POLICY.md"))
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
						cs.Running = inst.Status == session.StatusRunning || inst.Status == session.StatusWaiting || inst.Status == session.StatusIdle
						break
					}
				}
			}
		}

		statuses = append(statuses, cs)
	}

	// Check bridge daemon
	daemonRunning := session.IsBridgeDaemonRunning()

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
		fmt.Printf("Tip: %s\n", session.BridgeDaemonHint())
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
						if inst.Status == session.StatusRunning || inst.Status == session.StatusWaiting || inst.Status == session.StatusIdle {
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
	config, err := session.LoadUserConfig()
	var packages []string
	packages = append(packages, "toml")

	if err == nil && config != nil {
		if config.Conductor.Telegram.Token != "" {
			packages = append(packages, "aiogram")
		}
		if config.Conductor.Slack.BotToken != "" {
			packages = append(packages, "slack-bolt", "slack-sdk", "aiohttp")
		}
	}

	// Fallback: if no specific integration detected, install all
	if len(packages) == 1 {
		packages = append(packages, "aiogram", "slack-bolt", "slack-sdk", "aiohttp")
	}

	args := append([]string{"-m", "pip", "install", "--quiet", "--user"}, packages...)
	if err := exec.Command("python3", args...).Run(); err != nil {
		// Try without --user (e.g. virtualenvs, containers)
		args = append([]string{"-m", "pip", "install", "--quiet"}, packages...)
		if err := exec.Command("python3", args...).Run(); err != nil {
			// pip failed (e.g. PEP 668 externally-managed env) — rely on system packages
			fmt.Fprintf(os.Stderr, "Note: pip install failed; using system-installed packages.\n")
			fmt.Fprintf(os.Stderr, "If the bridge fails to start, install manually: pip3 install %s\n", strings.Join(packages, " "))
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
