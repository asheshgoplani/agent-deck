package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// Issue #1948 — `agent-deck remote drain <remote>`: the conductor-side PULL.
//
// A conductor on machine A that launches workers on machine B never learns when
// they finish. Transition notifications are parent-linked via
// parent_session_id, which cannot point across machines, and the inbox is
// filesystem-local — so finished reports sit unread on B and a quota-stalled
// remote session goes unnoticed for hours. The conductor's only recourse today
// is tmux-scraping or file polling.
//
// This command replaces that with one call: it runs the read-only `inbox
// export` on the remote over the EXISTING ssh path (SSHRunner — no new
// transport), and writes what comes back into the conductor's own local inbox.
//
// Why pull and not push: the conductor's own command IS the delivery event.
// There is no "did it arrive" question, therefore no ack channel, no retry
// ledger, and no delivery semantics to get wrong — the class of problem a
// previous push-relay design failed review on. Nothing on the remote is
// consumed, moved, truncated or marked, so two conductors draining the same
// host both get the records and the host's own conductor is unaffected.
//
// Idempotence is NOT re-implemented here. Records are written with
// session.WriteInboxEventIfNew, so the inbox's existing EventFingerprint dedup
// decides what is a duplicate; this command only reports the answer. Across a
// consumption boundary (the conductor drained its inbox, so the fingerprint set
// went with it) the #1225 consumer's turn_fingerprint ledger collapses the
// re-pulled record instead. Both layers already existed; the pull adds none.
//
// Ownership model: a remote is configured by this conductor
// (`agent-deck remote add <name> <user@host>`), and drain fetches what THAT
// host's agent-deck instance holds. Narrowing by parent is the deferred
// proposal (1) on #1948 (`--parent <conductor-id>@<host>`), which is sugar over
// this pull and is deliberately not built here.
const (
	// drainExitUsage marks a caller/config problem — an unknown remote, no
	// remotes configured, bad flags. Distinct from the unreachable code so a
	// script can tell "you asked for a host I don't know" from "I asked and the
	// host did not answer".
	drainExitUsage = 2
	// drainExitUnreachable marks a remote that could not be reached or did not
	// answer with records. Never reported as an empty drain: "the host said
	// nothing pending" and "the host said nothing" must not look alike.
	drainExitUnreachable = 3
)

// remoteRecordFetcher is the ssh seam. Production wires fetchRemoteRecordsOverSSH;
// tests substitute a stub so the command's reporting, dedup accounting and exit
// codes are provable without a network.
type remoteRecordFetcher func(ctx context.Context, name string, rc session.RemoteConfig) ([]session.TransitionNotificationEvent, error)

// remoteWriterProbe asks the remote whether anything is recording transitions
// there. A package-level seam rather than a parameter so the existing drain
// tests keep their signature; they leave it at the real implementation and are
// unaffected because an unreachable stub host simply reports "unknown".
var remoteWriterProbe = func(ctx context.Context, rc session.RemoteConfig, name string) (session.WriterStatus, error) {
	runner := session.NewSSHRunner(name, rc)
	return runner.FetchWriterStatus(ctx)
}

func handleRemoteDrain(args []string) {
	if code := runRemoteDrain(os.Stdout, os.Stderr, args, fetchRemoteRecordsOverSSH); code != 0 {
		os.Exit(code)
	}
}

// fetchRemoteRecordsOverSSH is the real transport: the same SSHRunner every
// other remote command uses, running the remote's read-only `inbox export`.
//
// On failure it asks the remote for its VERSION before reporting, because the
// most likely cause — a remote too old to have `inbox export` — is invisible in
// the failure itself: that binary rejects `--json` and exits non-zero with a
// flag error. `remote add` already probes `--version` this way. The extra round
// trip happens only on a path that has already failed.
func fetchRemoteRecordsOverSSH(ctx context.Context, name string, rc session.RemoteConfig) ([]session.TransitionNotificationEvent, error) {
	// #1421: a ControlMaster socket left behind by a dead master hangs the
	// reuse path forever. `remote sessions` sweeps first for the same reason.
	session.CleanStaleSSHSockets()
	runner := session.NewSSHRunner(name, rc)
	records, err := runner.FetchPendingRecords(ctx)
	if err == nil {
		return records, nil
	}
	remoteVersion, versionFound := runner.CheckBinary(ctx)
	if hint := staleRemoteBinaryHint(name, remoteVersion, versionFound); hint != "" {
		return nil, fmt.Errorf("%w\n  %s", err, hint)
	}
	return nil, err
}

