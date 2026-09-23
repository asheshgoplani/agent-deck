package query

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RowsSource is a native transcript read directly, never through the Recall
// index: the session's Claude Code JSONL or Codex rollout. Resolve, when
// set, re-resolves the live path (Codex re-creates its rollout after the
// trust prompt); follow emits resync_required when the answer changes.
type RowsSource struct {
	Harness string
	Path    string
	Resolve func() (string, error)
}

// RowsSession identifies the conversation a rows timeline belongs to.
type RowsSession struct {
	ID       string `json:"id,omitempty"`
	Harness  string `json:"harness"`
	NativeID string `json:"native_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Title    string `json:"title,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
}

// RowsTimeline is the `recall timeline --json` result. With --since, Turns
// holds only rows that started after the cursor; Updates carries changes to
// earlier rows (merge them by id) and Removed the ids to drop.
type RowsTimeline struct {
	Schema        string      `json:"schema"`
	Session       RowsSession `json:"session"`
	Source        string      `json:"source"`
	Turns         []Row       `json:"turns"`
	Updates       []Row       `json:"updates,omitempty"`
	Removed       []string    `json:"removed,omitempty"`
	ThroughCursor string      `json:"through_cursor,omitempty"`
	Status        *LiveStatus `json:"status,omitempty"`
}

// RowsOptions bounds a timeline read.
//   - Since resumes after a cursor (incremental fetch).
//   - Limit stops after that many new rows; the cursor points there, so the
//     next call with --since pages forward.
//   - Tail returns only the last N rows, reading backwards in growing byte
//     windows so a 100 MB transcript answers from its end.
//   - AgentID returns one Claude Code sub-agent sidechain.
type RowsOptions struct {
	Since   string
	Limit   int
	Tail    int
	AgentID string
}

// ErrRowsUnsupported is returned for a harness whose native format the rows
// reader does not stream directly.
var ErrRowsUnsupported = errors.New("recall rows: harness is not read directly")

// SupportsDirectRows reports whether ReadRows/FollowRows read the harness's
// native file without the index.
func SupportsDirectRows(harness string) bool { return harness == "claude" || harness == "codex" }

type rowsCursor struct {
	V int            `json:"v"`
	P string         `json:"p"`
	O int64          `json:"o"`
	A string         `json:"a"`
	S rowParserState `json:"s"`
}

