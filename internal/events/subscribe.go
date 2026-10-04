package events

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"sync/atomic"
	"time"
)

// pollInterval is how often a Subscription checks the active segment for
// newly appended frames once it has caught up. Short enough that `events
// follow` feels live; long enough not to busy-loop.
const pollInterval = 15 * time.Millisecond

// subscribeActiveHook is a test seam called between a poll's segment
// listing and its read of the active file. nil in production.
var subscribeActiveHook atomic.Pointer[func()]

// Subscription streams Frames with Cursor > the `after` value passed to
// Subscribe, oldest first, never skipping and never repeating one, for as
// long as ctx is not cancelled and the Bus is not closed. Bus.Close stops
// every subscription and waits for it before releasing the bus's files.
// Cancelling ctx (or the process dying) is the
// "kill the follower" half of the durability proof: a fresh Subscribe(ctx,
// after) with the last Cursor seen resumes exactly where it left off.
type Subscription struct {
	frames chan Frame
	errCh  chan error
}

// Frames returns the channel of frames in cursor order. It is closed when
// ctx is cancelled, the Bus is closed, or a read error occurs (check Err()
// after it closes; it is nil for a cancel or a close).
func (s *Subscription) Frames() <-chan Frame { return s.frames }

// Err returns the error that stopped the subscription, if any (non-blocking;
// only meaningful after Frames() has been drained/closed).
func (s *Subscription) Err() error {
	select {
	case err := <-s.errCh:
		return err
	default:
		return nil
	}
}

// Subscribe streams every frame with Cursor > after, then keeps streaming
// newly published frames until ctx is cancelled or the Bus is closed.
// after=0 replays the whole retained log. Returns ErrCursorTooOld (via
// Subscription.Err after the channel closes) if `after` predates every
// retained segment. Subscribe on a closed Bus returns a subscription whose
// channel is already closed.
func (b *Bus) Subscribe(ctx context.Context, after Cursor) (*Subscription, error) {
	sub := &Subscription{
		frames: make(chan Frame, 64),
		errCh:  make(chan error, 1),
	}
	if b == nil || !b.enabled {
		close(sub.frames)
		return sub, nil
	}
	// Register under the lock Close takes to set closed, so no subscription
	// is added once Close has started waiting for them.
	b.publishMu.RLock()
	if b.closed.Load() {
		b.publishMu.RUnlock()
		close(sub.frames)
		return sub, nil
	}
	b.subWg.Add(1)
	b.publishMu.RUnlock()
	go func() {
		defer b.subWg.Done()
		sub.run(ctx, b, after)
	}()
	return sub, nil
}

type segRef struct {
	path   string
	start  Cursor
	end    Cursor // 0 for the still-open active segment
	sealed bool
}

func (b *Bus) listAllSegments() ([]segRef, error) {
	if err := b.lockDisk(); err != nil {
		return nil, err
	}
	defer b.unlockDisk()
	sealed, err := listSealedSegments(b.dir)
	if err != nil {
		return nil, err
	}
	out := make([]segRef, 0, len(sealed)+1)
	for _, s := range sealed {
		out = append(out, segRef{path: s.path, start: s.start, end: s.end, sealed: true})
	}

	activePath := b.dir + string(os.PathSeparator) + activeSegmentName
	if _, err := os.Stat(activePath); err == nil {
		activeStart, _, _, err := b.activeBounds(activePath)
		if err != nil {
			return nil, err
		}
		if activeStart == 0 {
			last := Cursor(0)
			if len(sealed) > 0 {
				last = sealed[len(sealed)-1].end
			}
			checkpoint, err := readCursorCheckpoint(b.dir)
			if err != nil {
				return nil, err
			}
			if checkpoint > last {
				last = checkpoint
			}
			activeStart = last + 1
		}
		out = append(out, segRef{path: activePath, start: activeStart, sealed: false})
	}
	return out, nil
}

func (s *Subscription) run(parent context.Context, b *Bus, after Cursor) {
	defer close(s.frames)
	// ctx ends with the caller's context or with Bus.Close, so every ctx
	// check and every blocked frame send below also gives way to Close.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		select {
		case <-b.closeCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	emitted := after
	var lastActiveStart Cursor = 0
	var activeOffset int64

	for {
		if ctx.Err() != nil {
			return
		}
		segs, err := b.listAllSegments()
		if err != nil {
			s.errCh <- err
			return
		}
		if emitted > 0 && len(segs) > 0 && emitted+1 < segs[0].start {
			s.errCh <- ErrCursorTooOld
			return
		}
		madeProgress := false
		for _, seg := range segs {
			if seg.sealed {
				if seg.end <= emitted {
					continue
				}
				ok, n, err := s.streamFile(ctx, seg.path, &emitted)
				if err != nil {
					s.errCh <- err
					return
				}
				if n > 0 {
					madeProgress = true
				}
				if !ok {
					return
				}
				continue
			}

			// active (unsealed) segment
			if hook := subscribeActiveHook.Load(); hook != nil {
				(*hook)()
			}
			if seg.start != lastActiveStart {
				lastActiveStart = seg.start
				activeOffset = 0
			}
			var ok bool
			var newOffset int64
			var n int
			if b.keepCorrupt {
				ok, newOffset, n, err = s.streamActiveCommitted(ctx, b, seg, &emitted, activeOffset)
			} else {
				ok, newOffset, n, err = s.streamActive(ctx, seg.path, &emitted, activeOffset)
			}
			if err != nil {
				s.errCh <- err
				return
			}
			activeOffset = newOffset
			if n > 0 {
				madeProgress = true
			}
			if !ok {
				return
			}
		}

		if ctx.Err() != nil {
			return
		}
		if !madeProgress {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollInterval):
			}
		}
	}
}

