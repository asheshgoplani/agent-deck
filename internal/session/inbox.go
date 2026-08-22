package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Per-conductor inbox: a JSONL file at
// <agent-deck-dir>/inboxes/<parent-session-id>.jsonl — the durable per-parent
// outbox a conductor DRAINS at its own turn boundary (Stop hook) and on
// heartbeat (issue #1225). Two producers commit here (interactive
// running→waiting and one-shot run-task kernel-exit).
//
// Delivery contract (audit B1): the #1225 drain (DrainInboxForParent /
// DrainForStopHook in inbox_consumer.go) is AT-LEAST-ONCE WITH DEDUP — records
// are staged to an in-flight WAL before the inbox is truncated and the
// turn_fingerprint consumed-ledger collapses any re-delivered duplicate, so a
// crash re-delivers rather than loses. The legacy raw path below
// (WriteInboxEvent + ReadAndTruncateInbox, exposed as `agent-deck inbox <id>`)
// is the older at-most-once read+truncate used only for ad-hoc inspection;
// prefer the drain path for guaranteed delivery.
//
// Append-only writes guarantee that concurrent producers (the notifier
// daemon plus any ad-hoc CLI dispatcher) cannot clobber each other; inboxWriteMu
// is held across the read+remove so the legacy truncate is atomic relative to
// any concurrent WriteInboxEvent/CommitToInbox.

var inboxWriteMu sync.Mutex // serializes appends to a single inbox file

// fsyncFile is the seam through which the durable-write primitive
// (writeFileDurable) and its directory-fsync helper (fsyncDir) flush to disk.
// Production points it at (*os.File).Sync — a plain pass-through, zero overhead.
//
// It exists so the Tier 2 count-assertion perf tests (docs/perf-budget-suite.md)
// can gate the drain's I/O PATTERN on COUNTS rather than walltime. Disk fsync
// latency varies ~100× across SSD / HDD / cloud volumes and is un-normalizable,
// so "we now fsync once per record instead of once per batch" can only be caught
// by counting syncs — which requires a seam the test can wrap. The companion
// Tier 1 WARM walltime gate stubs this to a no-op so it measures pure CPU +
// Go-runtime cost with disk speed factored out (the doc's tmpfs/in-memory
// requirement). See SetFsyncHookForTest and internal/testutil.FsyncCounter.
var fsyncFile = (*os.File).Sync

// SetFsyncHookForTest swaps the durable-write fsync seam and returns a restore
// func (defer it). The two perf gates added for the durable per-parent outbox
// use it: the Tier 2 test wraps it with a counter to assert N messages drain in
// a CONSTANT number of fsyncs, and the Tier 1 WARM walltime test no-ops it to
// isolate in-process Go cost from disk. Production never calls it. Package tests
// run serially (no t.Parallel on the perf tests), so the global swap is safe.
func SetFsyncHookForTest(fn func(*os.File) error) func() {
	prev := fsyncFile
	fsyncFile = fn
	return func() { fsyncFile = prev }
}

// maxInboxLineBytes caps a single JSONL line when scanning inbox / WAL /
// dead-letter files. Audit B6: the old 1 MB cap silently truncated (and failed
// the whole drain on) an oversized event — a DoneSummary that swallowed a large
// worker log could exceed it. Raised to 16 MB so a fat-but-bounded summary
// scans cleanly; the producer also caps DoneSummary (see maxDoneSummaryBytes)
// so a line never reaches this ceiling in practice.
const maxInboxLineBytes = 16 * 1024 * 1024

// writeFileDurable writes data to path atomically and durably: a temp file is
// written and fsync'd, then renamed over path, then the parent directory is
// fsync'd so both the file contents and the rename survive a crash/power loss.
// This is the durability primitive behind the at-least-once drain (audit B1):
// the in-flight WAL must be on disk before the inbox is removed, and the
// consumed ledger must be on disk before the WAL is dropped.
func writeFileDurable(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := fsyncFile(f); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Best-effort directory fsync so the rename itself is durable. Some
	// filesystems reject directory fsync (EINVAL/ENOTSUP); treat that as benign
	// since the data fsync + atomic rename already give the core guarantee.
	fsyncDir(dir)
	return nil
}

// fsyncDir best-effort fsyncs a directory so a preceding rename is durable.
func fsyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = fsyncFile(d)
}

// inboxFingerprintCache holds, per inbox file path, the set of event
// fingerprints already persisted. Populated lazily on first write to a path
// (by scanning the existing file) and updated on every successful append.
//
// This cache is process-local. For cross-process correctness we still scan
// the file on the first write per path within a process, so a fresh process
// won't re-append events the previous process already wrote.
//
// Issue #824: a single logical event was being persisted repeatedly,
// producing 13 duplicate JSONL lines for one transition. The cache + lazy
// file scan reduces those to one.
var inboxFingerprintCache = map[string]map[string]struct{}{}

