package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	activeSegmentName = "active.ndjson"

	// defaultQueueCap bounds the Publish->writer channel. A full queue means
	// Publish drops the frame (counted in Stats) instead of blocking the
	// producer. Sized to comfortably absorb a burst well past the soak
	// test's 10k events before the writer (which does real disk I/O, so is
	// inherently slower than an in-memory channel send) has to catch up.
	defaultQueueCap = 16384

	// defaultMaxSegBytes / defaultMaxSegFrames rotate the active segment into
	// a sealed one. Small enough that the golden/soak tests rotate at least
	// once, large enough that normal use rotates a few times a day.
	defaultMaxSegBytes  = 8 << 20 // 8 MiB
	defaultMaxSegFrames = 50000

	// defaultRetainSegs bounds how many sealed segments compaction keeps.
	// Once exceeded, the oldest sealed segment is removed. A Subscribe whose
	// `after` cursor falls before the oldest retained segment gets
	// ErrCursorTooOld instead of silently skipping frames.
	defaultRetainSegs = 32

	// defaultFlushInterval bounds staleness of durability for producers that
	// never call Flush (e.g. the tmux %output hot path): at most this long
	// after a Publish, the frame is fsynced.
	defaultFlushInterval = 25 * time.Millisecond

	// defaultFlushBatch forces an fsync after this many unflushed frames,
	// independent of the ticker, so a fast burst doesn't wait a full
	// interval before durability catches up.
	defaultFlushBatch = 200
)

// ErrCursorTooOld is returned by Subscribe when `after` is older than every
// retained segment (i.e. compaction has already removed the frames the
// caller wants to resume from).
var ErrCursorTooOld = fmt.Errorf("events: cursor older than the retained log")

var segNameRE = regexp.MustCompile(`^seg-(\d+)-(\d+)\.ndjson$`)

// Bus is a single profile's durable, append-only event bus. See the package
// doc comment for the guarantees. The zero value is not usable; construct
// with Open or use Default().
type Bus struct {
	dir          string
	maxSegBytes  int64
	maxSegFrames int
	retainSegs   int

	enabled bool

	queue   chan queuedFrame
	closeCh chan struct{}
	closeWg sync.WaitGroup
	closed  atomic.Bool

	mu           sync.Mutex
	cursor       Cursor
	activeFile   *os.File
	activeStart  Cursor
	activeBytes  int64
	activeFrames int

	flushRequested atomic.Bool
	enqueued       atomic.Uint64
	written        atomic.Uint64
	synced         atomic.Uint64
	published      atomic.Uint64
	dropped        atomic.Uint64
}

type queuedFrame struct {
	kind      string
	sessionID string
	data      json.RawMessage
	ts        time.Time
}

// Stats is the JSON shape behind `agent-deck events stats --json`.
type Stats struct {
	Enabled   bool   `json:"enabled"`
	Dir       string `json:"dir"`
	Cursor    Cursor `json:"cursor"`
	Published uint64 `json:"published"`
	Written   uint64 `json:"written"`
	Synced    uint64 `json:"synced"`
	Dropped   uint64 `json:"dropped"`
	QueueLen  int    `json:"queue_len"`
	QueueCap  int    `json:"queue_cap"`
}

var (
	disabledWarnOnce sync.Once
)

func warnDisabled(reason string, err error) {
	disabledWarnOnce.Do(func() {
		if err != nil {
			slog.Warn("events: bus disabled, falling back to pre-bus behavior", "reason", reason, "error", err)
		} else {
			slog.Warn("events: bus disabled, falling back to pre-bus behavior", "reason", reason)
		}
	})
}

// disabledBus returns a non-nil, inert Bus: every method is a safe no-op.
func disabledBus() *Bus {
	b := &Bus{enabled: false}
	b.closed.Store(true)
	return b
}