// streamFile scans a fully-sealed (immutable) segment file from the start,
// emitting frames with Cursor > *emitted. Returns ok=false if ctx was
// cancelled mid-stream.
func (s *Subscription) streamFile(ctx context.Context, path string, emitted *Cursor) (ok bool, n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, ErrCursorTooOld
		}
		return false, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		frame, perr := ParseFrameLine(line)
		if perr != nil {
			continue // best-effort: skip an unparsable line rather than abort the stream
		}
		if frame.Cursor <= *emitted {
			continue
		}
		select {
		case s.frames <- frame:
			*emitted = frame.Cursor
			n++
		case <-ctx.Done():
			return false, n, nil
		}
	}
	return true, n, scanner.Err()
}

// streamActive tails the still-open active segment from a remembered byte
// offset, only emitting complete (newline-terminated) lines so it never
// hands out a frame that's still being written. Returns the new offset to
// resume from on the next poll.
func (s *Subscription) streamActive(ctx context.Context, path string, emitted *Cursor, fromOffset int64) (ok bool, newOffset int64, n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, fromOffset, 0, nil
		}
		return false, fromOffset, 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return false, fromOffset, 0, err
	}
	if info.Size() <= fromOffset {
		return true, fromOffset, 0, nil
	}
	if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
		return false, fromOffset, 0, err
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	offset := fromOffset
	for {
		line, rerr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			offset += int64(len(line))
			trimmed := line[:len(line)-1]
			if len(trimmed) > 0 {
				frame, perr := ParseFrameLine(trimmed)
				if perr == nil && frame.Cursor > *emitted {
					select {
					case s.frames <- frame:
						*emitted = frame.Cursor
						n++
					case <-ctx.Done():
						return false, offset, n, nil
					}
				}
			}
		}
		if rerr != nil {
			// Incomplete trailing line (still being written) or EOF: stop,
			// keep `offset` at the last complete line so the next poll
			// re-reads only the new bytes.
			break
		}
	}
	return true, offset, n, nil
}

// streamActiveCommitted is streamActive for a ledger bus: the new bytes are
// read under the writer lock, so a frame is visible only once the Commit
// that wrote it has returned (its fsync done, or rolled back). Under the
// same lock it checks that the active file is still the one listed: if a
// rotation sealed it since the listing, nothing is read and the next poll
// re-lists, so the sealed frames are streamed first and none is skipped.
// The frames are emitted after the lock is released: a follower that is
// slow to drain its channel never holds the lock.
func (s *Subscription) streamActiveCommitted(ctx context.Context, b *Bus, seg segRef, emitted *Cursor, fromOffset int64) (ok bool, newOffset int64, n int, err error) {
	if err := b.lockDisk(); err != nil {
		return false, fromOffset, 0, err
	}
	sealed, err := listSealedSegments(b.dir)
	if err != nil {
		b.unlockDisk()
		return false, fromOffset, 0, err
	}
	if len(sealed) > 0 && sealed[len(sealed)-1].start >= seg.start {
		b.unlockDisk()
		return true, fromOffset, 0, nil // rotated since the listing
	}
	data, err := readFrom(seg.path, fromOffset)
	b.unlockDisk()
	if err != nil {
		return false, fromOffset, 0, err
	}
	offset := fromOffset
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break // no complete line left: the next poll starts here
		}
		line := data[:i]
		data = data[i+1:]
		offset += int64(i + 1)
		if len(line) == 0 {
			continue
		}
		frame, perr := ParseFrameLine(line)
		if perr != nil || frame.Cursor <= *emitted {
			continue
		}
		select {
		case s.frames <- frame:
			*emitted = frame.Cursor
			n++
		case <-ctx.Done():
			return false, offset, n, nil
		}
	}
	return true, offset, n, nil
}

// readFrom returns the bytes of path from offset to its end (nil when the
// file is missing or not longer than offset).
func readFrom(path string, offset int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= offset {
		return nil, err
	}
	data := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(data, offset); err != nil && err != io.EOF {
		return nil, err
	}
	return data, nil
}
