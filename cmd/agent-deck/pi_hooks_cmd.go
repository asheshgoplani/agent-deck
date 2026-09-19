package main

import (
	"fmt"
	"io"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handlePiHooks(args []string) {
	if len(args) == 0 {
		printPiHooksUsage(os.Stderr)
		os.Exit(1)
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
		handlePiHooksStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown pi-hooks subcommand: %s\n", args[0])
		printPiHooksUsage(os.Stderr)
		os.Exit(1)
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
	fmt.Fprintln(w, "  status       Show current pi extension install status")
}

func handlePiHooksInstall() {
	dir := session.PiExtensionsDir()
	installed, err := session.InstallPiHooks(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error installing pi hooks: %v\n", err)
		os.Exit(1)
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
		os.Exit(1)
	}
	if removed {
		fmt.Println("pi hooks removed successfully.")
	} else {
		fmt.Println("No agent-deck pi hooks found to remove.")
	}
	fmt.Printf("Extension: %s\n", session.PiHookExtensionPath(dir))
}

func handlePiHooksStatus() {
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
}
