package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck deepseek` — the CLI half of the DeepSeek Harness integration.
//
// Project policy: every TUI capability ships a CLI equivalent with --help and
// --json, and agents are first-class users. The TUI shows a DeepSeek session's
// resolved binary, DSH_HOME, profile, and whether restart can resume; this
// command answers the same questions in a form an agent can parse, plus
// `profiles` and `sessions` for the two things that live inside DSH_HOME.
//
// It is READ-ONLY by design. Creating a dsh profile means running
// `dsh plugin --profile <name> add <package>`, which shells out to pnpm and
// installs arbitrary code; agent-deck prints that command rather than running it.

func handleDeepSeek(args []string) {
	if len(args) == 0 {
		printDeepSeekUsage(os.Stderr)
		os.Exit(1)
	}

	sub := args[0]
	rest := args[1:]
	jsonOut := false
	var positional []string
	for _, arg := range rest {
		switch arg {
		case "--json":
			jsonOut = true
		case "--help", "-h":
			printDeepSeekUsage(os.Stdout)
			return
		default:
			positional = append(positional, arg)
		}
	}

	switch sub {
	case "help", "--help", "-h":
		printDeepSeekUsage(os.Stdout)
	case "status":
		handleDeepSeekStatus(jsonOut)
	case "profiles":
		handleDeepSeekProfiles(jsonOut)
	case "sessions":
		workspace := ""
		if len(positional) > 0 {
			workspace = positional[0]
		}
		handleDeepSeekSessions(workspace, jsonOut)
	default:
		fmt.Fprintf(os.Stderr, "Unknown deepseek subcommand: %s\n", sub)
		printDeepSeekUsage(os.Stderr)
		os.Exit(1)
	}
}

func printDeepSeekUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck deepseek <command> [--json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Inspect the DeepSeek Harness (`dsh`) integration: which binary agent-deck")
	fmt.Fprintln(w, "would launch, which DSH_HOME it resolves, which profiles exist there, and")
	fmt.Fprintln(w, "which conversations a workspace has. Read-only.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  status                  Resolved binary, version, DSH_HOME, profile, resume support")
	fmt.Fprintln(w, "  profiles                List the profiles under $DSH_HOME/profiles")
	fmt.Fprintln(w, "  sessions [workspace]    List dsh session IDs recorded for a workspace")
	fmt.Fprintln(w, "                          (defaults to the current directory)")
	fmt.Fprintln(w, "  help                    Show this help message")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --json                  Emit machine-readable JSON instead of text")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Configuration lives in [deepseek] (command, config_dir, profile, patches,")
	fmt.Fprintln(w, "host, port, trusted_hosts, resume_flag, extra_args, env_file), with")
	fmt.Fprintln(w, "per-account overrides in [profiles.<account>.deepseek].config_dir.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Launch a session with:  agent-deck launch -c deepseek")
	fmt.Fprintln(w, "Install the CLI with:   npm install -g @deepseek-ai/dsh")
}

// deepSeekStatusReport is the --json payload of `deepseek status`.
type deepSeekStatusReport struct {
	Command   string `json:"command"`
	Resolved  string `json:"resolved_path,omitempty"`
	Installed bool   `json:"installed"`
	// IdentityVerified reports that agent-deck actually RAN the identity check
	// and it passed. It is false for an explicitly configured command, which is
	// taken at its word and never probed — reporting "verified" there would be a
	// claim agent-deck did not earn.
	IdentityVerified bool `json:"identity_verified"`
	// WrongDsh is set when something named dsh resolved but is NOT DeepSeek
	// Harness. Reported as its own state rather than folded into
	// installed:false, because "you have a different dsh" needs a different fix
	// from "you have no dsh".
	WrongDsh       bool   `json:"wrong_dsh_on_path,omitempty"`
	Version        string `json:"version,omitempty"`
	Home           string `json:"home"`
	HomeExists     bool   `json:"home_exists"`
	Profile        string `json:"profile"`
	ProfileMode    string `json:"profile_mode"`
	ResumeFlag     string `json:"resume_flag,omitempty"`
	ResumeSupport  bool   `json:"resume_supported"`
	ForkSupport    bool   `json:"fork_supported"`
	APIKeyPresent  bool   `json:"api_key_present"`
	CredentialFile string `json:"credential_file,omitempty"`
}

func handleDeepSeekStatus(jsonOut bool) {
	command := session.GetToolCommand("deepseek")
	report := deepSeekStatusReport{
		Command:       command,
		Home:          session.DeepSeekHomeDir(),
		Profile:       session.DeepSeekProfile(),
		ResumeFlag:    session.DeepSeekResumeFlag(),
		ResumeSupport: session.DeepSeekSupportsResume(),
		// dsh 0.1.0-rc.6 has no fork/branch command. Reported as a fact rather
		// than omitted so an agent can tell "not supported" from "unknown".
		ForkSupport:   false,
		APIKeyPresent: strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) != "",
	}
	report.ProfileMode = string(session.DeepSeekProfileMode(report.Profile))

	// Resolve the binary the same way the launcher would: the first token of
	// the configured command, on PATH or as an absolute path.
	if fields := strings.Fields(command); len(fields) > 0 {
		if path, err := exec.LookPath(fields[0]); err == nil {
			report.Resolved = path
			report.Version = deepSeekVersion(path, fields[1:])
			report.Installed = session.DeepSeekInstalled(command)
			// The identity check only runs for the bare default name; a
			// configured command is trusted, not probed.
			isBareDefault := strings.TrimSpace(command) == "dsh"
			report.IdentityVerified = isBareDefault && report.Installed
			// Something answers to the name but is not the harness.
			report.WrongDsh = isBareDefault && !report.Installed
		}
	}
	if info, err := os.Stat(report.Home); err == nil && info.IsDir() {
		report.HomeExists = true
	}
	credFile := filepath.Join(report.Home, ".credentials.yaml")
	if _, err := os.Stat(credFile); err == nil {
		report.CredentialFile = credFile
	}

	if jsonOut {
		emitDeepSeekJSON(report)
		return
	}

	fmt.Printf("Command:   %s\n", report.Command)
	switch {
	case report.Installed:
		fmt.Printf("Resolved:  %s\n", report.Resolved)
		if report.Version != "" {
			fmt.Printf("Version:   %s\n", report.Version)
		}
		if report.IdentityVerified {
			fmt.Println("Identity:  confirmed as DeepSeek Harness")
		} else {
			fmt.Println("Identity:  not checked — [deepseek].command is configured explicitly,")
			fmt.Println("           so agent-deck takes it at its word")
		}
	case report.WrongDsh:
		// The collision that actually matters: Debian/Ubuntu ship a `dsh`
		// ("dancer's shell") that executes commands on a group of machines over
		// remote shell. Launching a session against THAT would be considerably
		// worse than reporting nothing installed.
		fmt.Printf("Resolved:  %s\n", report.Resolved)
		fmt.Println("           ⚠  this is NOT DeepSeek Harness — it did not identify itself.")
		fmt.Println("           Debian/Ubuntu ship an unrelated `dsh` (\"dancer's shell\", a")
		fmt.Println("           distributed shell that runs commands on remote machines).")
		fmt.Println("           agent-deck will not launch it.")
		fmt.Println("           Install the harness (npm install -g @deepseek-ai/dsh), then")
		fmt.Println("           point [deepseek].command at it if both are on PATH.")
	default:
		fmt.Println("Resolved:  NOT FOUND on PATH")
		fmt.Println("           Install with: npm install -g @deepseek-ai/dsh")
	}
	fmt.Printf("DSH_HOME:  %s", report.Home)
	if !report.HomeExists {
		fmt.Print("  (does not exist yet; dsh creates it on first boot)")
	}
	fmt.Println()
	fmt.Printf("Profile:   %s  (mode: %s)\n", report.Profile, report.ProfileMode)
	if report.ResumeSupport {
		fmt.Printf("Resume:    %s <session-id> on restart\n", report.ResumeFlag)
	} else {
		fmt.Println("Resume:    off — restart re-boots the profile; dsh keeps its own")
		fmt.Println("           sessions under DSH_HOME, so nothing is lost. Set")
		fmt.Println("           [deepseek].resume_flag only for a profile whose app")
		fmt.Println("           documents one (neither shipped profile does).")
	}
	fmt.Println("Fork:      not supported by dsh")
	if report.APIKeyPresent {
		fmt.Println("API key:   DEEPSEEK_API_KEY is set in this environment")
	} else if report.CredentialFile != "" {
		fmt.Printf("API key:   DEEPSEEK_API_KEY unset; credentials file present (%s)\n", report.CredentialFile)
	} else {
		fmt.Println("API key:   DEEPSEEK_API_KEY unset and no credentials file — dsh will")
		fmt.Println("           exit with MISSING_CREDENTIAL until one is provided")
	}
}

// deepSeekVersion returns `<command> --version` output, or "". Bounded by the
// caller's process lifetime only: --version neither boots a profile nor touches
// the network, and a wedged binary here surfaces as a missing version line
// rather than a hang in the TUI (this command is CLI-only).
func deepSeekVersion(path string, extraArgs []string) string {
	args := append(append([]string{}, extraArgs...), "--version")
	// #nosec G204 -- path came from exec.LookPath over the configured
	// [deepseek].command, not from runtime user input, and the args are the
	// configured tokens plus a fixed literal.
	out, err := exec.Command(path, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// deepSeekProfilesReport is the --json payload of `deepseek profiles`.
type deepSeekProfilesReport struct {
	Home     string                `json:"home"`
	Profiles []deepSeekProfileInfo `json:"profiles"`
}

type deepSeekProfileInfo struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Active  bool     `json:"active"`
	Bundles []string `json:"bundles,omitempty"`
}

func handleDeepSeekProfiles(jsonOut bool) {
	home := session.DeepSeekHomeDir()
	active := session.DeepSeekProfile()
	report := deepSeekProfilesReport{Home: home, Profiles: []deepSeekProfileInfo{}}

	root := filepath.Join(home, "profiles")
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, entry := range entries {
			// `node_modules` is the installation fallback dsh heals on every
			// launch (one symlink per package), not a profile.
			if !entry.IsDir() || entry.Name() == "node_modules" {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			report.Profiles = append(report.Profiles, deepSeekProfileInfo{
				Name:    entry.Name(),
				Path:    dir,
				Active:  entry.Name() == active,
				Bundles: session.DeepSeekProfileBundles(dir),
			})
		}
	}

	if jsonOut {
		emitDeepSeekJSON(report)
		return
	}

	if len(report.Profiles) == 0 {
		fmt.Printf("No profiles under %s\n", root)
		fmt.Println("The web and headless profiles auto-initialize on first use:")
		fmt.Println("  dsh --profile web")
		fmt.Println("  dsh --profile headless \"say hi\"")
		fmt.Println("Any other profile: dsh plugin --profile <name> add <package>")
		return
	}
	for _, p := range report.Profiles {
		marker := " "
		if p.Active {
			marker = "*"
		}
		fmt.Printf("%s %-12s %s\n", marker, p.Name, p.Path)
		if len(p.Bundles) > 0 {
			fmt.Printf("    bundles: %s\n", strings.Join(p.Bundles, ", "))
		}
	}
	fmt.Println()
	fmt.Printf("* = the profile agent-deck launches ([deepseek].profile = %q)\n", active)
}

// deepSeekSessionsReport is the --json payload of `deepseek sessions`.
type deepSeekSessionsReport struct {
	Home      string   `json:"home"`
	Workspace string   `json:"workspace"`
	Sessions  []string `json:"sessions"`
	Resumable string   `json:"resumable,omitempty"`
}

func handleDeepSeekSessions(workspace string, jsonOut bool) {
	home := session.DeepSeekHomeDir()
	if strings.TrimSpace(workspace) == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspace = cwd
		}
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}

	report := deepSeekSessionsReport{
		Home:      home,
		Workspace: workspace,
		Sessions:  session.DeepSeekSessionIDs(home, workspace),
		Resumable: session.DiscoverDeepSeekSessionID(home, workspace),
	}
	if report.Sessions == nil {
		report.Sessions = []string{}
	}

	if jsonOut {
		emitDeepSeekJSON(report)
		return
	}

	if len(report.Sessions) == 0 {
		fmt.Printf("No dsh sessions recorded for %s\n", workspace)
		fmt.Printf("(index: %s)\n", filepath.Join(home, "storages", "workspace.json"))
		return
	}
	fmt.Printf("Workspace: %s\n", workspace)
	fmt.Printf("DSH_HOME:  %s\n", home)
	fmt.Println()
	for _, id := range report.Sessions {
		marker := " "
		if id == report.Resumable {
			marker = "*"
		}
		fmt.Printf("%s %s\n", marker, id)
	}
	if report.Resumable != "" {
		fmt.Println()
		fmt.Println("* = newest session whose body is still on disk")
	}
}

func emitDeepSeekJSON(payload any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}
