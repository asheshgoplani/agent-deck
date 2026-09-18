package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleConfig dispatches `agent-deck config` subcommands (#2093).
func handleConfig(profile string, args []string) {
	if len(args) == 0 {
		printConfigHelp()
		os.Exit(1)
	}

	switch args[0] {
	case "show":
		handleConfigShow(profile, args[1:])
	case "help", "--help", "-h":
		printConfigHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown config command: %s\n", args[0])
		fmt.Println()
		printConfigHelp()
		os.Exit(1)
	}
}

func printConfigHelp() {
	fmt.Println("Usage: agent-deck config show --effective [path] [--json]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  show --effective [path]   Print merged [worktree] settings for path (default: cwd),")
	fmt.Println("                            and which file supplied each value (default/global/dir-local).")
	fmt.Println("                            Also known as `config explain` in the #2093 proposal.")
	fmt.Println()
	fmt.Println("Directory-local overrides (#2093): a .agent-deck/config.toml found in the target")
	fmt.Println("directory or an ancestor (up to and including $HOME) can override default_location,")
	fmt.Println("path_template, and sparse_checkout. Nearer files win; unknown keys are refused.")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck config show --effective")
	fmt.Println("  agent-deck config show --effective ~/projects/example/feature-one")
	fmt.Println("  agent-deck config show --effective . --json")
}

// configEffectiveWorktreeJSON is the --json shape for `config show --effective`.
type configEffectiveWorktreeJSON struct {
	Path     string            `json:"path"`
	Worktree map[string]string `json:"worktree"`
	Sources  map[string]string `json:"sources"`
}

func handleConfigShow(_ string, args []string) {
	fs := flag.NewFlagSet("config show", flag.ExitOnError)
	// --effective is accepted, and ignored, for forward compatibility: the
	// merged view is currently the only one `config show` knows how to print.
	_ = fs.Bool("effective", false, "Show the merged effective settings (currently the only supported view)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck config show --effective [path] [--json]")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	targetDir := "."
	if rest := fs.Args(); len(rest) > 0 {
		targetDir = rest[0]
	}
	absDir, err := filepath.Abs(session.ExpandPath(targetDir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid path %q: %v\n", targetDir, err)
		os.Exit(1)
	}

	settings, sources, err := session.ResolveWorktreeSettingsForDir(absDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Display order, which also matches the sorted key order encoding/json uses
	// for the --json map.
	keys := []string{
		session.WorktreeKeyDefaultLocation,
		session.WorktreeKeyPathTemplate,
		session.WorktreeKeySparseCheckout,
	}
	worktree := map[string]string{
		session.WorktreeKeyDefaultLocation: settings.DefaultLocation,
		session.WorktreeKeyPathTemplate:    settings.Template(),
		session.WorktreeKeySparseCheckout:  settings.SparseCheckout,
	}

	if *jsonOutput {
		out := configEffectiveWorktreeJSON{Path: absDir, Worktree: worktree, Sources: sources}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to encode JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}

	fmt.Printf("Effective [worktree] settings for %s:\n\n", absDir)
	for _, k := range keys {
		value := worktree[k]
		if value == "" {
			value = `""`
		}
		fmt.Printf("  %-17s = %-24s (source: %s)\n", k, value, sources[k])
	}
}
