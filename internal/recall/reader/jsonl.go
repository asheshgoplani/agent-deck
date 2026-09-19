package reader

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// lineFunc receives one complete JSONL record (without its newline) at byte
// offset off, n bytes long including the newline. A non-nil error stops the
// scan before the record's bytes are consumed.
type lineFunc func(line []byte, off, n int64) error

// scanJSONL streams newline-terminated records of path from byte offset
// from and returns the offset just past the last record it handed to fn.
// It stops at a torn trailing line (the cursor never covers it), at stop
// (a harness-supplied upper bound, 0 for none), on ctx, and on the budget
// (ErrBudget; parsedTo is still valid). Over-long lines are consumed whole,
// counted on sink and never decoded, so one pathological record cannot
// grow the resident set. Claude, Codex and Pi share it.
func scanJSONL(ctx context.Context, path string, from, stop int64, sink Sink, b *Budget, fn lineFunc) (int64, error) {
	f, err := fsOpen(path)
	if err != nil {
		return from, err
	}
	defer f.Close()
	if from > 0 {
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			return from, err
		}
	}
	br := bufio.NewReaderSize(f, 256<<10)
	off := from
	var scratch []byte
	for lines := 0; ; lines++ {
		if lines&63 == 0 {
			if err := ctx.Err(); err != nil {
				return off, err
			}
			if b.Expired() {
				return off, ErrBudget
			}
		}
		line, n, complete, tooLong, err := readLine(br, &scratch)
		if err != nil && !errors.Is(err, io.EOF) {
			return off, err
		}
		if !complete {
			break // torn trailing line: left for the next sweep
		}
		if stop > 0 && off+int64(n) > stop {
			break // beyond the harness's own cursor: not yet final
		}
		if tooLong {
			sink.Count(CountLineTooLong, 1)
		} else if err := fn(line, off, int64(n)); err != nil {
			return off, err
		}
		off += int64(n)
		if !b.Consume(int64(n)) {
			return off, ErrBudget
		}
	}
	return off, nil
}

// readLine returns the next newline-terminated line. complete is false at a
// torn tail (no trailing newline); tooLong lines are consumed but not
// returned. n is the number of bytes consumed either way.
func readLine(br *bufio.Reader, scratch *[]byte) (line []byte, n int, complete, tooLong bool, err error) {
	*scratch = (*scratch)[:0]
	for {
		chunk, err := br.ReadSlice('\n')
		n += len(chunk)
		switch {
		case err == nil:
			if tooLong || len(*scratch)+len(chunk) > MaxLineBytes {
				return nil, n, true, true, nil
			}
			if len(*scratch) == 0 {
				return chunk, n, true, false, nil
			}
			*scratch = append(*scratch, chunk...)
			return *scratch, n, true, false, nil
		case errors.Is(err, bufio.ErrBufferFull):
			if !tooLong {
				if len(*scratch)+len(chunk) > MaxLineBytes {
					tooLong = true
					*scratch = (*scratch)[:0]
				} else {
					*scratch = append(*scratch, chunk...)
				}
			}
		case errors.Is(err, io.EOF):
			// Torn tail: bytes without a newline stay unconsumed for the
			// cursor's purposes; the next sweep re-reads them.
			return nil, 0, false, tooLong, io.EOF
		default:
			return nil, n, false, tooLong, err
		}
	}
}

// walkFiles calls emit for every regular file under dir (recursively, one
// readdir per directory and one lstat per file) whose name passes keep,
// skipping directories named in skip. It is the discovery walk every
// file-per-conversation harness shares; ctx cancellation stops it.
func walkFiles(ctx context.Context, dir string, skip map[string]bool, keep func(name string) bool, emit func(path string, info os.FileInfo) error) error {
	entries, err := fsReadDir(dir)
	if err != nil {
		return nil // vanished or unreadable: skip, the next sweep sees it
	}
	for _, d := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := d.Name()
		path := dir + string(os.PathSeparator) + name
		switch {
		case d.IsDir():
			if skip[name] {
				continue
			}
			if err := walkFiles(ctx, path, skip, keep, emit); err != nil {
				return err
			}
		case keep(name):
			path, info, ok := regularFile(d, path)
			if !ok {
				continue
			}
			if err := emit(path, info); err != nil {
				return err
			}
		}
	}
	return nil
}

// fileRef builds the common part of a SourceRef for a regular file.
func fileRef(harness string, r Root, path string, info os.FileInfo) SourceRef {
	dev, ino := FileIdentity(info)
	return SourceRef{
		Harness:       harness,
		Profile:       r.Profile,
		Path:          path,
		Dev:           dev,
		Ino:           ino,
		Size:          info.Size(),
		MtimeNS:       info.ModTime().UnixNano(),
		RetentionDays: r.RetentionDays,
	}
}

// dedup remembers (dev, ino) pairs so a file reachable through several
// paths is emitted once; files without an inode are never deduplicated.
type dedup map[[2]uint64]bool

func (d dedup) seen(dev, ino uint64) bool {
	if ino == 0 {
		return false
	}
	key := [2]uint64{dev, ino}
	if d[key] {
		return true
	}
	d[key] = true
	return false
}

// Locator is implemented by readers whose sources are single files a hook
// can name: Locate builds the SourceRef for one path under one of the
// roots, so a Stop hook can index exactly that file without a walk. ok is
// false when the path is not under any root of the harness or is not a
// transcript of it.
type Locator interface {
	Locate(path string, roots []Root) (SourceRef, bool)
}

// Locate finds the registered reader that owns path and its SourceRef.
func Locate(path string, roots []Root) (SourceRef, Reader, bool) {
	for _, rd := range Registry() {
		loc, ok := rd.(Locator)
		if !ok {
			continue
		}
		if ref, ok := loc.Locate(path, rootsOf(roots, rd.Harness())); ok {
			return ref, rd, true
		}
	}
	return SourceRef{}, nil, false
}

func rootsOf(roots []Root, harness string) []Root {
	var out []Root
	for _, r := range roots {
		if r.Harness == harness {
			out = append(out, r)
		}
	}
	return out
}

// locateUnder resolves path and returns it with its FileInfo and the root
// whose subdir (resolved) contains it, when it is a regular file there.
func locateUnder(path, subdir string, roots []Root) (string, os.FileInfo, Root, bool) {
	real, err := fsEvalSymlinks(path)
	if err != nil {
		return "", nil, Root{}, false
	}
	info, err := fsStat(real)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, Root{}, false
	}
	for _, r := range roots {
		base, err := fsEvalSymlinks(filepath.Join(r.Dir, subdir))
		if err != nil {
			continue
		}
		if real == base || strings.HasPrefix(real, base+string(os.PathSeparator)) {
			return real, info, r, true
		}
	}
	return "", nil, Root{}, false
}
