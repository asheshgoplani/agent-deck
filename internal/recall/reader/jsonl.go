package reader

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// regularFile resolves a directory entry to the regular file behind it (one
// lstat, plus a symlink resolution and stat when the entry is a link). ok is
// false for anything that is not a regular file.
func regularFile(d fs.DirEntry, path string) (string, fs.FileInfo, bool) {
	if d.Type()&fs.ModeSymlink == 0 {
		info, err := entryInfo(d)
		if err != nil || !info.Mode().IsRegular() {
			return "", nil, false
		}
		return path, info, true
	}
	resolved, err := fsEvalSymlinks(path)
	if err != nil {
		return "", nil, false
	}
	info, err := fsStat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, false
	}
	return resolved, info, true
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

// layout describes where a file-per-conversation harness keeps its
// transcripts under one root, so Discover and Locate are shared: base is
// the subdir of the root that is symlink-resolved and deduplicated once
// ("" for the root itself; Claude's "projects", which the worker-scratch
// homes link back to one config dir, so a tree reached through many roots
// is walked once), subdirs are the trees under base to walk (none: base
// itself), skip names directories never descended into, keep filters file
// names, and ident fills in the harness-specific fields of a SourceRef
// from its path and the resolved base (false: not a transcript after all).
type layout struct {
	harness string
	base    string
	subdirs []string
	skip    map[string]bool
	keep    func(name string) bool
	ident   func(ref *SourceRef, base string) bool
}

// discover walks every root once and emits each transcript once, files
// deduplicated by (dev, ino).
func (l layout) discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error {
	seenBase := map[string]bool{}
	seenFile := dedup{}
	subdirs := l.subdirs
	if len(subdirs) == 0 {
		subdirs = []string{""}
	}
	for _, r := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		base, err := fsEvalSymlinks(filepath.Join(r.Dir, l.base))
		if err != nil || seenBase[base] {
			continue // nothing to index, or a tree already walked
		}
		seenBase[base] = true
		for _, sub := range subdirs {
			err := walkFiles(ctx, filepath.Join(base, sub), l.skip, l.keep, func(path string, info os.FileInfo) error {
				ref := fileRef(l.harness, r, path, info)
				if seenFile.seen(ref.Dev, ref.Ino) || !l.ident(&ref, base) {
					return nil
				}
				return emit(ref)
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// locate builds the SourceRef of one transcript under a root (the Stop
// hook's path) with the same identity rules as the walk.
func (l layout) locate(path string, roots []Root) (SourceRef, bool) {
	if !l.keep(filepath.Base(path)) {
		return SourceRef{}, false
	}
	subdirs := l.subdirs
	if len(subdirs) == 0 {
		subdirs = []string{""}
	}
	for _, sub := range subdirs {
		real, info, r, ok := locateUnder(path, filepath.Join(l.base, sub), roots)
		if !ok {
			continue
		}
		for _, seg := range strings.Split(filepath.Dir(real), string(os.PathSeparator)) {
			if l.skip[seg] {
				return SourceRef{}, false
			}
		}
		ref := fileRef(l.harness, r, real, info)
		base, _ := fsEvalSymlinks(filepath.Join(r.Dir, l.base))
		if !l.ident(&ref, base) {
			return SourceRef{}, false
		}
		return ref, true
	}
	return SourceRef{}, false
}

// emitter is the state every reader's pass shares: the source, the sink,
// the tool calls awaiting their result, the model last announced and the
// first sink error, which ends the pass (the sink refused, for instance a
// quarantine, and the cursor stays put).
type emitter struct {
	src         SourceRef
	sink        Sink
	pending     map[string]pendingCall
	sessionSent bool
	model       string
	err         error
}

func newEmitter(src SourceRef, sink Sink) emitter {
	return emitter{src: src, sink: sink, pending: map[string]pendingCall{}}
}

// fail keeps the first sink error.
func (e *emitter) fail(err error) {
	if err != nil && e.err == nil {
		e.err = err
	}
}

// scan runs one JSONL pass over the source (see scanJSONL), handing each
// record to line, and flushes the unmatched calls at the end.
func (e *emitter) scan(ctx context.Context, from, stop int64, b *Budget, line func(line []byte, off, n int64)) (int64, error) {
	off, err := scanJSONL(ctx, e.src.Path, from, stop, e.sink, b, func(l []byte, off, n int64) error {
		line(l, off, n)
		return e.err
	})
	e.flush()
	return off, err
}

// session emits the Session record once per pass, with the source's
// native id when s names none; later calls are dropped, so the first
// record that knows the conversation wins.
func (e *emitter) session(s Session) {
	if e.sessionSent {
		return
	}
	e.sessionSent = true
	if s.NativeID == "" {
		s.NativeID = e.src.NativeID
	}
	e.sink.Session(s)
}

// setModel announces a model change once per distinct model.
func (e *emitter) setModel(model string) {
	if model == "" || model == e.model {
		return
	}
	e.model = model
	e.sink.Session(Session{Model: model})
}

// openCall records a call awaiting its result; a call without an id is
// emitted at once, unjoined.
func (e *emitter) openCall(id string, pc pendingCall) {
	if id == "" {
		e.emitCall(pc, pc.ts, false)
		return
	}
	e.pending[id] = pc
}

// closeCall joins a result to its pending call and emits it; a result
// whose call was never seen in this pass is emitted as orphan.
func (e *emitter) closeCall(id string, orphan pendingCall, resultTS time.Time, isError bool) {
	pc, ok := e.pending[id]
	if ok {
		delete(e.pending, id)
	} else {
		pc = orphan
	}
	e.emitCall(pc, resultTS, isError)
}

func (e *emitter) emitCall(pc pendingCall, resultTS time.Time, isError bool) {
	e.fail(e.sink.ToolCall(pc.call(resultTS, isError)))
}

// flush emits calls whose result never arrived in this pass (the result
// may land in the next append; it is then an unmatched result).
func (e *emitter) flush() {
	for id, pc := range e.pending {
		delete(e.pending, id)
		e.emitCall(pc, time.Time{}, false)
	}
}

// pendingCall is a tool_use waiting for its tool_result.
type pendingCall struct {
	name    string
	ts      time.Time
	digest  string
	touches []FileTouch
}

// call is the ToolCall for a result seen at resultTS (zero: none).
func (pc pendingCall) call(resultTS time.Time, isError bool) ToolCall {
	tc := ToolCall{Name: pc.name, TS: unixOrZero(pc.ts), IsError: isError, ArgDigest: pc.digest, Touches: pc.touches}
	if !pc.ts.IsZero() && !resultTS.IsZero() && resultTS.After(pc.ts) {
		tc.DurationMS = resultTS.Sub(pc.ts).Milliseconds()
	}
	return tc
}

// joinText appends one text block to a body, newline-separated.
func joinText(sb *strings.Builder, text string) {
	if text == "" {
		return
	}
	if sb.Len() > 0 {
		sb.WriteByte('\n')
	}
	sb.WriteString(text)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// unixOrZero is t.Unix(), with 0 (not the year-one epoch) for a missing
// timestamp.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// unixFloat converts a float seconds timestamp (Hermes) to time.Time.
func unixFloat(ts float64) time.Time {
	if ts <= 0 {
		return time.Time{}
	}
	sec := int64(ts)
	return time.Unix(sec, int64((ts-float64(sec))*1e9))
}
