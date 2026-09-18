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
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// resolveRemoteConfig loads the user config and looks up the RemoteConfig
// for name — the "load config, check config.Remotes, check the key"
// resolution every remote subcommand (and completion_cmd.go's
// printRemoteSessionCompletions) needs before it can build an SSHRunner.
// loadErr is only set when LoadUserConfig itself failed; exists is false
// whenever config.Remotes is nil or has no entry for name — callers that
// want a distinct message for each case check loadErr first.
func resolveRemoteConfig(name string) (rc session.RemoteConfig, exists bool, loadErr error) {
	config, err := session.LoadUserConfig()
	if err != nil {
		return session.RemoteConfig{}, false, err
	}
	if config.Remotes == nil {
		return session.RemoteConfig{}, false, nil
	}
	rc, exists = config.Remotes[name]
	return rc, exists, nil
}

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
		fmt.Println("  --retry")
		fmt.Println("        Clear cached poll/authentication state for all configured remotes (no SSH unless --check)")
		fmt.Println("  --json")
		fmt.Println("        Output as JSON")
	case "sessions":
		fmt.Println("Usage: agent-deck remote sessions [name] [options]")
		fmt.Println("\nOptions:")
		fmt.Println("  --json")
		fmt.Println("        Output as JSON: a bare array of sessions")
		fmt.Println("  --with-errors")
		fmt.Println("        With --json, wrap output as {\"sessions\":[...],\"errors\":[...]} and")
		fmt.Println("        exit 1 if any remote failed")
		fmt.Println("  --json-envelope")
		fmt.Println("        Alias for --json --with-errors")
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
		fmt.Println("  --from-build string")
		fmt.Println("        Install release-layout archives from a local directory")
		fmt.Println("  --force")
		fmt.Println("        Allow reinstalling or downgrading")
		fmt.Println("  --dry-run")
		fmt.Println("        Verify artifacts and show destination paths without installing")
		fmt.Println("  --json")
		fmt.Println("        Output every result as JSON")
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
	fmt.Println("  list/status/health, show/output/send, add/launch, session start/stop/restart/fork/archive/unarchive/set,")
	fmt.Println("  session switch/switch-preview/switch-account (runs on the remote; its own accounts and harnesses),")
	fmt.Println("  worktree list/info/cleanup, mcp list/attach, skill list/attached/attach/detach, group list/reorder")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add <name> <user@host>    Add a remote agent-deck instance")
	fmt.Println("  remove <name>             Remove a remote")
	fmt.Println("  list                      List configured remotes")
	fmt.Println("  sessions [name] [--json] [--with-errors|--json-envelope]")
	fmt.Println("                            Fetch sessions from remote(s); --json is a bare array,")
	fmt.Println("                            --with-errors wraps it with per-remote failures")
	fmt.Println("  drain <name>              Pull completion/transition records from a remote")
	fmt.Println("                            into this machine's inbox (read-only on the remote)")
	fmt.Println("  attach <name> <session>   Attach to a remote session")
	fmt.Println("  rename <name> <session> <new-title>  Rename a remote session")
	fmt.Println("  update [name | --all]     Install/update agent-deck on remote(s)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck remote add dev user@dev-box")
	fmt.Println("  agent-deck remote add prod user@prod-server --agent-deck-path /usr/local/bin/agent-deck")
	fmt.Println("  agent-deck remote list")
	fmt.Println("  agent-deck remote sessions dev --json")
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
	// A bare trailing "help" is a consent request, not a third operand: this
	// command takes exactly two positional values, so there is no legitimate
	// data value it could be confused with (issue #2025).
	if len(remaining) == 3 && remaining[2] == "help" {
		fs.Usage()
		return
	}
	if len(remaining) != 2 {
		fmt.Printf("Error: unexpected extra argument(s): %s\n", strings.Join(remaining[2:], " "))
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
	retry := fs.Bool("retry", false, "Clear cached poll/authentication state for all configured remotes (no SSH unless --check)")
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

	if *retry {
		for name := range config.Remotes {
			if err := session.ResetRemotePoll(name); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to reset remote poll: %v\n", err)
				os.Exit(1)
			}
		}
	}
	polls := session.LoadRemotePolls()
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
			InstalledFrom    string `json:"installed_from,omitempty"`
			VersionCheckedAt string `json:"version_checked_at,omitempty"`
			Outdated         bool   `json:"outdated"`
			// VersionState is the same same/older/newer/unknown compare the
			// remote preview panel shows (session.RemoteVersionCompare),
			// always present so scripts don't have to re-derive it from
			// Version/Outdated.
			VersionState   string `json:"version_state"`
			// BuildDiffers is true when VersionState is "same" but the raw
			// version strings differ only in build metadata (a "+local..."
			// suffix on one side) — the honest label for that case is "same
			// release, different build", not a bare "same" (walk defect #1).
			BuildDiffers   bool   `json:"build_differs,omitempty"`
			LastPollMS     *int64 `json:"last_poll_ms"`
			LastPollStatus string `json:"last_poll_status"`
			LastPollError  string `json:"last_poll_error"`
		}

		var remotes []remoteJSON
		for name, rc := range config.Remotes {
			row := remoteJSON{
				Name:          name,
				Host:          rc.Host,
				AgentDeckPath: rc.GetAgentDeckPath(),
				Profile:       rc.GetProfile(),
			}
			state := versions[name]
			row.VersionState = state.Compare(Version).String()
			row.BuildDiffers = state.BuildDiffers(Version)
			if !state.CheckedAt.IsZero() {
				row.VersionCheckedAt = state.CheckedAt.Format(time.RFC3339)
			}
			if state.Found {
				row.Version = state.Version
				row.InstalledFrom = state.InstalledFrom
				row.Outdated = state.Outdated(Version)
			}
			poll := configuredRemotePoll(polls[name], rc)
			row.LastPollMS, row.LastPollStatus, row.LastPollError = poll.LastPollMS, poll.LastPollStatus, poll.LastPollError
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

	fmt.Printf("%-15s %-30s %-20s %-10s %s\n", "NAME", "HOST", "PATH", "PROFILE", "VERSION / LAST POLL")
	fmt.Println(strings.Repeat("-", 84))
	for name, rc := range config.Remotes {
		fmt.Printf("%-15s %-30s %-20s %-10s %s\n", name, rc.Host, rc.GetAgentDeckPath(), rc.GetProfile(), remoteVersionColumn(versions[name], Version)+" / "+remotePollColumn(configuredRemotePoll(polls[name], rc)))
	}
	fmt.Printf("\nTotal: %d remotes (controller v%s)\n", len(config.Remotes), Version)
}

