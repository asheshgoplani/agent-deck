package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handleGeminiHooks(args []string) {
	if len(args) == 0 {
		printGeminiHooksUsage(os.Stderr)
		os.Exit(1)
	}

	switch args[0] {
	case "help", "--help", "-h":
		printGeminiHooksUsage(os.Stdout)
	case "install":
		handleGeminiHooksInstall()
	case "uninstall":
		handleGeminiHooksUninstall()
	case "status":
		handleGeminiHooksStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown gemini-hooks subcommand: %s\n", args[0])
		printGeminiHooksUsage(os.Stderr)
		os.Exit(1)
	}
}

func printGeminiHooksUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck gemini-hooks <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage Gemini CLI hook integration.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  install      Install agent-deck Gemini hooks")
	fmt.Fprintln(w, "  uninstall    Remove agent-deck Gemini hooks")
	fmt.Fprintln(w, "  status       Show Gemini hooks install status")
}

func handleGeminiHooksInstall() {
	configDir := getGeminiConfigDirForHooks()
	installed, err := session.InjectGeminiHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error installing Gemini hooks: %v\n", err)
		os.Exit(1)
	}
	if installed {
		fmt.Println("Gemini hooks installed successfully.")
		fmt.Printf("Config: %s/settings.json\n", configDir)
	} else {
		fmt.Println("Gemini hooks are already installed.")
	}
}

func handleGeminiHooksUninstall() {
	configDir := getGeminiConfigDirForHooks()
	removed, err := session.RemoveGeminiHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error removing Gemini hooks: %v\n", err)
		os.Exit(1)
	}
	if removed {
		fmt.Println("Gemini hooks removed successfully.")
	} else {
		fmt.Println("No agent-deck Gemini hooks found to remove.")
	}
}

func handleGeminiHooksStatus() {
	configDir := getGeminiConfigDirForHooks()
	installed := session.CheckGeminiHooksInstalled(configDir)
	configPath := filepath.Join(configDir, "settings.json")

	if installed {
		fmt.Println("Status: INSTALLED")
		fmt.Printf("Config: %s\n", configPath)
	} else {
		fmt.Println("Status: NOT INSTALLED")
		fmt.Println("Run 'agent-deck gemini-hooks install' to install.")
	}
}

func getGeminiConfigDirForHooks() string {
	return session.GetGeminiConfigDir()
}
