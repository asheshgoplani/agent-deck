package events

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

// lockDisk serializes append, rotation and counters between processes. ioMu
// also serializes users of this Bus's lock descriptor inside one process.
func (b *Bus) lockDisk() error {
	b.ioMu.Lock()
	if err := syscall.Flock(int(b.lockFile.Fd()), syscall.LOCK_EX); err != nil {
		b.ioMu.Unlock()
		return err
	}
	return nil
}

func (b *Bus) unlockDisk() {
	_ = syscall.Flock(int(b.lockFile.Fd()), syscall.LOCK_UN)
	b.ioMu.Unlock()
}

// activeBounds returns the cursors of the first and last frame of the
// active file by reading only its first and last line. On a bus that keeps
// malformed lines in place, a first or last line that does not parse falls
// back to the full scan (lenientBounds), which gives the same answer for a
// clean file; otherwise that is an error. Open repairs an incomplete tail
// before calling this; a locked writer never exposes a partial line.
func (b *Bus) activeBounds(path string) (first, last Cursor, size int64, err error) {
	first, last, size, err = strictBounds(path)
	if err != nil && b != nil && b.keepCorrupt && !os.IsNotExist(err) {
		return b.lenientBounds(path)
	}
	return first, last, size, err
}

// lenientBounds scans the whole file for the first and last parseable
// frame. A malformed line after the last parseable frame spent its cursor
// number (the same rule as recovery), so last counts it. A file holding
// only malformed lines (a rotation followed by corruption) spent the
// cursors right after everything before it: first and last are then
// absolute, on top of the sealed history, the checkpoint and any spent
// mark. A file with no lines at all is reported as empty.
func (b *Bus) lenientBounds(path string) (first, last Cursor, size int64, err error) {
	first, lastFrame, trailing, size, err := scanFrames(path)
	if err != nil || trailing == 0 {
		return first, lastFrame, size, err
	}
	if first > 0 {
		return first, lastFrame + trailing, size, nil
	}
	// The spent mark is part of the floor even when it already counted a
	// rolled-back commit whose number these lines would otherwise have
	// taken: at worst one extra cursor is spent, never one reused.
	floor, err := b.historyEnd()
	if err != nil {
		return 0, 0, size, err
	}
	return floor + 1, floor + trailing, size, nil
}

// lenientScans counts full scans (scanFrames); tests use it to prove a
// clean ledger stays on the first-and-last-line path.
var lenientScans atomic.Int64

// scanFrames reads a segment for the cursors of its first and last
// parseable frame and the number of malformed lines after the last one.
// The final line must be complete.
func scanFrames(path string) (first, lastFrame, trailing Cursor, size int64, err error) {
	lenientScans.Add(1)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	size = int64(len(data))
	if size == 0 {
		return 0, 0, 0, 0, nil
	}
	if data[len(data)-1] != '\n' {
		return 0, 0, 0, size, fmt.Errorf("events: incomplete active tail")
	}
	for _, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if ln == "" {
			continue
		}
		f, perr := ParseFrameLine([]byte(ln))
		if perr != nil {
			trailing++
			continue
		}
		trailing = 0
		if first == 0 {
			first = f.Cursor
		}
		lastFrame = f.Cursor
	}
	return first, lastFrame, trailing, size, nil
}

// lastFrameIn returns the cursor of the newest parseable frame of a
// segment, reading only its last line when that parses (0 for an empty or
// missing file, or one with no parseable frame).
func lastFrameIn(path string) (Cursor, error) {
	_, last, _, err := strictBounds(path)
	if err == nil || os.IsNotExist(err) {
		return last, nil
	}
	_, last, _, _, err = scanFrames(path)
	return last, err
}

// historyEnd is the highest cursor accounted for outside the active file:
// the newest sealed segment's end, the rotation checkpoint and, on a bus
// that keeps malformed lines, the spent mark a rolled-back commit leaves.
func (b *Bus) historyEnd() (Cursor, error) {
	end := Cursor(0)
	sealed, err := listSealedSegments(b.dir)
	if err != nil {
		return 0, err
	}
	if len(sealed) > 0 {
		end = sealed[len(sealed)-1].end
	}
	checkpoint, err := readCursorCheckpoint(b.dir)
	if err != nil {
		return 0, err
	}
	spent, err := b.spentMark()
	if err != nil {
		return 0, err
	}
	return max(end, checkpoint, spent), nil
}

func strictBounds(path string) (first, last Cursor, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, 0, 0, err
	}
	size = info.Size()
	if size == 0 {
		return 0, 0, 0, nil
	}
	firstLine, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil {
		return 0, 0, size, err
	}
	frame, err := ParseFrameLine(firstLine[:len(firstLine)-1])
	if err != nil {
		return 0, 0, size, err
	}
	first = frame.Cursor
	buf := make([]byte, 4096)
	end := size
	for end > 0 {
		start := end - int64(len(buf))
		if start < 0 {
			start = 0
		}
		n, readErr := f.ReadAt(buf[:end-start], start)
		if readErr != nil && readErr != io.EOF {
			return 0, 0, size, readErr
		}
		chunk := buf[:n]
		if end == size {
			if n == 0 || chunk[n-1] != '\n' {
				return 0, 0, size, fmt.Errorf("events: incomplete active tail")
			}
			chunk = chunk[:n-1]
		}
		if at := strings.LastIndexByte(string(chunk), '\n'); at >= 0 {
			line := make([]byte, size-(start+int64(at)+1)-1)
			if _, err := f.ReadAt(line, start+int64(at)+1); err != nil && err != io.EOF {
				return 0, 0, size, err
			}
			frame, err := ParseFrameLine(line)
			if err != nil {
				return 0, 0, size, err
			}
			return first, frame.Cursor, size, nil
		}
		end = start
	}
	return first, first, size, nil
}

