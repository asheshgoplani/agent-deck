package main

import (
	"errors"
	"flag"
	"os"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// cliFeature describes one human CLI subcommand for telemetry: the feature it
// counts by default, and features for specific second arguments.
type cliFeature struct {
	feature telemetry.Feature
	sub     map[string]telemetry.Feature
}

// cliFeatures is the single table of human CLI subcommands that telemetry
// counts. Anything absent (hook handlers, daemons, completion, internal
// helpers) records nothing: they run on every agent turn and would swamp
// the human-driven counts.
var cliFeatures = map[string]cliFeature{
	"add":     {},
	"list":    {},
	"ls":      {},
	"remove":  {},
	"rm":      {},
	"rename":  {feature: "rename"},
	"mv":      {feature: "rename"},
	"status":  {},
	"profile": {feature: "profile_switch"},
	"update":  {feature: "update"},
	"session": {sub: map[string]telemetry.Feature{
		"send": "session_send", "send-keys": "send_keys", "queue": "send_queue", "fork": "fork",
		"restart": "restart", "children": "session_children", "handoff": "session_handoff",
		"context": "session_context", "annotate": "session_annotate", "approve": "session_approve",
		"cleanup": "session_cleanup", "revive": "revive", "window": "window",
	}},
	"fleet":           {sub: map[string]telemetry.Feature{"status": "fleet_status", "recover": "fleet_recover"}},
	"mcp":             {sub: map[string]telemetry.Feature{"attach": "mcp_attach", "detach": "mcp_detach"}},
	"plugin":          {sub: map[string]telemetry.Feature{"install": "plugin_install"}},
	"skill":           {sub: map[string]telemetry.Feature{"attach": "skill_attach"}},
	"group":           {sub: map[string]telemetry.Feature{"create": "group_create", "move": "move_group"}},
	"try":             {feature: "try"},
	"launch":          {feature: "launch"},
	"health":          {feature: "health"},
	"doctor":          {feature: "doctor"},
	"accounts":        {feature: "accounts_switch"},
	"harness":         {feature: "harness"},
	"limits":          {feature: "limits"},
	"conductor":       {sub: map[string]telemetry.Feature{"start": "conductor_start", "setup": "conductor_start", "telegram": "conductor_telegram"}},
	"agents":          {},
	"agent":           {},
	"telegram-doctor": {feature: "conductor_telegram"},
	"watcher":         {feature: "watcher"},
	"openclaw":        {feature: "openclaw"},
	"oc":              {feature: "openclaw"},
	"remote":          {sub: map[string]telemetry.Feature{"add": "remote_add", "attach": "remote_attach"}},
	"remote-agent":    {feature: "remote_agent"},
	"worktree":        {sub: map[string]telemetry.Feature{"create": "worktree_create", "finish": "worktree_finish"}},
	"wt":              {sub: map[string]telemetry.Feature{"create": "worktree_create", "finish": "worktree_finish"}},
	"costs":           {feature: "costs"},
	"events":          {feature: "events"},
	"recall":          {feature: "recall_search", sub: map[string]telemetry.Feature{"timeline": "recall_timeline"}},
	"usage":           {feature: "usage"},
	"config":          {feature: "config_edit"},
	"web":             {feature: "web_ui"},
	"uninstall":       {},
	"migrate-paths":   {feature: "migrate_paths"},
	"hooks":           {sub: map[string]telemetry.Feature{"install": "hooks_install"}},
	"codex-hooks":     {sub: map[string]telemetry.Feature{"install": "hooks_install"}},
	"gemini-hooks":    {sub: map[string]telemetry.Feature{"install": "hooks_install"}},
	"hermes-hooks":    {sub: map[string]telemetry.Feature{"install": "hooks_install"}},
	"cursor-hooks":    {sub: map[string]telemetry.Feature{"install": "hooks_install"}},
	"tmux-hooks":      {sub: map[string]telemetry.Feature{"install": "hooks_install"}},
	"pi-hooks":        {sub: map[string]telemetry.Feature{"install": "hooks_install"}},
	"deepseek":        {feature: "deepseek"},
	"inbox":           {feature: "inbox_drain"},
	"feedback":        {feature: "feedback"},
}

// cliFeatureFor returns the feature a subcommand counts and whether the
// subcommand is counted at all. `--help` invocations are not counted.
func cliFeatureFor(subcommand string, rest []string) (telemetry.Feature, bool) {
	for _, a := range rest {
		if a == "-h" || a == "--help" || a == "help" {
			return "", false
		}
	}
	entry, ok := cliFeatures[subcommand]
	if !ok {
		return "", false
	}
	if len(rest) > 0 {
		if f, ok := entry.sub[rest[0]]; ok {
			return f, true
		}
	}
	return entry.feature, true
}

// cliOutcome holds the command begun at dispatch until it finishes, so its
// feature is counted with the command's outcome rather than before it runs.
var cliOutcome struct {
	mu      sync.Mutex
	pending bool
	feature telemetry.Feature
}

// beginCLITelemetry notes a human CLI subcommand at dispatch. Nothing is
// recorded until finishCLITelemetry.
func beginCLITelemetry(subcommand string, rest []string) {
	f, ok := cliFeatureFor(subcommand, rest)
	if !ok {
		return
	}
	cliOutcome.mu.Lock()
	cliOutcome.pending, cliOutcome.feature = true, f
	cliOutcome.mu.Unlock()
}

// markCLINoop marks the running command as having done nothing (for example
// a restart skipped by the freshness guard): the invocation is still counted,
// its feature is not.
func markCLINoop() {
	cliOutcome.mu.Lock()
	cliOutcome.feature = ""
	cliOutcome.mu.Unlock()
}

// finishCLITelemetry counts the begun command once, as failed when code is
// non-zero (no-op without consent; the telemetry package checks consent, TTY
// and every kill switch).
func finishCLITelemetry(code int) {
	cliOutcome.mu.Lock()
	pending, f := cliOutcome.pending, cliOutcome.feature
	cliOutcome.pending = false
	cliOutcome.mu.Unlock()
	if pending {
		telemetry.CLICommand(f, code != 0)
	}
}

// settleCLITelemetry is deferred by main after beginCLITelemetry: a handler
// that returns succeeded, one that panics failed (the panic continues).
func settleCLITelemetry() {
	if r := recover(); r != nil {
		finishCLITelemetry(2)
		panic(r)
	}
	finishCLITelemetry(0)
}

// exitCLI records the running command's outcome, then exits. CLI code calls
// it instead of os.Exit, which skips deferred calls.
func exitCLI(code int) {
	finishCLITelemetry(code)
	os.Exit(code)
}

// parseCLIFlags parses a flag.ContinueOnError FlagSet with the exit codes of
// flag.ExitOnError (0 for -help, 2 for a bad flag), but exits through exitCLI
// so the invocation is still recorded. flag.ExitOnError calls os.Exit itself.
func parseCLIFlags(fs *flag.FlagSet, args []string) error {
	exitOnFlagError(fs.Parse(args))
	return nil
}

// exitOnFlagError exits like flag.ExitOnError for a non-nil parse error. The
// flag package has already printed the error and usage. A help request counts
// as an invocation, not a use of the command's feature.
func exitOnFlagError(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, flag.ErrHelp) {
		markCLINoop()
		exitCLI(0)
	}
	exitCLI(2)
}
