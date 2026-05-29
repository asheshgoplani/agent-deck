package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Issue #1225: the durable per-parent outbox is the PRIMARY delivery channel,
// not the last-resort graveyard the old push path fell into. Two producers
// (interactive running→waiting and one-shot run-task kernel-exit) commit here;
// the parent drains it on its own turn boundary. This file holds the producer
// side: last-wins-per-child commit, the turn_fingerprint for exactly-once
// consumer effects, and the bounded dead-letter path that replaces the
// dropped_no_target ~1/sec runaway with a terminal state logged once.

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
	var signal string
	switch {
	case e.Kind == transitionKindFinished:
		signal = "finished|" + strings.ToLower(strings.TrimSpace(e.DoneStatus)) + "|" + strings.TrimSpace(e.DoneSummary)
	case strings.TrimSpace(e.LastOutputHash) != "":
		signal = "turn|" + strings.TrimSpace(e.LastOutputHash)
	default:
		signal = "flip|" + strings.ToLower(strings.TrimSpace(e.FromStatus)) + ">" + strings.ToLower(strings.TrimSpace(e.ToStatus))
	}
	sum := sha256.Sum256([]byte(child + "@" + signal))
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

	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	// Last-wins: drop any prior pending record for this child before appending
	// the fresh one. rewriteInboxLocked is atomic and invalidates the
	// fingerprint cache for the path.
	child := event.ChildSessionID
	if _, err := rewriteInboxLocked(path, func(ev TransitionNotificationEvent) bool {
		return ev.ChildSessionID == child
	}); err != nil {
		return err
	}

	return appendInboxLineLocked(path, event)
}

// appendInboxLineLocked marshals one event (with its EventFingerprint embedded)
// and appends it as a JSONL line. Caller holds inboxWriteMu. Also refreshes the
// process-local fingerprint cache so WriteInboxEvent's dedup stays consistent.
func appendInboxLineLocked(path string, event TransitionNotificationEvent) error {
	fp := EventFingerprint(event)
	line, err := json.Marshal(inboxWireEvent{TransitionNotificationEvent: event, Fingerprint: fp})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
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
	_ = writeDeadLetter(event)
	s.writeMissedOnce(event)
	return true
}

func (s *DeadLetterSink) writeMissedOnce(event TransitionNotificationEvent) {
	if strings.TrimSpace(s.missedPath) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.missedPath), 0o755); err != nil {
		return
	}
	entry := map[string]any{
		"ts":       time.Now().Format(time.RFC3339Nano),
		"target":   event.TargetSessionID,
		"child":    event.ChildSessionID,
		"reason":   "dead_letter_unresolvable",
		"attempts": event.Attempts,
		"fp":       EventFingerprint(event),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.missedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// writeDeadLetter appends a record to the child's dead-letter JSONL file.
func writeDeadLetter(event TransitionNotificationEvent) error {
	path := DeadLetterPathFor(event.ChildSessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(inboxWireEvent{
		TransitionNotificationEvent: event,
		Fingerprint:                 EventFingerprint(event),
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// --- unified producer commit (shared by interactive + one-shot) -------------

// resolveParentIDForInbox loads the registry and applies the
// suppression/orphan/conductor guards, returning the resolved parent id to
// commit to. transient is true on a storage error (the
// caller should retry later rather than dead-letter). An empty parentID with
// transient=false means the event is terminally undeliverable (orphan, removed
// child, self-pointing conductor, no-notify) and should be dead-lettered.
func (n *TransitionNotifier) resolveParentIDForInbox(event TransitionNotificationEvent) (parentID string, transient bool) {
	storage, err := NewStorageWithProfile(event.Profile)
	if err != nil {
		return "", true
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return "", true
	}
	byID := make(map[string]*Instance, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
	}

	child := byID[event.ChildSessionID]
	if child == nil || child.NoTransitionNotify {
		return "", false
	}
	// Top-level conductor self-suppress (issue #824 cause B): the root is not
	// an orphan, drop silently.
	if strings.TrimSpace(child.ParentSessionID) == "" && isConductorSessionTitle(child.Title) {
		return "", false
	}
	// Orphan-on-creation guard (issue #805 cause A): log one WARN per orphan.
	if strings.TrimSpace(child.ParentSessionID) == "" {
		n.logOrphanOnce(event, child.ID)
		return "", false
	}
	if strings.TrimSpace(child.ParentSessionID) == child.ID && isConductorSessionTitle(child.Title) {
		return "", false
	}
	parent := resolveParentNotificationTarget(child, byID)
	if parent == nil {
		return "", false
	}
	return parent.ID, false
}

// commitEventToInbox is the unified producer entry point: it resolves the
// parent and commits the event to the durable per-parent outbox (last-wins).
// Returns committed=true when the record durably landed; transient=true when a
// retryable error (storage/fs) occurred. committed=false, transient=false means
// terminally undeliverable — the caller dead-letters.
func (n *TransitionNotifier) commitEventToInbox(event TransitionNotificationEvent) (committed bool, transient bool) {
	parentID, t := n.resolveParentIDForInbox(event)
	if t {
		return false, true
	}
	if parentID == "" {
		return false, false
	}
	event.TargetSessionID = parentID
	event.TargetKind = "parent"
	event.DeliveryResult = transitionDeliveryCommitted
	if event.TurnFingerprint == "" {
		event.TurnFingerprint = TurnFingerprint(event)
	}
	if err := CommitToInbox(parentID, event); err != nil {
		return false, true
	}
	n.logEvent(event)
	return true, false
}

// ReadDeadLetter returns the dead-lettered records for a child (empty if none).
func ReadDeadLetter(childSessionID string) ([]TransitionNotificationEvent, error) {
	path := DeadLetterPathFor(childSessionID)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []TransitionNotificationEvent
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		ev, err := decodeInboxLine([]byte(ln))
		if err != nil {
			return out, fmt.Errorf("dead-letter decode: %w", err)
		}
		out = append(out, ev)
	}
	return out, nil
}
