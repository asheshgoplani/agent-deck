package events

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Options tunes a bus opened with OpenAt. The zero value is the bus Open
// creates: 8 MiB segments, 32 retained sealed segments, no time retention,
// a background writer.
type Options struct {
	// RetainSegments bounds the sealed segments kept after rotation
	// (default 32). Oldest beyond the bound are removed.
	RetainSegments int
	// RetentionDays removes a sealed segment once its newest frame is older
	// than this many days, independently of RetainSegments. 0 keeps segments
	// until the count bound removes them.
	RetentionDays int
	// MaxSegmentBytes rotates the active segment past this size (default
	// 8 MiB).
	MaxSegmentBytes int64
	// KeepCorrupt makes recovery leave a malformed line inside committed
	// history in place (logged, skipped by readers) instead of truncating
	// from it, so a corruption event never erases later messages. Only a
	// ledger wants this; the status bus keeps its truncating recovery.
	KeepCorrupt bool
	// MaxBytes bounds the retained log (sealed segments plus the active
	// file); Commit returns ErrQuota once it would be exceeded. 0 = none.
	MaxBytes int64
	// Private creates every file the bus writes with mode 0o600 (and the
	// directory 0o700) instead of the status bus's 0o644/0o755: for a log
	// that holds assistant text.
	Private bool
	// ReadOnly opens the log for Subscribe and Stats only: no writer
	// goroutine, no tail repair, no Commit. A follower (`events follow --bus
	// comms`) opens the ledger this way so only the owning daemon ever
	// writes it. The directory must already exist.
	ReadOnly bool
	// RetainFrom, when set, is asked at every compaction for the lowest
	// cursor a reader still needs (the comms ledger's pending-delivery
	// bound). A sealed segment holding any cursor at or above it is kept
	// whatever its age or the segment count say; MaxBytes still bounds the
	// log, so a stuck reader turns into an explicit quota error, never a
	// silent drop. 0 from the callback means no reader holds anything.
	RetainFrom func() Cursor
}

// ErrReadOnly is returned by Commit and reported by Publish (as a drop) on a
// bus opened with Options.ReadOnly.
var ErrReadOnly = errors.New("events: bus is read-only")

// ErrNoBus is returned by OpenAt in read-only mode when the directory does
// not exist: nothing has ever been written there.
var ErrNoBus = errors.New("events: no log at this path")

// ErrQuota is returned by Commit when the log is at Options.MaxBytes. The
// frame is not written; the caller keeps its source (a spool entry) and
// reports the overload.
var ErrQuota = errors.New("events: log quota exceeded")

// commitFault is a test seam: when set it is called at the named stage of
// Commit ("before-append", "before-sync") and may corrupt the bus state
// or return an error to inject a fault. nil in production.
var commitFault func(stage string, b *Bus) error

// OpenAt opens the durable bus rooted at dir with explicit options. It is
// Open plus the knobs a second log (the comms ledger) needs: time-based
// retention, and a read-only mode for followers.
func OpenAt(dir string, opts Options) (*Bus, error) {
	if opts.ReadOnly {
		return openReadOnly(dir, opts)
	}
	mode, dirMode := os.FileMode(0o644), os.FileMode(0o755)
	if opts.Private {
		mode, dirMode = 0o600, 0o700
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("events: create bus dir: %w", err)
	}
	if opts.Private {
		_ = os.Chmod(dir, dirMode) // an existing directory keeps its mode otherwise
	}
	b, err := openWith(dir, mode, opts.KeepCorrupt)
	if err != nil {
		return nil, err
	}
	b.applyOptions(opts)
	return b, nil
}

func (b *Bus) applyOptions(opts Options) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if opts.RetainSegments > 0 {
		b.retainSegs = opts.RetainSegments
	}
	if opts.RetentionDays > 0 {
		b.retention = time.Duration(opts.RetentionDays) * 24 * time.Hour
	}
	if opts.MaxSegmentBytes > 0 {
		b.maxSegBytes = opts.MaxSegmentBytes
	}
	if opts.MaxBytes > 0 {
		b.maxBytes = opts.MaxBytes
	}
	b.retainFrom = opts.RetainFrom
}

