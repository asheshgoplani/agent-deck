package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

const eventsUsage = "Usage: agent-deck events <follow|stats>"

// handleEvents dispatches `agent-deck events ...`. Registered as a plain
// command in cmd/agent-deck rather than through internal/core's registry:
// the registry from slice 1 (branch core/registry-slice1-20260922) does not
// build on top of this branch — see docs/events.md and RESULTS.md for why —
// so this follows the existing plain-dispatch pattern used by every other
// subcommand in main.go.
func handleEvents(profile string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, eventsUsage)
		os.Exit(1)
	}
	switch args[0] {
	case "follow":
		handleEventsFollow(args[1:])
	case "stats":
		handleEventsStats(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown events subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, eventsUsage)
		os.Exit(1)
	}
}

// handleEventsFollow implements `agent-deck events follow --json [--after <cursor>]`.
// It streams one canonical-JSON frame per line to stdout, oldest first, and
// keeps streaming newly published frames until interrupted (Ctrl-C /
// SIGTERM) or the bus reports an error (e.g. --after older than the
// retained log). --json is accepted for symmetry with every other agent-deck
// command's envelope convention; NDJSON is the only output shape this
// command has, so the flag doesn't change anything.
func handleEventsFollow(args []string) {
	fs := flag.NewFlagSet("agent-deck events follow", flag.ExitOnError)
	afterFlag := fs.Uint64("after", 0, "resume after this cursor (0 = from the beginning of the retained log)")
	_ = fs.Bool("json", true, "stream NDJSON frames (always on; kept for CLI symmetry)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck events follow --json [--after <cursor>]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bus := events.Default()
	sub, err := bus.Subscribe(ctx, events.Cursor(*afterFlag))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: events follow: %v\n", err)
		os.Exit(1)
	}

	enc := os.Stdout
	for frame := range sub.Frames() {
		line, err := frame.CanonicalJSON()
		if err != nil {
			continue
		}
		line = append(line, '\n')
		if _, err := enc.Write(line); err != nil {
			os.Exit(1)
		}
	}
	if err := sub.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: events follow: %v\n", err)
		os.Exit(1)
	}
}

// handleEventsStats implements `agent-deck events stats --json`.
func handleEventsStats(args []string) {
	fs := flag.NewFlagSet("agent-deck events stats", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "print stats as JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck events stats [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	stats := events.Default().Stats()
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(stats); err != nil {
			fmt.Fprintf(os.Stderr, "Error: encode stats: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("enabled:   %v\n", stats.Enabled)
	fmt.Printf("dir:       %s\n", stats.Dir)
	fmt.Printf("cursor:    %d\n", stats.Cursor)
	fmt.Printf("published: %d\n", stats.Published)
	fmt.Printf("written:   %d\n", stats.Written)
	fmt.Printf("synced:    %d\n", stats.Synced)
	fmt.Printf("dropped:   %d\n", stats.Dropped)
	fmt.Printf("queue:     %d/%d\n", stats.QueueLen, stats.QueueCap)
}