func encodeRowsCursor(c rowsCursor) string {
	c.V = 2
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeRowsCursor(s string) (rowsCursor, error) {
	var c rowsCursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.V != 2 || c.P == "" || c.O < 0 {
		return c, errors.New("recall rows: invalid cursor")
	}
	return c, nil
}

// anchorAt hashes the bytes just before off, so a resumed read detects a
// rewritten or replaced file instead of splicing two different histories.
func anchorAt(f io.ReaderAt, off int64) (string, error) {
	start := off - 256
	if start < 0 {
		start = 0
	}
	buf := make([]byte, off-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	h := sha256.Sum256(buf)
	return fmt.Sprintf("%x", h[:8]), nil
}

// ResyncError reports that a cursor no longer describes the source.
type ResyncError struct{ Reason string }

func (e *ResyncError) Error() string { return "recall rows: resync required: " + e.Reason }

// openAtCursor opens the cursor's file and checks that the bytes before the
// cursor are the ones it was issued for.
func openAtCursor(c rowsCursor) (*os.File, os.FileInfo, error) {
	f, err := os.Open(c.P)
	if err != nil {
		return nil, nil, &ResyncError{"source_missing"}
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if info.Size() < c.O {
		f.Close()
		return nil, nil, &ResyncError{"source_shortened"}
	}
	if a, err := anchorAt(f, c.O); err != nil || a != c.A {
		f.Close()
		return nil, nil, &ResyncError{"source_rewritten"}
	}
	return f, info, nil
}

// rowSet applies frames in order, which is exactly what a client does with a
// follow stream: rows append, updates merge by id (an update that sets
// queued:false moves the row to its absorb time, the end), removes drop.
type rowSet struct {
	rows []Row
	idx  map[string]int
	// Out-of-window changes (--since / --tail): patches and removals for
	// rows this set never saw.
	updates     []Row
	updateIdx   map[string]int
	removed     []string
	keepForeign bool
}

func newRowSet() *rowSet { return &rowSet{idx: map[string]int{}, updateIdx: map[string]int{}} }

func (s *rowSet) apply(f RowFrame) {
	switch f.Frame {
	case "row":
		if i, ok := s.idx[f.Row.ID]; ok {
			s.rows[i] = *f.Row
			return
		}
		s.idx[f.Row.ID] = len(s.rows)
		s.rows = append(s.rows, *f.Row)
	case "update":
		i, ok := s.idx[f.Row.ID]
		if !ok {
			if s.keepForeign {
				if j, seen := s.updateIdx[f.Row.ID]; seen {
					MergeRow(&s.updates[j], *f.Row)
				} else {
					s.updateIdx[f.Row.ID] = len(s.updates)
					s.updates = append(s.updates, *f.Row)
				}
			}
			return
		}
		MergeRow(&s.rows[i], *f.Row)
		if f.Row.Queued != nil && !*f.Row.Queued && f.Row.TS != "" && i != len(s.rows)-1 {
			row := s.rows[i]
			s.rows[i].ID = ""
			s.idx[row.ID] = len(s.rows)
			s.rows = append(s.rows, row)
		}
	case "remove":
		if i, ok := s.idx[f.ID]; ok {
			s.rows[i].ID = ""
			delete(s.idx, f.ID)
		} else if s.keepForeign {
			s.removed = append(s.removed, f.ID)
		}
	}
}

func (s *rowSet) list() []Row {
	out := make([]Row, 0, len(s.rows))
	for _, r := range s.rows {
		if r.ID != "" {
			out = append(out, r)
		}
	}
	return out
}

func (s *rowSet) len() int { return len(s.idx) }

// MergeRow applies an update frame's row to dst: every field present in the
// patch replaces dst's, meta keys merge.
func MergeRow(dst *Row, patch Row) {
	set := func(d *string, v string) {
		if v != "" {
			*d = v
		}
	}
	set(&dst.Kind, patch.Kind)
	set(&dst.TS, patch.TS)
	set(&dst.Title, patch.Title)
	set(&dst.Body, patch.Body)
	set(&dst.Summary, patch.Summary)
	set(&dst.Detail, patch.Detail)
	set(&dst.ToolID, patch.ToolID)
	set(&dst.RawType, patch.RawType)
	if patch.Finished != nil {
		dst.Finished = patch.Finished
	}
	if patch.IsError != nil {
		dst.IsError = patch.IsError
	}
	if patch.Queued != nil {
		dst.Queued = patch.Queued
	}
	if len(patch.Meta) > 0 {
		if dst.Meta == nil {
			dst.Meta = map[string]any{}
		}
		for k, v := range patch.Meta {
			dst.Meta[k] = v
		}
	}
	if patch.Children != nil {
		dst.Children = patch.Children
	}
}

// scanLines feeds every complete line of f in [from, to) to fn with the
// offset just past that line. A torn final line is left for the next read.
// fn returning errStopScan ends the scan after that line.
func scanLines(ctx context.Context, f io.ReaderAt, from, to int64, fn func(line []byte, end int64) error) (int64, error) {
	r := bufio.NewReaderSize(io.NewSectionReader(f, from, to-from), 256<<10)
	off := from
	for {
		if err := ctx.Err(); err != nil {
			return off, err
		}
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return off, nil
		}
		if err != nil {
			return off, err
		}
		off += int64(len(line))
		if err := fn(bytes.TrimRight(line, "\r\n"), off); err != nil {
			if errors.Is(err, errStopScan) {
				return off, nil
			}
			return off, err
		}
	}
}

var errStopScan = errors.New("stop scan")

// tailStart returns the first line start at or after size-tail.
func tailStart(f io.ReaderAt, size, tail int64) int64 {
	if tail <= 0 || tail >= size {
		return 0
	}
	start := size - tail
	buf := make([]byte, 64<<10)
	for pos := start - 1; pos < size; pos += int64(len(buf)) {
		n, err := f.ReadAt(buf, pos)
		if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
			return pos + int64(i) + 1
		}
		if err != nil {
			break
		}
	}
	return size
}

// ReadRows parses a native transcript into rows and returns the resume
// cursor for FollowRows or a later --since read. It never touches the index.
func ReadRows(ctx context.Context, src RowsSource, opts RowsOptions) (RowsTimeline, error) {
	var tl RowsTimeline
	if !SupportsDirectRows(src.Harness) {
		return tl, fmt.Errorf("%w: %s", ErrRowsUnsupported, src.Harness)
	}
	if opts.AgentID != "" {
		if src.Harness != "claude" {
			return tl, fmt.Errorf("recall rows: --agent is supported for Claude Code sidechains only")
		}
		rows, err := claudeSidechainRows(ctx, src.Path, opts.AgentID)
		tl.Turns = rows
		return tl, err
	}
	var f *os.File
	var size, start int64
	st := rowParserState{}
	set := newRowSet()
	if opts.Since != "" {
		c, err := decodeRowsCursor(opts.Since)
		if err != nil {
			return tl, &ResyncError{"invalid_cursor"}
		}
		if src.Path != "" && c.P != src.Path {
			return tl, &ResyncError{"source_moved"}
		}
		file, info, err := openAtCursor(c)
		if err != nil {
			return tl, err
		}
		f, size, start, st = file, info.Size(), c.O, c.S
		set.keepForeign = true
	} else {
		file, err := os.Open(src.Path)
		if err != nil {
			return tl, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return tl, err
		}
		f, size = file, info.Size()
	}
	defer f.Close()
	path := f.Name()
	if opts.Tail > 0 && opts.Since == "" {
		return readTail(ctx, src, f, size, opts.Tail)
	}
	p := newRowParser(src.Harness, st)
	end, err := scanLines(ctx, f, start, size, func(line []byte, _ int64) error {
		for _, fr := range p.line(line) {
			set.apply(fr)
		}
		if opts.Limit > 0 && set.len() >= opts.Limit {
			return errStopScan
		}
		return nil
	})
	if err != nil {
		return tl, err
	}
	tl.Turns = set.list()
	tl.Updates, tl.Removed = set.updates, set.removed
	if src.Harness == "claude" {
		tl.Turns = withClaudeSidechains(ctx, src.Path, tl.Turns)
		tl.Updates = withClaudeSidechains(ctx, src.Path, tl.Updates)
	}
	anchor, err := anchorAt(f, end)
	if err != nil {
		return tl, err
	}
	tl.ThroughCursor = encodeRowsCursor(rowsCursor{P: path, O: end, A: anchor, S: p.st})
	return tl, nil
}

// readTail parses growing windows from the end until n rows are found or
// the whole file was read, then keeps the last n rows.
func readTail(ctx context.Context, src RowsSource, f *os.File, size int64, n int) (RowsTimeline, error) {
	var tl RowsTimeline
	for window := int64(256 << 10); ; window *= 4 {
		start := tailStart(f, size, window)
		p := newRowParser(src.Harness, rowParserState{})
		set := newRowSet()
		end, err := scanLines(ctx, f, start, size, func(line []byte, _ int64) error {
			for _, fr := range p.line(line) {
				set.apply(fr)
			}
			return nil
		})
		if err != nil {
			return tl, err
		}
		rows := set.list()
		if len(rows) >= n || start == 0 {
			if len(rows) > n {
				rows = rows[len(rows)-n:]
			}
			if src.Harness == "claude" {
				rows = withClaudeSidechains(ctx, src.Path, rows)
			}
			anchor, err := anchorAt(f, end)
			if err != nil {
				return tl, err
			}
			tl.Turns = rows
			tl.ThroughCursor = encodeRowsCursor(rowsCursor{P: f.Name(), O: end, A: anchor, S: p.st})
			return tl, nil
		}
	}
}

// ClaudeSidechainPath is where Claude Code writes a sub-agent's transcript:
// <project>/<session>/subagents/agent-<id>.jsonl next to <session>.jsonl.
func ClaudeSidechainPath(transcript, agentID string) string {
	dir := strings.TrimSuffix(transcript, filepath.Ext(transcript))
	return filepath.Join(dir, "subagents", "agent-"+filepath.Base(agentID)+".jsonl")
}

// claudeSidechainRows reads one sub-agent transcript. Child ids are
// prefixed with the agent id so they never collide with the parent's.
func claudeSidechainRows(ctx context.Context, transcript, agentID string) ([]Row, error) {
	f, err := os.Open(ClaudeSidechainPath(transcript, agentID))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	p := newRowParser("claude", rowParserState{})
	set := newRowSet()
	if _, err := scanLines(ctx, f, 0, info.Size(), func(line []byte, _ int64) error {
		for _, fr := range p.line(line) {
			set.apply(fr)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	rows := set.list()
	for i := range rows {
		rows[i].ID = "sub:" + agentID + ":" + rows[i].ID
	}
	return rows, nil
}

func subagentID(r Row) string {
	id, _ := r.Meta["agent_id"].(string)
	return id
}

func withClaudeSidechains(ctx context.Context, transcript string, rows []Row) []Row {
	for i := range rows {
		// agent_id is only ever set on a subagent row (or its update).
		if subagentID(rows[i]) != "" && rows[i].Meta["status"] != "async_launched" {
			if children, err := claudeSidechainRows(ctx, transcript, subagentID(rows[i])); err == nil && len(children) > 0 {
				rows[i].Children = children
			}
		}
	}
	return rows
}

// FollowRows streams frames after cursor `after`, polling the file every
// poll interval (200 ms when zero). status, when set, is sampled once a
// second: a status frame goes out every second while the session runs and
// once more when it stops running.
//
// extra, when set, is called on every poll and its frames are emitted as
// they are (delivery frames of queued sends).
func FollowRows(ctx context.Context, src RowsSource, after string, poll time.Duration, status func() *LiveStatus, extra func() []RowFrame, emit func(RowFrame) error) error {
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	resync := func(reason string) error { return emit(RowFrame{Frame: "resync_required", Reason: reason}) }
	c, err := decodeRowsCursor(after)
	if err != nil {
		return resync("invalid_cursor")
	}
	if src.Path != "" && c.P != src.Path {
		return resync("source_moved")
	}
	f, opened, err := openAtCursor(c)
	if err != nil {
		var re *ResyncError
		if errors.As(err, &re) {
			return resync(re.Reason)
		}
		return err
	}
	defer f.Close()
	p := newRowParser(src.Harness, c.S)
	off := c.O
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var lastStatus []byte
	var lastStatusAt, lastResolveAt time.Time
	for {
		if info, err := os.Stat(c.P); err != nil || !os.SameFile(info, opened) {
			return resync("source_replaced")
		}
		size := int64(0)
		if info, err := f.Stat(); err == nil {
			size = info.Size()
		}
		if size < off {
			return resync("source_shortened")
		}
		if size > off {
			off, err = scanLines(ctx, f, off, size, func(line []byte, end int64) error {
				frames := p.line(line)
				if len(frames) == 0 {
					return nil
				}
				anchor, err := anchorAt(f, end)
				if err != nil {
					return err
				}
				for i := range frames {
					fr := frames[i]
					if fr.Frame == "update" && src.Harness == "claude" && subagentID(*fr.Row) != "" && fr.Row.Meta["status"] != "async_launched" {
						if children, err := claudeSidechainRows(ctx, c.P, subagentID(*fr.Row)); err == nil && len(children) > 0 {
							fr.Row.Children = children
						}
					}
					if i == len(frames)-1 {
						fr.Cursor = encodeRowsCursor(rowsCursor{P: c.P, O: end, A: anchor, S: cloneState(p.st)})
					}
					if err := emit(fr); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if extra != nil {
			for _, fr := range extra() {
				if err := emit(fr); err != nil {
					return err
				}
			}
		}
		now := time.Now()
		if status != nil && now.Sub(lastStatusAt) >= time.Second {
			lastStatusAt = now
			if s := status(); s != nil {
				b, _ := json.Marshal(s)
				if s.Running || !bytes.Equal(b, lastStatus) {
					lastStatus = b
					if err := emit(RowFrame{Frame: "status", LiveStatus: s}); err != nil {
						return err
					}
				}
			}
		}
		if src.Resolve != nil && now.Sub(lastResolveAt) >= 2*time.Second {
			lastResolveAt = now
			if live, err := src.Resolve(); err == nil && live != "" && live != c.P {
				return resync("source_moved")
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func cloneState(s rowParserState) rowParserState {
	out := rowParserState{Model: s.Model, Recent: append([]string(nil), s.Recent...)}
	if len(s.Pending) > 0 {
		out.Pending = make(map[string]string, len(s.Pending))
		for k, v := range s.Pending {
			out.Pending[k] = v
		}
	}
	return out
}
