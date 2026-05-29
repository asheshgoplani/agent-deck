package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleInbox is the dispatch entry for `agent-deck inbox <session-id>`. It
// drains the per-conductor inbox file that the transition notifier commits
// completions to (issue #1225). The bare form is the legacy raw read+truncate
// (at-most-once); the `drain` subcommand is the durable consumer path. See
// internal/session/inbox.go.
func handleInbox(args []string) {
	if err := runInbox(os.Stdout, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runInbox is the testable seam — handleInbox wires it to os.Stdout/Stderr;
// tests pass a buffer.
//
// Forms:
//
//	agent-deck inbox <session-id>          legacy raw drain (read + truncate)
//	agent-deck inbox drain [--json] <id>   issue #1225 consumer drain — collapses
//	                                       last-wins per child and dedups
//	                                       re-delivery via turn_fingerprint. This
//	                                       is the conductor's heartbeat step.
func runInbox(stdout io.Writer, args []string) error {
	if len(args) > 0 && args[0] == "drain" {
		return runInboxDrain(stdout, args[1:])
	}

	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox <session-id>")
		fmt.Fprintln(stdout, "       agent-deck inbox drain [--json] <session-id>")
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Drain pending completion events from the parent's durable outbox.")
		fmt.Fprintln(stdout, "The `drain` form (issue #1225) collapses last-wins per child and")
		fmt.Fprintln(stdout, "dedups re-delivery via turn_fingerprint; run it first on every")
		fmt.Fprintln(stdout, "heartbeat. Reading clears the inbox.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("expected exactly one session id argument")
	}
	sessionID := fs.Arg(0)

	events, err := session.ReadAndTruncateInbox(sessionID)
	if err != nil {
		return fmt.Errorf("read inbox: %w", err)
	}
	printInboxEvents(stdout, events)
	return nil
}

// runInboxDrain is the issue #1225 consumer path: exactly-once-per-turn,
// last-wins-per-child. Used by the conductor heartbeat and any machine consumer.
func runInboxDrain(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("inbox drain", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the drained events as a JSON array")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox drain [--json] [<session-id>|self]")
		fmt.Fprintln(stdout, "With no id (or 'self'), drains the caller's own session.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	sessionID, err := resolveDrainTarget(fs.Args())
	if err != nil {
		fs.Usage()
		return err
	}

	events, err := session.DrainInboxForParent(sessionID)
	if err != nil {
		return fmt.Errorf("drain inbox: %w", err)
	}

	if *asJSON {
		if events == nil {
			events = []session.TransitionNotificationEvent{}
		}
		enc := json.NewEncoder(stdout)
		return enc.Encode(events)
	}

	printInboxEvents(stdout, events)
	return nil
}

// resolveDrainTarget returns the session id to drain. With no positional arg,
// or the literal "self", it resolves the caller's OWN session (audit B7) — the
// conductor template runs `agent-deck inbox drain self` as heartbeat step 1.
func resolveDrainTarget(args []string) (string, error) {
	switch len(args) {
	case 0:
		return resolveSelfSessionID()
	case 1:
		if strings.EqualFold(strings.TrimSpace(args[0]), "self") {
			return resolveSelfSessionID()
		}
		return args[0], nil
	default:
		return "", fmt.Errorf("expected at most one session id argument")
	}
}

// resolveSelfSessionID resolves the caller's own session id robustly across
// worktree / sandbox / cron contexts (audit B7). It prefers AGENTDECK_INSTANCE_ID
// (always injected into agent-deck-managed sessions, and the only signal that
// survives when there is no tmux — worktrees, sandboxes, cron heartbeats), then
// AGENT_DECK_SESSION_ID, and only falls back to the tmux session name last.
func resolveSelfSessionID() (string, error) {
	for _, v := range []string{
		os.Getenv("AGENTDECK_INSTANCE_ID"),
		os.Getenv("AGENT_DECK_SESSION_ID"),
	} {
		if s := strings.TrimSpace(v); s != "" {
			return s, nil
		}
	}
	if s := strings.TrimSpace(GetCurrentSessionID()); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("no session id given and could not resolve the current session " +
		"(set AGENTDECK_INSTANCE_ID, run inside an agent-deck tmux session, or pass an explicit id)")
}

func printInboxEvents(stdout io.Writer, events []session.TransitionNotificationEvent) {
	if len(events) == 0 {
		fmt.Fprintln(stdout, "No pending events.")
		return
	}
	for _, ev := range events {
		fmt.Fprintf(stdout, "%s  child=%s title=%q profile=%s %s→%s\n",
			ev.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			ev.ChildSessionID,
			ev.ChildTitle,
			ev.Profile,
			ev.FromStatus,
			ev.ToStatus,
		)
	}
	fmt.Fprintf(stdout, "\nDrained %d event(s).\n", len(events))
}