// CursorBefore returns a cursor from which a reader sees every frame
// written at or after t: the cursor just before the oldest sealed segment
// whose newest frame (its mtime) is not older than t, or just before the
// active file when every sealed segment is older. 0 means read from the
// start. Frames before t may still follow it; callers filter by their own
// timestamps. It saves a reader of the newest day a scan of 90 days.
func (b *Bus) CursorBefore(t time.Time) (Cursor, error) {
	if b == nil || !b.enabled {
		return 0, errors.New("events: bus disabled")
	}
	segs, err := b.listAllSegments()
	if err != nil {
		return 0, err
	}
	for _, s := range segs {
		if !s.sealed {
			return max(s.start, 1) - 1, nil
		}
		info, err := os.Stat(s.path)
		if err != nil || !info.ModTime().Before(t) {
			return max(s.start, 1) - 1, nil
		}
	}
	return 0, nil
}

// Oldest returns the first cursor of the retained log (the start of the
// oldest segment), 0 when nothing is retained. A reader whose position is
// below Oldest()-1 has lost records to compaction and must say so.
func (b *Bus) Oldest() (Cursor, error) {
	if b == nil || !b.enabled {
		return 0, errors.New("events: bus disabled")
	}
	segs, err := b.listAllSegments()
	if err != nil || len(segs) == 0 {
		return 0, err
	}
	return segs[0].start, nil
}

// openReadOnly builds a Bus that can list segments, Subscribe and report
// Stats but never appends: no writer loop, no active-segment repair, and
// Commit / Publish refuse. The cross-process lock file is still opened
// because segment listing takes the shared lock to see a consistent view.
func openReadOnly(dir string, opts Options) (*Bus, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoBus
		}
		return nil, fmt.Errorf("events: stat bus dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("events: %s is not a directory", dir)
	}
	// A follower never creates anything: the writer made writer.lock when it
	// opened the log, and flock works on a read-only descriptor.
	lockFile, err := os.Open(filepath.Join(dir, "writer.lock"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoBus
		}
		return nil, fmt.Errorf("events: open writer lock: %w", err)
	}
	b := &Bus{
		dir:          dir,
		maxSegBytes:  defaultMaxSegBytes,
		maxSegFrames: defaultMaxSegFrames,
		retainSegs:   defaultRetainSegs,
		enabled:      true,
		readOnly:     true,
		keepCorrupt:  opts.KeepCorrupt,
		fileMode:     0o644,
		lockFile:     lockFile,
		closeCh:      make(chan struct{}),
	}
	b.applyOptions(opts)
	return b, nil
}

// ReadOnly reports whether the bus was opened with Options.ReadOnly.
func (b *Bus) ReadOnly() bool { return b != nil && b.readOnly }

// Commit appends one frame synchronously: the line is written and fsynced
// under the cross-process writer lock before Commit returns, and the
// returned Frame carries the cursor it was assigned. It is the primitive a
// ledger needs where Publish's "never blocks, may drop" contract is wrong:
// a record whose loss the consumer could not detect. Rotation and
// compaction run here as in the background writer. A disabled, failed,
// closed or read-only bus returns an error and appends nothing.
func (b *Bus) Commit(kind, sessionID string, data any) (Frame, error) {
	if b == nil || !b.enabled {
		return Frame{}, errors.New("events: bus disabled")
	}
	if b.readOnly {
		return Frame{}, ErrReadOnly
	}
	if b.closed.Load() {
		return Frame{}, errors.New("events: bus closed")
	}
	if b.failed.Load() {
		return Frame{}, errors.New("events: bus disabled after an earlier write failure")
	}
	raw, err := marshalData(data)
	if err != nil {
		return Frame{}, err
	}
	qf := queuedFrame{kind: kind, sessionID: sessionID, data: raw, ts: time.Now()}

	if err := b.lockDisk(); err != nil {
		return Frame{}, fmt.Errorf("events: lock: %w", err)
	}
	defer b.unlockDisk()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.refreshLocked(); err != nil {
		b.fail(err)
		return Frame{}, err
	}
	if b.maxBytes > 0 && b.totalBytes+int64(len(raw))+96 > b.maxBytes {
		// Compaction normally runs at rotation; at the quota there may be
		// no rotation to come, so give age, count and released readers a
		// chance before refusing.
		b.compactLocked()
		if sealed, err := listSealedSegments(b.dir); err == nil {
			b.totalBytes = sealedBytes(sealed) + b.activeBytes
		}
		if b.totalBytes+int64(len(raw))+96 > b.maxBytes {
			return Frame{}, ErrQuota
		}
	}
	if commitFault != nil {
		if err := commitFault("before-append", b); err != nil {
			b.fail(err)
			return Frame{}, err
		}
	}
	// Snapshot so a failed append or sync can be rolled back: the frame is
	// committed only once its bytes AND their fsync succeeded.
	before := b.activeBytes
	beforeCursor, beforeEventID, beforeFrames, beforeTotal := b.cursor, b.lastEventID, b.activeFrames, b.totalBytes
	if err := b.appendFrameLocked(qf); err != nil {
		err = b.spendRolledBack(beforeCursor+1, err)
		b.fail(err)
		return Frame{}, err
	}
	syncErr := error(nil)
	if commitFault != nil {
		syncErr = commitFault("before-sync", b)
	}
	if syncErr == nil {
		syncErr = b.activeFile.Sync()
	}
	if syncErr != nil {
		err := fmt.Errorf("events: sync: %w", syncErr)
		if terr := b.activeFile.Truncate(before); terr != nil {
			err = fmt.Errorf("%v; rollback failed: %w", err, terr)
		}
		b.cursor, b.lastEventID, b.activeBytes, b.activeFrames, b.totalBytes = beforeCursor, beforeEventID, before, beforeFrames, beforeTotal
		b.written.Add(^uint64(0)) // undo the append's count
		err = b.spendRolledBack(beforeCursor+1, err)
		b.fail(err)
		return Frame{}, err
	}
	// A committed frame counts as accepted and written, so Flush (which waits
	// for synced >= enqueued) and Close's drop accounting stay consistent
	// with the background writer's view.
	b.enqueued.Add(1)
	b.published.Add(1)
	b.synced.Store(b.written.Load())
	if b.activeFrames == 1 && b.durable() {
		// First frame of a fresh active file: the file's directory entry
		// must be durable too, not only its bytes.
		if err := fsyncDir(b.dir); err != nil {
			b.fail(fmt.Errorf("events: sync dir: %w", err))
			return Frame{}, err
		}
	}
	committed := Frame{Cursor: b.cursor, EventID: b.lastEventID, TS: qf.ts.UnixMilli(), Kind: kind, SessionID: sessionID, Data: raw}
	if b.activeBytes >= b.maxSegBytes || b.activeFrames >= b.maxSegFrames {
		b.rotateLocked()
	}
	return committed, nil
}

