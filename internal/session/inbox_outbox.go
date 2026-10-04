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
	"slices"
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
// side: distinct-turn retention, the turn_fingerprint for exactly-once
// consumer effects, and the bounded dead-letter path that replaces the
// dropped_no_target ~1/sec runaway with a terminal state logged once.

// maxDoneSummaryBytes bounds the per-record completion summary (audit B6). A
// worker's DoneSummary is sourced from a sentinel with no inherent size limit;
// without a cap a worker that dumps a large log into it could grow a single
// JSONL line past the scanner cap and fail the entire drain. 32 KB is generous
// for a human-readable completion summary while keeping the line scannable.
const maxDoneSummaryBytes = 32 * 1024

// maxPendingTurnsPerChild preserves distinct turn notifications without
// allowing one stopped/no-longer-draining child producer to grow its parent's
// inbox without bound. The total valid queue is therefore bounded by the
// number of children. A turn past the bound is not evicted and not refused:
// it is folded into ONE overflow digest record per child (issue #2481 item 7)
// that carries the newest turn and counts every folded turn, so the queue
// holds at most maxPendingTurnsPerChild+1 records per child and the producer
// never fails or retries on a full inbox.
const maxPendingTurnsPerChild = 64

// inboxCommitOutcome says what a successful commitToInbox did.
type inboxCommitOutcome int

const (
	inboxCommitAdded     inboxCommitOutcome = iota // a new record was queued
	inboxCommitReplaced                            // a pending copy of the same turn was replaced (a retry)
	inboxCommitDigested                            // the turn was folded into the child's overflow digest
	inboxCommitUnchanged                           // a replay was already counted in the digest
)

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
//   - stale-hash transitions (OutputHashStale, issue #2184): the from→to flip
//     plus the emit instant. The hash did not advance since the child's last
//     notified turn, so it cannot identify THIS turn; keying on it would hand a
//     new completion the fingerprint of an already-consumed one and the drain
//     would drop it. The emit instant is safe here for the reason
//     unownedTurnSignal gives: a transition has one producer that stamps it
//     once, so a retry of the same stamped record still collapses.
//   - fallback: the from→to flip
//
// Format "<child_id>@<hex16>" keeps it greppable and child-scoped.
func TurnFingerprint(e TransitionNotificationEvent) string {
	child := strings.TrimSpace(e.ChildSessionID)
	originChild := strings.TrimSpace(e.SourceRemote) + "\x00" + child
	flip := strings.ToLower(strings.TrimSpace(e.FromStatus)) + ">" + strings.ToLower(strings.TrimSpace(e.ToStatus))
	var signal string
	switch {
	case e.Kind == transitionKindFinished:
		signal = "finished|" + strings.ToLower(strings.TrimSpace(e.DoneStatus)) + "|" + strings.TrimSpace(e.DoneSummary)
		// Issue #2469: a sentinel turn is one record, so a later turn that
		// repeats an earlier completion's status and summary must not collide
		// with the consumed one. The turn signal (turn:<uuid>) tells them
		// apart; records from the hook-file path carry none and keep the
		// legacy key.
		if hash := strings.TrimSpace(e.LastOutputHash); hash != "" {
			signal += "|" + hash
		}
	case e.OutputHashStale:
		signal = "flip|" + flip + "|" + emitInstantSignal(e.Timestamp)
	case strings.TrimSpace(e.LastOutputHash) != "":
		signal = "turn|" + strings.TrimSpace(e.LastOutputHash)
	default:
		signal = "flip|" + flip
	}
	sum := sha256.Sum256([]byte(originChild + "@" + signal))
	return child + "@" + hex.EncodeToString(sum[:])[:16]
}

// CommitToInbox writes one completion record to the parent's durable inbox with
// EXACTLY-ONCE-PER-TURN semantics: a retry replaces its matching pending turn,
// while every distinct unacknowledged turn remains queued. The write is atomic
// (temp file + rename via rewriteInboxLocked, then
// a single append under the same lock). Stamps TurnFingerprint when absent.
//
// This is the unified producer entry point for both the interactive
// (running→waiting) and one-shot (run-task kernel-exit) paths.
func CommitToInbox(parentSessionID string, event TransitionNotificationEvent) error {
	_, _, err := commitToInbox(parentSessionID, event)
	return err
}

