package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const eventsUsage = "Usage: agent-deck events <follow|stats|publish>"

// handleEvents dispatches `agent-deck events ...` through the CLI adapter.
func handleEvents(profile string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, eventsUsage)
		exitCLI(1)
	}
	switch args[0] {
	case "--help", "-h", "help":
		fmt.Fprintln(os.Stdout, eventsUsage)
		return
	case "follow":
		handleEventsFollow(profile, args[1:])
	case "stats":
		handleEventsStats(args[1:])
	case "publish":
		handleEventsPublish(profile, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown events subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, eventsUsage)
		exitCLI(1)
	}
}

// busFlagHelp documents the --bus selector shared by follow and stats.
const busFlagHelp = "which log to read: events (the status bus, default) or comms (the comms ledger, docs/comms.md)"

// openBusForRead returns the bus a read-only command streams or counts.
// The events bus is the process-owned Default(); the comms ledger is
// opened read-only so only the notify daemon ever writes it.
func openBusForRead(name string) (*events.Bus, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "events":
		return events.Default(), nil
	case "comms":
		return comms.OpenReader(events.CurrentProfile())
	default:
		return nil, fmt.Errorf("unknown --bus %q (events or comms)", name)
	}
}

// Read-only followers use the already resolved profile and never take a writer handle.
func openFollowerBus(name string, readOnly bool) (*events.Bus, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if readOnly && (name == "" || name == "events") {
		return events.OpenReader(events.CurrentProfile())
	}
	return openBusForRead(name)
}

// handleEventsFollow implements `agent-deck events follow --json [--after <cursor>]
// [--kind <prefix>[,<prefix>]] [--session <id>] [--bus events|comms]`. It streams one canonical-JSON
// frame per line to stdout, oldest first, and keeps streaming newly published
// frames until interrupted (Ctrl-C / SIGTERM) or the bus reports an error
// (e.g. --after older than the retained log). --json is accepted for symmetry
// with every other agent-deck command's envelope convention; NDJSON is the
// only output shape this command has, so the flag doesn't change anything.
// --kind and --session only filter what is printed; cursors stay the bus's.
func handleEventsFollow(profile string, args []string) {
	fs := flag.NewFlagSet("agent-deck events follow", flag.ContinueOnError)
	afterFlag := fs.Uint64("after", 0, "resume after this cursor (0 = from the beginning of the retained log)")
	_ = fs.Bool("json", true, "stream NDJSON frames (always on; kept for CLI symmetry)")
	kindFlag := fs.String("kind", "", "only frames whose kind equals or starts with one of these comma-separated prefixes (e.g. session.status,session.turn,macapp.)")
	sessionFlag := fs.String("session", "", "only frames for this session id")
	busFlag := fs.String("bus", "events", busFlagHelp)
	readOnlyFlag := fs.Bool("read-only", false, "do not restart pending send workers while observing events")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck events follow --json [--after <cursor>] [--kind <prefix,...>] [--session <id>] [--bus events|comms] [--read-only]")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	var kinds []string
	for _, k := range strings.Split(*kindFlag, ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds = append(kinds, k)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A client that only follows the bus still gets queued sends that a
	// reboot left unfinished delivered: their workers restart here.
	// Read-only: the queue directory is only looked at, never created.
	recoverFollowerWorkers(profile, *readOnlyFlag, kickPendingSendWorkers)

	bus, err := openFollowerBus(*busFlag, *readOnlyFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: events follow: %v\n", err)
		exitCLI(1)
	}
	if bus.ReadOnly() {
		defer bus.Close()
	}
	defer bus.Want(followDemandKinds(kinds)...)()
	sub, err := bus.Subscribe(ctx, events.Cursor(*afterFlag))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: events follow: %v\n", err)
		exitCLI(1)
	}

	for frame := range sub.Frames() {
		if !eventMatches(frame, kinds, *sessionFlag) {
			continue
		}
		line, err := frame.CanonicalJSON()
		if err != nil {
			continue
		}
		line = append(line, '\n')
		if _, err := os.Stdout.Write(line); err != nil {
			exitCLI(1)
		}
	}
	if err := sub.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: events follow: %v\n", err)
		exitCLI(1)
	}
}

func recoverFollowerWorkers(profile string, readOnly bool, kick func(string, string, string)) {
	if readOnly {
		return
	}
	if p, err := session.ResolveProfileForStorage(profile); err == nil {
		if dir, err := session.GetProfileDir(p); err == nil {
			kick(profile, sendqueue.Dir(dir), "")
		}
	}
}

// followDemandKinds returns the on-demand kinds (tmux.output) a follower
// with this --kind filter prints. Those payload-less kinds are only written
// while such a follower runs, so it takes a lease on each of them.
func followDemandKinds(kinds []string) []string {
	var wanted []string
	for _, k := range events.DemandKinds {
		if eventMatches(events.Frame{Kind: k}, kinds, "") {
			wanted = append(wanted, k)
		}
	}
	return wanted
}

