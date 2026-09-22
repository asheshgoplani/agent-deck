package events

import (
	"bufio"
	"context"
	"io"
	"os"
	"time"
)

// pollInterval is how often a Subscription checks the active segment for
// newly appended frames once it has caught up. Short enough that `events
// follow` feels live; long enough not to busy-loop.
const pollInterval = 15 * time.Millisecond

// Subscription streams Frames with Cursor > the `after` value passed to
// Subscribe, oldest first, never skipping and never repeating one, for as
// long as ctx is not cancelled. Cancelling ctx (or the process dying) is the
// "kill the follower" half of the durability proof: a fresh Subscribe(ctx,
// after) with the last Cursor seen resumes exactly where it left off.
type Subscription struct {
	frames chan Frame
	errCh  chan error
}

// Frames returns the channel of frames in cursor order. It is closed when
// ctx is cancelled or a read error occurs (check Err() after it closes).
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
// newly published frames until ctx is cancelled. after=0 replays the whole
// retained log. Returns ErrCursorTooOld (via Subscription.Err after the
// channel closes) if `after` predates every retained segment.
func (b *Bus) Subscribe(ctx context.Context, after Cursor) (*Subscription, error) {
	sub := &Subscription{
		frames: make(chan Frame, 64),
		errCh:  make(chan error, 1),
	}
	if b == nil || !b.enabled {
		close(sub.frames)
		return sub, nil
	}
	go sub.run(ctx, b, after)
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
		activeStart, _, _, err := activeBounds(activePath)
		if err != nil {
			return nil, err
		}
		if activeStart == 0 {
			if len(sealed) > 0 {
				activeStart = sealed[len(sealed)-1].end + 1
			} else {
				activeStart = 1
			}
		}
		out = append(out, segRef{path: activePath, start: activeStart, sealed: false})
	}
	return out, nil
}

func (s *Subscription) run(ctx context.Context, b *Bus, after Cursor) {
	defer close(s.frames)

	if err := s.checkNotTooOld(b, after); err != nil {
		s.errCh <- err
		return
	}

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
			if seg.start != lastActiveStart {
				lastActiveStart = seg.start
				activeOffset = 0
			}
			ok, newOffset, n, err := s.streamActive(ctx, seg.path, &emitted, activeOffset)
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

// checkNotTooOld errors out if `after` is older than the oldest byte
// currently retained (compaction already dropped it).
func (s *Subscription) checkNotTooOld(b *Bus, after Cursor) error {
	if after == 0 {
		return nil
	}
	sealed, err := listSealedSegments(b.dir)
	if err != nil {
		return err
	}
	if len(sealed) == 0 {
		return nil
	}
	oldestRetainedStart := sealed[0].start
	if after+1 < oldestRetainedStart {
		return ErrCursorTooOld
	}
	return nil
}

// streamFile scans a fully-sealed (immutable) segment file from the start,
// emitting frames with Cursor > *emitted. Returns ok=false if ctx was
// cancelled mid-stream.
func (s *Subscription) streamFile(ctx context.Context, path string, emitted *Cursor) (ok bool, n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, 0, nil
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