// commitToInbox is CommitToInbox that also returns the record as stored and
// whether the write added it, replaced a still-pending copy of the same turn
// (a retry), folded it into the child's overflow digest, or left an already
// counted digest replay unchanged.
func commitToInbox(parentSessionID string, event TransitionNotificationEvent) (stored TransitionNotificationEvent, outcome inboxCommitOutcome, err error) {
	if strings.TrimSpace(parentSessionID) == "" {
		return event, inboxCommitAdded, errors.New("inbox commit: empty parent session id")
	}
	// Audit B6: cap DoneSummary at the producer so a worker dumping a large log
	// into its summary can't grow a JSONL line past the scanner cap and fail the
	// drain. The turn_fingerprint is derived AFTER capping so the fingerprint is
	// stable for a given (capped) summary. The injected reason only ever shows a
	// one-line summary anyway, so the truncated prefix is sufficient signal.
	event.DoneSummary = capDoneSummary(event.DoneSummary)
	// Issue #2469: the carried child text has a hard ceiling too, whatever
	// the producer's configured cap was.
	event.Text = CapTurnText(event.Text, MaxTurnTextBytes)
	if event.TurnFingerprint == "" {
		event.TurnFingerprint = TurnFingerprint(event)
	}
	if event.TargetSessionID == "" {
		event.TargetSessionID = parentSessionID
	}

	path := InboxPathFor(parentSessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return event, inboxCommitAdded, err
	}
	fileLock, err := AcquireConfigFileLock(path)
	if err != nil {
		return event, inboxCommitAdded, fmt.Errorf("lock inbox commit: %w", err)
	}
	defer fileLock.Release()

	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	pendingForChild, retry, digest, err := pendingTurnsForChildLocked(path, event)
	if err != nil {
		return event, inboxCommitAdded, err
	}
	outcome = inboxCommitAdded
	switch {
	case digest != nil && event.OverflowTurns == 0 && digestHoldsTurn(*digest, event.TurnFingerprint):
		// A replay of a turn already folded into the digest: it is counted
		// there, and replacing the digest with it would lose the count.
		return *digest, inboxCommitUnchanged, nil
	case digest != nil && event.OverflowTurns == 0 && sameInboxTurn(*digest, event):
		event = foldIntoOverflowDigest(event, digest)
		outcome = inboxCommitDigested
	case retry:
		outcome = inboxCommitReplaced
	case pendingForChild >= maxPendingTurnsPerChild:
		event = foldIntoOverflowDigest(event, digest)
		outcome = inboxCommitDigested
	}

	// Drop only a retry of this exact turn before appending the fresh copy.
	// rewriteInboxLocked is atomic and invalidates the
	// fingerprint cache for the path.
	if _, err := rewriteInboxLocked(path, func(ev TransitionNotificationEvent) bool {
		return sameInboxTurn(ev, event)
	}); err != nil {
		return event, outcome, err
	}

	return event, outcome, appendInboxLineLocked(path, event)
}

// sameInboxTurn reports whether the pending record ev is the turn event
// carries: the same turn fingerprint, or (issue #2481) the same child
// transcript turn uuid, so a turn that escalated after its record was written
// (info, then error or a completion) replaces its pending record instead of
// queueing a second one. Records without a turn uuid (legacy, hook-file
// completions) match on the fingerprint only.
func sameInboxTurn(ev, event TransitionNotificationEvent) bool {
	fp := ev.TurnFingerprint
	if fp == "" {
		fp = TurnFingerprint(ev)
	}
	if fp == event.TurnFingerprint {
		return true
	}
	uuid := strings.TrimSpace(event.TurnUUID)
	return uuid != "" && strings.TrimSpace(ev.TurnUUID) == uuid &&
		ev.ChildSessionID == event.ChildSessionID && ev.SourceRemote == event.SourceRemote
}

