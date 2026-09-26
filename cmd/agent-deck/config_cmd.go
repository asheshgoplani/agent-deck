package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	case "get":
		handleConfigGet(args[1:])
	case "set":
		handleConfigSet(args[1:])
	case "schema":
		handleConfigSchema(args[1:])
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
	fmt.Println("       agent-deck config get <key> [--json]")
	fmt.Println("       agent-deck config set <key> <value> [--json]")
	fmt.Println("       agent-deck config schema [--json]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  get <key>                 Print one setting's effective value (its default when unset).")
	fmt.Println("  set <key> <value>         Write one setting to config.toml with the TUI Settings writer;")
	fmt.Println("                            prints the value read back. Lists are comma-separated.")
	fmt.Println("  schema                    Every settable key: section, label, help, type, allowed values,")
	fmt.Println("                            default, restart_required. The keys are the TUI Settings rows")
	fmt.Println("                            plus recall.enabled, macapp.*, core.daemon (docs/macapp-core.md).")
	fmt.Println("  show --effective [path]   Print merged [worktree] settings for path (default: cwd),")
	fmt.Println("                            and which file supplied each value (default/global/dir-local).")
	fmt.Println("                            Also known as `config explain` in the #2093 proposal.")
	fmt.Println()
	fmt.Println("Directory-local overrides (#2093): a .agent-deck/config.toml found in the target")
	fmt.Println("directory or an ancestor (up to and including $HOME) can override default_location,")
	fmt.Println("path_template, and sparse_checkout. Nearer files win; unknown keys are refused.")
	fmt.Println()
	fmt.Println("A dir-local file may come from a checkout you don't fully trust, so its")
	fmt.Println("default_location/path_template values are bounded: an absolute path, a \"~\"-relative")
	fmt.Println("path, or one that resolves outside the directory containing the outermost dir-local")
	fmt.Println("config file for the target path is refused and falls back to the next source (an")
	fmt.Println("outer dir-local file, then global config, then the built-in default). Global config")
	fmt.Println("and --location/--template flags are unaffected and remain fully trusted. A refused")
	fmt.Println("value and its fallback are shown by `config show --effective`.")
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
	// Rejections lists, per key, any dir-local default_location/path_template
	// value that was refused (untrusted value outside the workspace boundary,
	// absolute, "~"-relative, or containing a literal ".." segment) and what
	// it fell back to. A key with no rejections is omitted.
	Rejections map[string][]string `json:"rejections,omitempty"`
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

	settings, sources, rejections, err := session.ResolveWorktreeSettingsForDir(absDir)
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
		out := configEffectiveWorktreeJSON{Path: absDir, Worktree: worktree, Sources: sources, Rejections: rejections}
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
		for _, reason := range rejections[k] {
			fmt.Printf("      rejected: %s\n", reason)
		}
	}
}

// configValueJSON is the --json shape of `config get` and `config set`.
type configValueJSON struct {
	Key             string `json:"key"`
	Value           any    `json:"value"`
	Type            string `json:"type"`
	Default         any    `json:"default"`
	RestartRequired bool   `json:"restart_required"`
	Path            string `json:"path"`
}

func configKeyFlags(name string, args []string) (*flag.FlagSet, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(2)
	}
	return fs, jsonOutput
}

func printConfigValue(k session.ConfigKey, v any, jsonOutput bool) {
	if jsonOutput {
		path, _ := session.GetUserConfigPath()
		var def any
		for _, sk := range session.ConfigKeys() {
			if sk.Key == k.Key {
				def = sk.Default
			}
		}
		b, _ := json.MarshalIndent(configValueJSON{Key: k.Key, Value: v, Type: k.Type, Default: def, RestartRequired: k.RestartRequired, Path: path}, "", "  ")
		fmt.Println(string(b))
		return
	}
	if list, ok := v.([]string); ok {
		fmt.Println(strings.Join(list, ","))
		return
	}
	fmt.Println(v)
}

func handleConfigGet(args []string) {
	fs, jsonOutput := configKeyFlags("config get", args)
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() != 1 {
		out.Error("usage: agent-deck config get <key> [--json]", ErrCodeInvalidOperation)
		os.Exit(2)
	}
	k, ok := session.LookupConfigKey(fs.Arg(0))
	if !ok {
		out.Error(fmt.Sprintf("unknown config key %q (see agent-deck config schema)", fs.Arg(0)), ErrCodeNotFound)
		os.Exit(2)
	}
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	printConfigValue(k, k.Get(cfg), *jsonOutput)
}

func handleConfigSet(args []string) {
	fs, jsonOutput := configKeyFlags("config set", args)
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() != 2 {
		out.Error("usage: agent-deck config set <key> <value> [--json]", ErrCodeInvalidOperation)
		os.Exit(2)
	}
	k, v, err := session.SetConfigValue(fs.Arg(0), fs.Arg(1))
	if err != nil {
		code := ErrCodeInvalidOperation
		if k.Key == "" {
			code = ErrCodeNotFound
		}
		out.Error(err.Error(), code)
		os.Exit(2)
	}
	printConfigValue(k, v, *jsonOutput)
}

func handleConfigSchema(args []string) {
	_, jsonOutput := configKeyFlags("config schema", args)
	keys := session.ConfigKeys()
	if *jsonOutput {
		b, _ := json.MarshalIndent(map[string]any{"keys": keys}, "", "  ")
		fmt.Println(string(b))
		return
	}
	section := ""
	for _, k := range keys {
		if k.Section != section {
			section = k.Section
			fmt.Printf("\n[%s]\n", section)
		}
		fmt.Printf("  %-34s %-6s %s\n", k.Key, k.Type, k.Help)
	}
}