// Open opens (creating if needed) the durable bus rooted at dir. dir is
// typically the value returned by busDir(); tests pass an isolated temp dir.
// Open recovers the last-assigned Cursor by reading the tail of the log, so
// it is durable and monotonic across restarts.
func Open(dir string) (*Bus, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("events: create bus dir: %w", err)
	}
	b := &Bus{
		dir:          dir,
		maxSegBytes:  defaultMaxSegBytes,
		maxSegFrames: defaultMaxSegFrames,
		retainSegs:   defaultRetainSegs,
		enabled:      true,
		queue:        make(chan queuedFrame, defaultQueueCap),
		closeCh:      make(chan struct{}),
	}

	sealed, err := listSealedSegments(dir)
	if err != nil {
		return nil, fmt.Errorf("events: list segments: %w", err)
	}

	lastCursor, err := recoverActiveSegment(dir)
	if err != nil {
		return nil, fmt.Errorf("events: recover active segment: %w", err)
	}
	if lastCursor == 0 && len(sealed) > 0 {
		lastCursor = sealed[len(sealed)-1].end
	}

	f, err := os.OpenFile(filepath.Join(dir, activeSegmentName), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("events: open active segment: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("events: stat active segment: %w", err)
	}

	b.cursor = lastCursor
	b.activeFile = f
	b.activeStart = lastCursor + 1
	b.activeBytes = info.Size()
	b.activeFrames = countLines(dir, activeSegmentName)

	b.closeWg.Add(1)
	go b.writerLoop()
	return b, nil
}

// recoverActiveSegment reads active.ndjson (if present) to find the highest
// valid cursor it contains, truncating a trailing partial line (an
// incomplete write from a prior crash) so appends start clean.
func recoverActiveSegment(dir string) (Cursor, error) {
	path := filepath.Join(dir, activeSegmentName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}

	var last Cursor
	validEnd := 0
	lines := splitLinesKeepEnds(data)
	for _, ln := range lines {
		trimmed := strings.TrimRight(string(ln), "\n")
		if trimmed == "" {
			validEnd += len(ln)
			continue
		}
		f, perr := ParseFrameLine([]byte(trimmed))
		if perr != nil || !strings.HasSuffix(string(ln), "\n") {
			// Partial/corrupt trailing line: stop here, and truncate it away.
			break
		}
		last = f.Cursor
		validEnd += len(ln)
	}
	if validEnd != len(data) {
		if err := os.WriteFile(path, data[:validEnd], 0o644); err != nil {
			return 0, err
		}
	}
	return last, nil
}

func splitLinesKeepEnds(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i+1])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

func countLines(dir, name string) int {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}

type sealedSegment struct {
	path  string
	start Cursor
	end   Cursor
}

