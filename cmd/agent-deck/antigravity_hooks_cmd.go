package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handleAntigravityHooks(args []string) {
	if len(args) == 0 {
		printAntigravityHooksUsage(os.Stderr)
		os.Exit(1)
	}

	switch args[0] {
	case "help", "--help", "-h":
		printAntigravityHooksUsage(os.Stdout)
	case "install":
		handleAntigravityHooksInstall()
	case "uninstall":
		handleAntigravityHooksUninstall()
	case "status":
		handleAntigravityHooksStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown antigravity-hooks subcommand: %s\n", args[0])
		printAntigravityHooksUsage(os.Stderr)
		os.Exit(1)
	}
}

func printAntigravityHooksUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck antigravity-hooks <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage Antigravity CLI (agy) hook integration.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  install      Install agent-deck Antigravity hooks")
	fmt.Fprintln(w, "  uninstall    Remove agent-deck Antigravity hooks")
	fmt.Fprintln(w, "  status       Show Antigravity hooks install status")
}

func handleAntigravityHooksInstall() {
	configDir := session.GetAntigravityConfigDir()
	installed, err := session.InjectAntigravityHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error installing Antigravity hooks: %v\n", err)
		os.Exit(1)
	}
	if installed {
		fmt.Println("Antigravity hooks installed successfully.")
		fmt.Printf("Config: %s/hooks.json\n", configDir)
	} else {
		fmt.Println("Antigravity hooks are already installed.")
	}
}

func handleAntigravityHooksUninstall() {
	configDir := session.GetAntigravityConfigDir()
	removed, err := session.RemoveAntigravityHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error removing Antigravity hooks: %v\n", err)
		os.Exit(1)
	}
	if removed {
		fmt.Println("Antigravity hooks removed successfully.")
	} else {
		fmt.Println("No agent-deck Antigravity hooks found to remove.")
	}
}

func handleAntigravityHooksStatus() {
	configDir := session.GetAntigravityConfigDir()
	installed := session.CheckAntigravityHooksInstalled(configDir)
	configPath := filepath.Join(configDir, "hooks.json")

	if installed {
		fmt.Println("Status: INSTALLED")
		fmt.Printf("Config: %s\n", configPath)
	} else {
		fmt.Println("Status: NOT INSTALLED")
		fmt.Println("Run 'agent-deck antigravity-hooks install' to install.")
	}
}
