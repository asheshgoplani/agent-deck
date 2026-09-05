package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Dead-letter / terminal-drop reasons (audit B5). Without distinguishing these,
// an orphan, a removed child, a removed/cross-profile parent, and an intentional
// no-notify all collapsed to the same silent path, so an operator could not tell
// a misconfiguration from a benign suppression. The reason is stamped on the
// event and written to both the dead-letter record and the missed-log line.
const (
	deadLetterReasonUnresolvable  = "unresolvable"   // generic / parent found but not live
	deadLetterReasonChildMissing  = "child_removed"  // child gone between commit and resolve
	deadLetterReasonParentMissing = "parent_removed" // ParentSessionID set but parent not in this profile (removed or cross-profile)
	deadLetterReasonOrphan        = "orphan"         // child has no ParentSessionID (logged once via orphan log)
	deadLetterReasonNoNotify      = "no_notify"      // child opted out — intentional, not a failure
	deadLetterReasonSelfConductor = "self_conductor" // top-level/self-pointing conductor — intentional
)

// Issue #1225: the durable per-parent outbox is the PRIMARY delivery channel,
// not the last-resort graveyard the old push path fell into. Two producers
// (interactive running→waiting and one-shot run-task kernel-exit) commit here;
// the parent drains it on its own turn boundary. This file holds the producer
// side: last-wins-per-child commit, the turn_fingerprint for exactly-once
// consumer effects, and the bounded dead-letter path that replaces the
// dropped_no_target ~1/sec runaway with a terminal state logged once.

// maxDoneSummaryBytes bounds the per-record completion summary (audit B6). A
// worker's DoneSummary is sourced from a sentinel with no inherent size limit;
// without a cap a worker that dumps a large log into it could grow a single
// JSONL line past the scanner cap and fail the entire drain. 32 KB is generous
// for a human-readable completion summary while keeping the line scannable.
const maxDoneSummaryBytes = 32 * 1024

// capDoneSummary truncates an over-long completion summary to maxDoneSummaryBytes,
// appending a marker so an operator sees the summary was clipped. Truncation is
// byte-based (a multi-byte rune at the boundary is tolerated — the marker makes
// the clip obvious and JSON marshalling escapes any partial bytes safely).
func capDoneSummary(s string) string {
	if len(s) <= maxDoneSummaryBytes {
		return s
	}
	const marker = "…[truncated]"
	// keep is a positive compile-time constant (marker << maxDoneSummaryBytes).
	keep := maxDoneSummaryBytes - len(marker)
	return s[:keep] + marker
}

// inboxWireEvent is the on-disk JSONL shape: the event plus the legacy "fp"
// fingerprint used by the producer-side dedup in WriteInboxEvent. Defined once
// here so both the legacy append path and CommitToInbox serialize identically.
type inboxWireEvent struct {
	TransitionNotificationEvent
	Fingerprint string `json:"fp,omitempty"`
}

// decodeInboxLine parses one JSONL inbox line into a TransitionNotificationEvent.
func decodeInboxLine(line []byte) (TransitionNotificationEvent, error) {
	var w inboxWireEvent
	if err := json.Unmarshal(line, &w); err != nil {
		return TransitionNotificationEvent{}, err
	}
	return w.TransitionNotificationEvent, nil
}

