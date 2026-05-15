package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Per-conductor inbox: a JSONL file at
// <agent-deck-dir>/inboxes/<parent-session-id>.jsonl that holds transition
// events the in-process retry path could not deliver. The conductor consumes
// it on its next idle pass via `agent-deck inbox <session>` and the file is
// truncated atomically so the same event is never re-delivered (loss is
// preferable to flood once it's in the consumer's hands).
//
// Append-only writes guarantee that concurrent producers (the notifier
// daemon plus any ad-hoc CLI dispatcher) cannot clobber each other; the
// rename-on-truncate pattern keeps the read+clear pair atomic relative to
// any concurrent writer that opens with O_APPEND between the read and the
// rename.

var inboxWriteMu sync.Mutex // serializes appends to a single inbox file

// inboxFingerprintCache holds, per inbox file path, the set of event
// fingerprints already persisted. Populated lazily on first write to a path
// (by scanning the existing file) and updated on every successful append.
//
// This cache is process-local. For cross-process correctness we still scan
// the file on the first write per path within a process, so a fresh process
// won't re-append events the previous process already wrote.
//
// Issue #824: scheduleBusyRetry's exhaustion path was firing repeatedly
// for the same logical event, producing 13 duplicate JSONL lines for a
// single transition. The cache + lazy file scan reduces those to one.
var inboxFingerprintCache = map[string]map[string]struct{}{}

// InboxDir returns the directory that holds per-parent inbox files.
func InboxDir() string {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "inboxes")
	}
	return filepath.Join(dir, "inboxes")
}

// InboxPathFor returns the absolute inbox path for a given parent session id.
// The parent id is treated as a filename and must not contain path separators
// or shell metacharacters; agent-deck session ids are URL-safe by convention,
// so this is enforced by sanitizing rather than escaping.
func InboxPathFor(parentSessionID string) string {
	return filepath.Join(InboxDir(), sanitizeInboxName(parentSessionID)+".jsonl")
}

func sanitizeInboxName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "_unknown"
	}
	r := strings.NewReplacer(string(os.PathSeparator), "_", "..", "_", " ", "_")
	return r.Replace(id)
}