// staleRemoteBinaryHint explains a failed fetch when the remote's own version
// accounts for it. It says nothing when the remote is current — a wrong guess
// about the cause is worse than no guess, which is exactly how the previous
// stdout-shape diagnosis misfired in both directions.
func staleRemoteBinaryHint(remoteName, remoteVersion string, found bool) string {
	if !found {
		return fmt.Sprintf("No agent-deck answered `--version` on that host. Install it with `agent-deck remote update %s`.", remoteName)
	}
	if update.CompareVersions(remoteVersion, Version) < 0 {
		return fmt.Sprintf("The remote runs v%s, older than this build (v%s). `inbox export` — the read this drain performs — exists only in newer builds; update it with `agent-deck remote update %s`.",
			remoteVersion, Version, remoteName)
	}
	return ""
}

// remoteDrainResult is the --json shape, for a conductor that consumes the
// drain programmatically on its heartbeat.
type remoteDrainResult struct {
	Remote          string `json:"remote"`
	Host            string `json:"host"`
	TargetSessionID string `json:"target_session_id"`
	Fetched         int    `json:"fetched"`
	Written         int    `json:"written"`
	Duplicates      int    `json:"duplicates"`
	Unknown         int    `json:"unknown"`
	// Writer reports whether the remote had anything recording transitions.
	// It is present on every successful drain; an absent/invalid probe response
	// fails the drain before a result can be emitted.
	Writer  *session.WriterStatus                 `json:"writer,omitempty"`
	Records []session.TransitionNotificationEvent `json:"records"`
}

