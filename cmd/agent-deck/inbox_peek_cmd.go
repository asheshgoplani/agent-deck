package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// runInboxPeek renders the parent's pending records WITHOUT consuming them,
// in the same text the prompt-time drain injects (issue #2469). For a human
// or a heartbeat that wants to look before acting.
func runInboxPeek(stdout io.Writer, args []string, explicitProfile string) error {
	fs := flag.NewFlagSet("inbox peek", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the pending records as a JSON array")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox peek [--json] [<session-id>|self]")
		fmt.Fprintln(stdout, "Show pending records without consuming them.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	sessionID, err := resolveDrainTarget(fs.Args())
	if err != nil {
		fs.Usage()
		return err
	}
	sessionID, err = resolveInboxDrainSessionInProfile(sessionID, explicitProfile)
	if err != nil {
		return err
	}
	events, err := session.ReadInboxEventsForDisplay(sessionID)
	if err != nil {
		return fmt.Errorf("peek inbox: %w", err)
	}
	if *asJSON {
		if events == nil {
			events = []session.TransitionNotificationEvent{}
		}
		return json.NewEncoder(stdout).Encode(events)
	}
	if len(events) == 0 {
		fmt.Fprintln(stdout, "No pending records.")
		return nil
	}
	fmt.Fprint(stdout, session.FormatInboxRecords(events, fmt.Sprintf("%d pending record(s), not consumed:", len(events))))
	return nil
}
