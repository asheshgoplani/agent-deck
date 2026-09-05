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

type errorTrackingWriter struct {
	io.Writer
	err error
}

func (w *errorTrackingWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.Writer.Write(p)
	if err != nil {
		w.err = err
	}
	return n, err
}

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

type inboxProfileCorruptError struct {
	profiles []profileLoadError
}

func (e *inboxProfileCorruptError) Error() string {
	var failures []string
	for _, failure := range e.profiles {
		failures = append(failures, fmt.Sprintf("profile %q: %v", failure.profile, failure.err))
	}
	return fmt.Sprintf("Error: inbox drain resolution aborted because these profiles could not be read: %s. Nothing was drained.", strings.Join(failures, "; "))
}

func (e *inboxProfileCorruptError) Unwrap() []error {
	errs := make([]error, 0, len(e.profiles))
	for _, failure := range e.profiles {
		errs = append(errs, failure.err)
	}
	return errs
}

type profileLoadError struct {
	profile string
	err     error
}

func newInboxProfileCorruptError(profile string, err error) *inboxProfileCorruptError {
	return &inboxProfileCorruptError{profiles: []profileLoadError{{profile: profile, err: err}}}
}

func unreadableProfilesNote(failures []profileLoadError) string {
	if len(failures) == 0 {
		return ""
	}
	var profiles []string
	for _, failure := range failures {
		profiles = append(profiles, fmt.Sprintf("%q", failure.profile))
	}
	return fmt.Sprintf("\nUnreadable profiles encountered during resolution: %s.", strings.Join(profiles, ", "))
}

type deadLettersPendingError struct{ count int }

func (e *deadLettersPendingError) Error() string {
	return fmt.Sprintf("inbox drain incomplete: %d dead-lettered event(s) require attention", e.count)
}