// foldIntoOverflowDigest turns event (a turn past the per-child bound) into
// the child's overflow digest record. The digest carries the newest turn's
// status and text, keeps the fingerprint of the digest it replaces (so it is
// still one pending record, and a consumer that already acted on an older copy
// of it does not act twice), and counts every turn folded into it. A turn that
// is itself a digest (pulled from a remote) adds its whole count. An urgent
// turn keeps the digest urgent.
func foldIntoOverflowDigest(event TransitionNotificationEvent, prev *TransitionNotificationEvent) TransitionNotificationEvent {
	// The newest folded turn may escalate under the same UUID. Update its
	// content and tier without counting the logical turn a second time.
	alreadyCounted := prev != nil && event.OverflowTurns == 0 && sameInboxTurn(*prev, event)
	if event.OverflowTurns <= 0 {
		event.OverflowTurns = 1
	}
	if prev == nil {
		return event
	}
	if alreadyCounted {
		event.OverflowTurns = 0
	}
	folds := append(append([]string(nil), prev.OverflowFolded...), event.OverflowFolded...)
	folds = append(folds, event.TurnFingerprint)
	if len(folds) > maxPendingTurnsPerChild {
		folds = folds[len(folds)-maxPendingTurnsPerChild:]
	}
	event.OverflowFolded = folds
	event.TurnFingerprint = prev.TurnFingerprint
	event.OverflowTurns += prev.OverflowTurns
	if prev.IsUrgent() && !event.IsUrgent() {
		event.Tier = prev.Tier
	}
	event.Question = event.Question || prev.Question
	return event
}

// digestHoldsTurn reports whether the turn with fingerprint fp is already
// counted in digest.
func digestHoldsTurn(digest TransitionNotificationEvent, fp string) bool {
	return fp == digest.TurnFingerprint || slices.Contains(digest.OverflowFolded, fp)
}

// pendingTurnsForChildLocked counts the child's durable pending turns, reports
// whether event is a retry already present in the queue, and returns the
// child's pending overflow digest (nil when none). Caller holds inboxWriteMu
// and the cross-process config-file lock.
func pendingTurnsForChildLocked(path string, event TransitionNotificationEvent) (count int, retry bool, digest *TransitionNotificationEvent, err error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil, nil
		}
		return 0, false, nil, err
	}
	defer f.Close()

	// A crash between rewrite and append, or a legacy file, can leave two rows
	// for one logical turn. Counting rows would then spend the bound twice.
	seen := map[string]struct{}{}
	if err := forEachInboxLine(f, func(line []byte) error {
		ev, decodeErr := decodeInboxLine(line)
		if decodeErr != nil {
			return nil
		}
		if ev.ChildSessionID != event.ChildSessionID {
			return nil
		}
		fp := ev.TurnFingerprint
		if fp == "" {
			fp = TurnFingerprint(ev)
		}
		if sameInboxTurn(ev, event) {
			retry = true
		}
		if ev.OverflowTurns > 0 {
			ev.TurnFingerprint = fp
			digest = &ev
		}
		seen[fp] = struct{}{}
		return nil
	}); err != nil {
		return 0, false, nil, err
	}
	return len(seen), retry, digest, nil
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
	if err := appendRotatingLogLine(s.missedPath, line, transitionLogRotation); err != nil {
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
	defer closeFile(f, &err)
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

// DeadLetterStoreCounts is the per-store breakdown of parked records.
type DeadLetterStoreCounts struct {
	DeadLetter int `json:"dead_letter"`
	Unowned    int `json:"unowned"`
}

// Total is the sum across stores.
func (c DeadLetterStoreCounts) Total() int { return c.DeadLetter + c.Unowned }

// CountDeadLetterRecords returns the number of unresolved records currently in
// the dead-letter directory and the discovery-only _unowned ledger. Inbox
// drain uses this to avoid reporting a clean state while undelivered events are
// parked out of sight.
func CountDeadLetterRecords() (int, error) {
	counts, err := CountDeadLetterStores()
	return counts.Total(), err
}

// CountDeadLetterStores is CountDeadLetterRecords with the per-store split, so
// an operator can tell a dead letter from a discovery copy (audit P1-4).
func CountDeadLetterStores() (DeadLetterStoreCounts, error) {
	var counts DeadLetterStoreCounts
	entries, err := os.ReadDir(DeadLetterDir())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return counts, err
		}
		entries = nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		// Unknown/corrupt is still pending operator work. Counting every
		// nonblank physical record prevents a truncated legacy append from
		// making a non-empty ledger look clean (#1877).
		n, err := countNonblankInboxRecords(filepath.Join(DeadLetterDir(), entry.Name()))
		counts.DeadLetter += n
		if err != nil {
			return counts, err
		}
	}
	unowned, err := countNonblankInboxRecords(InboxPathFor(UnownedInboxID))
	counts.Unowned = unowned
	return counts, err
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
//
// sender is the session a tagged send's reply must also reach (see
// replySenderFor), resolved independently of the parent.
func (n *TransitionNotifier) resolveParentIDForInbox(event TransitionNotificationEvent) (parent, sender *Instance, transient bool, reason string) {
	storage, err := NewStorageWithProfile(event.Profile)
	if err != nil {
		return nil, nil, true, ""
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return nil, nil, true, ""
	}
	byID := make(map[string]*Instance, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
	}

	child := byID[event.ChildSessionID]
	if child == nil {
		// Child removed between commit/observe and resolve — terminal, but the
		// operator should know a completion was dropped (audit B5).
		return nil, nil, false, deadLetterReasonChildMissing
	}
	if child.NoTransitionNotify {
		return nil, nil, false, deadLetterReasonNoNotify
	}
	parent, reason = n.resolveInboxParent(event, child, byID)
	return parent, replySenderFor(event, child, parent, byID), false, reason
}

