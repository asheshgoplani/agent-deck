package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

func handleRemote(profile string, args []string) {
	if len(args) == 0 {
		printRemoteUsage()
		return
	}

	switch args[0] {
	case "add":
		handleRemoteAdd(args[1:])
	case "remove", "rm":
		handleRemoteRemove(args[1:])
	case "list", "ls":
		handleRemoteList(args[1:])
	case "sessions":
		handleRemoteSessions(args[1:])
	case "attach":
		handleRemoteAttach(args[1:])
	case "rename":
		handleRemoteRename(args[1:])
	case "update":
		handleRemoteUpdate(args[1:])
	case "forward", "fwd":
		handleRemoteForward(args[1:])
	default:
		fmt.Printf("Unknown remote command: %s\n", args[0])
		printRemoteUsage()
		os.Exit(1)
	}
}

func printRemoteUsage() {
	fmt.Println("Usage: agent-deck remote <command> [options]")
	fmt.Println()
	fmt.Println("Manage remote agent-deck instances.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add <name> <user@host>    Add a remote agent-deck instance")
	fmt.Println("  remove <name>             Remove a remote")
	fmt.Println("  list                      List configured remotes")
	fmt.Println("  sessions [name]           Fetch sessions from remote(s)")
	fmt.Println("  attach <name> <session>   Attach to a remote session")
	fmt.Println("  rename <name> <session> <new-title>  Rename a remote session")
	fmt.Println("  update [name]             Install/update agent-deck on remote(s)")
	fmt.Println("  forward <sub> <name> ...  Manage port forwarding (add/remove/list)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck remote add dev user@dev-box")
	fmt.Println("  agent-deck remote add prod user@prod-server --agent-deck-path /usr/local/bin/agent-deck")
	fmt.Println("  agent-deck remote add dev user@dev-box --forward L:8444:localhost:8444")
	fmt.Println("  agent-deck remote list")
	fmt.Println("  agent-deck remote sessions dev")
	fmt.Println("  agent-deck remote attach dev my-session")
	fmt.Println("  agent-deck remote rename dev my-session new-name")
	fmt.Println("  agent-deck remote update          # Update all remotes")
	fmt.Println("  agent-deck remote update dev      # Update specific remote")
}

func isValidRemoteName(name string) bool {
	return name != "" && !strings.ContainsAny(name, " /\\.:")
}

// portForwardFlags implements flag.Value for repeatable --forward flags.
type portForwardFlags []string

func (p *portForwardFlags) String() string { return strings.Join(*p, ",") }
func (p *portForwardFlags) Set(val string) error {
	*p = append(*p, val)
	return nil
}

