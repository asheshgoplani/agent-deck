package main

import (
	"encoding/json"
	"errors"
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
func handleInbox(profile string, args []string) {
	if err := runInboxWithProfile(os.Stdout, args, profile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(inboxExitCode(err))
	}
}

type inboxTargetNotFoundError struct{ identifier string }

func (e *inboxTargetNotFoundError) Error() string {
	return fmt.Sprintf("Error: inbox drain target %q could not be resolved. Nothing was drained; this is NOT an empty inbox.", e.identifier)
}

type inboxTargetAmbiguousError struct{ message string }

func (e *inboxTargetAmbiguousError) Error() string {
	return "Error: inbox drain target is ambiguous. Nothing was drained.\n" + e.message
}

// inboxExitCode is the #1991 drain-resolution contract: 2 means the target
// does not exist, 3 means the supplied title/prefix is ambiguous, and 1 is a
// storage, usage, or drain failure. Resolution failures never drain events.
func inboxExitCode(err error) int {
	var notFound *inboxTargetNotFoundError
	if errors.As(err, &notFound) {
		return 2
	}
	var ambiguous *inboxTargetAmbiguousError
	if errors.As(err, &ambiguous) {
		return 3
	}
	return 1
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
	return runInboxWithProfile(stdout, args, "")
}

func runInboxWithProfile(stdout io.Writer, args []string, explicitProfile string) error {
	if len(args) > 0 && args[0] == "drain" {
		return runInboxDrain(stdout, args[1:], explicitProfile)
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
func runInboxDrain(stdout io.Writer, args []string, explicitProfile string) error {
	fs := flag.NewFlagSet("inbox drain", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the drained events as a JSON array")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox drain [--json] [<session-id>|self]")
		fmt.Fprintln(stdout, "With no id (or 'self'), drains the caller's own session.")
		fmt.Fprintln(stdout, "Full session IDs resolve across all profiles; titles and shortened IDs")
		fmt.Fprintln(stdout, "resolve only within the effective profile.")
		fmt.Fprintln(stdout, "If a full ID exists in multiple profiles, qualify it with global -p/--profile.")
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

func resolveInboxDrainSession(identifier string) (string, error) {
	return resolveInboxDrainSessionInProfile(identifier, "")
}

func resolveInboxDrainSessionInProfile(identifier, explicitProfile string) (string, error) {
	// An explicit global -p/--profile qualifier is authoritative, including
	// when corrupt/restored registries contain the same full ID elsewhere.
	if explicitProfile != "" {
		return resolveInboxDrainSessionLocally(identifier, explicitProfile)
	}

	// Check exact IDs in every profile before applying the profile-local
	// flexible resolver. Do not assume registry uniqueness: draining is
	// destructive, so duplicate full IDs must fail closed and name every
	// profile that must be disambiguated.
	profiles, err := session.ListProfiles()
	if err != nil {
		return "", fmt.Errorf("list profiles: %w", err)
	}
	var exactProfiles []string
	for _, profile := range profiles {
		_, profileInstances, _, loadErr := loadSessionData(profile)
		if loadErr != nil {
			return "", fmt.Errorf("load profile %q: %w", profile, loadErr)
		}
		for _, inst := range profileInstances {
			if inst.ID == identifier {
				exactProfiles = append(exactProfiles, profile)
				break
			}
		}
	}
	if len(exactProfiles) == 1 {
		return identifier, nil
	}
	if len(exactProfiles) > 1 {
		return "", &inboxTargetAmbiguousError{message: fmt.Sprintf(
			"Full session ID %q exists in profiles %s. Use -p/--profile to choose one.",
			identifier, strings.Join(exactProfiles, ", "))}
	}

	return resolveInboxDrainSessionLocally(identifier, "")
}

func resolveInboxDrainSessionLocally(identifier, profile string) (string, error) {
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return "", err
	}
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		if errCode == ErrCodeAmbiguous {
			return "", &inboxTargetAmbiguousError{message: errMsg}
		}
		return "", &inboxTargetNotFoundError{identifier: identifier}
	}
	return inst.ID, nil
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