// resolveInboxParent applies the conductor/orphan/missing-parent guards to a
// child that is in the registry and accepts transition events. A nil parent
// comes with the terminal reason.
func (n *TransitionNotifier) resolveInboxParent(event TransitionNotificationEvent, child *Instance, byID map[string]*Instance) (*Instance, string) {
	parentID := strings.TrimSpace(child.ParentSessionID)
	// Top-level (or self-pointing) conductor self-suppress (issue #824 cause
	// B): the root is not an orphan, drop silently. The same predicate decides
	// what the cursor export ships (remote_talkback.go).
	if isSelfSuppressedConductor(child) {
		return nil, deadLetterReasonSelfConductor
	}
	// Orphan-on-creation guard (issue #805 cause A): log one WARN per orphan.
	if parentID == "" {
		n.logOrphanOnce(event, child.ID)
		return nil, deadLetterReasonOrphan
	}
	// Parent referenced but not present in this profile's registry: removed
	// mid-flight, or the child's parent lives in a DIFFERENT profile (we only
	// load event.Profile's registry). Either way it's terminal — but distinguish
	// it so the operator isn't left guessing (audit B5).
	if byID[parentID] == nil {
		return nil, deadLetterReasonParentMissing
	}
	parent := resolveParentNotificationTarget(child, byID)
	if parent == nil {
		return nil, deadLetterReasonUnresolvable
	}
	return parent, ""
}

// replySenderFor resolves the sender of the tagged send that started this
// turn (comms redesign PR5), or of the held send whose background work this
// task turn settled (issue #2473), whether or not the child has a parent: a
// top-level conductor or a peer answering a question must reach the asker
// too. The sender must still be in the registry, must be a Claude-compatible
// session (only those drain a reply at prompt time; a wake line typed into a
// shell or another harness would run or strand it) and must not be the child
// itself (a self-send), its parent or the resolved notification target
// (those already hold the record, so no duplicate). An unknown or removed
// sender gets nothing.
func replySenderFor(event TransitionNotificationEvent, child, parent *Instance, byID map[string]*Instance) *Instance {
	if !eventAnswersSend(event) {
		return nil
	}
	sender := byID[strings.TrimSpace(event.FromID)]
	if sender == nil || !IsClaudeCompatible(sender.Tool) {
		return nil
	}
	alreadyHoldsRecord := sender.ID == child.ID ||
		sender.ID == strings.TrimSpace(child.ParentSessionID) ||
		(parent != nil && sender.ID == parent.ID)
	if alreadyHoldsRecord {
		return nil
	}
	return sender
}

