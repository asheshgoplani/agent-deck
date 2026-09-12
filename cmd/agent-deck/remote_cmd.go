package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

func handleRemote(profile string, args []string) {
	if len(args) == 0 {
		printRemoteUsage()
		return
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRemoteUsage()
		return
	}
	if args[0] == "exec" {
		if len(args) == 2 && helpRequested(args[1:]) {
			printRemoteSubcommandUsage("exec")
			return
		}
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: agent-deck remote exec <name> <command> [arguments]")
			os.Exit(2)
		}
		handleRemoteExec(args[1], args[2:])
		return
	}
	// Help for management commands is local; named-remote command arguments
	// (including help flags) belong to the remote process.
	if isRemoteManagementCommand(args[0]) && helpRequested(args[1:]) {
		printRemoteSubcommandUsage(args[0])
		return
	}
	// Existing configurations may use a management verb as a remote name.
	// Refuse ambiguous syntax instead of accidentally mutating local config.
	if config, err := session.LoadUserConfig(); err == nil {
		if _, exists := config.Remotes[args[0]]; exists && isRemoteManagementCommand(args[0]) {
			fmt.Fprintf(os.Stderr, "Ambiguous remote name %q; use 'agent-deck remote exec %s <command>' or rename this remote in config before managing remotes\n", args[0], args[0])
			os.Exit(2)
		}
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
	case "drain":
		handleRemoteDrain(args[1:])
	case "attach":
		handleRemoteAttach(args[1:])
	case "rename":
		handleRemoteRename(args[1:])
	case "update":
		handleRemoteUpdate(args[1:])
	default:
		handleRemoteExec(args[0], args[1:])
	}
}

func isRemoteManagementCommand(name string) bool {
	switch name {
	case "add", "remove", "rm", "list", "ls", "sessions", "drain", "attach", "rename", "update", "exec":
		return true
	}
	return false
}

func printRemoteSubcommandUsage(command string) {
	switch command {
	case "exec":
		fmt.Println("Usage: agent-deck remote exec <name> <command> [arguments]")
	case "add":
		fmt.Println("Usage: agent-deck remote add <name> <user@host> [options]")
		fmt.Println("\nOptions:")
		fmt.Println("  --agent-deck-path string")
		fmt.Println("        Path to agent-deck on the remote (default: agent-deck)")
		fmt.Println("  --profile string")
		fmt.Println("        Remote profile to use (default: default)")
	case "remove", "rm":
		fmt.Println("Usage: agent-deck remote remove <name>")
	case "list", "ls":
		fmt.Println("Usage: agent-deck remote list [options]")
		fmt.Println("\nOptions:")
		fmt.Println("  --check")
		fmt.Println("        Ask each remote for its agent-deck version now (one SSH call per remote)")
		fmt.Println("  --json")
		fmt.Println("        Output as JSON")
	case "sessions":
		fmt.Println("Usage: agent-deck remote sessions [name] [options]")
		fmt.Println("\nOptions:")
		fmt.Println("  --json")
		fmt.Println("        Output as JSON")
	case "drain":
		printRemoteDrainUsage(os.Stdout)
	case "attach":
		fmt.Println("Usage: agent-deck remote attach <remote-name> <session-title-or-id>")
	case "rename":
		fmt.Println("Usage: agent-deck remote rename <remote-name> <session-title-or-id> <new-title>")
	case "update":
		fmt.Println("Usage: agent-deck remote update [name | --all]")
		fmt.Println("\nUpdates every remote that is older than this controller (v" + Version + ") when no name is given.")
		fmt.Println("Exit status is 1 when any remote failed.")
		fmt.Println("\nOptions:")
		fmt.Println("  --all")
		fmt.Println("        Update every configured remote that is older than this controller")
	default:
		printRemoteUsage()
	}
}

