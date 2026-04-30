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
