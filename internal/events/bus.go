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

	// defaultFlushInterval controls how often pending drop counts are written.
	defaultFlushInterval = 50 * time.Millisecond
	maxWriteBatch        = 256
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

	queue          chan queuedFrame
	closeCh        chan struct{}
	closeWg        sync.WaitGroup
	publishMu      sync.RWMutex
	closed         atomic.Bool
	failed         atomic.Bool
	abandoned      atomic.Bool
	ioMu           sync.Mutex
	lockFile       *os.File
	persistedDrops uint64

	mu           sync.Mutex
	cursor       Cursor
	activeFile   *os.File
	activeStart  Cursor
	activeBytes  int64
	activeFrames int

	enqueued          atomic.Uint64
	written           atomic.Uint64
	synced            atomic.Uint64
	published         atomic.Uint64
	dropped           atomic.Uint64
	discardedAccepted atomic.Uint64
}

type queuedFrame struct {
	profile   string
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
	lockFile, err := os.OpenFile(filepath.Join(dir, "writer.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("events: open writer lock: %w", err)
	}
	b.lockFile = lockFile
	if err := b.lockDisk(); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("events: lock bus: %w", err)
	}
	defer b.unlockDisk()

	sealed, err := listSealedSegments(dir)
	if err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("events: list segments: %w", err)
	}

	lastCursor, err := recoverActiveSegment(dir)
	if err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("events: recover active segment: %w", err)
	}
	if len(sealed) > 0 && sealed[len(sealed)-1].end > lastCursor {
		lastCursor = sealed[len(sealed)-1].end
	}
	checkpoint, err := readCursorCheckpoint(dir)
	if err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("events: read cursor checkpoint: %w", err)
	}
	if checkpoint > lastCursor {
		lastCursor = checkpoint
	}

	f, err := os.OpenFile(filepath.Join(dir, activeSegmentName), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("events: open active segment: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		_ = lockFile.Close()
		return nil, fmt.Errorf("events: stat active segment: %w", err)
	}

	b.cursor = lastCursor
	b.activeFile = f
	b.activeStart = lastCursor + 1
	if info.Size() > 0 {
		first, activeLast, _, err := activeBounds(filepath.Join(dir, activeSegmentName))
		if err != nil {
			_ = f.Close()
			_ = lockFile.Close()
			return nil, err
		}
		if activeLast < checkpoint {
			_ = f.Close()
			_ = lockFile.Close()
			return nil, fmt.Errorf("events: active cursor %d precedes checkpoint %d", activeLast, checkpoint)
		}
		b.activeStart = first
	}
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

// Publish enqueues an event for durable append without waiting for disk. A
// full queue drops the frame and increments Stats().Dropped. A disabled or
// failed bus makes this a no-op.
func (b *Bus) Publish(kind, sessionID string, data any) {
	if b == nil || !b.enabled || b.closed.Load() || b.failed.Load() {
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	qf := queuedFrame{kind: kind, sessionID: sessionID, data: raw, ts: time.Now()}
	b.enqueue(qf)
}

func (b *Bus) enqueue(qf queuedFrame) {
	if b == nil || !b.enabled || b.closed.Load() || b.failed.Load() {
		return
	}
	b.publishMu.RLock()
	defer b.publishMu.RUnlock()
	if b.abandoned.Load() {
		b.dropped.Add(1)
		return
	}
	if b.closed.Load() || b.failed.Load() {
		return
	}
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
	deadline := time.Now().Add(timeout)
	for b.synced.Load() < target {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

// Cursor returns the last cursor appended by this Bus. Use Flush to wait for
// all accepted frames to be synced.
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
	var diskCursor Cursor
	var diskDrops uint64
	var pendingDrops uint64
	if err := b.lockDisk(); err == nil {
		b.mu.Lock()
		if err := b.refreshLocked(); err == nil {
			diskCursor = b.cursor
		}
		b.mu.Unlock()
		diskDrops, _ = b.diskDropsLocked()
		pendingDrops = b.dropped.Load() - b.persistedDrops
		b.unlockDisk()
	}
	if diskCursor == 0 {
		diskCursor = b.Cursor()
	}
	return Stats{
		Enabled:   !b.failed.Load(),
		Dir:       b.dir,
		Cursor:    diskCursor,
		Published: b.published.Load(),
		Written:   b.written.Load(),
		Synced:    b.synced.Load(),
		Dropped:   diskDrops + pendingDrops,
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
	b.publishMu.Lock()
	if !b.closed.CompareAndSwap(false, true) {
		b.publishMu.Unlock()
		return nil
	}
	b.publishMu.Unlock()
	close(b.closeCh)
	b.closeWg.Wait()
	if b.abandoned.Load() {
		// No writer remains. Count exactly the accepted frames that were not
		// appended, including a batch interrupted by the exit deadline.
		b.dropped.Add(b.enqueued.Load() - b.written.Load() - b.discardedAccepted.Load())
	}
	if err := b.lockDisk(); err == nil {
		if err := b.persistDropsLocked(); err != nil {
			b.fail(err)
		}
		b.unlockDisk()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.activeFile != nil {
		err := b.activeFile.Close()
		_ = b.lockFile.Close()
		return err
	}
	_ = b.lockFile.Close()
	return nil
}

func (b *Bus) writerLoop() {
	defer b.closeWg.Done()
	ticker := time.NewTicker(defaultFlushInterval)
	defer ticker.Stop()
	syncTicker := time.NewTicker(time.Second)
	defer syncTicker.Stop()
	batch := make([]queuedFrame, 0, maxWriteBatch)
	flush := func(sync bool) {
		if len(batch) > 0 || sync {
			b.writeBatch(batch, sync)
			batch = batch[:0]
		}
	}

	for {
		select {
		case qf, ok := <-b.queue:
			if !ok {
				flush(true)
				return
			}
			batch = append(batch, qf)
			if len(batch) == maxWriteBatch {
				flush(false)
			}
		case <-ticker.C:
			flush(false)
		case <-syncTicker.C:
			flush(true)
		case <-b.closeCh:
			for {
				select {
				case qf := <-b.queue:
					batch = append(batch, qf)
					if len(batch) == maxWriteBatch {
						flush(false)
					}
				default:
					flush(true)
					return
				}
			}
		}
	}
}

// writeBatch takes the cross-process lock once for up to maxWriteBatch frames.
// Sync runs on the one-second tick and at shutdown, never per output line.
func (b *Bus) writeBatch(batch []queuedFrame, sync bool) {
	if b.abandoned.Load() {
		return
	}
	if b.failed.Load() {
		b.dropAccepted(uint64(len(batch)))
		return
	}
	if err := b.lockDisk(); err != nil {
		b.fail(err)
		b.dropAccepted(uint64(len(batch)))
		return
	}
	defer b.unlockDisk()
	if b.abandoned.Load() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(batch) > 0 {
		if err := b.refreshLocked(); err != nil {
			b.fail(err)
			b.dropAccepted(uint64(len(batch)))
			return
		}
		for i, qf := range batch {
			if b.abandoned.Load() {
				break
			}
			if err := b.appendFrameLocked(qf); err != nil {
				b.fail(err)
				b.dropAccepted(uint64(len(batch) - i))
				break
			}
			if b.failed.Load() {
				b.dropAccepted(uint64(len(batch) - i - 1))
				break
			}
		}
	}
	rotate := b.activeBytes >= b.maxSegBytes || b.activeFrames >= b.maxSegFrames
	if sync && b.activeFile != nil && (b.synced.Load() != b.written.Load() || rotate) {
		if err := b.activeFile.Sync(); err != nil {
			b.fail(fmt.Errorf("events: sync: %w", err))
		} else {
			b.synced.Store(b.written.Load())
		}
	}
	if sync && !b.failed.Load() && rotate {
		b.rotateLocked()
	}
	if err := b.persistDropsLocked(); err != nil {
		b.fail(err)
	}
}

func (b *Bus) dropAccepted(n uint64) {
	b.discardedAccepted.Add(n)
	b.dropped.Add(n)
}

// appendFrameLocked runs inside one batch's disk and in-process locks.
func (b *Bus) appendFrameLocked(qf queuedFrame) error {
	next := b.cursor + 1
	f := Frame{
		Cursor:    next,
		EventID:   genEventID(),
		TS:        qf.ts.UnixMilli(),
		Kind:      qf.kind,
		SessionID: qf.sessionID,
		Data:      qf.data,
	}
	line, err := f.CanonicalJSON()
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if b.activeFile == nil {
		return fmt.Errorf("events: active file unavailable")
	}
	before := b.activeBytes
	n, err := b.activeFile.Write(line)
	if err != nil || n != len(line) {
		_ = b.activeFile.Truncate(before)
		return fmt.Errorf("events: append: %w", err)
	}
	b.cursor = next
	b.activeBytes += int64(n)
	b.activeFrames++
	b.written.Add(1)

	return nil
}

func (b *Bus) fail(err error) {
	if b.failed.CompareAndSwap(false, true) {
		slog.Warn("events: bus disabled after write failure", "error", err)
	}
}

// rotateLocked seals the active segment (rename to seg-<start>-<end>.ndjson)
// and opens a fresh, empty active segment. Caller holds b.mu.
func (b *Bus) rotateLocked() {
	if b.activeFrames == 0 {
		return
	}
	if err := b.activeFile.Close(); err != nil {
		b.fail(err)
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
		b.fail(err)
		return
	}

	f, err := os.OpenFile(oldPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		b.fail(err)
		return
	}
	b.activeFile = f
	b.activeStart = b.cursor + 1
	b.activeBytes = 0
	b.activeFrames = 0
	if err := writeCursorCheckpoint(b.dir, b.cursor); err != nil {
		b.fail(err)
		return
	}

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