// commitEventToInbox is the unified producer entry point: it resolves the
// parent and commits the event to the durable per-parent outbox (last-wins).
// Returns committed=true when the record durably landed; transient=true when a
// retryable error (storage/fs) occurred. committed=false, transient=false means
// terminally undeliverable — the caller dead-letters.
func (n *TransitionNotifier) commitEventToInbox(event TransitionNotificationEvent) (committed bool, transient bool, reason string) {
	parent, sender, t, reason := n.resolveParentIDForInbox(event)
	if t {
		return false, true, ""
	}
	// The sender's copy is taken before the parent paths stamp their own
	// target fields onto event.
	reply := event
	if parent == nil {
		committed, transient, reason = n.commitParentlessEvent(event, reason)
		// No parent holds the turn (a top-level conductor, a peer, an unowned
		// or retried record): the session that asked still gets its answer,
		// without waiting on the unowned ledger.
		n.commitReplyToSender(sender, reply)
		return committed, transient, reason
	}
	parentID := parent.ID
	event.TargetSessionID = parentID
	event.TargetKind = "parent"
	event.DeliveryResult = transitionDeliveryCommitted
	if event.TurnFingerprint == "" {
		event.TurnFingerprint = TurnFingerprint(event)
	}
	// A turn the parent already has is not written again (issue #2481: a child
	// with no turn signal flapping running->waiting re-sent one fingerprint 136
	// times). Either its consumed-turn ledger holds it, so the next drain would
	// drop the copy and a wake would cost one empty "[INBOX]" turn (issue
	// #2240), or the same record is still pending and already owed a wake.
	// Report it committed (exactly-once effects): no record, no log line, no
	// wake. A pending copy with a different tier is still replaced.
	if turnAlreadyConsumed(parentID, event.TurnFingerprint) || pendingSameRecord(parentID, event) {
		commsLog.Debug("commit_skipped_known_turn",
			slog.String("parent", parentID), slog.String("turn", event.TurnFingerprint))
		n.clearCommitBackpressure(event.ChildSessionID)
		n.commitReplyToSender(sender, reply)
		return true, false, ""
	}
	stored, outcome, err := commitToInbox(parentID, event)
	if err != nil {
		// The turn is retried on the next poll, but the sender's answer must
		// not wait on the parent's backlog.
		n.commitReplyToSender(sender, reply)
		return false, true, ""
	}
	n.commitReplyToSender(sender, reply)
	if outcome == inboxCommitUnchanged {
		return true, false, ""
	}
	if outcome == inboxCommitDigested {
		n.noteCommitBackpressure(stored)
	} else {
		n.clearCommitBackpressure(event.ChildSessionID)
	}
	// Log the incoming turn identity, not the digest's fixed fingerprint.
	n.logEvent(event)
	n.wakeCommittedInbox(parent, parentWakeEvent(stored, parent))
	return true, false, ""
}

// wakeCommittedInbox applies the same consumed-turn, tier and idle/debounce
// gates to ordinary records, overflow digests and replies.
func (n *TransitionNotifier) wakeCommittedInbox(parent *Instance, event TransitionNotificationEvent) {
	parentID := parent.ID
	// A turn the parent's consumed-turn ledger already holds is dropped by its
	// next drain, so waking it would cost one empty "[INBOX]" turn for nothing
	// (issue #2240, notify-daemon restart re-delivery). The record itself is left
	// as committed: ledger dedup semantics and delivery ordering are unchanged,
	// only the nudge is withheld.
	if turnAlreadyConsumed(parentID, event.TurnFingerprint) {
		commsLog.Debug("wake_nudge_skipped_consumed_turn",
			slog.String("parent", parentID), slog.String("turn", event.TurnFingerprint))
		return
	}
	// Issue #2469, design principle 1: only the tiers listed in [inbox]
	// wake_on (default: urgent) wake the parent. An info record stays durably
	// queued and rides the parent's next turn or the info digest; it is never
	// lost, it just does not buy a turn of its own.
	if !ResolveInboxConfig(parent.Title).WakesFor(event.Tier) {
		_ = BumpInboxStats(parentID, func(s *InboxStats) { s.WakeupsSuppressed++ })
		commsLog.Debug("wake_nudge_skipped_tier",
			slog.String("parent", parentID), slog.String("tier", event.Tier), slog.String("turn", event.TurnFingerprint))
		return
	}
	_ = BumpInboxStats(parentID, func(s *InboxStats) { s.WakeupsUrgent++ })
	// Issue #1225 Tier-2: now that the record durably landed, wake an IDLE parent
	// to drain it immediately instead of on its next ~14-min heartbeat. This is
	// the event-driven trigger — fired the moment the completion is committed,
	// not on a poll. Best-effort and non-fatal: a dropped nudge is harmless
	// because this same record is still drained on the parent's next turn.
	n.fireWakeNudge(parent, event)
}

// pendingSameRecord reports whether the parent's inbox already holds this
// child's record for the same turn fingerprint, kind and tier.
func pendingSameRecord(parentID string, event TransitionNotificationEvent) bool {
	pending, err := ReadInboxEvents(parentID)
	if err != nil {
		return false
	}
	for _, ev := range pending {
		fp := ev.TurnFingerprint
		if fp == "" {
			fp = TurnFingerprint(ev)
		}
		if ev.ChildSessionID == event.ChildSessionID && fp == event.TurnFingerprint &&
			ev.Kind == event.Kind && ev.Tier == event.Tier {
			return true
		}
	}
	return false
}