func printRemoteUsage() {
	fmt.Println("Usage: agent-deck remote <command> [options]")
	fmt.Println()
	fmt.Println("Manage remote agent-deck instances.")
	fmt.Println("Run commands: agent-deck remote <name> <command> [arguments]")
	fmt.Println("  Use remote exec <name> <command> when a name matches a management command.")
	fmt.Println("  list/status, show/output/send, add/launch, session start/stop/restart/fork/archive/unarchive/set,")
	fmt.Println("  worktree list/info/cleanup, mcp list/attach, skill list/attach, group list/reorder")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add <name> <user@host>    Add a remote agent-deck instance")
	fmt.Println("  remove <name>             Remove a remote")
	fmt.Println("  list                      List configured remotes")
	fmt.Println("  sessions [name]           Fetch sessions from remote(s)")
	fmt.Println("  drain <name|user@host>    Pull completion/transition records from a remote")
	fmt.Println("                            into this machine's inbox (read-only on the remote)")
	fmt.Println("  attach <name> <session>   Attach to a remote session")
	fmt.Println("  rename <name> <session> <new-title>  Rename a remote session")
	fmt.Println("  update [name | --all]     Install/update agent-deck on remote(s)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck remote add dev user@dev-box")
	fmt.Println("  agent-deck remote add prod user@prod-server --agent-deck-path /usr/local/bin/agent-deck")
	fmt.Println("  agent-deck remote list")
	fmt.Println("  agent-deck remote sessions dev")
	fmt.Println("  agent-deck remote drain dev       # pull finished/stalled reports from dev")
	fmt.Println("  agent-deck remote attach dev my-session")
	fmt.Println("  agent-deck remote rename dev my-session new-name")
	fmt.Println("  agent-deck remote update --all    # Update every remote older than this controller")
	fmt.Println("  agent-deck remote update dev      # Update specific remote")
}

func isValidRemoteName(name string) bool {
	return name != "" && !strings.ContainsAny(name, " /\\.:") && !isRemoteManagementCommand(name)
}

func handleRemoteAdd(args []string) {
	fs := flag.NewFlagSet("remote add", flag.ExitOnError)
	agentDeckPath := fs.String("agent-deck-path", "", "Path to agent-deck on the remote (default: agent-deck)")
	remoteProfile := fs.String("profile", "", "Remote profile to use (default: default)")

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
		fmt.Println("Error: remote name must not contain spaces, slashes, dots, or colons, or match a remote management command")
		os.Exit(1)
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
		Host: host,
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
	check := fs.Bool("check", false, "Ask each remote for its agent-deck version now")
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

	versions := session.LoadRemoteVersions()
	if *check {
		versions = probeRemoteVersions(context.Background(), config.Remotes)
	}

	if *jsonOutput {
		type remoteJSON struct {
			Name          string `json:"name"`
			Host          string `json:"host"`
			AgentDeckPath string `json:"agent_deck_path"`
			Profile       string `json:"profile"`
			// Version is the last agent-deck version the remote reported
			// (empty when never checked); Outdated is true when it is older
			// than this controller.
			Version          string `json:"version,omitempty"`
			VersionCheckedAt string `json:"version_checked_at,omitempty"`
			Outdated         bool   `json:"outdated"`
		}

		var remotes []remoteJSON
		for name, rc := range config.Remotes {
			row := remoteJSON{
				Name:          name,
				Host:          rc.Host,
				AgentDeckPath: rc.GetAgentDeckPath(),
				Profile:       rc.GetProfile(),
			}
			if state, ok := versions[name]; ok && state.Found {
				row.Version = state.Version
				row.VersionCheckedAt = state.CheckedAt.Format(time.RFC3339)
				row.Outdated = state.Outdated(Version)
			}
			remotes = append(remotes, row)
		}

		output, err := json.MarshalIndent(remotes, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(output))
		return
	}

	fmt.Printf("%-15s %-30s %-20s %-10s %s\n", "NAME", "HOST", "PATH", "PROFILE", "VERSION")
	fmt.Println(strings.Repeat("-", 84))
	for name, rc := range config.Remotes {
		fmt.Printf("%-15s %-30s %-20s %-10s %s\n", name, rc.Host, rc.GetAgentDeckPath(), rc.GetProfile(), remoteVersionColumn(versions[name], Version))
	}
	fmt.Printf("\nTotal: %d remotes (controller v%s)\n", len(config.Remotes), Version)
}

// remoteVersionColumn renders the VERSION cell of `remote list`: the cached
// version, "↑" appended when it is older than controller, "-" when unknown.
func remoteVersionColumn(state session.RemoteVersionState, controller string) string {
	if !state.Found || state.Version == "" {
		return "-"
	}
	if state.Outdated(controller) {
		return "v" + state.Version + " ↑"
	}
	return "v" + state.Version
}

