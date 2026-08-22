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
}

func configuredAccountSlots(config *session.UserConfig) []accountListEntry {
	accounts := make([]accountListEntry, 0)
	if config == nil {
		return accounts
	}
	for name := range config.Profiles {
		if dir := config.GetProfileClaudeConfigDir(name); dir != "" {
			accounts = append(accounts, accountListEntry{Name: name, ConfigDir: dir})
		}
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
	return accounts
}

func handleAccounts(args []string) {
	fs := flag.NewFlagSet("accounts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck accounts [--json]")
		fmt.Fprintln(fs.Output())
		fmt.Fprintln(fs.Output(), "List named account slots configured as [profiles.<name>.claude].config_dir.")
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

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: load config: %v\n", err)
		os.Exit(1)
	}
	accounts := configuredAccountSlots(config)
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(accounts); err != nil {
			fmt.Fprintf(os.Stderr, "Error: encode accounts: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(accounts) == 0 {
		fmt.Println("No named account slots configured.")
		return
	}
	for _, account := range accounts {
		fmt.Printf("%-20s %s\n", account.Name, account.ConfigDir)
	}
}
