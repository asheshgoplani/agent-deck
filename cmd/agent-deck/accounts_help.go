package main

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Shared account help text for the commands that can select one (`add`,
// `launch`, `session fork`).
//
// Every function here is gated on presence: with no [profiles.<name>.<tool>]
// home configured anywhere, they print nothing and return "", so a zero-config
// user's `--help` output is byte-identical to the pre-feature one.

// printAccountsHelpBlock writes the "Accounts:" section of a command's usage,
// listing what the user has actually configured. Silent when nothing is.
func printAccountsHelpBlock() {
	cfg, err := session.LoadUserConfig()
	if err != nil || !session.HasAnyAccounts(cfg) {
		return
	}

	fmt.Println()
	fmt.Println("Accounts:")
	fmt.Println("  --account <name> picks the config home the tool launches against.")
	for _, family := range session.AccountFamilies() {
		accounts := session.AccountsForFamily(cfg, family)
		if len(accounts) == 0 {
			continue
		}
		names := make([]string, 0, len(accounts))
		for _, a := range accounts {
			names = append(names, a.Name)
		}
		fmt.Printf("  %-9s (%s): %s\n", family.Name, family.EnvVar, strings.Join(names, ", "))
	}
	fmt.Println("  Full detail incl. login state: agent-deck accounts list")
}

// accountNamesHint renders the accounts valid for one tool as a comma-separated
// list for an error or warning message.
func accountNamesHint(tool string) string {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return "none configured"
	}
	names := session.AccountNamesForTool(cfg, tool)
	if len(names) == 0 {
		return "none configured"
	}
	return strings.Join(names, ", ")
}

// forkCfgForAccounts loads the user config for the fork account check, tolerating
// a read error by returning nil (the check then reports "no home configured",
// which is the honest answer when the config cannot be read).
func forkCfgForAccounts() *session.UserConfig {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return nil
	}
	return cfg
}