func runRemoteDrain(stdout, stderr io.Writer, args []string, fetch remoteRecordFetcher) (exitCode int) {
	trackedOut := &errorTrackingWriter{Writer: stdout}
	trackedErr := &errorTrackingWriter{Writer: stderr}
	stdout, stderr = trackedOut, trackedErr
	defer func() {
		if exitCode == 0 && (trackedOut.err != nil || trackedErr.err != nil) {
			exitCode = 1
		}
	}()
	fs := flag.NewFlagSet("remote drain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit the drain result as JSON")
	into := fs.String("into", "", "Local session id whose inbox receives the records (default: this session)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: agent-deck remote drain <remote-name|user@host> [--into <session-id>] [--json]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Pull completion and transition records from a remote agent-deck instance")
		fmt.Fprintln(stderr, "into this machine's inbox. The remote is read-only: nothing there is")
		fmt.Fprintln(stderr, "consumed, so draining twice — or from two conductors — is safe.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return drainExitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return drainExitUsage
	}
	requested := fs.Arg(0)

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(stderr, "Error: failed to load config: %v\n", err)
		return 1
	}

	name, rc, found := lookupRemoteFor(config, requested)
	if !found {
		reportUnknownRemote(stderr, config, requested)
		return drainExitUsage
	}

	// The receiving inbox: an explicit --into, else this session (the same
	// self-resolution `inbox drain self` uses — one rule for "which session am
	// I", not a second copy of it).
	var targetArgs []string
	if strings.TrimSpace(*into) != "" {
		targetArgs = []string{*into}
	}
	targetID, err := resolveDrainTarget(targetArgs)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	targetID, err = resolveInboxDrainSession(targetID)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return inboxExitCode(err)
	}

	records, err := fetch(context.Background(), name, rc)

	// Probe after the fetch. Its success is part of the drain transaction: the
	// export and writer status are separate SSH calls, so a missing answer here
	// can mean the host stalled after returning a partial export.
	var writer *session.WriterStatus
	ws, writerProbeErr := remoteWriterProbe(context.Background(), rc, name)
	if writerProbeErr == nil {
		writer = &ws
	}
	if err != nil {
		fmt.Fprintf(stderr, "Error: remote '%s' (%s) could not be drained: %v\n", name, rc.Host, err)
		// True for both shapes of failure: a host that never answered, and a
		// host that answered "I cannot read my own records" (review P2c). The
		// underlying error above says which.
		fmt.Fprintln(stderr, "Nothing was pulled. This is a FAILED drain, NOT an empty inbox.")
		return drainExitUnreachable
	}
	if writerProbeErr != nil {
		fmt.Fprintf(stderr, "UNVERIFIED: remote '%s' (%s) writer-status probe failed after records were fetched.\n", name, rc.Host)
		fmt.Fprintf(stderr, "Probe failure: %v\n", writerProbeErr)
		fmt.Fprintln(stderr, "The remote may have stalled mid-drain; no fetched record was classified as finished or written locally.")
		return drainExitUnreachable
	}
	if writer != nil && !writer.Running {
		fmt.Fprintf(stderr, "STALLED: remote '%s' (%s) is not recording session transitions.\n", name, rc.Host)
		fmt.Fprintf(stderr, "%s\n", writer.Detail)
		fmt.Fprintln(stderr, "No fetched record was classified as finished or written locally.")
		return drainExitUnreachable
	}

	records, fresh, written, duplicates, unknown, restoreDetected, err := ingestRemoteRecords(name, targetID, records)
	if err != nil {
		fmt.Fprintf(stderr, "Error: writing to local inbox %s: %v\n", targetID, err)
		return 1
	}
	if restoreDetected {
		fmt.Fprintf(stderr, "Warning: detected consumed-ledger restore for inbox %s; forgotten window-inside records are unknown, never new.\n", targetID)
	}

	if *asJSON {
		if records == nil {
			records = []session.TransitionNotificationEvent{}
		}
		result := remoteDrainResult{
			Remote:          name,
			Host:            rc.Host,
			TargetSessionID: targetID,
			Fetched:         len(records),
			Written:         written,
			Duplicates:      duplicates,
			Unknown:         unknown,
			Writer:          writer,
			Records:         records,
		}
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "Remote '%s' (%s) → inbox %s\n", name, rc.Host, targetID)
	if len(records) == 0 {
		// Reachable and empty is a real, distinct answer — say so, rather than
		// printing nothing and letting it read like a failed call.
		//
		// But "empty" only means "all caught up" if something was WATCHING.
		// Records are written solely by the remote's notify-daemon; with none
		// running, every drain returns empty forever and reads as healthy. That
		// ambiguity cost three rounds of the 2026-08-20 field test, so it gets
		// said out loud rather than inferred.
		fmt.Fprintln(stdout, "  Reachable: the remote holds no completion or transition records.")
		if writer != nil && !writer.Running {
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, "  ⚠ NOTHING IS RECORDING ON THAT HOST — this empty result is not evidence that its sessions are idle.")
			fmt.Fprintf(stdout, "    %s\n", writer.Detail)
		} else if writer == nil {
			fmt.Fprintln(stdout, "  (That remote is too old to report whether a writer is running, so this empty result is unverified.)")
		}
		return 0
	}
	// Print what actually arrived, not everything the remote still holds. The
	// remote is read-only by design, so a steady-state heartbeat re-fetches the
	// same backlog every time; listing all of it made each run look like fresh
	// work and buried the one new record when there was one. Field finding,
	// 2026-08-20.
	if written > 0 {
		printInboxEventLines(stdout, fresh)
		if unknown > 0 {
			fmt.Fprintf(stdout, "\n  %d record(s) fetched, %d new (shown above), %d already present, %d unknown.\n",
				len(records), written, duplicates, unknown)
		} else {
			fmt.Fprintf(stdout, "\n  %d record(s) fetched, %d new (shown above), %d already present.\n",
				len(records), written, duplicates)
		}
	} else if unknown > 0 {
		fmt.Fprintf(stdout, "  %d record(s) fetched, 0 new, %d already present, %d unknown.\n",
			len(records), duplicates, unknown)
	} else {
		fmt.Fprintf(stdout, "  %d record(s) fetched, nothing new — all already present.\n", len(records))
	}
	return 0
}