func handleRemoteAdd(args []string) {
	fs := flag.NewFlagSet("remote add", flag.ExitOnError)
	agentDeckPath := fs.String("agent-deck-path", "", "Path to agent-deck on the remote (default: agent-deck)")
	remoteProfile := fs.String("profile", "", "Remote profile to use (default: default)")
	var forwards portForwardFlags
	fs.Var(&forwards, "forward", "Port forward rule (e.g., L:8444:localhost:8444). Can be repeated.")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck remote add <name> <user@host> [options]")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	// Reorder: move flags before positional args so Go's flag package sees them
	reordered := reorderRemoteArgs(fs, args)
	if err := fs.Parse(reordered); err != nil {
		os.Exit(1)
	}

	remaining := fs.Args()
	if len(remaining) < 2 {
		fmt.Println("Error: requires <name> and <user@host> arguments")
		fs.Usage()
		os.Exit(1)
	}

	name := remaining[0]
	host := remaining[1]

	// Validate name (no spaces, slashes, dots, or colons).
	// Colon is reserved by the UI's internal remote session identifier format.
	if !isValidRemoteName(name) {
		fmt.Println("Error: remote name must not contain spaces, slashes, dots, or colons")
		os.Exit(1)
	}

	// Parse port forward flags
	var portForwards []session.PortForward
	for _, f := range forwards {
		pf, err := session.ParsePortForwardFlag(f)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		portForwards = append(portForwards, pf)
	}

	// Load existing config
	config, err := session.LoadUserConfig()
	if err != nil {
		config = &session.UserConfig{}
	}

	if config.Remotes == nil {
		config.Remotes = make(map[string]session.RemoteConfig)
	}

	if _, exists := config.Remotes[name]; exists {
		fmt.Printf("Error: remote '%s' already exists (use 'agent-deck remote remove %s' first)\n", name, name)
		os.Exit(1)
	}

	rc := session.RemoteConfig{
		Host:         host,
		PortForwards: portForwards,
	}
	if *agentDeckPath != "" {
		rc.AgentDeckPath = *agentDeckPath
	}
	if *remoteProfile != "" {
		rc.Profile = *remoteProfile
	}

	config.Remotes[name] = rc

	if err := session.SaveUserConfig(config); err != nil {
		fmt.Printf("Error: failed to save config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Added remote '%s' (%s)\n", name, host)

	// Check if agent-deck is available on the remote
	runner := session.NewSSHRunner(name, rc)
	ctx := context.Background()
	remoteVersion, found := runner.CheckBinary(ctx)
	if found {
		fmt.Printf("  Remote agent-deck: v%s\n", remoteVersion)
		if update.CompareVersions(remoteVersion, Version) < 0 {
			fmt.Printf("  Note: remote is older than local (v%s). Run 'agent-deck remote update %s' to update.\n", Version, name)
		}
	} else {
		fmt.Printf("  agent-deck not found on remote at '%s'\n", rc.GetAgentDeckPath())
		fmt.Printf("  Installing v%s...\n", Version)
		if err := installOnRemote(runner, ctx); err != nil {
			fmt.Printf("  Warning: auto-install failed: %v\n", err)
			fmt.Printf("  You can install manually or run: agent-deck remote update %s\n", name)
		} else {
			fmt.Printf("  ✓ Installed agent-deck v%s on remote '%s'\n", Version, name)
		}
	}
}

func handleRemoteRemove(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: agent-deck remote remove <name>")
		os.Exit(1)
	}

	name := args[0]

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	if config.Remotes == nil {
		fmt.Printf("Error: remote '%s' not found\n", name)
		os.Exit(1)
	}

	if _, exists := config.Remotes[name]; !exists {
		fmt.Printf("Error: remote '%s' not found\n", name)
		os.Exit(1)
	}

	delete(config.Remotes, name)

	// Remove empty map to keep config clean
	if len(config.Remotes) == 0 {
		config.Remotes = nil
	}

	if err := session.SaveUserConfig(config); err != nil {
		fmt.Printf("Error: failed to save config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Removed remote '%s'\n", name)
}

func handleRemoteList(args []string) {
	fs := flag.NewFlagSet("remote list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	_ = fs.Parse(args)

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	if len(config.Remotes) == 0 {
		fmt.Println("No remotes configured.")
		fmt.Println("\nAdd one with: agent-deck remote add <name> <user@host>")
		return
	}

	if *jsonOutput {
		type remoteJSON struct {
			Name          string                `json:"name"`
			Host          string                `json:"host"`
			AgentDeckPath string                `json:"agent_deck_path"`
			Profile       string                `json:"profile"`
			PortForwards  []session.PortForward `json:"port_forwards,omitempty"`
		}

		var remotes []remoteJSON
		for name, rc := range config.Remotes {
			remotes = append(remotes, remoteJSON{
				Name:          name,
				Host:          rc.Host,
				AgentDeckPath: rc.GetAgentDeckPath(),
				Profile:       rc.GetProfile(),
				PortForwards:  rc.PortForwards,
			})
		}

		output, err := json.MarshalIndent(remotes, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(output))
		return
	}

	fmt.Printf("%-15s %-30s %-20s %-10s %s\n", "NAME", "HOST", "PATH", "FORWARDS", "PROFILE")
	fmt.Println(strings.Repeat("-", 80))
	for name, rc := range config.Remotes {
		fwdSummary := formatForwardSummary(rc.PortForwards)
		fmt.Printf("%-15s %-30s %-20s %-10s %s\n", name, rc.Host, rc.GetAgentDeckPath(), fwdSummary, rc.GetProfile())
	}
	fmt.Printf("\nTotal: %d remotes\n", len(config.Remotes))
}

func handleRemoteSessions(args []string) {
	fs := flag.NewFlagSet("remote sessions", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	_ = fs.Parse(args)

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	if len(config.Remotes) == 0 {
		fmt.Println("No remotes configured.")
		return
	}

	// Filter to specific remote if name provided
	remoteName := ""
	if len(fs.Args()) > 0 {
		remoteName = fs.Args()[0]
	}

	ctx := context.Background()
	var allSessions []session.RemoteSessionInfo

	for name, rc := range config.Remotes {
		if remoteName != "" && name != remoteName {
			continue
		}

		runner := session.NewSSHRunner(name, rc)
		sessions, err := runner.FetchSessions(ctx)
		if err != nil {
			if !*jsonOutput {
				fmt.Printf("  [%s] Error: %v\n", name, err)
			}
			continue
		}

		for i := range sessions {
			sessions[i].RemoteName = name
		}
		allSessions = append(allSessions, sessions...)

		if !*jsonOutput {
			fmt.Printf("\n═══ Remote: %s (%s) ═══\n\n", name, rc.Host)
			if len(sessions) == 0 {
				fmt.Println("  No sessions found.")
			} else {
				fmt.Printf("  %-20s %-15s %-10s %s\n", "TITLE", "TOOL", "STATUS", "ID")
				fmt.Printf("  %s\n", strings.Repeat("-", 60))
				for _, s := range sessions {
					title := s.Title
					if len(title) > 20 {
						title = title[:17] + "..."
					}
					id := s.ID
					if len(id) > 8 {
						id = id[:8]
					}
					fmt.Printf("  %-20s %-15s %-10s %s\n", title, s.Tool, s.Status, id)
				}
			}
		}
	}

	if remoteName != "" {
		if _, exists := config.Remotes[remoteName]; !exists {
			fmt.Printf("Error: remote '%s' not found\n", remoteName)
			os.Exit(1)
		}
	}

	if *jsonOutput {
		output, err := json.MarshalIndent(allSessions, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(output))
	}
}

func handleRemoteAttach(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: agent-deck remote attach <remote-name> <session-title-or-id>")
		os.Exit(1)
	}

	remoteName := args[0]
	sessionRef := args[1]

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	if config.Remotes == nil {
		fmt.Printf("Error: remote '%s' not found\n", remoteName)
		os.Exit(1)
	}

	rc, exists := config.Remotes[remoteName]
	if !exists {
		fmt.Printf("Error: remote '%s' not found\n", remoteName)
		os.Exit(1)
	}

	// Try to resolve session reference (could be title or ID)
	runner := session.NewSSHRunner(remoteName, rc)

	ctx := context.Background()
	sessions, err := runner.FetchSessions(ctx)
	if err != nil {
		fmt.Printf("Error: failed to fetch remote sessions: %v\n", err)
		os.Exit(1)
	}

	// Find matching session by title or ID prefix
	var matchID string
	for _, s := range sessions {
		if s.Title == sessionRef || strings.HasPrefix(s.ID, sessionRef) {
			matchID = s.ID
			break
		}
	}

	if matchID == "" {
		fmt.Printf("Error: session '%s' not found on remote '%s'\n", sessionRef, remoteName)
		os.Exit(1)
	}

	if err := runner.Attach(matchID); err != nil {
		fmt.Printf("Error: failed to attach: %v\n", err)
		os.Exit(1)
	}
}

func handleRemoteRename(args []string) {
	if len(args) < 3 {
		fmt.Println("Usage: agent-deck remote rename <remote-name> <session-title-or-id> <new-title>")
		os.Exit(1)
	}

	remoteName := args[0]
	sessionRef := args[1]
	newTitle := strings.Join(args[2:], " ")

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	if config.Remotes == nil {
		fmt.Printf("Error: remote '%s' not found\n", remoteName)
		os.Exit(1)
	}

	rc, exists := config.Remotes[remoteName]
	if !exists {
		fmt.Printf("Error: remote '%s' not found\n", remoteName)
		os.Exit(1)
	}

	runner := session.NewSSHRunner(remoteName, rc)
	ctx := context.Background()

	// Resolve session reference
	sessions, err := runner.FetchSessions(ctx)
	if err != nil {
		fmt.Printf("Error: failed to fetch remote sessions: %v\n", err)
		os.Exit(1)
	}

	var matchID, oldTitle string
	for _, s := range sessions {
		if s.Title == sessionRef || strings.HasPrefix(s.ID, sessionRef) {
			matchID = s.ID
			oldTitle = s.Title
			break
		}
	}

	if matchID == "" {
		fmt.Printf("Error: session '%s' not found on remote '%s'\n", sessionRef, remoteName)
		os.Exit(1)
	}

	_, err = runner.RunCommand(ctx, "rename", matchID, newTitle)
	if err != nil {
		fmt.Printf("Error: failed to rename session: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Renamed '%s' → '%s' on remote '%s'\n", oldTitle, newTitle, remoteName)
}

func handleRemoteUpdate(args []string) {
	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	if len(config.Remotes) == 0 {
		fmt.Println("No remotes configured.")
		return
	}

	// Filter to specific remote if name provided
	remoteName := ""
	if len(args) > 0 {
		remoteName = args[0]
	}

	ctx := context.Background()

	for name, rc := range config.Remotes {
		if remoteName != "" && name != remoteName {
			continue
		}

		fmt.Printf("\n═══ Remote: %s (%s) ═══\n", name, rc.Host)

		runner := session.NewSSHRunner(name, rc)

		// Check current version
		remoteVersion, found := runner.CheckBinary(ctx)
		if found {
			fmt.Printf("  Current version: v%s\n", remoteVersion)
			if update.CompareVersions(remoteVersion, Version) >= 0 {
				fmt.Printf("  ✓ Up to date (local: v%s)\n", Version)
				continue
			}
			fmt.Printf("  Updating to v%s...\n", Version)
		} else {
			fmt.Printf("  agent-deck not found, installing v%s...\n", Version)
		}

		if err := installOnRemote(runner, ctx); err != nil {
			fmt.Printf("  ✗ Failed: %v\n", err)
		} else {
			fmt.Printf("  ✓ Installed v%s\n", Version)
		}
	}

	if remoteName != "" {
		if _, exists := config.Remotes[remoteName]; !exists {
			fmt.Printf("\nError: remote '%s' not found\n", remoteName)
			os.Exit(1)
		}
	}
}

// updateRemotesAfterLocalUpdate prompts the user to update remotes after a successful local update.
func updateRemotesAfterLocalUpdate(newVersion string) {
	config, err := session.LoadUserConfig()
	if err != nil || config == nil || len(config.Remotes) == 0 {
		return
	}

	fmt.Printf("\nYou have %d remote(s) configured. Update them too? [Y/n] ", len(config.Remotes))
	reader := bufio.NewReader(os.Stdin)
	response, readErr := reader.ReadString('\n')
	if !shouldProceedWithRemoteUpdate(response, readErr) {
		return
	}

	ctx := context.Background()
	for name, rc := range config.Remotes {
		fmt.Printf("\n═══ Remote: %s (%s) ═══\n", name, rc.Host)
		runner := session.NewSSHRunner(name, rc)

		remoteVersion, found := runner.CheckBinary(ctx)
		if found {
			fmt.Printf("  Current version: v%s\n", remoteVersion)
			if update.CompareVersions(remoteVersion, newVersion) >= 0 {
				fmt.Printf("  ✓ Up to date\n")
				continue
			}
			fmt.Printf("  Updating to v%s...\n", newVersion)
		} else {
			fmt.Printf("  agent-deck not found, installing v%s...\n", newVersion)
		}

		if err := installOnRemote(runner, ctx); err != nil {
			fmt.Printf("  ✗ Failed: %v\n", err)
		} else {
			fmt.Printf("  ✓ Installed v%s\n", newVersion)
		}
	}
}

func shouldProceedWithRemoteUpdate(response string, readErr error) bool {
	normalized := strings.TrimSpace(strings.ToLower(response))

	// If stdin is not interactive and no input was provided, fail closed.
	if errors.Is(readErr, io.EOF) && normalized == "" {
		return false
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false
	}

	if normalized == "" || normalized == "y" || normalized == "yes" {
		return true
	}
	return false
}

// installOnRemote detects the remote platform and deploys the matching agent-deck binary.
// It first tries to find a matching release on GitHub. If no release is available for the
// local version, it falls back to downloading the latest release for the remote platform.
func installOnRemote(runner *session.SSHRunner, ctx context.Context) error {
	// Detect remote platform
	goos, goarch, err := runner.DetectPlatform(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("  Platform: %s/%s\n", goos, goarch)

	// Fetch latest release from GitHub
	release, err := update.FetchLatestRelease()
	if err != nil {
		return fmt.Errorf("failed to fetch release info: %w", err)
	}

	// Get download URL for the remote's platform
	downloadURL := update.GetAssetURLForPlatform(release, goos, goarch)
	if downloadURL == "" {
		return fmt.Errorf("no release binary available for %s/%s", goos, goarch)
	}

	// Download and extract the binary
	fmt.Printf("  Downloading %s/%s binary...\n", goos, goarch)
	binaryData, err := update.DownloadAndExtractBinary(downloadURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Deploy to remote
	fmt.Printf("  Deploying to %s...\n", runner.Host)
	if err := runner.DeployBinary(ctx, binaryData); err != nil {
		return fmt.Errorf("deploy failed: %w", err)
	}

	return nil
}

// formatForwardSummary returns a compact summary like "2L 1R" for port forwards.
func formatForwardSummary(forwards []session.PortForward) string {
	if len(forwards) == 0 {
		return "-"
	}
	counts := map[string]int{}
	for _, pf := range forwards {
		counts[pf.Direction]++
	}
	var parts []string
	for _, dir := range []string{"L", "R", "D"} {
		if c := counts[dir]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d%s", c, dir))
		}
	}
	return strings.Join(parts, " ")
}

func handleRemoteForward(args []string) {
	if len(args) == 0 {
		printRemoteForwardUsage()
		return
	}

	switch args[0] {
	case "list", "ls":
		handleRemoteForwardList(args[1:])
	case "add":
		handleRemoteForwardAdd(args[1:])
	case "remove", "rm":
		handleRemoteForwardRemove(args[1:])
	default:
		fmt.Printf("Unknown forward command: %s\n", args[0])
		printRemoteForwardUsage()
		os.Exit(1)
	}
}

func printRemoteForwardUsage() {
	fmt.Println("Usage: agent-deck remote forward <command> <remote-name> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list <name>                List configured port forwards")
	fmt.Println("  add <name> <spec>...       Add port forward(s)")
	fmt.Println("  remove <name> <spec>...    Remove port forward(s)")
	fmt.Println()
	fmt.Println("Spec format: D:spec where D is L (local), R (remote), or D (dynamic)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck remote forward list dev")
	fmt.Println("  agent-deck remote forward add dev L:8444:localhost:8444")
	fmt.Println("  agent-deck remote forward add dev L:3000:localhost:3000 R:9090:localhost:9090")
	fmt.Println("  agent-deck remote forward remove dev L:8444:localhost:8444")
}

func handleRemoteForwardList(args []string) {
	fs := flag.NewFlagSet("remote forward list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	_ = fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: agent-deck remote forward list <remote-name> [--json]")
		os.Exit(1)
	}
	name := remaining[0]

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	rc, exists := config.Remotes[name]
	if !exists {
		fmt.Printf("Error: remote '%s' not found\n", name)
		os.Exit(1)
	}

	if *jsonOutput {
		output, err := json.MarshalIndent(rc.PortForwards, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(output))
		return
	}

	if len(rc.PortForwards) == 0 {
		fmt.Printf("No port forwards configured for remote '%s'.\n", name)
		return
	}

	fmt.Printf("Port forwards for remote '%s':\n\n", name)
	fmt.Printf("  %-12s %s\n", "DIRECTION", "SPEC")
	fmt.Printf("  %s\n", strings.Repeat("-", 40))
	for _, pf := range rc.PortForwards {
		var dirLabel string
		switch pf.Direction {
		case "L":
			dirLabel = "local (-L)"
		case "R":
			dirLabel = "remote (-R)"
		case "D":
			dirLabel = "dynamic (-D)"
		}
		fmt.Printf("  %-12s %s\n", dirLabel, pf.Spec)
	}
	fmt.Printf("\nTotal: %d forward(s)\n", len(rc.PortForwards))
}

func handleRemoteForwardAdd(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: agent-deck remote forward add <remote-name> <spec>...")
		fmt.Println("Example: agent-deck remote forward add dev L:8444:localhost:8444")
		os.Exit(1)
	}

	name := args[0]
	specs := args[1:]

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	rc, exists := config.Remotes[name]
	if !exists {
		fmt.Printf("Error: remote '%s' not found\n", name)
		os.Exit(1)
	}

	// Build set of existing forwards for dedup
	existing := make(map[string]bool)
	for _, pf := range rc.PortForwards {
		existing[pf.Direction+":"+pf.Spec] = true
	}

	var added int
	for _, s := range specs {
		pf, err := session.ParsePortForwardFlag(s)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		key := pf.Direction + ":" + pf.Spec
		if existing[key] {
			fmt.Printf("  Skipped (duplicate): %s\n", s)
			continue
		}
		rc.PortForwards = append(rc.PortForwards, pf)
		existing[key] = true
		added++
	}

	config.Remotes[name] = rc
	if err := session.SaveUserConfig(config); err != nil {
		fmt.Printf("Error: failed to save config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Added %d forward(s) to remote '%s' (%d total)\n", added, name, len(rc.PortForwards))
}

func handleRemoteForwardRemove(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: agent-deck remote forward remove <remote-name> <spec>...")
		fmt.Println("Example: agent-deck remote forward remove dev L:8444:localhost:8444")
		os.Exit(1)
	}

	name := args[0]
	specs := args[1:]

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	rc, exists := config.Remotes[name]
	if !exists {
		fmt.Printf("Error: remote '%s' not found\n", name)
		os.Exit(1)
	}

	// Build set of specs to remove
	toRemove := make(map[string]bool)
	for _, s := range specs {
		pf, err := session.ParsePortForwardFlag(s)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		toRemove[pf.Direction+":"+pf.Spec] = true
	}

	var kept []session.PortForward
	var removed int
	for _, pf := range rc.PortForwards {
		key := pf.Direction + ":" + pf.Spec
		if toRemove[key] {
			removed++
		} else {
			kept = append(kept, pf)
		}
	}

	rc.PortForwards = kept
	config.Remotes[name] = rc
	if err := session.SaveUserConfig(config); err != nil {
		fmt.Printf("Error: failed to save config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Removed %d forward(s) from remote '%s' (%d remaining)\n", removed, name, len(rc.PortForwards))
}

// reorderRemoteArgs moves flags before positional args for Go's flag package.
func reorderRemoteArgs(fs *flag.FlagSet, args []string) []string {
	// Collect known value flags from the FlagSet
	valueFlags := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		valueFlags["--"+f.Name] = true
	})

	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			// If it's a value flag without =, consume next arg too
			if !strings.Contains(arg, "=") && valueFlags[arg] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, arg)
		}
	}
	return append(flags, positional...)
}
