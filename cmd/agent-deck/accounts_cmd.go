package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type accountListEntry struct {
	Name      string `json:"name"`
	ConfigDir string `json:"config_dir"`
	// Exists reports whether ConfigDir is present on disk. A configured
	// account whose directory has never been created is not logged in yet:
	// `CLAUDE_CONFIG_DIR=<dir> claude` then `/login` creates it.
	Exists bool `json:"exists"`
}

func configuredAccountSlots(config *session.UserConfig) []accountListEntry {
	return configuredAccountSlotsForHarness(config, "claude")
}

// accountsHarnessOK canonicalises the --harness value to the harness families
// that have named account slots (a config_dir / codex_home binding).
func accountsHarnessOK(harness string) (string, error) {
	switch canonical := session.CanonicalSwitchHarnessForUI(harness); canonical {
	case "claude", "codex":
		return canonical, nil
	default:
		return "", fmt.Errorf("harness %q has no named account slots (supported: claude, codex)", harness)
	}
}

// configuredAccountSlotsForHarness lists the slots bound for one harness
// family: [profiles.<name>.claude].config_dir or [profiles.<name>.codex].config_dir.
func configuredAccountSlotsForHarness(config *session.UserConfig, harness string) []accountListEntry {
	accounts := make([]accountListEntry, 0)
	if config == nil {
		return accounts
	}
	dirFor := config.GetProfileClaudeConfigDir
	if harness == "codex" {
		dirFor = config.GetProfileCodexConfigDir
	}
	for name := range config.Profiles {
		if dir := dirFor(name); dir != "" {
			info, statErr := os.Stat(dir)
			accounts = append(accounts, accountListEntry{
				Name:      name,
				ConfigDir: dir,
				Exists:    statErr == nil && info.IsDir(),
			})
		}
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
	return accounts
}

func handleAccounts(args []string) {
	fs := flag.NewFlagSet("accounts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	harness := fs.String("harness", "claude", "Harness family whose slots to list: claude ([profiles.<name>.claude].config_dir) or codex ([profiles.<name>.codex].config_dir)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck accounts [--harness claude|codex] [--json]")
		fmt.Fprintln(fs.Output())
		fmt.Fprintln(fs.Output(), "List named account slots configured as [profiles.<name>.claude].config_dir")
		fmt.Fprintln(fs.Output(), "(or [profiles.<name>.codex].config_dir with --harness codex).")
		fmt.Fprintln(fs.Output())
		fmt.Fprintln(fs.Output(), "These names are the valid values for `launch --account`, `session set <id>")
		fmt.Fprintln(fs.Output(), "account` and `session switch-account`, and for the account row in the TUI's")
		fmt.Fprintln(fs.Output(), "New Session and Edit Session dialogs.")
		fmt.Fprintln(fs.Output())
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return
		}
		os.Exit(2)
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "accounts does not accept positional arguments: %s\n", strings.Join(fs.Args(), " "))
		os.Exit(2)
	}

	harnessFamily, err := accountsHarnessOK(*harness)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}
	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: load config: %v\n", err)
		os.Exit(1)
	}
	accounts := configuredAccountSlotsForHarness(config, harnessFamily)
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(accounts); err != nil {
			fmt.Fprintf(os.Stderr, "Error: encode accounts: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(accounts) == 0 {
		fmt.Println("No named account slots configured.")
		fmt.Printf("Add one as [profiles.<name>.%s].config_dir in %s.\n", harnessFamily, effectiveUserConfigPathForHelp())
		return
	}
	for _, account := range accounts {
		status := "ok"
		if !account.Exists {
			status = "not created — log in with: CLAUDE_CONFIG_DIR=" + account.ConfigDir + " claude"
			if harnessFamily == "codex" {
				status = "not created — log in with: CODEX_HOME=" + account.ConfigDir + " codex"
			}
		}
		fmt.Printf("%-20s %-40s %s\n", account.Name, account.ConfigDir, status)
	}
}