// ingestRemoteRecords writes the pulled records into the local inbox, stamping
// each with the remote it came from, and returns the records AS STORED so the
// report shows what actually landed. The dedup decision covers both pending
// inbox fingerprints and the durable consumed-turn ledger; this only counts
// the answers.
// fresh carries the records that were ACTUALLY committed by this call, separate
// from stored (everything fetched). The display needs the distinction: a drain
// that commits nothing was re-printing the entire backlog as though it had just
// arrived, which reads like new work on every heartbeat.
func ingestRemoteRecords(remoteName, targetID string, records []session.TransitionNotificationEvent) (
	stored, fresh []session.TransitionNotificationEvent, written, duplicates, unknown int, restoreDetected bool, err error) {
	stored = make([]session.TransitionNotificationEvent, 0, len(records))
	for _, ev := range records {
		ev.SourceRemote = remoteName
		// Review round 2 (blocking): a child id is caller-chosen and only unique
		// on the host that minted it, so two hosts running the same named task
		// mint the same id. Everything downstream keys identity on the child id
		// — collapseLastWins, TurnFingerprint, EventFingerprint, the sweeps — so
		// unscoped ids make one host's record silently destroy the other's while
		// the drain reports "1 new". Namespacing here fixes all of them at once,
		// in the repo's existing `<remote>:<session>` spelling.
		ev.ChildSessionID = session.RemoteScopedChildID(remoteName, ev.ChildSessionID)
		// The remote derived its TurnFingerprint from the UNSCOPED id, so it
		// carries the same collision into the consumed-turn ledger. Re-derive it
		// from the scoped record.
		ev.TurnFingerprint = session.TurnFingerprint(ev)
		// The record now belongs to THIS conductor's inbox; the remote's own
		// target id is meaningless on this machine.
		ev.TargetSessionID = targetID
		ev.TargetKind = "parent"
		stored = append(stored, ev)

		presence, werr := session.WriteInboxEventIfUnseen(targetID, ev)
		if werr != nil {
			return stored, fresh, written, duplicates, unknown, restoreDetected, werr
		}
		switch presence {
		case session.InboxEventInserted:
			written++
			fresh = append(fresh, ev)
		case session.InboxEventAlreadyPresent:
			duplicates++
		case session.InboxEventPresenceUnknownAfterLedgerRestore:
			unknown++
			restoreDetected = true
		default:
			unknown++
		}
	}
	return stored, fresh, written, duplicates, unknown, restoreDetected, nil
}

// lookupRemoteFor resolves what the user typed to a configured remote: the
// remote's name, or its host string (`user@host`) so the command reads the way
// #1948 spells it — `remote drain <host>`.
func lookupRemoteFor(config *session.UserConfig, requested string) (string, session.RemoteConfig, bool) {
	requested = strings.TrimSpace(requested)
	if config == nil || len(config.Remotes) == 0 || requested == "" {
		return "", session.RemoteConfig{}, false
	}
	if rc, ok := config.Remotes[requested]; ok {
		return requested, rc, true
	}
	for _, name := range sortedRemoteNames(config) {
		if config.Remotes[name].Host == requested {
			return name, config.Remotes[name], true
		}
	}
	return "", session.RemoteConfig{}, false
}

// reportUnknownRemote states plainly that the host is unknown and shows what IS
// configured. An unknown host must never be mistakable for a host that had
// nothing to report.
func reportUnknownRemote(stderr io.Writer, config *session.UserConfig, requested string) {
	if config == nil || len(config.Remotes) == 0 {
		fmt.Fprintf(stderr, "Error: unknown remote '%s': no remotes are configured.\n", requested)
		fmt.Fprintln(stderr, "Add one with: agent-deck remote add <name> <user@host>")
		return
	}
	fmt.Fprintf(stderr, "Error: unknown remote '%s' — it is not a configured remote name or host.\n", requested)
	fmt.Fprintln(stderr, "Configured remotes:")
	for _, name := range sortedRemoteNames(config) {
		fmt.Fprintf(stderr, "  %s  %s\n", name, config.Remotes[name].Host)
	}
	fmt.Fprintln(stderr, "Add one with: agent-deck remote add <name> <user@host>")
}

func sortedRemoteNames(config *session.UserConfig) []string {
	names := make([]string, 0, len(config.Remotes))
	for name := range config.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