// InboxDir returns the directory that holds per-parent inbox files.
func InboxDir() string {
	dir, err := dataPath("inboxes", "inboxes")
	if err != nil {
		return tempAgentDeckPath("inboxes")
	}
	return dir
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
// guard for issue #824 (the same logical event persisted multiple times).
// The #1225 drain path layers turn_fingerprint dedup on top for at-least-once
// delivery with exactly-once effects (see inbox_consumer.go).
func WriteInboxEvent(parentSessionID string, event TransitionNotificationEvent) error {
	_, err := WriteInboxEventIfNew(parentSessionID, event)
	return err
}

// WriteInboxEventIfNew is WriteInboxEvent with the dedup decision reported:
// written is false exactly when the event's EventFingerprint is already
// persisted in the file and the append was therefore skipped.
//
// Issue #1948's `remote drain` is the caller that needs the answer — it reports
// "N new, M already present" so a second drain of the same remote is visibly a
// no-op. It reads the outcome of the dedup that already exists rather than
// re-deciding duplication with a rule of its own.
func WriteInboxEventIfNew(parentSessionID string, event TransitionNotificationEvent) (bool, error) {
	if strings.TrimSpace(parentSessionID) == "" {
		return false, errors.New("inbox: empty parent session id")
	}
	path := InboxPathFor(parentSessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	fileLock, err := AcquireConfigFileLock(path)
	if err != nil {
		return false, fmt.Errorf("lock inbox append: %w", err)
	}
	defer fileLock.Release()

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
		return false, nil
	}

	line, err := json.Marshal(inboxWireEvent{TransitionNotificationEvent: event, Fingerprint: fp})
	if err != nil {
		return false, err
	}
	if err := atomicAppendInboxLineLocked(path, line); err != nil {
		return false, err
	}
	seen[fp] = struct{}{}
	return true, nil
}

// atomicAppendInboxLineLocked gives an inbox append an old-or-new crash
// boundary. It builds a complete replacement beside the inbox, fsyncs it, and
// atomically renames it over the old file. SIGKILL before rename leaves the old
// complete inbox; SIGKILL after rename leaves the new complete inbox. A partial
// JSONL tail is never installed as the authoritative file.
//
// Caller holds inboxWriteMu.
func atomicAppendInboxLineLocked(path string, line []byte) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Keep the capacity calculation explicit and checked. Although os.ReadFile
	// cannot normally return a slice close to MaxInt, adding attacker-influenced
	// line length without a guard can wrap and turn the allocation into a panic
	// (CodeQL go/allocation-size-overflow).
	capacity, err := checkedInboxAppendCapacity(len(existing), len(line))
	if err != nil {
		return err
	}
	data := make([]byte, 0, capacity)
	data = append(data, existing...)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, line...)
	data = append(data, '\n')
	return writeFileDurable(path, data, 0o644)
}

func maxInt() int { return int(^uint(0) >> 1) }

func checkedInboxAppendCapacity(existingLen, lineLen int) (int, error) {
	if existingLen < 0 || lineLen < 0 || existingLen > maxInt()-1 || lineLen > maxInt()-existingLen-1 {
		return 0, fmt.Errorf("inbox append too large: existing=%d line=%d", existingLen, lineLen)
	}
	return existingLen + lineLen + 1, nil
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
	scanner.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
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
	// Audit B6: a scanner error (e.g. an oversized line at the raised cap, or a
	// read fault) must not silently yield an INCOMPLETE dedup set — that would
	// let WriteInboxEvent re-append events the file already holds. On error we
	// reset to the empty set: a fresh full scan failed, so treat dedup state as
	// unknown rather than partially-known. The caller's write still proceeds;
	// worst case is a duplicate the drain's turn_fingerprint dedup collapses.
	if err := scanner.Err(); err != nil {
		return map[string]struct{}{}
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
// TTL, persisted entries for children that never see another transition
// accumulate unboundedly. Seven days is a generous "old enough to give up
// on" horizon, sized in days because the inbox is the operator-facing
// drain that a conductor may not visit for a while.
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
// never transition again would otherwise leave their last persisted
// entry in the inbox forever. The TTL puts a hard ceiling on inbox growth.
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
	scanner.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
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
// This is the LEGACY raw at-most-once path (exposed as `agent-deck inbox <id>`
// for ad-hoc inspection). The read+remove pair IS atomic against concurrent
// WriteInboxEvent/CommitToInbox — inboxWriteMu is held across Open→Remove, so a
// producer cannot interleave a write between them. What it does NOT provide is
// crash durability: a process death after the file is removed but before the
// caller acts loses the records. For the guaranteed at-least-once-with-dedup
// contract use DrainInboxForParent / DrainForStopHook (inbox_consumer.go), which
// stage to an in-flight WAL before truncating.
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
	scanner.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
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

// ReadInboxEventsForDisplay is a deliberately UI-grade snapshot, not the
// durable inbox consumer API. It never consumes, locks, repairs, or promises
// delivery semantics; it skips malformed/torn records and reports a missing
// inbox as an empty display. Durable consumers must not use this function.
func ReadInboxEventsForDisplay(parentSessionID string) ([]TransitionNotificationEvent, error) {
	if strings.TrimSpace(parentSessionID) == "" {
		return nil, errors.New("inbox peek: empty parent session id")
	}
	data, err := os.ReadFile(InboxPathFor(parentSessionID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []TransitionNotificationEvent
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev TransitionNotificationEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}