func (b *Bus) refreshLocked() error {
	path := filepath.Join(b.dir, activeSegmentName)
	first, last, size, err := b.activeBounds(path)
	if err != nil {
		return err
	}
	if b.activeFile != nil {
		_ = b.activeFile.Close()
	}
	flags := os.O_CREATE | os.O_RDWR | os.O_APPEND
	if b.readOnly {
		flags = os.O_RDONLY // a follower never creates or appends
	}
	f, err := os.OpenFile(path, flags, b.fileMode)
	if err != nil {
		b.activeFile = nil
		return err
	}
	b.activeFile = f
	if first == 0 {
		sealed, err := listSealedSegments(b.dir)
		if err != nil {
			return err
		}
		if len(sealed) > 0 && sealed[len(sealed)-1].end > b.cursor {
			b.cursor = sealed[len(sealed)-1].end
		}
		checkpoint, err := readCursorCheckpoint(b.dir)
		if err != nil {
			return err
		}
		if checkpoint > b.cursor {
			b.cursor = checkpoint
		}
		spent, err := b.spentMark()
		if err != nil {
			return err
		}
		if spent > b.cursor {
			b.cursor = spent
		}
		b.activeStart = b.cursor + 1
		b.activeFrames = 0
	} else {
		checkpoint, err := readCursorCheckpoint(b.dir)
		if err != nil {
			return err
		}
		if last < checkpoint {
			return fmt.Errorf("events: active cursor %d precedes checkpoint %d", last, checkpoint)
		}
		spent, err := b.spentMark()
		if err != nil {
			return err
		}
		last = max(last, spent)
		if last != b.cursor {
			b.activeFrames = countLines(b.dir, activeSegmentName)
		}
		b.cursor = last
		b.activeStart = first
	}
	b.activeBytes = size
	return nil
}

const dropsFileName = "drops.count"
const cursorFileName = "cursor.state"

// spentFileName holds the highest cursor a rolled-back Commit spent on a
// bus that keeps malformed lines (the ledger). A follower may have been
// handed nothing for it, but a consumer must never see that number reused
// by a different frame, so the next commit continues above it.
const spentFileName = "spent.cursor"

func readCursorCheckpoint(dir string) (Cursor, error) {
	return readCursorFile(dir, cursorFileName)
}

// spentMark returns the spent mark (0 on a bus that does not keep one).
func (b *Bus) spentMark() (Cursor, error) {
	if !b.keepCorrupt {
		return 0, nil
	}
	return readCursorFile(b.dir, spentFileName)
}

// spendLocked records cursor as spent: written durably before the failed
// Commit returns its error.
func (b *Bus) spendLocked(cursor Cursor) error {
	return writeSmallFile(b.dir, "spent.tmp."+strconv.Itoa(os.Getpid()), spentFileName,
		[]byte(strconv.FormatUint(uint64(cursor), 10)+"\n"), b.fileMode, true)
}

func readCursorFile(dir, name string) (Cursor, error) {
	data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- a file of this bus
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return Cursor(value), err
}

func writeCursorCheckpoint(dir string, cursor Cursor, mode os.FileMode, durable bool) error {
	return writeSmallFile(dir, "cursor.tmp."+strconv.Itoa(os.Getpid()), cursorFileName,
		[]byte(strconv.FormatUint(uint64(cursor), 10)+"\n"), mode, durable)
}

// writeSmallFile writes data to a temp file and renames it over name. With
// durable it also fsyncs the file before the rename and the directory
// after, so the checkpoint is on disk before anything relies on it; without
// it the write is the rename-only form the status bus always used.
func writeSmallFile(dir, tmpName, name string, data []byte, mode os.FileMode, durable bool) error {
	tmp := filepath.Join(dir, tmpName)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if durable {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if durable {
		fsyncDirBestEffort(dir)
	}
	return nil
}

func (b *Bus) diskDropsLocked() (uint64, error) {
	data, err := os.ReadFile(filepath.Join(b.dir, dropsFileName))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

func (b *Bus) persistDropsLocked() error {
	current := b.dropped.Load()
	if current == b.persistedDrops {
		return nil
	}
	previous, err := b.diskDropsLocked()
	if err != nil {
		return err
	}
	if err := writeSmallFile(b.dir, "drops.tmp."+strconv.Itoa(os.Getpid()), dropsFileName,
		[]byte(strconv.FormatUint(previous+current-b.persistedDrops, 10)+"\n"), b.fileMode, b.durable()); err != nil {
		return err
	}
	b.persistedDrops = current
	return nil
}