// configuredRemotePoll treats a changed SSH identity as never polled.
func configuredRemotePoll(state session.RemotePollState, rc session.RemoteConfig) session.RemotePollState {
	if !state.Matches(rc) || state.LastPollStatus == "" {
		return session.RemotePollState{LastPollStatus: "unknown"}
	}
	return state
}

func remotePollColumn(state session.RemotePollState) string {
	text := state.LastPollStatus
	if state.LastPollMS != nil {
		text += fmt.Sprintf(" (%dms)", *state.LastPollMS)
	}
	if state.LastPollError != "" {
		text += ": " + state.LastPollError
	}
	return text
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
	// Keep fresh probe results even if the best-effort cache write failed.
	cached := session.LoadRemoteVersions()
	for name, state := range states {
		if cached[name].Version == state.Version {
			state.InstalledFrom = cached[name].InstalledFrom
			states[name] = state
		}
	}
	return states
}

type remoteSessionError struct {
	Name  string `json:"name"`
	Host  string `json:"host"`
	Error string `json:"error"`
}

type remoteSessionsOutput struct {
	Sessions []session.RemoteSessionInfo `json:"sessions"`
	Errors   []remoteSessionError        `json:"errors"`
}

// parseRemoteSessionsArgs parses `remote sessions` flags. envelope reports
// whether --with-errors or --json-envelope opted into the
// {"sessions":[...],"errors":[...]} shape; jsonOutput covers either JSON
// form. --json alone must keep emitting the bare array, since existing
// consumers (conductor scripts, skills) pipe it through `jq '.[]'` and a
// shape change breaks them silently.
func parseRemoteSessionsArgs(args []string) (remoteName string, jsonOutput bool, envelope bool, err error) {
	fs := flag.NewFlagSet("remote sessions", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonFlag := fs.Bool("json", false, "Output as JSON (bare array of sessions)")
	withErrorsFlag := fs.Bool("with-errors", false, "With --json, wrap output as {\"sessions\":[...],\"errors\":[...]}")
	jsonEnvelopeFlag := fs.Bool("json-envelope", false, "Alias for --json --with-errors")
	if err := fs.Parse(reorderRemoteArgs(fs, args)); err != nil {
		return "", false, false, err
	}
	if len(fs.Args()) > 1 {
		return "", false, false, fmt.Errorf("accepts at most one remote name")
	}
	if len(fs.Args()) == 1 {
		remoteName = fs.Args()[0]
	}
	envelope = *withErrorsFlag || *jsonEnvelopeFlag
	jsonOutput = *jsonFlag || envelope
	return remoteName, jsonOutput, envelope, nil
}

func addRemoteSessionFetch(
	output *remoteSessionsOutput,
	name string,
	host string,
	sessions []session.RemoteSessionInfo,
	err error,
) bool {
	if err != nil {
		output.Errors = append(output.Errors, remoteSessionError{
			Name:  name,
			Host:  host,
			Error: err.Error(),
		})
		return false
	}
	for i := range sessions {
		sessions[i].RemoteName = name
	}
	output.Sessions = append(output.Sessions, sessions...)
	return true
}

// writeRemoteSessionsJSON prints the opt-in envelope. Both slices are
// normalized to non-nil so they always marshal as `[]` rather than `null`.
func writeRemoteSessionsJSON(output remoteSessionsOutput) {
	if output.Sessions == nil {
		output.Sessions = []session.RemoteSessionInfo{}
	}
	if output.Errors == nil {
		output.Errors = []remoteSessionError{}
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to format JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}

// writeRemoteSessionsArray prints the default bare array. Before walk defect
// #2 a nil slice marshaled as `null` (matching what agent-deck emitted
// before #2207); a nil slice here is normalized to `[]` so a zero-session
// remote gives every `--json` consumer (this bare array and the
// --with-errors envelope) the same empty-array shape instead of one that
// makes `jq '.[]'` choke on `null`.
func writeRemoteSessionsArray(sessions []session.RemoteSessionInfo) {
	if sessions == nil {
		sessions = []session.RemoteSessionInfo{}
	}
	encoded, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		fmt.Printf("Error: failed to format JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}

func handleRemoteSessions(args []string) {
	remoteName, jsonOutput, envelope, err := parseRemoteSessionsArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: remote sessions flag parsing failed: %v\n", err)
		os.Exit(2)
	}

	config, err := session.LoadUserConfig()
	if err != nil {
		if envelope {
			writeRemoteSessionsJSON(remoteSessionsOutput{
				Errors: []remoteSessionError{{Name: "config", Error: err.Error()}},
			})
		} else {
			// Matches the pre-#2207 release: plain text even under --json,
			// since a config load failure has no session list to report.
			fmt.Printf("Error: failed to load config: %v\n", err)
		}
		os.Exit(1)
	}

	if len(config.Remotes) == 0 {
		if envelope {
			writeRemoteSessionsJSON(remoteSessionsOutput{})
		} else {
			fmt.Println("No remotes configured.")
		}
		return
	}

	// #1421: sweep orphaned SSH ControlMaster sockets first so a stale socket
	// (master died on a remote update / network drop) doesn't hang this command
	// forever on the ControlMaster=auto reuse.
	session.CleanStaleSSHSockets()

	if remoteName != "" {
		if _, exists := config.Remotes[remoteName]; !exists {
			if envelope {
				writeRemoteSessionsJSON(remoteSessionsOutput{
					Errors: []remoteSessionError{{Name: remoteName, Error: "remote not found"}},
				})
			} else {
				fmt.Printf("Error: remote '%s' not found\n", remoteName)
			}
			os.Exit(1)
		}
	}

	ctx := context.Background()

	// Both slices start nil and are only appended to; writeRemoteSessionsArray
	// and writeRemoteSessionsJSON each normalize a nil Sessions to `[]` before
	// marshaling (walk defect #2).
	var output remoteSessionsOutput

	for name, rc := range config.Remotes {
		if remoteName != "" && name != remoteName {
			continue
		}

		runner := session.NewSSHRunner(name, rc)
		sessions, err := runner.FetchSessions(ctx)
		if !addRemoteSessionFetch(&output, name, rc.Host, sessions, err) {
			if !jsonOutput {
				fmt.Printf("  [%s] Error: %v\n", name, err)
			}
			continue
		}

		if !jsonOutput {
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

	switch {
	case envelope:
		writeRemoteSessionsJSON(output)
	case jsonOutput:
		writeRemoteSessionsArray(output.Sessions)
	}
	if envelope && len(output.Errors) != 0 {
		os.Exit(1)
	}
}

func handleRemoteAttach(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: agent-deck remote attach <remote-name> <session-title-or-id>")
		os.Exit(1)
	}

	remoteName := args[0]
	sessionRef := args[1]

	rc, exists, err := resolveRemoteConfig(remoteName)
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}
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

	rc, exists, err := resolveRemoteConfig(remoteName)
	if err != nil {
		fmt.Printf("Error: failed to load config: %v\n", err)
		os.Exit(1)
	}
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
	fromBuild := fs.String("from-build", "", "Install archives from a local build directory")
	force := fs.Bool("force", false, "Allow reinstalling or downgrading")
	dryRun := fs.Bool("dry-run", false, "Show the verified installation plan without changing remotes")
	jsonOutput := fs.Bool("json", false, "Output every result as JSON")
	_ = fs.Parse(reorderRemoteArgs(fs, args))
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "Error: expected one remote name or --all")
		os.Exit(2)
	}
	opts := remoteUpdateCLIOptions{JSON: *jsonOutput, Update: session.RemoteUpdateOptions{Force: *force, DryRun: *dryRun}}
	if *fromBuild != "" {
		build, err := session.LoadLocalBuild(*fromBuild)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		opts.Update.LocalBuild = build
	}

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	if len(config.Remotes) == 0 {
		if *jsonOutput {
			fmt.Println("[]")
		} else {
			fmt.Println("No remotes configured.")
		}
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
			fmt.Fprintf(os.Stderr, "Error: remote '%s' not found\n", name)
			os.Exit(1)
		}
		remotes = map[string]session.RemoteConfig{name: rc}
	}

	results := runRemoteUpdatesCLI(context.Background(), remotes, Version, remoteSweepWait, opts)
	if *jsonOutput {
		if err := writeRemoteUpdateJSON(os.Stdout, results); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	} else {
		printRemoteUpdateTable(os.Stdout, results)
	}
	if session.CountRemoteUpdateFailures(results) > 0 {
		os.Exit(1)
	}
}