// InboxTargetKindReply is the TargetKind of a record that answers the
// receiving session's own tagged send (comms redesign PR5), as opposed to a
// child's turn delivered to its "parent".
const InboxTargetKindReply = "reply"

// commitParentlessEvent handles a turn whose child has no resolvable parent.
// Missing and non-live parents do not make the event disposable: it is
// persisted in the reserved, drainable unowned ledger. Deliberate suppression
// and a removed child remain terminal drops.
func (n *TransitionNotifier) commitParentlessEvent(event TransitionNotificationEvent, reason string) (committed, transient bool, resultReason string) {
	if !isUnownedReason(reason) {
		return false, false, reason
	}
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

// parentWakeEvent is the event the parent's wake gate sees. A turn that
// answers the parent's OWN tagged send is a reply to it (comms redesign PR5):
// the parent asked, so an URGENT reply (a question back, a sentinel, an error)
// wakes it as a reply target whatever its title, as a sibling sender would be.
// A plain answer is info and wakes nobody; the parent reads it at its next
// prompt. Only a Claude-compatible parent qualifies (the reply gate requires
// it); any other parent keeps the conductor-only gate. The committed record
// keeps TargetKind "parent".
func parentWakeEvent(event TransitionNotificationEvent, parent *Instance) TransitionNotificationEvent {
	if eventAnswersSend(event) && parent != nil &&
		strings.TrimSpace(event.FromID) == parent.ID && IsClaudeCompatible(parent.Tool) {
		event.TargetKind = InboxTargetKindReply
	}
	return event
}

// commitReplyToSender commits a second copy of a turn that answered a tagged
// send to the sender's own inbox (comms redesign PR5): TargetKind "reply",
// always urgent, so the session that asked is woken and its prompt-time
// drain injects the answer. It does not wait on the parent: it runs whether
// the parent's copy landed, the turn is terminally parentless, or the
// parent's commit failed and the turn will be retried. A retry is
// idempotent: a reply the sender already consumed is not committed again,
// and one still pending is replaced in place without a second wake. A
// failure is logged; the turn's own retry (if any) tries again.
func (n *TransitionNotifier) commitReplyToSender(sender *Instance, event TransitionNotificationEvent) {
	if sender == nil {
		return
	}
	if event.TurnFingerprint == "" {
		event.TurnFingerprint = TurnFingerprint(event)
	}
	if turnAlreadyConsumed(sender.ID, event.TurnFingerprint) {
		return
	}
	event.TargetSessionID = sender.ID
	event.TargetKind = InboxTargetKindReply
	event.Tier = TurnTierUrgent
	event.DeliveryResult = transitionDeliveryCommitted
	event.DeadLetterReason = ""
	stored, outcome, err := commitToInbox(sender.ID, event)
	if err != nil {
		commsLog.Warn("reply_commit_failed",
			slog.String("sender", sender.ID), slog.String("child", event.ChildSessionID), slog.String("error", err.Error()))
		return
	}
	if outcome == inboxCommitReplaced || outcome == inboxCommitUnchanged {
		// The same answer is already pending; do not announce a retry.
		return
	}
	n.logEvent(event)
	n.wakeCommittedInbox(sender, stored)
}

// noteCommitBackpressure logs a saturated parent inbox ONCE per child: from
// here on the child's turns fold into one overflow digest record instead of
// queueing one record each.
func (n *TransitionNotifier) noteCommitBackpressure(event TransitionNotificationEvent) {
	child := strings.TrimSpace(event.ChildSessionID)
	n.overflowMu.Lock()
	if n.overflowWarned == nil {
		n.overflowWarned = map[string]bool{}
	}
	already := n.overflowWarned[child]
	n.overflowWarned[child] = true
	n.overflowMu.Unlock()
	if already {
		return
	}
	slog.Warn("inbox_turn_overflow",
		slog.String("child", child),
		slog.String("parent", event.TargetSessionID),
		slog.Int("limit", maxPendingTurnsPerChild),
		slog.Int("overflow_turns", event.OverflowTurns))
}

// clearCommitBackpressure re-arms the saturation warning after a commit for
// this child succeeds.
func (n *TransitionNotifier) clearCommitBackpressure(childSessionID string) {
	child := strings.TrimSpace(childSessionID)
	n.overflowMu.Lock()
	delete(n.overflowWarned, child)
	n.overflowMu.Unlock()
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
