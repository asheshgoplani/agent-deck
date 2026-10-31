package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func handleKimiHooks(args []string) {
	if len(args) == 0 {
		printKimiHooksUsage(os.Stderr)
		exitCLI(1)
	}
	if hooksHelpRequested(args) {
		printKimiHooksUsage(os.Stdout)
		return
	}

	switch args[0] {
	case "help", "--help", "-h":
		printKimiHooksUsage(os.Stdout)
	case "install":
		handleKimiHooksInstall()
	case "uninstall":
		handleKimiHooksUninstall()
	case "status":
		handleKimiHooksStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown kimi-hooks subcommand: %s\n", args[0])
		printKimiHooksUsage(os.Stderr)
		exitCLI(1)
	}
}

func printKimiHooksUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck kimi-hooks <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage Kimi Code hook integration.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  install      Install agent-deck Kimi hooks")
	fmt.Fprintln(w, "  uninstall    Remove agent-deck Kimi hooks")
	fmt.Fprintln(w, "  status       Show Kimi hooks install status")
	fmt.Fprintln(w, "  help         Show this help message")
}

func handleKimiHooksInstall() {
	configDir := session.GetKimiConfigDir()
	installed, err := session.InjectKimiHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error installing Kimi hooks: %v\n", err)
		exitCLI(1)
	}
	if err := setKimiHooksDecision("accepted"); err != nil {
		fmt.Fprintf(os.Stderr, "Error recording Kimi hooks choice: %v\n", err)
		exitCLI(1)
	}
	if installed {
		fmt.Println("Kimi hooks installed successfully.")
		fmt.Printf("Config: %s\n", filepath.Join(configDir, "config.toml"))
	} else {
		fmt.Println("Kimi hooks are already installed.")
	}
}

func handleKimiHooksUninstall() {
	configDir := session.GetKimiConfigDir()
	removed, err := session.RemoveKimiHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error removing Kimi hooks: %v\n", err)
		exitCLI(1)
	}
	if err := setKimiHooksDecision("declined"); err != nil {
		fmt.Fprintf(os.Stderr, "Error recording Kimi hooks choice: %v\n", err)
		exitCLI(1)
	}
	if removed {
		fmt.Println("Kimi hooks removed successfully.")
	} else {
		fmt.Println("No agent-deck Kimi hooks found to remove.")
	}
}

func handleKimiHooksStatus() {
	configDir := session.GetKimiConfigDir()
	configPath := filepath.Join(configDir, "config.toml")
	fmt.Printf("Config: %s\n", configPath)
	if session.CheckKimiHooksInstalled(configDir) {
		fmt.Println("Status: INSTALLED")
		return
	}
	fmt.Println("Status: NOT INSTALLED")
	fmt.Println("Run 'agent-deck kimi-hooks install' to install.")
}

func setKimiHooksDecision(decision string) error {
	if db := statedb.GetGlobal(); db != nil {
		return db.SetMeta("kimi_hooks_prompted", decision)
	}
	profile, err := session.ResolveProfileForStorage("")
	if err != nil {
		return fmt.Errorf("resolve effective profile: %w", err)
	}
	dbPath, err := session.GetDBPathForProfile(profile)
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open statedb: %w", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		return fmt.Errorf("migrate statedb: %w", err)
	}
	if err := db.BindProfile(statedb.ProfileNameForDir(filepath.Dir(dbPath))); err != nil {
		return fmt.Errorf("bind statedb profile: %w", err)
	}
	return db.SetMeta("kimi_hooks_prompted", decision)
}