// WriteInboxEvent appends one event to the parent's inbox as a JSONL line.
// Safe for concurrent callers within a single process.
//
// Fingerprint dedup: events that share an EventFingerprint with one already
// persisted in the file are silently skipped. This is the producer-side
// guard for issue #824 (scheduleBusyRetry firing the same exhaustion path
// for the same logical event multiple times). Consumers still get
// at-most-once delivery via ReadAndTruncateInbox.
func WriteInboxEvent(parentSessionID string, event TransitionNotificationEvent) error {
	if strings.TrimSpace(parentSessionID) == "" {
		return errors.New("inbox: empty parent session id")
	}
	path := InboxPathFor(parentSessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	fp := EventFingerprint(event)

	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	seen, ok := inboxFingerprintCache[path]
	if !ok {
		// Lazy file scan recovers dedup state across process restarts. Without
		// this a fresh process would happily re-append events that a prior
		// process had already persisted.
		seen = loadInboxFingerprintsLocked(path)
		inboxFingerprintCache[path] = seen
	}
	if _, dup := seen[fp]; dup {
		return nil
	}

	// Embed the fingerprint into the persisted JSON so on-disk state is
	// self-describing — the file-scan recovery path can reconstruct the
	// dedup set without re-deriving fingerprints from the event body.
	type wireEvent struct {
		TransitionNotificationEvent
		Fingerprint string `json:"fp,omitempty"`
	}
	line, err := json.Marshal(wireEvent{TransitionNotificationEvent: event, Fingerprint: fp})
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
	seen[fp] = struct{}{}
	return nil
}

// loadInboxFingerprintsLocked scans an existing inbox file and returns the
// set of fingerprints already persisted. Caller holds inboxWriteMu.
//
// Two formats are tolerated: the new format with an explicit "fp" field,
// and the legacy format from before this fix where the event was stored
// without a fingerprint. For legacy lines we re-derive the fingerprint
// from the event fields so dedup still applies.
func loadInboxFingerprintsLocked(path string) map[string]struct{} {
	out := map[string]struct{}{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var probe struct {
			TransitionNotificationEvent
			Fingerprint string `json:"fp"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue
		}
		fp := probe.Fingerprint
		if fp == "" {
			fp = EventFingerprint(probe.TransitionNotificationEvent)
		}
		out[fp] = struct{}{}
	}
	return out
}

// ResetInboxFingerprintCacheForTest clears the process-local dedup cache.
// Tests use it to simulate a fresh process so the on-disk recovery path is
// exercised. Production code does not call this.
func ResetInboxFingerprintCacheForTest() {
	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()
	inboxFingerprintCache = map[string]map[string]struct{}{}
}

// defaultInboxTTL is the age past which a persisted inbox entry is swept
// by SweepInboxByTTL. Issue #962 variant (running-session): without a
// TTL, deferred_target_busy entries for children that never see another
// transition accumulate unboundedly. Seven days is the same horizon the
// deferred-queue uses for "old enough to give up on" semantics, scaled
// up from minutes to days because the inbox is the operator-facing
// drain rather than the in-process retry path.
const defaultInboxTTL = 7 * 24 * time.Hour

// InboxTTL returns the configured age past which persisted inbox events
// are swept. Honors AGENT_DECK_INBOX_TTL (parsed by time.ParseDuration)
// and falls back to defaultInboxTTL when unset or unparseable.
func InboxTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv("AGENT_DECK_INBOX_TTL"))
	if raw == "" {
		return defaultInboxTTL
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultInboxTTL
	}
	return d
}

// SweepInboxByTuple drops every entry in the parent's inbox file whose
// (child_session_id, from_status, to_status) matches the given tuple.
// Returns the count of dropped entries.
//
// Issue #962 variant: when a transition for (child, from, to) is later
// delivered successfully, any earlier persisted entry for the same
// tuple represents an event the operator no longer needs to see — the
// state it described has already been signaled to the conductor by the
// live send. Without this sweep, the inbox JSONL grows by one entry
// every time the target is busy at first-attempt time.
//
// Idempotent and best-effort: missing files are not an error. The
// rewrite is atomic via temp file + rename, mirroring
// SweepInboxesForChildSession.
func SweepInboxByTuple(parentSessionID, childSessionID, fromStatus, toStatus string) (int, error) {
	if strings.TrimSpace(parentSessionID) == "" {
		return 0, errors.New("inbox tuple sweep: empty parent session id")
	}
	if strings.TrimSpace(childSessionID) == "" {
		return 0, errors.New("inbox tuple sweep: empty child session id")
	}

	path := InboxPathFor(parentSessionID)

	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	return rewriteInboxLocked(path, func(ev TransitionNotificationEvent) bool {
		return ev.ChildSessionID == childSessionID &&
			ev.FromStatus == fromStatus &&
			ev.ToStatus == toStatus
	})
}

// SweepInboxByTTL walks every inbox file and drops entries older than
// maxAge (computed against TransitionNotificationEvent.Timestamp).
// Returns the total entries dropped across all inbox files.
//
// Issue #962 variant: defense-in-depth alongside SweepInboxByTuple. The
// tuple sweep relies on a future successful transition for the same
// (child, from, to) to clear stale entries. Children that complete and
// never transition again would otherwise leave their last
// deferred_target_busy entry in the inbox forever. The TTL puts a hard
// ceiling on inbox growth.
func SweepInboxByTTL(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, errors.New("inbox TTL sweep: non-positive maxAge")
	}

	dir := InboxDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	cutoff := time.Now().Add(-maxAge)

	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	totalDropped := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		dropped, err := rewriteInboxLocked(path, func(ev TransitionNotificationEvent) bool {
			// Drop entries whose timestamp is older than the cutoff.
			// Entries with a zero timestamp (e.g. legacy or test data
			// without a stable clock) are conservatively kept.
			if ev.Timestamp.IsZero() {
				return false
			}
			return ev.Timestamp.Before(cutoff)
		})
		if err != nil {
			return totalDropped, err
		}
		totalDropped += dropped
	}
	return totalDropped, nil
}

// rewriteInboxLocked streams one inbox file and writes out every line
// whose decoded event does NOT match shouldDrop. Returns the count of
// dropped lines. Caller holds inboxWriteMu.
//
// Mirrors the rm_sweep.go strategy: temp file + atomic rename, with
// unparseable lines preserved verbatim to avoid silent data loss during
// cleanup.
func rewriteInboxLocked(path string, shouldDrop func(TransitionNotificationEvent) bool) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	var kept [][]byte
	var dropped int
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var ev TransitionNotificationEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			kept = append(kept, append([]byte(nil), raw...))
			continue
		}
		if shouldDrop(ev) {
			dropped++
			continue
		}
		kept = append(kept, append([]byte(nil), raw...))
	}
	if err := scanner.Err(); err != nil {
		return dropped, err
	}
	_ = f.Close()

	if dropped == 0 {
		return 0, nil
	}

	if len(kept) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return dropped, err
		}
		delete(inboxFingerprintCache, path)
		return dropped, nil
	}

	tmp := path + ".sweep.tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return dropped, err
	}
	w := bufio.NewWriter(out)
	for _, line := range kept {
		if _, err := w.Write(line); err != nil {
			_ = w.Flush()
			_ = out.Close()
			_ = os.Remove(tmp)
			return dropped, err
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			_ = w.Flush()
			_ = out.Close()
			_ = os.Remove(tmp)
			return dropped, err
		}
	}
	if err := w.Flush(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return dropped, err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return dropped, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return dropped, err
	}
	delete(inboxFingerprintCache, path)
	return dropped, nil
}

// ReadAndTruncateInbox reads all events from the parent's inbox and removes
// the file. Returns an empty slice (not an error) when the inbox doesn't
// exist or holds no parseable lines.
//
// The read+truncate pair is not atomic against a concurrent writer: a write
// that lands between os.Open and os.Remove is lost. This is acceptable for
// the conductor's expected drain cadence (seconds) but documented so callers
// don't expect at-least-once semantics across producer/consumer races. When
// strict atomicity matters, callers should externally serialize.
func ReadAndTruncateInbox(parentSessionID string) ([]TransitionNotificationEvent, error) {
	if strings.TrimSpace(parentSessionID) == "" {
		return nil, errors.New("inbox: empty parent session id")
	}
	path := InboxPathFor(parentSessionID)

	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []TransitionNotificationEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev TransitionNotificationEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // skip corrupt lines rather than failing the whole drain
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}

	// Close before remove on Windows-friendly path; we already deferred Close
	// but on Linux Remove works on open files. Be explicit anyway.
	_ = f.Close()
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return out, err
	}
	// Truncating drops the dedup cache for this path: the next write should
	// be free to land, even if the same fingerprint was just drained. The
	// drain itself is the consumer's acknowledgement.
	delete(inboxFingerprintCache, path)
	return out, nil
}
