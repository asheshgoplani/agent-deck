package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handlePiHooks(args []string) {
	if len(args) == 0 {
		printPiHooksUsage(os.Stderr)
		exitCLI(1)
	}

	// A help request anywhere in the argument list must print usage and exit
	// without side effects (#1993).
	if hooksHelpRequested(args) {
		printPiHooksUsage(os.Stdout)
		return
	}

	switch args[0] {
	case "help", "--help", "-h":
		printPiHooksUsage(os.Stdout)
	case "install":
		handlePiHooksInstall()
	case "uninstall":
		handlePiHooksUninstall()
	case "status":
		handlePiHooksStatus(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown pi-hooks subcommand: %s\n", args[0])
		printPiHooksUsage(os.Stderr)
		exitCLI(1)
	}
}

func printPiHooksUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck pi-hooks <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage pi (pi-coding-agent) hook integration.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Installs an extension into pi's global extension directory that")
	fmt.Fprintln(w, "forwards pi's lifecycle events to agent-deck, so a pi session's status")
	fmt.Fprintln(w, "comes from real events instead of reading the pane.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  install      Install or upgrade the agent-deck pi extension")
	fmt.Fprintln(w, "  uninstall    Remove the agent-deck pi extension")
	fmt.Fprintln(w, "  status       Show the extension install status, the pi version, whether")
	fmt.Fprintln(w, "               the wake socket is on (or why not)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Status options:")
	fmt.Fprintln(w, "  -p, --profile <name>  Profile to check the socket path for (default: current)")
}

func handlePiHooksInstall() {
	dir := session.PiExtensionsDir()
	installed, err := session.InstallPiHooks(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error installing pi hooks: %v\n", err)
		exitCLI(1)
	}
	if installed {
		fmt.Println("pi hooks installed successfully.")
	} else {
		fmt.Println("pi hooks are already installed.")
	}
	fmt.Printf("Extension: %s\n", session.PiHookExtensionPath(dir))
	if installed {
		fmt.Println("Restart running pi sessions to pick it up.")
	}
}

func handlePiHooksUninstall() {
	dir := session.PiExtensionsDir()
	removed, err := session.RemovePiHooks(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error removing pi hooks: %v\n", err)
		exitCLI(1)
	}
	if removed {
		fmt.Println("pi hooks removed successfully.")
	} else {
		fmt.Println("No agent-deck pi hooks found to remove.")
	}
	fmt.Printf("Extension: %s\n", session.PiHookExtensionPath(dir))
}

// piHooksProfile is the profile status computes the socket path for: -p or
// --profile after the subcommand wins, then AGENTDECK_PROFILE (which main's
// global -p has already set by the time we get here). The forms are the ones
// main's own extractor knows.
func piHooksProfile(args []string) string {
	explicit := ""
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "-p" || args[i] == "--profile") && i+1 < len(args):
			explicit = args[i+1]
			i++
		case strings.HasPrefix(args[i], "-p="):
			explicit = strings.TrimPrefix(args[i], "-p=")
		case strings.HasPrefix(args[i], "--profile="):
			explicit = strings.TrimPrefix(args[i], "--profile=")
		}
	}
	return session.GetEffectiveProfile(explicit)
}

func handlePiHooksStatus(args []string) {
	dir := session.PiExtensionsDir()
	state, version := session.InspectPiHooks(dir)
	switch state {
	case session.PiHooksInstalled:
		fmt.Printf("Status: INSTALLED (v%d)\n", version)
	case session.PiHooksOutdated:
		fmt.Printf("Status: OUTDATED (on disk v%d, current v%d)\n", version, session.PiHookExtensionVersion())
		fmt.Println("Run 'agent-deck pi-hooks install' to upgrade.")
	case session.PiHooksForeign:
		fmt.Println("Status: FOREIGN")
		fmt.Println("A file agent-deck did not write occupies this path; move it aside to install.")
	default:
		fmt.Println("Status: NOT INSTALLED")
		fmt.Println("Run 'agent-deck pi-hooks install' to install.")
	}
	fmt.Printf("Extension: %s\n", session.PiHookExtensionPath(dir))

	// The path length does not depend on the instance, so any id will do.
	sock := session.PiSocketPath(piHooksProfile(args), "status", session.PiStatusEnv())
	if sock.Version != "" {
		fmt.Printf("Pi: %s (via %s --version)\n", sock.Version, sock.Binary)
	}
	// Why the socket is off, most basic cause first: the extension that serves
	// it, then the pi it needs, then the path it needs.
	switch {
	case state == session.PiHooksNotInstalled || state == session.PiHooksForeign:
		fmt.Println("Socket: off (extension not installed)")
	case state == session.PiHooksOutdated:
		fmt.Printf("Socket: off (extension is v%d, current v%d)\n", version, session.PiHookExtensionVersion())
	case sock.Path == "":
		fmt.Printf("Socket: off (%s)\n", sock.Detail)
	default:
		fmt.Println("Socket: on")
	}
}

// pi extension states as `doctor` and `session show` report them (#3098).
// Without the extension a pi session's status and startup are read from the
// pane only, which a custom footer or editor can defeat.
const (
	piHooksInstalled    = "installed"
	piHooksOutdated     = "outdated"
	piHooksForeign      = "foreign"
	piHooksNotInstalled = "not-installed"
	piHooksUnknown      = "unknown"
)

// piHooksReport is the extension state for one pi extensions dir. Version is
// the on-disk version when the file is ours; Extension is "" when unknown.
type piHooksReport struct {
	State     string `json:"state"`
	Version   int    `json:"version,omitempty"`
	Extension string `json:"extension,omitempty"`
}

// piHooksStateForDir classifies the extension in a pi extensions dir.
func piHooksStateForDir(dir string) piHooksReport {
	state, version := session.InspectPiHooks(dir)
	report := piHooksReport{Version: version, Extension: session.PiHookExtensionPath(dir)}
	switch state {
	case session.PiHooksInstalled:
		report.State = piHooksInstalled
	case session.PiHooksOutdated:
		report.State = piHooksOutdated
	case session.PiHooksForeign:
		report.State = piHooksForeign
	default:
		report.State = piHooksNotInstalled
	}
	return report
}

// piHooksStateForInstance reports the extension a session's pi loads: empty
// for other tools, unknown for a remote or sandboxed session (its extensions
// dir is not this host's).
func piHooksStateForInstance(inst *session.Instance) piHooksReport {
	if inst == nil || inst.Tool != "pi" {
		return piHooksReport{}
	}
	if inst.IsSSH() || inst.IsSandboxed() {
		return piHooksReport{State: piHooksUnknown}
	}
	return piHooksStateForDir(session.PiExtensionsDir())
}

// piHooksLine is the human line for an extension state; every state that
// needs action names the command.
func piHooksLine(r piHooksReport) string {
	switch r.State {
	case piHooksInstalled:
		return fmt.Sprintf("extension installed (v%d, %s)", r.Version, r.Extension)
	case piHooksOutdated:
		return fmt.Sprintf("extension outdated (v%d on disk, current v%d, %s); run '%s' to upgrade",
			r.Version, session.PiHookExtensionVersion(), r.Extension, session.PiHooksInstallCommand())
	case piHooksForeign:
		return fmt.Sprintf("extension blocked: %s was not written by agent-deck; move it aside, then run '%s'", r.Extension, session.PiHooksInstallCommand())
	case piHooksNotInstalled:
		return fmt.Sprintf("extension not installed (pane detection only); run '%s' (%s)", session.PiHooksInstallCommand(), r.Extension)
	default:
		return "extension unknown (remote or sandboxed session)"
	}
}