// TurnFingerprint returns a stable identifier for a child's completed TURN, for
// exactly-once consumer effects (issue #1225). Unlike EventFingerprint — which
// keys on Timestamp.UnixNano() so a single logical event's retries collapse —
// TurnFingerprint deliberately omits the emit instant: two emits of the same
// turn (e.g. the same record re-delivered after a daemon restart that
// re-stamped Timestamp) share a fingerprint, so the draining parent acts once.
//
// Turn signal precedence:
//   - finished (one-shot) events: the completion outcome (status + summary)
//   - interactive transitions: the child's pane-content hash at the flip
//     (LastOutputHash), which advances once per turn
//   - fallback: the from→to flip
//
// Format "<child_id>@<hex16>" keeps it greppable and child-scoped.
func TurnFingerprint(e TransitionNotificationEvent) string {
	child := strings.TrimSpace(e.ChildSessionID)
	originChild := strings.TrimSpace(e.SourceRemote) + "\x00" + child
	var signal string
	switch {
	case e.Kind == transitionKindFinished:
		signal = "finished|" + strings.ToLower(strings.TrimSpace(e.DoneStatus)) + "|" + strings.TrimSpace(e.DoneSummary)
	case strings.TrimSpace(e.LastOutputHash) != "":
		signal = "turn|" + strings.TrimSpace(e.LastOutputHash)
	default:
		signal = "flip|" + strings.ToLower(strings.TrimSpace(e.FromStatus)) + ">" + strings.ToLower(strings.TrimSpace(e.ToStatus))
	}
	sum := sha256.Sum256([]byte(originChild + "@" + signal))
	return child + "@" + hex.EncodeToString(sum[:])[:16]
}