// remoteSweepWait bounds how long an explicit update waits for a sweep this
// controller is already running (the TUI's startup sweep, typically).
const remoteSweepWait = 2 * time.Minute

type remoteUpdateCLIOptions struct {
	Update session.RemoteUpdateOptions
	JSON   bool
}

// runRemoteUpdatesCLI is the explicit `remote update` run. It first waits
// up to wait for a sweep this controller is already running: racing it
// would only meet the remotes' deploy locks. When the sweep is still going
// afterwards, the remotes it covers are reported as being updated by it
// (skipped, not failed) and the rest are updated here; otherwise this run
// marks itself as the sweep so a later one waits in turn (#2244).
func runRemoteUpdatesCLI(ctx context.Context, remotes map[string]session.RemoteConfig, target string, wait time.Duration, options ...remoteUpdateCLIOptions) []session.RemoteUpdateResult {
	opts := remoteUpdateCLIOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.Update.LocalBuild != nil {
		target = opts.Update.LocalBuild.Version
	}
	if opts.Update.DryRun {
		return runRemoteUpdates(ctx, remotes, target, true, opts)
	}
	names := remoteNames(remotes)
	deadline := time.Now().Add(wait)
	var announced bool
	for {
		end, err := session.BeginRemoteSweep(names)
		if err == nil {
			defer end()
			return runRemoteUpdates(ctx, remotes, target, true, opts)
		}
		if !errors.Is(err, session.ErrRemoteSweepRunning) || time.Now().After(deadline) {
			break
		}
		if sweep, ok := session.RemoteSweepInProgress(); ok && !announced && !opts.JSON {
			fmt.Printf("A remote sweep is already running on this controller (pid %d, started %s); waiting for it...\n", sweep.PID, sweep.StartedAt.Format(time.Kitchen))
			announced = true
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(remoteSweepPoll(wait)):
		}
	}

	sweep, running := session.RemoteSweepInProgress()
	rest := make(map[string]session.RemoteConfig, len(remotes))
	results := make([]session.RemoteUpdateResult, 0, len(names))
	for _, name := range names {
		if running && sweep.Covers(name) {
			skipped := session.RemoteUpdateResult{
				Name: name, Host: remotes[name].Host, To: strings.TrimPrefix(target, "v"),
				Outcome: session.RemoteUpdateOutcomeSkipped,
				Note:    fmt.Sprintf("sweep already in progress, remote %s is being updated by %d", name, sweep.PID),
			}
			results = append(results, skipped)
			if !opts.JSON {
				fmt.Printf("\n═══ Remote: %s (%s) ═══\n  %s\n", name, remotes[name].Host, formatRemoteUpdateResult(skipped))
			}
			continue
		}
		rest[name] = remotes[name]
	}
	if len(rest) > 0 {
		results = append(results, runRemoteUpdates(ctx, rest, target, true, opts)...)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
}

// remoteSweepPoll is how often the wait re-checks; short waits (tests) poll
// faster.
func remoteSweepPoll(wait time.Duration) time.Duration {
	if wait < time.Second {
		return wait / 4
	}
	return 500 * time.Millisecond
}

// runRemoteUpdates drives session.UpdateRemotes with the CLI's progress and
// per-remote reporting. installMissing mirrors the explicit CLI contract
// (a remote without agent-deck gets it installed); the unattended paths
// pass false.
func runRemoteUpdates(ctx context.Context, remotes map[string]session.RemoteConfig, target string, installMissing bool, options ...remoteUpdateCLIOptions) []session.RemoteUpdateResult {
	cli := remoteUpdateCLIOptions{}
	if len(options) > 0 {
		cli = options[0]
	}
	opts := cli.Update
	opts.InstallMissing = installMissing
	if !cli.JSON {
		opts.Progress = func(line string) { fmt.Printf("  %s\n", line) }
		opts.OnResult = func(r session.RemoteUpdateResult) { fmt.Printf("  %s\n", formatRemoteUpdateResult(r)) }
	}
	opts.NewRunner = func(name string, rc session.RemoteConfig) session.RemoteBinaryInstaller {
		if !cli.JSON {
			fmt.Printf("\n═══ Remote: %s (%s) ═══\n", name, rc.Host)
		}
		if remoteUpdateRunner != nil {
			return remoteUpdateRunner(name, rc)
		}
		return session.NewSSHRunner(name, rc)
	}
	return session.UpdateRemotes(ctx, remotes, target, opts)
}

// remoteUpdateRunner builds the installer for the CLI update paths. A
// package variable so tests substitute a stub; nil means NewSSHRunner.
var remoteUpdateRunner func(name string, rc session.RemoteConfig) session.RemoteBinaryInstaller

// formatRemoteUpdateResult renders one remote's outcome with the CLI's glyphs.
func formatRemoteUpdateResult(r session.RemoteUpdateResult) string {
	line := ""
	switch r.Outcome {
	case session.RemoteUpdateOutcomeUpdated:
		if r.From == "" {
			line = fmt.Sprintf("✓ Installed v%s", r.To)
		} else {
			line = fmt.Sprintf("✓ Updated v%s → v%s", r.From, r.To)
		}
	case session.RemoteUpdateOutcomeCurrent:
		line = fmt.Sprintf("✓ Up to date (v%s)", r.From)
	case session.RemoteUpdateOutcomeSkipped:
		if r.Err == nil {
			return "– Skipped: " + r.Note
		}
		line = fmt.Sprintf("– Skipped: %v", r.Err)
	default:
		line = fmt.Sprintf("✗ Failed: %v", r.Err)
	}
	// The installer's report: where the binary went, what was left alone,
	// what a failing second deploy still changed (#2244).
	if r.Note != "" {
		line += "; " + r.Note
	}
	return line
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

	results := runPostUpdateRemoteSweep(context.Background(), config.Remotes, newVersion, unattended)
	fmt.Printf("\n%s\n", remoteUpdateSummary(results))
}

// runPostUpdateRemoteSweep is the sweep after a successful local update,
// split from the prompt so a test can drive it against a stubbed runner.
// It installs onto remotes it could not version only when a person
// answered the prompt: the unattended sweep never pushes a binary onto a
// host whose agent-deck it could not run (offline, or a probe that failed
// for any reason), the same contract as the startup sweep (#2164). The
// run is stamped so the next TUI start does not sweep again at once.
func runPostUpdateRemoteSweep(ctx context.Context, remotes map[string]session.RemoteConfig, newVersion string, unattended bool) []session.RemoteUpdateResult {
	end, err := session.BeginRemoteSweep(remoteNames(remotes))
	if err != nil {
		// A sweep from a TUI is still running: it pushes the version it was
		// started with. Let it finish rather than race it (#2244).
		fmt.Printf("  a remote sweep is already running on this controller; run `agent-deck remote update --all` after it finishes\n")
		return nil
	}
	defer end()
	results := runRemoteUpdates(ctx, remotes, newVersion, !unattended)
	_ = session.MarkRemoteAutoUpdateRan(time.Now())
	return results
}

// remoteNames returns the remotes' names in sorted order.
func remoteNames(remotes map[string]session.RemoteConfig) []string {
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
		isBool := false
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
			isBool = bf.IsBoolFlag()
		}
		valueFlags["--"+f.Name] = !isBool
		valueFlags["-"+f.Name] = !isBool
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
// a claim in the shared version cache (check and stamp under one lock, so
// two TUIs starting together run one sweep, and a stamp that cannot be
// written means no sweep); the explicit `agent-deck update` path resets
// that stamp too, so a fresh controller version still sweeps promptly.
func startRemoteAutoUpdate() {
	// Cache claims can wait on another writer, so they also belong off startup.
	go func() {
		settings := session.GetUpdateSettings()
		config, err := session.LoadUserConfig()
		if err != nil || config == nil {
			return
		}
		if !session.ClaimRemoteAutoUpdateRun(settings, len(config.Remotes), time.Now()) {
			return
		}
		remotes := config.Remotes
		runRemoteAutoUpdate(remotes, Version)
	}()
}

// runRemoteAutoUpdate is the body of the startup sweep, split out so a test
// can run it synchronously against a stubbed runner. The caller has already
// claimed the run (stamped the cache), so a crash mid-sweep does not retry
// on every restart.
func runRemoteAutoUpdate(remotes map[string]session.RemoteConfig, target string) []session.RemoteUpdateResult {
	log := logging.ForComponent(logging.CompSession)
	// Authentication must not be retried by a startup sweep after a TUI poll
	// paused it. Explicit remote checks and updates remain user-controlled.
	polls := session.LoadRemotePolls()
	eligible := make(map[string]session.RemoteConfig, len(remotes))
	var skipped []session.RemoteUpdateResult
	for name, rc := range remotes {
		if state := polls[name]; state.Matches(rc) && state.LastPollStatus == "auth_failed" {
			skipped = append(skipped, session.RemoteUpdateResult{Name: name, Host: rc.Host, Outcome: session.RemoteUpdateOutcomeSkipped, Note: "auth failed; polling paused"})
			log.Info("remote_auto_update_skipped", slog.String("remote", name), slog.String("reason", "auth failed"))
			continue
		}
		eligible[name] = rc
	}
	remotes = eligible
	if len(remotes) == 0 {
		sort.Slice(skipped, func(i, j int) bool { return skipped[i].Name < skipped[j].Name })
		return skipped
	}
	end, err := session.BeginRemoteSweep(remoteNames(remotes))
	if err != nil {
		log.Info("remote_auto_update_skipped", slog.String("reason", err.Error()))
		return nil
	}
	defer end()
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
	results = append(results, skipped...)
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	log.Info("remote_auto_update_done", slog.String("summary", remoteUpdateSummary(results)))
	return results
}

// remoteAutoUpdateRunner builds the installer for the startup sweep. A
// package variable so tests substitute a stub; nil means NewSSHRunner.
var remoteAutoUpdateRunner func(name string, rc session.RemoteConfig) session.RemoteBinaryInstaller
