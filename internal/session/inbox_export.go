package session

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Issue #1948 — the REMOTE-SIDE READ of the cross-machine pull.
//
// A conductor on machine A that launches workers on machine B never learns when
// they finish: transition notifications are parent-linked via ParentSessionID,
// which cannot point across machines, and the inbox at
// <agent-deck-dir>/inboxes/<id>.jsonl is filesystem-local. The fix is a PULL —
// the conductor runs `agent-deck remote drain <remote>`, which runs the export
// below on B over the existing ssh path (SSHRunner) and writes what comes back
// into its OWN inbox.
//
// Pull, not push, is the whole point: the conductor's own command IS the
// delivery event, so there is no "did it arrive" handshake to get wrong — the
// delivery/ack semantics a previous push-relay design failed review on. Nothing
// here sends, acknowledges, or remembers who asked.
//
// Two properties this file is responsible for:
//
//  1. NON-DESTRUCTIVE. Every path below opens files read-only. A drain must not
//     consume, move, truncate or mark anything on the exporting host, so two
//     conductors draining the same host both get the records and the host's own
//     conductor still drains its inbox exactly as before.
//
//  2. FINGERPRINT-STABLE. Every exported record is fingerprintable by the
//     EXISTING EventFingerprint rule, so the local WriteInboxEventIfNew dedup —
//     the one dedup this repo has for inbox records — collapses a re-drain with
//     no second rule to keep in sync. That is why the ledger→event synthesis
//     below runs the summary through the same capDoneSummary the producer uses:
//     a record committed by the producer and the same completion read back off
//     the ledger must hash identically.
//
// The dead-letter store is deliberately NOT exported. Every completion it holds
// is already in the completion ledger (both producers — the one-shot task worker
// and the transition daemon — write the ledger unconditionally), and the
// dead-letter trail additionally holds records that were suppressed ON PURPOSE
// (no_notify, self_conductor). Re-delivering those to a remote conductor would
// undo a deliberate suppression.

// ExportPendingRecords returns every completion and transition record this host
// can report to a draining conductor, read-only.
//
// Two sources, unioned and deduplicated by EventFingerprint:
//
//   - the completion ledger (runtime/completion-ledger/*.json) — the durable,
//     non-destructive last-known completion per child. It is written on every
//     completion regardless of whether a parent could be resolved, which is
//     exactly the cross-machine case: a worker whose conductor lives on another
//     machine has no resolvable local parent, so the inbox never sees it.
//   - the pending per-parent inbox files (inboxes/*.jsonl) — the transition
//     records (running→waiting, quota-stalled) already committed on this host
//     and not yet consumed.
//
// Records are ordered oldest-first (Timestamp, then child id) so the output is
// stable across calls.
func ExportPendingRecords() ([]TransitionNotificationEvent, error) {
	ledger, err := exportLedgerRecords()
	if err != nil {
		return nil, err
	}
	pending, err := exportInboxRecords()
	if err != nil {
		return nil, err
	}

	out := make([]TransitionNotificationEvent, 0, len(ledger)+len(pending))
	out = append(out, ledger...)
	out = append(out, pending...)
	out = dedupByEventFingerprint(out)

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].ChildSessionID < out[j].ChildSessionID
	})
	return out, nil
}

// dedupByEventFingerprint collapses records that share an EventFingerprint,
// keeping the first. A completion that is BOTH committed to a local inbox and
// mirrored to the ledger is one logical event and must cross the wire once —
// and it is the same rule the receiving inbox dedups with, so which copy
// survives cannot matter.
func dedupByEventFingerprint(events []TransitionNotificationEvent) []TransitionNotificationEvent {
	seen := make(map[string]struct{}, len(events))
	out := make([]TransitionNotificationEvent, 0, len(events))
	for _, ev := range events {
		fp := EventFingerprint(ev)
		if _, dup := seen[fp]; dup {
			continue
		}
		seen[fp] = struct{}{}
		out = append(out, ev)
	}
	return out
}

// exportLedgerRecords reads the completion ledger and synthesizes one finished
// event per entry. Read-only: the ledger is explicitly the store a parent may
// query "without consuming any delivery event" (see completion_ledger.go).
func exportLedgerRecords() ([]TransitionNotificationEvent, error) {
	dir, err := completionLedgerDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []TransitionNotificationEvent
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		entry, ok := readLedgerFile(filepath.Join(dir, e.Name()))
		if !ok {
			continue // unreadable or corrupt entry: skip, don't fail the export
		}
		out = append(out, completionLedgerEvent(entry))
	}
	return out, nil
}

// completionLedgerEvent converts a ledger entry into the finished-kind event
// shape the inbox stores, matching what the producer would have committed had
// the parent been resolvable on this machine.
//
// capDoneSummary is applied for fingerprint agreement, not for the file size:
// CommitToInbox caps the summary BEFORE deriving fingerprints, so an uncapped
// ledger copy of the same completion would hash differently and defeat the
// dedup that makes a re-drain a no-op.
func completionLedgerEvent(e CompletionLedgerEntry) TransitionNotificationEvent {
	ev := TransitionNotificationEvent{
		ChildSessionID: strings.TrimSpace(e.ChildID),
		ChildTitle:     strings.TrimSpace(e.Title),
		Profile:        strings.TrimSpace(e.Profile),
		Kind:           transitionKindFinished,
		DoneStatus:     strings.ToLower(strings.TrimSpace(e.Status)),
		DoneSummary:    capDoneSummary(strings.TrimSpace(e.Summary)),
		Timestamp:      e.FinishedAt,
	}
	ev.TurnFingerprint = TurnFingerprint(ev)
	return ev
}

// exportInboxRecords reads every per-parent inbox file without consuming any of
// them. The dead-letter subdirectory is skipped by the IsDir check.
func exportInboxRecords() ([]TransitionNotificationEvent, error) {
	dir := InboxDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	// Held across the whole read so a concurrent producer's append or a
	// concurrent drain's truncate cannot interleave mid-file, exactly as the
	// other inbox readers do.
	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	var out []TransitionNotificationEvent
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		events, err := readInboxEventsLocked(filepath.Join(dir, e.Name()))
		if err != nil {
			// One unreadable inbox must not hide every other host record.
			continue
		}
		out = append(out, events...)
	}
	return out, nil
}

// ReadInboxEvents returns a parent's pending inbox records WITHOUT consuming
// them — the non-destructive counterpart to ReadAndTruncateInbox, used by the
// #1948 drain to report how many records were new versus already present.
func ReadInboxEvents(parentSessionID string) ([]TransitionNotificationEvent, error) {
	if strings.TrimSpace(parentSessionID) == "" {
		return nil, errors.New("inbox read: empty parent session id")
	}
	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()
	return readInboxEventsLocked(InboxPathFor(parentSessionID))
}

// readLedgerFile parses one completion-ledger file. Shared by ReadLedgerEntry
// (which addresses it by child id) and the export walk (which addresses it by
// directory entry), so there is exactly one parse of this format.
func readLedgerFile(path string) (CompletionLedgerEntry, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return CompletionLedgerEntry{}, false
	}
	var e CompletionLedgerEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return CompletionLedgerEntry{}, false
	}
	return e, true
}