func eventMatches(f events.Frame, kinds []string, sessionID string) bool {
	if sessionID != "" && f.SessionID != sessionID {
		return false
	}
	if len(kinds) == 0 {
		return true
	}
	for _, k := range kinds {
		if f.Kind == k || (strings.HasSuffix(k, ".") && strings.HasPrefix(f.Kind, k)) || strings.HasPrefix(f.Kind, k+".") {
			return true
		}
	}
	return false
}

// macappKindPrefix is the only namespace a client may publish from the CLI.
// macapp.* frames drive Mac app plugins (docs/macapp-core.md) and need
// [macapp] plugins = true.
const macappKindPrefix = "macapp."

// handleEventsPublish implements `agent-deck events publish --kind <kind>
// [--session <id>] [--data <json> | --data-file <path|->] [--json]`.
func handleEventsPublish(profile string, args []string) {
	fs := flag.NewFlagSet("agent-deck events publish", flag.ContinueOnError)
	kind := fs.String("kind", "", "frame kind; must be in the macapp.* namespace")
	sessionID := fs.String("session", "", "session id the frame is about (default: the current session, $AGENTDECK_INSTANCE_ID)")
	data := fs.String("data", "", "JSON object payload")
	dataFile := fs.String("data-file", "", "read the JSON payload from this file, or - for stdin")
	jsonOut := fs.Bool("json", false, "print {ok, kind, session_id, cursor} as JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck events publish --kind macapp.<name> [--session <id>] [--data <json> | --data-file <path|->] [--json]")
		fmt.Fprintln(os.Stderr, "Needs [macapp] plugins = true in config.toml.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		exitCLI(2)
	}
	out := NewCLIOutput(*jsonOut, false)
	if !strings.HasPrefix(*kind, macappKindPrefix) || len(*kind) == len(macappKindPrefix) {
		out.Error("events publish: --kind must be in the macapp.* namespace", ErrCodeInvalidOperation)
		exitCLI(2)
	}
	if cfg, _ := session.LoadUserConfig(); cfg == nil || !cfg.Macapp.Plugins {
		out.Error("events publish is off: set [macapp] plugins = true in config.toml (docs/macapp-core.md)", ErrCodeInvalidOperation)
		exitCLI(2)
	}
	raw := []byte(*data)
	if *dataFile != "" {
		var err error
		if *dataFile == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(*dataFile)
		}
		if err != nil {
			out.Error(fmt.Sprintf("events publish: read data: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}
	var payload any
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			out.Error(fmt.Sprintf("events publish: data is not JSON: %v", err), ErrCodeInvalidOperation)
			exitCLI(2)
		}
	}
	sid := *sessionID
	if sid == "" {
		sid = os.Getenv("AGENTDECK_INSTANCE_ID")
	}
	bus := events.Default()
	before := bus.Cursor()
	bus.Publish(*kind, sid, payload)
	if !bus.Flush(2 * time.Second) {
		out.Error("events publish: bus did not commit the frame within 2s", ErrCodeInvalidOperation)
		exitCLI(1)
	}
	cursor := bus.Cursor()
	if cursor <= before {
		out.Error("events publish: bus is disabled or dropped the frame (see events stats)", ErrCodeInvalidOperation)
		exitCLI(1)
	}
	out.Success(fmt.Sprintf("published %s at cursor %d", *kind, cursor), map[string]any{"ok": true, "kind": *kind, "session_id": sid, "cursor": uint64(cursor), "profile": profile})
}

// handleEventsStats implements `agent-deck events stats [--json] [--bus events|comms]`.
func handleEventsStats(args []string) {
	fs := flag.NewFlagSet("agent-deck events stats", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print stats as JSON")
	busFlag := fs.String("bus", "events", busFlagHelp)
	readOnlyFlag := fs.Bool("read-only", false, "read existing event log without opening a writer")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck events stats [--json] [--bus events|comms] [--read-only]")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, args); err != nil {
		exitCLI(1)
	}

	bus, err := openFollowerBus(*busFlag, *readOnlyFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: events stats: %v\n", err)
		exitCLI(1)
	}
	if bus.ReadOnly() {
		defer bus.Close()
	}
	stats := bus.Stats()
	kinds, kindErr := bus.KindCounts()
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		payload := struct {
			events.Stats
			Kinds map[string]uint64 `json:"kinds"`
		}{stats, kinds}
		if err := enc.Encode(payload); err != nil {
			fmt.Fprintf(os.Stderr, "Error: encode stats: %v\n", err)
			exitCLI(1)
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
	if kindErr == nil {
		names := make([]string, 0, len(kinds))
		for k := range kinds {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Printf("kind:      %-24s %d\n", k, kinds[k])
		}
	}
}
