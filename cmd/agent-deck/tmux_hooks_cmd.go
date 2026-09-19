package main

import (
	"fmt"
	"io"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// handleTmuxHooks manages the after-new-window hook agent-deck keeps in a
// reserved slot of the tmux server's global hook array (#2259). Every
// session start installs it, and it stays on the server, which outlives
// agent-deck, until removed here or by the server restarting.
func handleTmuxHooks(args []string) {
	if len(args) == 0 {
		printTmuxHooksUsage(os.Stderr)
		os.Exit(1)
	}

	// A help request anywhere in the argument list must print usage and exit
	// without side effects (#1993).
	if hooksHelpRequested(args) {
		printTmuxHooksUsage(os.Stdout)
		return
	}

	switch args[0] {
	case "help", "--help", "-h":
		printTmuxHooksUsage(os.Stdout)
	case "install":
		handleTmuxHooksInstall()
	case "uninstall":
		handleTmuxHooksUninstall()
	case "status":
		handleTmuxHooksStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown tmux-hooks subcommand: %s\n", args[0])
		printTmuxHooksUsage(os.Stderr)
		os.Exit(1)
	}
}

func printTmuxHooksUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck tmux-hooks <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage the after-new-window hook agent-deck keeps on the tmux server")
	fmt.Fprintln(w, "(slot after-new-window[2259]; it sizes windows opened by hand inside")
	fmt.Fprintln(w, "agent-deck sessions and persists on the server after agent-deck exits).")
	fmt.Fprintln(w, "Targets the server named by [tmux].socket_name, else the default one.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  install      Install or refresh the hook (every session start does this too)")
	fmt.Fprintln(w, "  uninstall    Remove the hook, only if the slot holds agent-deck's")
	fmt.Fprintln(w, "  status       Show whether the slot is absent, agent-deck's, or foreign")
	fmt.Fprintln(w, "  help         Show this help message")
}

func handleTmuxHooksInstall() {
	socket := tmux.DefaultSocketName()
	slot := tmuxHookSlotDescription(socket)
	state, err := tmux.InstallWindowPolicyHook(socket)
	exitOnTmuxHookError("installing", err)
	switch state {
	case tmux.WindowPolicyHookForeign:
		fmt.Printf("Left alone: %s holds a hook agent-deck did not install.\n", slot)
	case tmux.WindowPolicyHookOwned:
		fmt.Printf("tmux window policy hook refreshed in %s.\n", slot)
	default:
		fmt.Printf("tmux window policy hook installed in %s.\n", slot)
	}
}

func handleTmuxHooksUninstall() {
	socket := tmux.DefaultSocketName()
	slot := tmuxHookSlotDescription(socket)
	state, err := tmux.UninstallWindowPolicyHook(socket)
	exitOnTmuxHookError("removing", err)
	switch state {
	case tmux.WindowPolicyHookOwned:
		fmt.Printf("tmux window policy hook removed from %s.\n", slot)
	case tmux.WindowPolicyHookForeign:
		fmt.Printf("Left alone: %s holds a hook agent-deck did not install.\n", slot)
	default:
		fmt.Printf("No agent-deck hook found in %s.\n", slot)
	}
}

func handleTmuxHooksStatus() {
	socket := tmux.DefaultSocketName()
	state, err := tmux.WindowPolicyHookStatus(socket)
	exitOnTmuxHookError("reading", err)
	fmt.Printf("Status: %s\n", tmuxHookStatusLabel(state))
	fmt.Printf("Slot: %s\n", tmuxHookSlotDescription(socket))
	if state == tmux.WindowPolicyHookAbsent {
		fmt.Println("Installed by the next session start, or by 'agent-deck tmux-hooks install'.")
	}
}

func exitOnTmuxHookError(verb string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error %s the tmux hook (is a tmux server running?): %v\n", verb, err)
		os.Exit(1)
	}
}

func tmuxHookStatusLabel(state tmux.WindowPolicyHookState) string {
	switch state {
	case tmux.WindowPolicyHookOwned:
		return "INSTALLED"
	case tmux.WindowPolicyHookForeign:
		return "FOREIGN (slot holds a hook agent-deck did not install)"
	default:
		return "NOT INSTALLED"
	}
}

// tmuxHookSlotDescription names the slot and the server it lives on for
// user-facing output ("" = the default server).
func tmuxHookSlotDescription(socket string) string {
	if socket == "" {
		return "after-new-window[2259] on the default tmux server"
	}
	return fmt.Sprintf("after-new-window[2259] on tmux server -L %s", socket)
}
