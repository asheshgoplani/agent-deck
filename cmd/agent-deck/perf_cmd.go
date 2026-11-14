package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/optiming"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// cliTimedSubcommands lists, per command, the subcommands timed on their
// own (cli.session.send, cli.session.start). Any other first argument (a
// session id, a group name) is never put in the op name: the invocation is
// timed as cli.<command>.
var cliTimedSubcommands = map[string][]string{
	"session": {"start", "stop", "restart", "send", "send-keys", "output", "show", "list", "fork", "remove",
		"cleanup", "prune", "archive", "unarchive", "revive", "handoff", "children", "queue", "context",
		"annotate", "approve", "window", "current", "focus", "set", "wait"},
	"group":     {"create", "delete", "move", "list", "rename"},
	"conductor": {"setup", "start", "stop", "status", "list", "teardown", "telegram", "notify", "heartbeat-tick"},
	"events":    {"publish", "list"},
	"profile":   {"list", "create", "delete", "default"},
	"remote":    {"add", "remove", "list", "sessions", "update"},
	"worktree":  {"create", "finish", "list", "cleanup"},
	"wt":        {"create", "finish", "list", "cleanup"},
	"mcp":       {"list", "attach", "detach"},
	"skill":     {"list", "attach", "detach"},
	"fleet":     {"status", "recover"},
	"plugin":    {"install", "list"},
	"telemetry": {"status", "on", "off", "enable", "disable", "preview", "show-last", "reset-id"},
	"inbox":     {"drain", "export", "writer-status"},
	"msg":       {"read", "peek", "ack", "export", "stats"},
	"accounts":  {"list", "switch"},
}

// cliUntimed are commands that run until the user leaves them (servers,
// daemons, attaches, follows): their lifetime is not an operation's speed.
var cliUntimed = map[string]bool{
	"web": true, "daemon": true, "notify-daemon": true, "mcp-proxy": true, "watcher": true,
	"cli.session.attach": true, "cli.remote.attach": true, "cli.events.follow": true,
	"cli.session.wait": true, "cli.conductor.start": true,
}

// cliTimingOp names the timed operation of one CLI invocation, built only
// from registered command words: never titles, paths or messages.
func cliTimingOp(subcommand string, rest []string) (string, bool) {
	if !commandRegistry[subcommand] || cliUntimed[subcommand] || strings.HasPrefix(subcommand, "-") {
		return "", false
	}
	op := "cli." + subcommand
	if len(rest) > 0 && slices.Contains(cliTimedSubcommands[subcommand], rest[0]) {
		op += "." + rest[0]
	} else if len(rest) > 0 && (rest[0] == "attach" || rest[0] == "follow") {
		op += "." + rest[0]
	}
	if cliUntimed[op] {
		return "", false
	}
	return op, true
}

// enableTimingsFromConfig applies `[logs] timings = true` (config.toml is
// cached after the first load).
func enableTimingsFromConfig() {
	if cfg, err := session.LoadUserConfig(); err == nil && cfg != nil && cfg.Logs.Timings {
		optiming.Enable()
	}
}

var cliTiming struct {
	mu     sync.Mutex
	finish func(failed bool)
}

// beginCLITiming starts the wall-clock span of a CLI invocation (no-op unless
// timings are on). finishCLITiming ends it with the exit code.
func beginCLITiming(subcommand string, rest []string) {
	if !optiming.Enabled() {
		return
	}
	op, ok := cliTimingOp(subcommand, rest)
	if !ok {
		return
	}
	cliTiming.mu.Lock()
	cliTiming.finish = optiming.StartResult(op)
	cliTiming.mu.Unlock()
}

func finishCLITiming(code int) {
	cliTiming.mu.Lock()
	finish := cliTiming.finish
	cliTiming.finish = nil
	cliTiming.mu.Unlock()
	if finish != nil {
		finish(code != 0)
	}
}

func handlePerf(args []string) {
	fs := flag.NewFlagSet("perf", flag.ContinueOnError)
	fs.SetOutput(helpOutput(args, os.Stdout, os.Stderr))
	jsonOutput := fs.Bool("json", false, "Output the per-operation summary as JSON")
	since := fs.Duration("since", 24*time.Hour, "Only count operations finished within this window")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck perf [--json] [--since 24h]\n\nHow fast each operation was on this machine: count, p50, p95 and max per operation, against its target p95.\nRecording is off by default. Turn it on with AGENTDECK_TIMINGS=1 or [logs] timings = true in config.toml;\nevery agent-deck process (CLI, TUI, web, notify daemon) then appends one line per operation to timings.jsonl\nin the cache directory. Nothing is sent anywhere. Benchmarks: docs/performance.md.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return
		}
		exitCLI(2)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "perf does not accept positional arguments")
		exitCLI(2)
	}
	path, err := optiming.Path()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cache dir: %v\n", err)
		exitCLI(1)
	}
	entries, err := optiming.ReadFile(path, time.Now().Add(-*since))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: read %s: %v\n", path, err)
		exitCLI(1)
	}
	sum := optiming.Summarize(entries)
	if *jsonOutput {
		out := struct {
			File       string             `json:"file"`
			Recording  bool               `json:"recording"`
			SinceHours float64            `json:"since_hours"`
			Operations []optiming.Summary `json:"operations"`
		}{path, optiming.Enabled(), since.Hours(), sum}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}
	if len(sum) == 0 {
		fmt.Printf("No operation timings in the last %s (%s).\n", *since, path)
		if !optiming.Enabled() {
			fmt.Println("Recording is off. Turn it on with AGENTDECK_TIMINGS=1 or [logs] timings = true in config.toml.")
		}
		return
	}
	fmt.Printf("Operation timings, last %s (%s):\n\n", *since, path)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "OPERATION\tCOUNT\tFAILED\tP50\tP95\tMAX\tTARGET P95\t")
	for _, s := range sum {
		target, verdict := "-", ""
		if s.Target > 0 {
			target = fmtMS(s.Target)
			verdict = "ok"
			if s.Over {
				verdict = "OVER"
			}
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\n", s.Op, s.Count, s.Fails, fmtMS(s.P50MS), fmtMS(s.P95MS), fmtMS(s.MaxMS), target, verdict)
	}
	_ = tw.Flush()
}

func fmtMS(ms float64) string {
	switch {
	case ms >= 10000:
		return fmt.Sprintf("%.0fs", ms/1000)
	case ms >= 1000:
		return fmt.Sprintf("%.1fs", ms/1000)
	case ms >= 10:
		return fmt.Sprintf("%.0fms", ms)
	default:
		return fmt.Sprintf("%.1fms", ms)
	}
}