// spendRolledBack marks a rolled-back commit's cursor as spent on a bus
// that keeps malformed lines (the ledger), so a reopen continues above it
// and no consumer ever sees that number on a different frame. Followers of
// that bus read the active file under the writer lock and so never saw the
// rolled-back bytes; the mark covers every other reader and a crash. A
// failure to write the mark is added to err. Other buses keep their
// rollback as it was.
func (b *Bus) spendRolledBack(cursor Cursor, err error) error {
	if !b.keepCorrupt {
		return err
	}
	if serr := b.spendLocked(cursor); serr != nil {
		return fmt.Errorf("%w; spent mark for cursor %d not written: %v", err, cursor, serr)
	}
	return err
}

// Ends returns the bus cursor (the highest cursor assigned, including any
// spent by a malformed line or a rolled-back commit) and the cursor of the
// newest parseable frame in the retained log (0 when none is retained).
// No frame will ever carry a cursor between the two, so a reader that has
// seen lastFrame has read everything up to end.
func (b *Bus) Ends() (end, lastFrame Cursor, err error) {
	if b == nil || !b.enabled {
		return 0, 0, errors.New("events: bus disabled")
	}
	if err := b.lockDisk(); err != nil {
		return 0, 0, err
	}
	defer b.unlockDisk()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.refreshLocked(); err != nil {
		return 0, 0, err
	}
	end = b.cursor
	if lastFrame, err = lastFrameIn(filepath.Join(b.dir, activeSegmentName)); err != nil || lastFrame > 0 {
		return end, lastFrame, err
	}
	sealed, err := listSealedSegments(b.dir)
	if err != nil {
		return end, 0, err
	}
	for i := len(sealed) - 1; i >= 0; i-- {
		if lastFrame, err = lastFrameIn(sealed[i].path); err != nil || lastFrame > 0 {
			return end, lastFrame, err
		}
	}
	return end, 0, nil
}

// expiredSegments lists sealed segments whose newest frame is older than the
// bus retention window (none when no window is set). Age is read from the
// segment file's mtime: a sealed segment is never written again, so its
// mtime is the instant of its last frame.
func (b *Bus) expiredSegments(sealed []sealedSegment, now time.Time) []sealedSegment {
	if b.retention <= 0 {
		return nil
	}
	var out []sealedSegment
	for _, s := range sealed {
		info, err := os.Stat(s.path)
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > b.retention {
			out = append(out, s)
		}
	}
	return out
}

// fsyncDir syncs a directory and reports the error (Commit's first frame of
// a fresh file). Filesystems that refuse directory fsync return EINVAL or
// ENOTSUP, which is treated as "not needed here", not as a failure.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}
