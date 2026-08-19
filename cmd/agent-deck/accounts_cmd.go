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

// handleAccounts implements `agent-deck accounts …` — the agent-facing view of
// account selection.
//
// It exists because an agent choosing an account has no way to see the config:
// it needs the account names, which tool each one belongs to, where its home
// is, whether that home is actually logged in, and which live sessions are
// already sitting on it (so a fleet can spread itself across accounts instead
// of piling onto one and hitting a quota wall).
func handleAccounts(profile string, args []string) {
	if len(args) == 0 {
		printAccountsHelp()
		return
	}
	switch args[0] {
	case "list", "ls":
		handleAccountsList(profile, args[1:])
	case "help", "--help", "-h":
		printAccountsHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown accounts subcommand: %s\n\n", args[0])
		printAccountsHelp()
		os.Exit(1)
	}
}

func printAccountsHelp() {
	fmt.Println("Usage: agent-deck accounts <subcommand>")
	fmt.Println()
	fmt.Println("Inspect the account slots configured in " + effectiveUserConfigPathForHelp() + ".")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  list    List configured accounts per tool, with homes, login state, and live sessions")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck accounts list")
	fmt.Println("  agent-deck accounts list --json")
	fmt.Println("  agent-deck accounts list --tool codex")
}

// accountJSON is the `accounts list --json` row. Stable shape: agents parse it.
type accountJSON struct {
	Name       string   `json:"name"`
	Tool       string   `json:"tool"`
	EnvVar     string   `json:"env_var"`
	Home       string   `json:"home"`
	HomeExists bool     `json:"home_exists"`
	Login      string   `json:"login"`
	Sessions   []string `json:"sessions"`
	SessionIDs []string `json:"session_ids"`
	Default    bool     `json:"default"`
}

func handleAccountsList(profile string, args []string) {
	fs := flag.NewFlagSet("accounts list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	toolFilter := fs.String("tool", "", "Only show accounts usable by this tool (e.g. claude, codex)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck accounts list [options]")
		fmt.Println()
		fmt.Println("List every account slot configured in " + effectiveUserConfigPathForHelp() + ".")
		fmt.Println()
		fmt.Println("An account is one [profiles.<name>.<tool>] block naming a config home.")
		fmt.Println("A session launched with --account <name> runs with that tool's home env var")
		fmt.Println("pointed at that directory, which is what makes it a separate login.")
		fmt.Println()
		fmt.Println("Login state is a cheap on-disk check (a credential file in the home); it is")
		fmt.Println("never a network call, so it can lag a token that expired server-side.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck accounts list")
		fmt.Println("  agent-deck accounts list --json")
		fmt.Println("  agent-deck accounts list --tool codex --json")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	cfg, cfgErr := session.LoadUserConfig()
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to read %s: %v\n", effectiveUserConfigPathForHelp(), cfgErr)
		os.Exit(1)
	}

	// Live sessions are best-effort: `accounts list` must still answer the
	// config question on a machine whose state db is unreadable.
	var instances []*session.Instance
	if _, loaded, _, err := loadSessionData(profile); err == nil {
		instances = loaded
	}
	inUse := session.AccountsInUse(instances)

	families := session.AccountFamilies()
	if filter := strings.TrimSpace(*toolFilter); filter != "" {
		family, ok := session.AccountFamilyForTool(filter)
		if !ok {
			fmt.Fprintf(os.Stderr, "Error: tool %q has no account family (its config home is not selectable per session)\n", filter)
			os.Exit(1)
		}
		families = []session.AccountFamily{family}
	}

	rows := make([]accountJSON, 0, 8)
	for _, family := range families {
		for _, acct := range session.AccountsForFamily(cfg, family) {
			titles, ids := sessionsOnAccount(inUse[acct.Name], family)
			rows = append(rows, accountJSON{
				Name:       acct.Name,
				Tool:       acct.Family,
				EnvVar:     acct.EnvVar,
				Home:       acct.Home,
				HomeExists: acct.HomeExists,
				Login:      string(acct.Login),
				Sessions:   titles,
				SessionIDs: ids,
				Default:    cfg != nil && strings.TrimSpace(cfg.DefaultAccount) == acct.Name,
			})
		}
	}

	if *jsonOutput {
		// Always an array, never null — an agent branching on length should
		// not have to special-case a nil.
		out, err := json.MarshalIndent(map[string]any{"accounts": rows}, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
		return
	}

	if len(rows) == 0 {
		fmt.Println("No accounts configured.")
		fmt.Println()
		fmt.Printf("Add one to %s, e.g.:\n", effectiveUserConfigPathForHelp())
		fmt.Println()
		fmt.Println("  [profiles.work.claude]")
		fmt.Println("  config_dir = \"~/.agent-accounts/claude/work\"")
		fmt.Println()
		fmt.Println("  [profiles.codex-work.codex]")
		fmt.Println("  codex_home = \"~/.agent-accounts/codex/work\"")
		return
	}

	printAccountsTable(rows)
}

// sessionsOnAccount reduces the live instances bound to an account to titles
// and ids, keeping only those whose tool actually belongs to this account's
// family. One account name can legitimately carry both a claude and a codex
// block, and a claude session on it should not be listed under the codex row.
func sessionsOnAccount(instances []*session.Instance, family session.AccountFamily) ([]string, []string) {
	titles := make([]string, 0, len(instances))
	ids := make([]string, 0, len(instances))
	for _, inst := range instances {
		if inst == nil || family.Matches == nil || !family.Matches(inst.Tool) {
			continue
		}
		titles = append(titles, inst.Title)
		ids = append(ids, inst.ID)
	}
	sort.Strings(titles)
	sort.Strings(ids)
	return titles, ids
}

func printAccountsTable(rows []accountJSON) {
	nameW, homeW := len("ACCOUNT"), len("HOME")
	for _, r := range rows {
		if len(r.Name) > nameW {
			nameW = len(r.Name)
		}
		if len(r.Home) > homeW {
			homeW = len(r.Home)
		}
	}

	current := ""
	for _, r := range rows {
		if r.Tool != current {
			if current != "" {
				fmt.Println()
			}
			current = r.Tool
			fmt.Printf("%s  (%s)\n", strings.ToUpper(r.Tool), r.EnvVar)
			fmt.Printf("  %-*s  %-*s  %-11s  %s\n", nameW, "ACCOUNT", homeW, "HOME", "LOGIN", "SESSIONS")
		}

		login := r.Login
		if !r.HomeExists {
			login = "no home yet"
		}
		sessions := "-"
		if len(r.Sessions) > 0 {
			sessions = strings.Join(r.Sessions, ", ")
		}
		marker := " "
		if r.Default {
			marker = "*"
		}
		fmt.Printf("%s %-*s  %-*s  %-11s  %s\n", marker, nameW, r.Name, homeW, r.Home, login, sessions)
	}

	fmt.Println()
	fmt.Println("Use: agent-deck launch . -c <tool> --account <name>   (or pick one in the TUI new-session dialog)")
	for _, r := range rows {
		if r.Default {
			fmt.Println("* = default_account")
			break
		}
	}
}
