package events

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// ReadOnly opens the log for Subscribe and Stats only: no writer
	// goroutine, no tail repair, no Commit. A follower (`events follow --bus
	// comms`) opens the ledger this way so only the owning daemon ever
	// writes it. The directory must already exist.
	ReadOnly bool
}

// ErrReadOnly is returned by Commit and reported by Publish (as a drop) on a
// bus opened with Options.ReadOnly.
var ErrReadOnly = errors.New("events: bus is read-only")

// ErrNoBus is returned by OpenAt in read-only mode when the directory does
// not exist: nothing has ever been written there.
var ErrNoBus = errors.New("events: no log at this path")

// OpenAt opens the durable bus rooted at dir with explicit options. It is
// Open plus the knobs a second log (the comms ledger) needs: time-based
// retention, and a read-only mode for followers.
func OpenAt(dir string, opts Options) (*Bus, error) {
	if opts.ReadOnly {
		return openReadOnly(dir, opts)
	}
	b, err := Open(dir)
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
	lockFile, err := os.OpenFile(filepath.Join(dir, "writer.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("events: open writer lock: %w", err)
	}
	b := &Bus{
		dir:          dir,
		maxSegBytes:  defaultMaxSegBytes,
		maxSegFrames: defaultMaxSegFrames,
		retainSegs:   defaultRetainSegs,
		enabled:      true,
		readOnly:     true,
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
	if err := b.appendFrameLocked(qf); err != nil {
		b.fail(err)
		return Frame{}, err
	}
	if err := b.activeFile.Sync(); err != nil {
		err = fmt.Errorf("events: sync: %w", err)
		b.fail(err)
		return Frame{}, err
	}
	// A committed frame counts as accepted and written, so Flush (which waits
	// for synced >= enqueued) and Close's drop accounting stay consistent
	// with the background writer's view.
	b.enqueued.Add(1)
	b.published.Add(1)
	b.synced.Store(b.written.Load())
	committed := Frame{Cursor: b.cursor, EventID: b.lastEventID, TS: qf.ts.UnixMilli(), Kind: kind, SessionID: sessionID, Data: raw}
	if b.activeBytes >= b.maxSegBytes || b.activeFrames >= b.maxSegFrames {
		b.rotateLocked()
	}
	return committed, nil
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