// inboxExitCode is the drain-resolution contract (#1991, extended): 2 means the
// target does not exist, 3 means the supplied title/prefix is ambiguous, 4 means
// the drain ran but dead-lettered events remain and require attention, and 1 is
// a storage, usage, or drain failure. Resolution failures never drain events.
func inboxExitCode(err error) int {
	var notFound *inboxTargetNotFoundError
	if errors.As(err, &notFound) {
		return 2
	}
	var ambiguous *inboxTargetAmbiguousError
	if errors.As(err, &ambiguous) {
		return 3
	}
	var pending *deadLettersPendingError
	if errors.As(err, &pending) {
		return 4
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

func runInboxWithProfile(stdout io.Writer, args []string, explicitProfile string) (retErr error) {
	tracked := &errorTrackingWriter{Writer: stdout}
	stdout = tracked
	defer func() {
		if retErr == nil && tracked.err != nil {
			retErr = fmt.Errorf("write inbox output: %w", tracked.err)
		}
	}()
	if len(args) > 0 && args[0] == "drain" {
		return runInboxDrain(stdout, args[1:], explicitProfile)
	}
	if len(args) > 0 && args[0] == "export" {
		return runInboxExport(stdout, args[1:])
	}
	if len(args) > 0 && args[0] == "writer-status" {
		return runInboxWriterStatus(stdout, args[1:])
	}

	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox <session-id>")
		fmt.Fprintln(stdout, "       agent-deck inbox drain [--json] <session-id>")
		fmt.Fprintln(stdout, "       agent-deck inbox export [--json]")
		fmt.Fprintln(stdout, "       agent-deck inbox writer-status [--json]")
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Drain pending completion events from the parent's durable outbox.")
		fmt.Fprintln(stdout, "The `drain` form (issue #1225) collapses last-wins per child and")
		fmt.Fprintln(stdout, "dedups re-delivery via turn_fingerprint; run it first on every")
		fmt.Fprintln(stdout, "heartbeat. Reading clears the inbox.")
		fmt.Fprintln(stdout, "The `export` form (issue #1948) READS this host's completion and")
		fmt.Fprintln(stdout, "transition records without consuming anything; it is what a")
		fmt.Fprintln(stdout, "conductor on another machine runs over ssh via `remote drain`.")
		fmt.Fprintln(stdout, "The `writer-status` form reports whether a notify-daemon is")
		fmt.Fprintln(stdout, "actually recording transitions here — without it, an empty export")
		fmt.Fprintln(stdout, "cannot be told apart from a host where nothing has been watching.")
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
		if err := enc.Encode(events); err != nil {
			return err
		}
	} else {
		printInboxEvents(stdout, events)
	}

	deadLetters, err := session.CountDeadLetterRecords()
	if err != nil {
		return fmt.Errorf("count dead letters: %w", err)
	}
	if deadLetters > 0 {
		if !*asJSON {
			fmt.Fprintf(stdout, "WARNING: %d dead-lettered event(s) require attention.\n", deadLetters)
		}
		return &deadLettersPendingError{count: deadLetters}
	}
	return nil
}

func resolveInboxDrainSession(identifier string) (string, error) {
	return resolveInboxDrainSessionInProfile(identifier, "")
}

func resolveInboxDrainSessionInProfile(identifier, explicitProfile string) (string, error) {
	// Check exact IDs in every profile before applying the profile-local
	// flexible resolver. Do not assume registry uniqueness: draining is
	// destructive, so duplicate full IDs must fail closed and name every
	// profile that must be disambiguated.
	profiles, err := session.ListProfiles()
	if err != nil {
		return "", fmt.Errorf("list profiles: %w", err)
	}
	effectiveProfile := explicitProfile
	if effectiveProfile == "" {
		effectiveProfile, err = session.ResolveProfileForStorage("")
		if err != nil {
			return "", fmt.Errorf("resolve effective profile: %w", err)
		}
	}
	return resolveInboxDrainSessionInProfiles(identifier, effectiveProfile, explicitProfile != "", profiles)
}

// resolveInboxDrainSessionInProfiles performs the destructive resolver's
// global exact-ID pass while retaining title matches from the effective
// profile. ResolveSession normally gives an exact title precedence over IDs;
// that is convenient for non-destructive commands, but unsafe here when a
// title is literally another session's full ID.
func resolveInboxDrainSessionInProfiles(identifier, effectiveProfile string, explicitlyQualified bool, profiles []string) (string, error) {
	type candidate struct {
		profile string
		inst    *session.Instance
	}
	var exactIDs, titleMatches []candidate
	var loadFailures []profileLoadError
	for _, profile := range profiles {
		_, profileInstances, _, loadErr := loadSessionData(profile)
		if loadErr != nil {
			// Keep scanning readable stores: a provable ambiguity takes precedence
			// over an unreadable store, independent of profile directory order.
			loadFailures = append(loadFailures, profileLoadError{profile: profile, err: loadErr})
			continue
		}
		for _, inst := range profileInstances {
			if inst.ID == identifier {
				exactIDs = append(exactIDs, candidate{profile: profile, inst: inst})
			}
			if profile == effectiveProfile && inst.Title == identifier {
				titleMatches = append(titleMatches, candidate{profile: profile, inst: inst})
			}
		}
	}

	var conflictingTitles []candidate
	for _, title := range titleMatches {
		if title.inst.ID != identifier {
			conflictingTitles = append(conflictingTitles, title)
		}
	}
	if len(exactIDs) > 0 && len(conflictingTitles) > 0 {
		var named []string
		for _, match := range append(exactIDs, conflictingTitles...) {
			named = append(named, fmt.Sprintf("%s (%s, profile %q)", match.inst.Title, match.inst.ID, match.profile))
		}
		return "", &inboxTargetAmbiguousError{message: fmt.Sprintf(
			"%q is both a full session ID and another session's exact title:\n  - %s\nRename the title or use -p/--profile only if it uniquely identifies the intended session.%s",
			identifier, strings.Join(named, "\n  - "), unreadableProfilesNote(loadFailures))}
	}
	if len(exactIDs) > 1 {
		if explicitlyQualified {
			var qualified []candidate
			for _, match := range exactIDs {
				if match.profile == effectiveProfile {
					qualified = append(qualified, match)
				}
			}
			if len(qualified) == 1 {
				if len(loadFailures) > 0 {
					return "", &inboxProfileCorruptError{profiles: loadFailures}
				}
				return identifier, nil
			}
		}
		var exactProfiles []string
		for _, match := range exactIDs {
			exactProfiles = append(exactProfiles, match.profile)
		}
		return "", &inboxTargetAmbiguousError{message: fmt.Sprintf(
			"Full session ID %q exists in profiles %s. Use -p/--profile to choose one.%s",
			identifier, strings.Join(exactProfiles, ", "), unreadableProfilesNote(loadFailures))}
	}
	if len(loadFailures) > 0 {
		return "", &inboxProfileCorruptError{profiles: loadFailures}
	}
	if len(exactIDs) == 1 {
		if explicitlyQualified && exactIDs[0].profile != effectiveProfile {
			return "", &inboxTargetNotFoundError{identifier: identifier}
		}
		return identifier, nil
	}

	return resolveInboxDrainSessionLocally(identifier, effectiveProfile)
}

func resolveInboxDrainSessionLocally(identifier, profile string) (string, error) {
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return "", newInboxProfileCorruptError(profile, err)
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

// runInboxExport is the issue #1948 remote-side READ: it prints this host's
// completion and transition records WITHOUT consuming them, so a conductor on
// another machine can pull them over ssh (`agent-deck remote drain <remote>`)
// and write them into its own inbox.
//
// Non-destructive is the contract, not a side effect: two conductors draining
// this host must both get the records, and this host's own conductor must still
// find its inbox exactly as it left it.
func runInboxExport(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("inbox export", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the records as a JSON array")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox export [--json]")
		fmt.Fprintln(stdout, "Print this host's completion/transition records without consuming them.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("inbox export takes no positional arguments")
	}

	records, err := session.ExportPendingRecords()
	if err != nil {
		return fmt.Errorf("export inbox records: %w", err)
	}

	if *asJSON {
		if records == nil {
			records = []session.TransitionNotificationEvent{}
		}
		return json.NewEncoder(stdout).Encode(records)
	}

	if len(records) == 0 {
		fmt.Fprintln(stdout, "No records.")
		return nil
	}
	printInboxEventLines(stdout, records)
	fmt.Fprintf(stdout, "\nExported %d record(s). Nothing was consumed.\n", len(records))
	return nil
}

// runInboxWriterStatus answers the question an empty export cannot: is anything
// on this host actually recording session transitions?
//
// FINDING A of the 2026-08-20 field test. Records are written only by the
// notify-daemon, so on a host without one every drain returns empty and reads as
// "all caught up". Three field rounds were spent before anyone suspected the
// writer rather than the reader.
func runInboxWriterStatus(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("inbox writer-status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the status as JSON")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox writer-status [--json]")
		fmt.Fprintln(stdout, "Report whether a notify-daemon is recording transitions on this host.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("inbox writer-status takes no positional arguments")
	}

	status := session.ReadWriterStatus()
	if *asJSON {
		return json.NewEncoder(stdout).Encode(status)
	}
	if status.Running {
		fmt.Fprintf(stdout, "writer: RUNNING (heartbeat %ds ago)\n", status.AgeSeconds)
	} else {
		fmt.Fprintln(stdout, "writer: NOT RUNNING")
	}
	fmt.Fprintln(stdout, status.Detail)
	return nil
}

func printInboxEvents(stdout io.Writer, events []session.TransitionNotificationEvent) {
	if len(events) == 0 {
		fmt.Fprintln(stdout, "No pending events.")
		return
	}
	printInboxEventLines(stdout, events)
	fmt.Fprintf(stdout, "\nDrained %d event(s).\n", len(events))
}

// printInboxEventLines renders one line per event. Shared by the drain and the
// #1948 export so the two never drift into two spellings of one record.
func printInboxEventLines(stdout io.Writer, events []session.TransitionNotificationEvent) {
	for _, ev := range events {
		fmt.Fprintf(stdout, "%s  child=%s title=%q profile=%s",
			ev.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			ev.ChildSessionID,
			ev.ChildTitle,
			ev.Profile,
		)
		// A completion carries no from→to flip; printing a bare arrow for it
		// (as this line always did) reads like a lost status.
		if ev.Kind != "" {
			fmt.Fprintf(stdout, " %s=%s", ev.Kind, ev.DoneStatus)
		} else {
			fmt.Fprintf(stdout, " %s→%s", ev.FromStatus, ev.ToStatus)
		}
		// #1948: a pulled record's host is the one fact a cross-machine record
		// would otherwise lose.
		if ev.SourceRemote != "" {
			fmt.Fprintf(stdout, " remote=%s", ev.SourceRemote)
		}
		fmt.Fprintln(stdout)
	}
}
