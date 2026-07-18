package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handleCursorHooks(args []string) {
	if len(args) == 0 {
		printCursorHooksUsage(os.Stderr)
		os.Exit(1)
	}

	switch args[0] {
	case "help", "--help", "-h":
		printCursorHooksUsage(os.Stdout)
	case "install":
		handleCursorHooksInstall()
	case "uninstall":
		handleCursorHooksUninstall()
	case "status":
		handleCursorHooksStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown cursor-hooks subcommand: %s\n", args[0])
		printCursorHooksUsage(os.Stderr)
		os.Exit(1)
	}
}

func printCursorHooksUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck cursor-hooks <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage Cursor Agent CLI hook integration.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  install      Install agent-deck Cursor hooks")
	fmt.Fprintln(w, "  uninstall    Remove agent-deck Cursor hooks")
	fmt.Fprintln(w, "  status       Show Cursor hooks install status")
}

func handleCursorHooksInstall() {
	configDir := getCursorConfigDirForHooks()
	installed, err := session.InjectCursorHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error installing Cursor hooks: %v\n", err)
		os.Exit(1)
	}
	// Explicit install clears the durable opt-out so TUI startup may
	// auto-reinstall again (issue #1672).
	if err := session.SetCursorHooksEnabled(true); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not update config.toml: %v\n", err)
	}
	if installed {
		fmt.Println("Cursor hooks installed successfully.")
		fmt.Printf("Config: %s/hooks.json\n", configDir)
	} else {
		fmt.Println("Cursor hooks are already installed.")
	}
}

func handleCursorHooksUninstall() {
	configDir := getCursorConfigDirForHooks()
	removed, err := session.RemoveCursorHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error removing Cursor hooks: %v\n", err)
		os.Exit(1)
	}
	// Persist the opt-out so TUI startup does not silently reinstall the
	// hooks on the next launch (issue #1672).
	if err := session.SetCursorHooksEnabled(false); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not persist opt-out to config.toml: %v\n", err)
		fmt.Fprintln(os.Stderr, "TUI startup may reinstall the hooks. Set [cursor] hooks_enabled = false manually.")
	} else {
		fmt.Println("Auto-install disabled ([cursor] hooks_enabled = false in config.toml).")
		fmt.Println("Run 'agent-deck cursor-hooks install' to re-enable.")
	}
	if removed {
		fmt.Println("Cursor hooks removed successfully.")
	} else {
		fmt.Println("No agent-deck Cursor hooks found to remove.")
	}
}

func handleCursorHooksStatus() {
	configDir := getCursorConfigDirForHooks()
	installed := session.CheckCursorHooksInstalled(configDir)
	configPath := filepath.Join(configDir, "hooks.json")

	if installed {
		fmt.Println("Status: INSTALLED")
		fmt.Printf("Config: %s\n", configPath)
	} else {
		fmt.Println("Status: NOT INSTALLED")
		fmt.Println("Run 'agent-deck cursor-hooks install' to install.")
	}
	if cfg, err := session.LoadUserConfig(); err == nil && cfg != nil && !cfg.Cursor.GetHooksEnabled() {
		fmt.Println("Auto-install: DISABLED ([cursor] hooks_enabled = false in config.toml)")
	}
}

func getCursorConfigDirForHooks() string {
	return session.GetCursorConfigDir()
}