// CommitToInbox writes one completion record to the parent's durable inbox with
// LAST-WINS-PER-CHILD semantics: any existing unacked record for the same child
// is dropped first, so there is at most ONE pending record per child (issue
// #1225 — kills flood at the source; the old path appended one line per busy
// retry). The write is atomic (temp file + rename via rewriteInboxLocked, then
// a single append under the same lock). Stamps TurnFingerprint when absent.
//
// This is the unified producer entry point for both the interactive
// (running→waiting) and one-shot (run-task kernel-exit) paths.
func CommitToInbox(parentSessionID string, event TransitionNotificationEvent) error {
	if strings.TrimSpace(parentSessionID) == "" {
		return errors.New("inbox commit: empty parent session id")
	}
	// Audit B6: cap DoneSummary at the producer so a worker dumping a large log
	// into its summary can't grow a JSONL line past the scanner cap and fail the
	// drain. The turn_fingerprint is derived AFTER capping so the fingerprint is
	// stable for a given (capped) summary. The injected reason only ever shows a
	// one-line summary anyway, so the truncated prefix is sufficient signal.
	event.DoneSummary = capDoneSummary(event.DoneSummary)
	if event.TurnFingerprint == "" {
		event.TurnFingerprint = TurnFingerprint(event)
	}
	if event.TargetSessionID == "" {
		event.TargetSessionID = parentSessionID
	}

	path := InboxPathFor(parentSessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	fileLock, err := AcquireConfigFileLock(path)
	if err != nil {
		return fmt.Errorf("lock inbox commit: %w", err)
	}
	defer fileLock.Release()

	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	// Last-wins: drop any prior pending record for this child before appending
	// the fresh one. rewriteInboxLocked is atomic and invalidates the
	// fingerprint cache for the path.
	child := event.ChildSessionID
	source := event.SourceRemote
	if _, err := rewriteInboxLocked(path, func(ev TransitionNotificationEvent) bool {
		return ev.ChildSessionID == child && ev.SourceRemote == source
	}); err != nil {
		return err
	}

	return appendInboxLineLocked(path, event)
}

// appendInboxLineLocked marshals one event and atomically installs an old-or-new
// complete inbox file. Caller holds inboxWriteMu. It also refreshes the
// process-local fingerprint cache so WriteInboxEvent's dedup stays consistent.
func appendInboxLineLocked(path string, event TransitionNotificationEvent) error {
	fp := EventFingerprint(event)
	line, err := json.Marshal(inboxWireEvent{TransitionNotificationEvent: event, Fingerprint: fp})
	if err != nil {
		return err
	}
	if err := atomicAppendInboxLineLocked(path, line); err != nil {
		return err
	}
	seen, ok := inboxFingerprintCache[path]
	if !ok {
		seen = map[string]struct{}{}
		inboxFingerprintCache[path] = seen
	}
	seen[fp] = struct{}{}
	return nil
}

// --- dead-letter (bounded terminal state for unresolvable targets) -----------

// MaxUnresolvedAttempts bounds how many times the producer re-attempts an
// unresolvable target before the record is moved to the dead-letter store and
// the miss is logged ONCE. The old path logged dropped_no_target on every ~1s
// poll forever; this caps it (Temporal/DBOS/outbox all cap retries).
const MaxUnresolvedAttempts = 5

// DeadLetterDir returns the directory holding dead-lettered inbox records.
func DeadLetterDir() string {
	return filepath.Join(InboxDir(), "dead-letter")
}

// DeadLetterPathFor returns the dead-letter JSONL path for a child.
func DeadLetterPathFor(childSessionID string) string {
	return filepath.Join(DeadLetterDir(), sanitizeInboxName(childSessionID)+".jsonl")
}

// DeadLetterSink tracks per-child unresolved attempt counts and emits exactly
// one missed-log line when a record crosses MaxUnresolvedAttempts. Concurrency-
// safe. The missed-log path is injectable for tests.
type DeadLetterSink struct {
	missedPath string
	mu         sync.Mutex
	attempts   map[string]int
	logged     map[string]bool
}

// NewDeadLetterSink builds a sink that writes its single missed line to
// missedPath (use the notifier-missed.log path in production).
func NewDeadLetterSink(missedPath string) *DeadLetterSink {
	return &DeadLetterSink{
		missedPath: missedPath,
		attempts:   map[string]int{},
		logged:     map[string]bool{},
	}
}

// RecordUnresolvable accounts one failed attempt to resolve event's target.
// It returns true exactly once — on the attempt that crosses
// MaxUnresolvedAttempts — at which point the record is parked in the
// dead-letter store and a single missed line is written. Further calls for the
// same child are no-ops (no runaway, no second log line).
func (s *DeadLetterSink) RecordUnresolvable(event TransitionNotificationEvent) bool {
	child := strings.TrimSpace(event.ChildSessionID)
	if child == "" {
		return false
	}
	s.mu.Lock()
	if s.logged[child] {
		s.mu.Unlock()
		return false
	}
	s.attempts[child]++
	n := s.attempts[child]
	if n < MaxUnresolvedAttempts {
		s.mu.Unlock()
		return false
	}
	s.logged[child] = true
	s.mu.Unlock()

	event.Attempts = n
	appended, err := writeDeadLetter(event)
	if err != nil {
		return false
	}
	if appended {
		s.writeMissedOnce(event)
	}
	return true
}

func (s *DeadLetterSink) writeMissedOnce(event TransitionNotificationEvent) {
	if strings.TrimSpace(s.missedPath) == "" {
		return
	}
	// Audit B4: the missed-log line is the operator's ONE signal that a
	// completion was dropped (unresolvable target). A silent failure to write it
	// leaves zero visibility, so surface every error path here.
	reason := strings.TrimSpace(event.DeadLetterReason)
	if reason == "" {
		reason = deadLetterReasonUnresolvable
	}
	if err := os.MkdirAll(filepath.Dir(s.missedPath), 0o755); err != nil {
		commsLog.Warn("dead_letter_missed_log_mkdir_failed",
			slog.String("child", event.ChildSessionID), slog.String("error", err.Error()))
		return
	}
	entry := map[string]any{
		"ts":       time.Now().Format(time.RFC3339Nano),
		"target":   event.TargetSessionID,
		"child":    event.ChildSessionID,
		"reason":   reason,
		"attempts": event.Attempts,
		"fp":       EventFingerprint(event),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		commsLog.Warn("dead_letter_missed_log_marshal_failed",
			slog.String("child", event.ChildSessionID), slog.String("error", err.Error()))
		return
	}
	f, err := os.OpenFile(s.missedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		commsLog.Warn("dead_letter_missed_log_open_failed",
			slog.String("child", event.ChildSessionID), slog.String("error", err.Error()))
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		commsLog.Warn("dead_letter_missed_log_write_failed",
			slog.String("child", event.ChildSessionID), slog.String("error", err.Error()))
	}
}

// writeDeadLetter durably appends a record to the child's dead-letter JSONL
// file unless that stable event fingerprint is already present. The file is
// the cross-restart dedup ledger; the file lock makes read-check-append atomic
// across daemon processes. appended is true only for a newly installed record.
func writeDeadLetter(event TransitionNotificationEvent) (appended bool, err error) {
	path := DeadLetterPathFor(event.ChildSessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	fileLock, err := AcquireConfigFileLock(path)
	if err != nil {
		return false, fmt.Errorf("lock dead-letter: %w", err)
	}
	defer fileLock.Release()

	fp := EventFingerprint(event)
	if found, err := deadLetterContainsFingerprint(path, fp); err != nil {
		return false, err
	} else if found {
		return false, nil
	}
	line, err := json.Marshal(inboxWireEvent{
		TransitionNotificationEvent: event,
		Fingerprint:                 fp,
	})
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return false, err
	}
	// Audit B2: fsync the dead-letter append. Dead-letter is the operator's
	// terminal forensic trail for an unresolvable completion; it must survive a
	// crash, same as the primary inbox append.
	if err := f.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

func deadLetterContainsFingerprint(path, fingerprint string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
	for scanner.Scan() {
		var wire inboxWireEvent
		if json.Unmarshal(scanner.Bytes(), &wire) == nil && wire.Fingerprint == fingerprint {
			return true, nil
		}
	}
	return false, scanner.Err()
}

// CountDeadLetterRecords returns the number of unresolved records currently in
// the dead-letter directory and the discovery-only _unowned ledger. Inbox
// drain uses this to avoid reporting a clean state while undelivered events are
// parked out of sight.
func CountDeadLetterRecords() (int, error) {
	entries, err := os.ReadDir(DeadLetterDir())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		entries = nil
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(DeadLetterDir(), entry.Name()))
		if err != nil {
			return count, err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
		for scanner.Scan() {
			// Unknown/corrupt is still pending operator work. Counting every
			// nonblank physical record prevents a truncated legacy append from
			// making a non-empty ledger look clean (#1877).
			if strings.TrimSpace(scanner.Text()) != "" {
				count++
			}
		}
		scanErr := scanner.Err()
		closeErr := f.Close()
		if scanErr != nil {
			return count, scanErr
		}
		if closeErr != nil {
			return count, closeErr
		}
	}
	unowned, err := countNonblankInboxRecords(InboxPathFor(UnownedInboxID))
	return count + unowned, err
}

func countNonblankInboxRecords(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count, scanner.Err()
}

// --- unified producer commit (shared by interactive + one-shot) -------------

// resolveParentIDForInbox loads the registry and applies the
// suppression/orphan/conductor guards, returning the resolved parent instance to
// commit to (the instance — not just its id — so the caller can idle-gate a
// wake-nudge against the same freshly-resolved status without a second tmux
// probe). transient is true on a storage error (the caller should retry later
// rather than dead-letter). A nil parent with transient=false means the event is
// terminally undeliverable (orphan, removed child, self-pointing conductor,
// no-notify) and should be dead-lettered.
func (n *TransitionNotifier) resolveParentIDForInbox(event TransitionNotificationEvent) (parent *Instance, transient bool, reason string) {
	storage, err := NewStorageWithProfile(event.Profile)
	if err != nil {
		return nil, true, ""
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return nil, true, ""
	}
	byID := make(map[string]*Instance, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
	}

	child := byID[event.ChildSessionID]
	if child == nil {
		// Child removed between commit/observe and resolve — terminal, but the
		// operator should know a completion was dropped (audit B5).
		return nil, false, deadLetterReasonChildMissing
	}
	if child.NoTransitionNotify {
		return nil, false, deadLetterReasonNoNotify
	}
	// Top-level conductor self-suppress (issue #824 cause B): the root is not
	// an orphan, drop silently.
	if strings.TrimSpace(child.ParentSessionID) == "" && isConductorSessionTitle(child.Title) {
		return nil, false, deadLetterReasonSelfConductor
	}
	// Orphan-on-creation guard (issue #805 cause A): log one WARN per orphan.
	if strings.TrimSpace(child.ParentSessionID) == "" {
		n.logOrphanOnce(event, child.ID)
		return nil, false, deadLetterReasonOrphan
	}
	if strings.TrimSpace(child.ParentSessionID) == child.ID && isConductorSessionTitle(child.Title) {
		return nil, false, deadLetterReasonSelfConductor
	}
	// Parent referenced but not present in this profile's registry: removed
	// mid-flight, or the child's parent lives in a DIFFERENT profile (we only
	// load event.Profile's registry). Either way it's terminal — but distinguish
	// it so the operator isn't left guessing (audit B5).
	if byID[strings.TrimSpace(child.ParentSessionID)] == nil {
		return nil, false, deadLetterReasonParentMissing
	}
	parent = resolveParentNotificationTarget(child, byID)
	if parent == nil {
		return nil, false, deadLetterReasonUnresolvable
	}
	return parent, false, ""
}

// commitEventToInbox is the unified producer entry point: it resolves the
// parent and commits the event to the durable per-parent outbox (last-wins).
// Returns committed=true when the record durably landed; transient=true when a
// retryable error (storage/fs) occurred. committed=false, transient=false means
// terminally undeliverable — the caller dead-letters.
func (n *TransitionNotifier) commitEventToInbox(event TransitionNotificationEvent) (committed bool, transient bool, reason string) {
	parent, t, reason := n.resolveParentIDForInbox(event)
	if t {
		return false, true, ""
	}
	if parent == nil {
		// Missing and non-live parents do not make the event disposable. Persist
		// it in the reserved, drainable unowned ledger. Deliberate suppression
		// and a removed child remain terminal drops.
		if isUnownedReason(reason) {
			event.DeadLetterReason = reason
			written, err := recordUnownedTransition(event)
			if err != nil {
				return false, true, ""
			}
			if written {
				event.TargetKind = "unowned"
				event.DeliveryResult = transitionDeliveryCommitted
				n.logEvent(event)
			}
			// Keep the existing operator-visible forensic copy/missed-log for
			// non-benign terminal reasons. The durable delivery result remains a
			// success because _unowned is now the actionable copy.
			n.terminalDrop(event, reason)
			// Preserve the reason in the result so completion replay can distinguish
			// this discovery copy from an ackable parent-inbox commit.
			return true, false, reason
		}
		return false, false, reason
	}
	parentID := parent.ID
	event.TargetSessionID = parentID
	event.TargetKind = "parent"
	event.DeliveryResult = transitionDeliveryCommitted
	if event.TurnFingerprint == "" {
		event.TurnFingerprint = TurnFingerprint(event)
	}
	if err := CommitToInbox(parentID, event); err != nil {
		return false, true, ""
	}
	n.logEvent(event)
	// Issue #1225 Tier-2: now that the record durably landed, wake an IDLE parent
	// to drain it immediately instead of on its next ~14-min heartbeat. This is
	// the event-driven trigger — fired the moment the completion is committed,
	// not on a poll. Best-effort and non-fatal: a dropped nudge is harmless
	// because this same record is still drained on the parent's next turn.
	n.fireWakeNudge(parent, event)
	return true, false, ""
}

// ReadDeadLetter returns the dead-lettered records for a child (empty if none).
//
// Audit B3: corrupt lines are SKIPPED (matching ReadAndTruncateInbox), not
// fatal. Dead-letter is the operator's last-resort forensic trail; one
// garbled/truncated line must not hide every valid record after it. The scanner
// uses the raised line cap so a fat (capped) summary still reads back.
func ReadDeadLetter(childSessionID string) ([]TransitionNotificationEvent, error) {
	f, err := os.Open(DeadLetterPathFor(childSessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []TransitionNotificationEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		ev, derr := decodeInboxLine([]byte(line))
		if derr != nil {
			continue // skip corrupt lines rather than blinding the operator to the rest
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}
