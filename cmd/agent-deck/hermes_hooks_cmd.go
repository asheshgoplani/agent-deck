package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handleHermesHooks(args []string) {
	if len(args) == 0 {
		printHermesHooksUsage(os.Stderr)
		os.Exit(1)
	}

	switch args[0] {
	case "help", "--help", "-h":
		printHermesHooksUsage(os.Stdout)
	case "install":
		handleHermesHooksInstall()
	case "uninstall":
		handleHermesHooksUninstall()
	case "status":
		handleHermesHooksStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown hermes-hooks subcommand: %s\n", args[0])
		printHermesHooksUsage(os.Stderr)
		os.Exit(1)
	}
}

func printHermesHooksUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck hermes-hooks <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage Hermes Agent shell hook integration.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  install      Install agent-deck Hermes hooks")
	fmt.Fprintln(w, "  uninstall    Remove agent-deck Hermes hooks")
	fmt.Fprintln(w, "  status       Show Hermes hooks install status")
	fmt.Fprintln(w, "  help         Show this help message")
}

func handleHermesHooksInstall() {
	configDir := getHermesConfigDirForHooks()
	installed, err := session.InjectHermesHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error installing Hermes hooks: %v\n", err)
		os.Exit(1)
	}
	if installed {
		fmt.Println("Hermes hooks installed successfully.")
		fmt.Printf("Config: %s\n", filepath.Join(configDir, "config.yaml"))
	} else {
		fmt.Println("Hermes hooks are already installed.")
	}
}

func handleHermesHooksUninstall() {
	configDir := getHermesConfigDirForHooks()
	removed, err := session.RemoveHermesHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error removing Hermes hooks: %v\n", err)
		os.Exit(1)
	}
	if removed {
		fmt.Println("Hermes hooks removed successfully.")
	} else {
		fmt.Println("No agent-deck Hermes hooks found to remove.")
	}
}

func handleHermesHooksStatus() {
	configDir := getHermesConfigDirForHooks()
	installed := session.CheckHermesHooksInstalled(configDir)
	configPath := filepath.Join(configDir, "config.yaml")

	if installed {
		fmt.Println("Status: INSTALLED")
		fmt.Printf("Config: %s\n", configPath)
	} else {
		fmt.Println("Status: NOT INSTALLED")
		fmt.Println("Run 'agent-deck hermes-hooks install' to install.")
	}
}

func getHermesConfigDirForHooks() string {
	return session.GetHermesConfigDir()
}