// probeRemoteVersions asks every remote for its version now and refreshes
// the shared cache so the TUI and later list calls see the same answer.
func probeRemoteVersions(ctx context.Context, remotes map[string]session.RemoteConfig) map[string]session.RemoteVersionState {
	states := make(map[string]session.RemoteVersionState, len(remotes))
	for name, rc := range remotes {
		version, found := session.NewSSHRunner(name, rc).CheckBinary(ctx)
		states[name] = session.RemoteVersionState{Version: version, Found: found, CheckedAt: time.Now()}
	}
	_ = session.RecordRemoteVersions(states)
	return states
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

	// #1421: sweep orphaned SSH ControlMaster sockets first so a stale socket
	// (master died on a remote update / network drop) doesn't hang this command
	// forever on the ControlMaster=auto reuse.
	session.CleanStaleSSHSockets()

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
	fs := flag.NewFlagSet("remote update", flag.ExitOnError)
	all := fs.Bool("all", false, "Update every configured remote that is older than this controller")
	_ = fs.Parse(reorderRemoteArgs(fs, args))

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	if len(config.Remotes) == 0 {
		fmt.Println("No remotes configured.")
		return
	}

	remotes := config.Remotes
	if name := fs.Arg(0); name != "" {
		if *all {
			fmt.Fprintln(os.Stderr, "Error: pass either a remote name or --all, not both")
			os.Exit(2)
		}
		rc, exists := config.Remotes[name]
		if !exists {
			fmt.Printf("Error: remote '%s' not found\n", name)
			os.Exit(1)
		}
		remotes = map[string]session.RemoteConfig{name: rc}
	}

	results := runRemoteUpdates(context.Background(), remotes, Version, true)
	if failed := session.CountRemoteUpdateFailures(results); failed > 0 {
		fmt.Printf("\n%s\n", remoteUpdateSummary(results))
		os.Exit(1)
	}
	fmt.Printf("\n%s\n", remoteUpdateSummary(results))
}

// runRemoteUpdates drives session.UpdateRemotes with the CLI's progress and
// per-remote reporting. installMissing mirrors the explicit CLI contract
// (a remote without agent-deck gets it installed); the unattended paths
// pass false.
func runRemoteUpdates(ctx context.Context, remotes map[string]session.RemoteConfig, target string, installMissing bool) []session.RemoteUpdateResult {
	return session.UpdateRemotes(ctx, remotes, target, session.RemoteUpdateOptions{
		InstallMissing: installMissing,
		Progress: func(line string) {
			fmt.Printf("  %s\n", line)
		},
		OnResult: func(r session.RemoteUpdateResult) {
			fmt.Printf("  %s\n", formatRemoteUpdateResult(r))
		},
		NewRunner: func(name string, rc session.RemoteConfig) session.RemoteBinaryInstaller {
			fmt.Printf("\n═══ Remote: %s (%s) ═══\n", name, rc.Host)
			return session.NewSSHRunner(name, rc)
		},
	})
}

// formatRemoteUpdateResult renders one remote's outcome with the CLI's glyphs.
func formatRemoteUpdateResult(r session.RemoteUpdateResult) string {
	switch r.Outcome {
	case session.RemoteUpdateOutcomeUpdated:
		if r.From == "" {
			return fmt.Sprintf("✓ Installed v%s", r.To)
		}
		return fmt.Sprintf("✓ Updated v%s → v%s", r.From, r.To)
	case session.RemoteUpdateOutcomeCurrent:
		return fmt.Sprintf("✓ Up to date (v%s)", r.From)
	case session.RemoteUpdateOutcomeSkipped:
		return fmt.Sprintf("– Skipped: %v", r.Err)
	default:
		return fmt.Sprintf("✗ Failed: %v", r.Err)
	}
}

// remoteUpdateSummary is the closing line of a multi-remote run:
// "3 remotes: 1 updated, 1 already current, 1 failed".
func remoteUpdateSummary(results []session.RemoteUpdateResult) string {
	counts := map[session.RemoteUpdateOutcome]int{}
	for _, r := range results {
		counts[r.Outcome]++
	}
	parts := []string{}
	if n := counts[session.RemoteUpdateOutcomeUpdated]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", n))
	}
	if n := counts[session.RemoteUpdateOutcomeCurrent]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d already current", n))
	}
	if n := counts[session.RemoteUpdateOutcomeSkipped]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", n))
	}
	if n := counts[session.RemoteUpdateOutcomeFailed]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	noun := "remotes"
	if len(results) == 1 {
		noun = "remote"
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d %s", len(results), noun)
	}
	return fmt.Sprintf("%d %s: %s", len(results), noun, strings.Join(parts, ", "))
}