func listSealedSegments(dir string) ([]sealedSegment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []sealedSegment
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := segNameRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		start, _ := strconv.ParseUint(m[1], 10, 64)
		end, _ := strconv.ParseUint(m[2], 10, 64)
		out = append(out, sealedSegment{path: filepath.Join(dir, e.Name()), start: Cursor(start), end: Cursor(end)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out, nil
}

// genEventID returns a short, unique-enough random id. Collisions are
// harmless (EventID is informational; Cursor is the ordering/identity key),
// so 8 random bytes is ample.
func genEventID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Publish enqueues an event for durable append and returns immediately. It
// never blocks the caller: if the internal queue is full (a slow disk or a
// burst outrunning the writer), the frame is dropped and Stats().Dropped
// increments. A disabled or failed-to-open bus makes this a no-op.
func (b *Bus) Publish(kind, sessionID string, data any) {
	if b == nil || !b.enabled || b.closed.Load() {
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	qf := queuedFrame{kind: kind, sessionID: sessionID, data: raw, ts: time.Now()}
	select {
	case b.queue <- qf:
		b.published.Add(1)
		b.enqueued.Add(1)
	default:
		b.dropped.Add(1)
	}
}

// Flush blocks (up to timeout) until every frame Published so far has been
// fsynced to the append log, or returns false on timeout. It is meant for
// low-frequency producers (a status write, a transition) that want the
// common case to be durable before the process exits; it is itself bounded,
// so a stuck disk degrades to "returns false", never an indefinite block.
func (b *Bus) Flush(timeout time.Duration) bool {
	if b == nil || !b.enabled || b.closed.Load() {
		return false
	}
	target := b.enqueued.Load()
	if b.synced.Load() >= target {
		return true
	}
	b.flushRequested.Store(true)
	deadline := time.Now().Add(timeout)
	for b.synced.Load() < target {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

// Cursor returns the last cursor assigned (enqueued, not necessarily synced
// yet — use Flush to wait for durability).
func (b *Bus) Cursor() Cursor {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cursor
}

// Stats reports the counters behind `events stats --json`.
func (b *Bus) Stats() Stats {
	if b == nil || !b.enabled {
		return Stats{Enabled: false}
	}
	return Stats{
		Enabled:   true,
		Dir:       b.dir,
		Cursor:    b.Cursor(),
		Published: b.published.Load(),
		Written:   b.written.Load(),
		Synced:    b.synced.Load(),
		Dropped:   b.dropped.Load(),
		QueueLen:  len(b.queue),
		QueueCap:  cap(b.queue),
	}
}

// Close flushes any queued frames, fsyncs and stops the background writer.
// Safe to call once; a nil or already-disabled Bus is a no-op.
func (b *Bus) Close() error {
	if b == nil || !b.enabled {
		return nil
	}
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(b.closeCh)
	b.closeWg.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.activeFile != nil {
		return b.activeFile.Close()
	}
	return nil
}

func (b *Bus) writerLoop() {
	defer b.closeWg.Done()
	ticker := time.NewTicker(defaultFlushInterval)
	defer ticker.Stop()

	drain := func() {
		for {
			select {
			case qf, ok := <-b.queue:
				if !ok {
					return
				}
				b.writeFrame(qf)
			default:
				return
			}
		}
	}

	for {
		select {
		case qf, ok := <-b.queue:
			if !ok {
				b.mu.Lock()
				b.syncLocked()
				b.mu.Unlock()
				return
			}
			b.writeFrame(qf)
		case <-ticker.C:
			b.mu.Lock()
			if b.activeFile != nil {
				b.syncLocked()
			}
			b.mu.Unlock()
		case <-b.closeCh:
			drain()
			b.mu.Lock()
			b.syncLocked()
			b.mu.Unlock()
			return
		}
	}
}

// writeFrame assigns the next cursor, appends the canonical line to the
// active segment and rotates if the segment is now over threshold. Called
// only from the writer goroutine.
func (b *Bus) writeFrame(qf queuedFrame) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.cursor++
	f := Frame{
		Cursor:    b.cursor,
		EventID:   genEventID(),
		TS:        qf.ts.UnixMilli(),
		Kind:      qf.kind,
		SessionID: qf.sessionID,
		Data:      qf.data,
	}
	line, err := f.CanonicalJSON()
	if err != nil {
		return
	}
	line = append(line, '\n')
	if b.activeFile == nil {
		return
	}
	n, err := b.activeFile.Write(line)
	if err != nil {
		return
	}
	b.activeBytes += int64(n)
	b.activeFrames++
	b.written.Add(1)

	if b.activeBytes >= b.maxSegBytes || b.activeFrames >= b.maxSegFrames {
		b.rotateLocked()
		return
	}

	if b.activeFrames%defaultFlushBatch == 0 || b.flushRequested.Load() {
		b.syncLocked()
		b.flushRequested.Store(false)
	}
}

// syncLocked fsyncs the active segment and advances the synced watermark to
// the last written cursor. Caller holds b.mu, except at startup/teardown
// paths that call it without contention.
func (b *Bus) syncLocked() {
	if b.activeFile == nil {
		return
	}
	if err := b.activeFile.Sync(); err != nil {
		return
	}
	b.synced.Store(b.written.Load())
}

// rotateLocked seals the active segment (rename to seg-<start>-<end>.ndjson)
// and opens a fresh, empty active segment. Caller holds b.mu.
func (b *Bus) rotateLocked() {
	if b.activeFrames == 0 {
		return
	}
	if err := b.activeFile.Sync(); err != nil {
		return
	}
	b.synced.Store(b.written.Load())
	if err := b.activeFile.Close(); err != nil {
		return
	}

	sealedName := fmt.Sprintf("seg-%020d-%020d.ndjson", uint64(b.activeStart), uint64(b.cursor))
	oldPath := filepath.Join(b.dir, activeSegmentName)
	newPath := filepath.Join(b.dir, sealedName)
	if err := os.Rename(oldPath, newPath); err != nil {
		// Best effort: reopen the old path so the bus keeps working even if
		// the rename failed (e.g. permissions raced). Rotation is a safety
		// optimization, not a correctness requirement.
		f, oerr := os.OpenFile(oldPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
		if oerr == nil {
			b.activeFile = f
		}
		return
	}

	f, err := os.OpenFile(oldPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	b.activeFile = f
	b.activeStart = b.cursor + 1
	b.activeBytes = 0
	b.activeFrames = 0

	b.compactLocked()
}

// compactLocked removes the oldest sealed segments beyond retainSegs.
// Rotation/compaction never touches the active segment, so it can never
// corrupt an in-progress write or race a subscriber tailing it.
func (b *Bus) compactLocked() {
	sealed, err := listSealedSegments(b.dir)
	if err != nil {
		return
	}
	if len(sealed) <= b.retainSegs {
		return
	}
	toRemove := sealed[:len(sealed)-b.retainSegs]
	for _, s := range toRemove {
		_ = os.Remove(s.path)
	}
}
