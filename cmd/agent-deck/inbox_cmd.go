package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleInbox is the dispatch entry for `agent-deck inbox <session-id>`. It
// drains the per-conductor inbox file populated by the transition notifier
// when in-process retries exhaust against a busy parent. Reading truncates;
// callers should expect at-most-once delivery (consumer-side dedup, not
// producer-side, intentional — see internal/session/inbox.go).
func handleInbox(args []string) {
	if err := runInbox(os.Stdout, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runInbox is the testable seam — handleInbox wires it to os.Stdout/Stderr;
// tests pass a buffer.
func runInbox(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox <session-id>")
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Drain pending transition events from the parent's inbox file.")
		fmt.Fprintln(stdout, "Events are written here when in-process retries exhaust against")
		fmt.Fprintln(stdout, "a busy parent. Reading clears the inbox.")
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
	if len(events) == 0 {
		fmt.Fprintln(stdout, "No pending events.")
		return nil
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
	return nil
}