// updateRemotesAfterLocalUpdate runs after a successful local update. With
// [updates] auto_update_remotes (the default) it pushes newVersion to every
// older remote without asking; with the key off it prompts as before.
func updateRemotesAfterLocalUpdate(newVersion string) {
	config, err := session.LoadUserConfig()
	if err != nil || config == nil || len(config.Remotes) == 0 {
		return
	}

	unattended := session.GetUpdateSettings().GetAutoUpdateRemotes()
	if unattended {
		fmt.Printf("\nauto_update_remotes is on: updating %d remote(s) to v%s\n", len(config.Remotes), newVersion)
	} else {
		fmt.Printf("\nYou have %d remote(s) configured. Update them too? [Y/n] ", len(config.Remotes))
		reader := bufio.NewReader(os.Stdin)
		response, readErr := reader.ReadString('\n')
		if !shouldProceedWithRemoteUpdate(response, readErr) {
			return
		}
	}

	results := runRemoteUpdates(context.Background(), config.Remotes, newVersion, postUpdateInstallsMissing(unattended))
	fmt.Printf("\n%s\n", remoteUpdateSummary(results))
	_ = session.MarkRemoteAutoUpdateRan(time.Now())
}

// postUpdateInstallsMissing decides whether the post-update sweep installs
// onto remotes it could not version. Only when a person answered the prompt:
// the unattended sweep never pushes a binary onto a host whose agent-deck
// it could not run (offline, or a probe that failed for any reason), the
// same contract as the startup sweep (#2164).
func postUpdateInstallsMissing(unattended bool) bool {
	return !unattended
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

// installOnRemote deploys the controller's version onto one remote with the
// CLI's progress lines (used by `remote add` when the binary is missing).
func installOnRemote(runner *session.SSHRunner, ctx context.Context) error {
	_, err := session.DeployRemoteBinary(ctx, runner, Version, session.RemoteUpdateOptions{
		Progress: func(line string) { fmt.Printf("  %s\n", line) },
	})
	return err
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

// startRemoteAutoUpdate is the unattended half of [updates]
// auto_update_remotes: on TUI startup, push the controller's version to every
// remote that reports an older one. It never blocks startup (the sweep runs
// in a goroutine), never prompts, and only writes to the debug log because
// the TUI owns the screen by then. Throttled to once per check interval via
// the shared version cache; the explicit `agent-deck update` path resets
// that stamp too, so a fresh controller version still sweeps promptly.
func startRemoteAutoUpdate() {
	settings := session.GetUpdateSettings()
	config, err := session.LoadUserConfig()
	if err != nil || config == nil {
		return
	}
	if !session.ShouldAutoUpdateRemotes(settings, len(config.Remotes), session.RemoteAutoUpdateRanAt(), time.Now()) {
		return
	}
	remotes := config.Remotes
	go runRemoteAutoUpdate(remotes, Version)
}

// runRemoteAutoUpdate is the body of the startup sweep, split out so a test
// can run it synchronously against a stubbed runner.
func runRemoteAutoUpdate(remotes map[string]session.RemoteConfig, target string) []session.RemoteUpdateResult {
	log := logging.ForComponent(logging.CompSession)
	// Stamp first so a crash mid-sweep does not retry on every restart.
	if err := session.MarkRemoteAutoUpdateRan(time.Now()); err != nil {
		log.Warn("remote_auto_update_stamp_failed", slog.String("error", err.Error()))
	}
	log.Info("remote_auto_update_start", slog.Int("remotes", len(remotes)), slog.String("target", target))
	results := session.UpdateRemotes(context.Background(), remotes, target, session.RemoteUpdateOptions{
		InstallMissing: false,
		NewRunner:      remoteAutoUpdateRunner,
		OnResult: func(r session.RemoteUpdateResult) {
			attrs := []any{slog.String("remote", r.Name), slog.String("host", r.Host), slog.String("outcome", r.String())}
			if r.Outcome == session.RemoteUpdateOutcomeFailed {
				log.Warn("remote_auto_update_result", attrs...)
			} else {
				log.Info("remote_auto_update_result", attrs...)
			}
		},
	})
	log.Info("remote_auto_update_done", slog.String("summary", remoteUpdateSummary(results)))
	return results
}

// remoteAutoUpdateRunner builds the installer for the startup sweep. A
// package variable so tests substitute a stub; nil means NewSSHRunner.
var remoteAutoUpdateRunner func(name string, rc session.RemoteConfig) session.RemoteBinaryInstaller
