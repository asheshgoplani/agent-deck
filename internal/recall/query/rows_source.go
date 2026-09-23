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

// RowsSource is a native transcript read directly, with no Recall index:
// the session's Claude Code JSONL or Codex rollout. Resolve, when set,
// re-resolves the live path (Codex re-creates its rollout after the trust
// prompt); follow emits resync_required when the answer changes.
type RowsSource struct {
	Harness string
	Path    string
	Resolve func() (string, error)
}

// RowsSession identifies the conversation a rows timeline belongs to.
type RowsSession struct {
	ID       string `json:"id,omitempty"`
	Title    string `json:"title,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Harness  string `json:"harness"`
	Path     string `json:"path,omitempty"`
	NativeID string `json:"native_id,omitempty"`
}

// RowsTimeline is the `recall timeline --rows` result.
type RowsTimeline struct {
	Schema        string      `json:"schema"`
	Session       RowsSession `json:"session"`
	Source        string      `json:"source"`
	Rows          []Row       `json:"rows"`
	ThroughCursor string      `json:"through_cursor,omitempty"`
	Status        *LiveStatus `json:"status,omitempty"`
}

// RowsOptions bounds a timeline read. TailBytes > 0 starts at the first line
// boundary that many bytes before the end, for a fast first paint; results
// for calls before the window are then dropped. AgentID selects one Claude
// sub-agent sidechain instead of the main transcript.
type RowsOptions struct {
	TailBytes int64
	AgentID   string
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

// anchorAt hashes the bytes just before off, so a resumed follow detects a
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

// rowSet applies frames in order: rows append, updates merge by id,
// removes drop. It is what a client does with a follow stream.
type rowSet struct {
	rows []Row
	idx  map[string]int
}

func newRowSet() *rowSet { return &rowSet{idx: map[string]int{}} }

func (s *rowSet) apply(f RowFrame) {
	switch f.Type {
	case "row":
		if i, ok := s.idx[f.Row.ID]; ok {
			s.rows[i] = *f.Row
			return
		}
		s.idx[f.Row.ID] = len(s.rows)
		s.rows = append(s.rows, *f.Row)
	case "update":
		if i, ok := s.idx[f.Row.ID]; ok {
			MergeRow(&s.rows[i], *f.Row)
		}
	case "remove":
		if i, ok := s.idx[f.ID]; ok {
			s.rows[i].ID = ""
			delete(s.idx, f.ID)
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

// MergeRow copies every non-empty field of patch into dst (update frames).
func MergeRow(dst *Row, patch Row) {
	set := func(d *string, v string) {
		if v != "" {
			*d = v
		}
	}
	set(&dst.Kind, patch.Kind)
	set(&dst.Timestamp, patch.Timestamp)
	set(&dst.Title, patch.Title)
	set(&dst.Text, patch.Text)
	set(&dst.ToolName, patch.ToolName)
	set(&dst.Command, patch.Command)
	set(&dst.Path, patch.Path)
	set(&dst.Delivery, patch.Delivery)
	set(&dst.ParentID, patch.ParentID)
	set(&dst.AgentID, patch.AgentID)
	set(&dst.Status, patch.Status)
	if len(patch.Input) > 0 {
		dst.Input = patch.Input
	}
	if patch.Result != nil {
		dst.Result = patch.Result
	}
	if patch.DurationMs != 0 {
		dst.DurationMs = patch.DurationMs
	}
	if patch.Images != 0 {
		dst.Images = patch.Images
	}
	if patch.Tokens != nil {
		dst.Tokens = patch.Tokens
	}
	if len(patch.Raw) > 0 {
		dst.Raw = patch.Raw
	}
}

// scanLines feeds every complete line of f in [from, to) to fn with the
// offset just past that line. A torn final line is left for the next read.
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
			return off, err
		}
	}
}

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
// cursor for FollowRows. It never touches the Recall index.
func ReadRows(ctx context.Context, src RowsSource, opts RowsOptions) ([]Row, string, error) {
	if !SupportsDirectRows(src.Harness) {
		return nil, "", fmt.Errorf("%w: %s", ErrRowsUnsupported, src.Harness)
	}
	path := src.Path
	if opts.AgentID != "" {
		if src.Harness != "claude" {
			return nil, "", fmt.Errorf("recall rows: --agent is supported for Claude Code sidechains only")
		}
		path = ClaudeSidechainPath(src.Path, opts.AgentID)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	p := newRowParser(src.Harness, rowParserState{})
	set := newRowSet()
	start := tailStart(f, info.Size(), opts.TailBytes)
	end, err := scanLines(ctx, f, start, info.Size(), func(line []byte, _ int64) error {
		for _, fr := range p.line(line) {
			set.apply(fr)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	rows := set.list()
	if src.Harness == "claude" && opts.AgentID == "" {
		rows = withClaudeSidechains(ctx, src.Path, rows)
	}
	if opts.AgentID != "" {
		for i := range rows {
			rows[i].ID = "sub:" + opts.AgentID + ":" + rows[i].ID
			rows[i].ParentID = "agent:" + opts.AgentID
		}
		return rows, "", nil
	}
	anchor, err := anchorAt(f, end)
	if err != nil {
		return nil, "", err
	}
	return rows, encodeRowsCursor(rowsCursor{P: path, O: end, A: anchor, S: p.st}), nil
}

// ClaudeSidechainPath is where Claude Code writes a sub-agent's transcript:
// <project>/<session>/subagents/agent-<id>.jsonl next to <session>.jsonl.
func ClaudeSidechainPath(transcript, agentID string) string {
	dir := strings.TrimSuffix(transcript, filepath.Ext(transcript))
	return filepath.Join(dir, "subagents", "agent-"+filepath.Base(agentID)+".jsonl")
}

// claudeSidechainRows reads one sub-agent transcript as children of the
// subagent row parentID. A missing file yields no rows.
func claudeSidechainRows(ctx context.Context, transcript, agentID, parentID string) []Row {
	rows, _, err := ReadRows(ctx, RowsSource{Harness: "claude", Path: transcript}, RowsOptions{AgentID: agentID})
	if err != nil {
		return nil
	}
	for i := range rows {
		rows[i].ParentID = parentID
	}
	return rows
}

func withClaudeSidechains(ctx context.Context, transcript string, rows []Row) []Row {
	var out []Row
	for _, r := range rows {
		out = append(out, r)
		if r.Kind == "subagent" && r.AgentID != "" {
			out = append(out, claudeSidechainRows(ctx, transcript, r.AgentID, r.ID)...)
		}
	}
	return out
}

// FollowRows streams frames after cursor `after`, polling the file every
// poll interval (200 ms when zero). status, when set, is sampled once a
// second and emitted as a status frame whenever it changes.
func FollowRows(ctx context.Context, src RowsSource, after string, poll time.Duration, status func() *LiveStatus, emit func(RowFrame) error) error {
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	c, err := decodeRowsCursor(after)
	if err != nil {
		return emit(RowFrame{Type: "resync_required", Reason: "invalid_cursor"})
	}
	if src.Path != "" && c.P != src.Path {
		return emit(RowFrame{Type: "resync_required", Reason: "source_moved"})
	}
	f, err := os.Open(c.P)
	if err != nil {
		return emit(RowFrame{Type: "resync_required", Reason: "source_missing"})
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if opened.Size() < c.O {
		return emit(RowFrame{Type: "resync_required", Reason: "source_shortened"})
	}
	if a, err := anchorAt(f, c.O); err != nil || a != c.A {
		return emit(RowFrame{Type: "resync_required", Reason: "source_rewritten"})
	}
	p := newRowParser(src.Harness, c.S)
	off := c.O
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var lastStatus []byte
	var lastStatusAt, lastResolveAt time.Time
	for {
		if info, err := os.Stat(c.P); err != nil || !os.SameFile(info, opened) {
			return emit(RowFrame{Type: "resync_required", Reason: "source_replaced"})
		}
		size := int64(0)
		if info, err := f.Stat(); err == nil {
			size = info.Size()
		}
		if size < off {
			return emit(RowFrame{Type: "resync_required", Reason: "source_shortened"})
		}
		if size > off {
			var pending []RowFrame
			var emitErr error
			off, err = scanLines(ctx, f, off, size, func(line []byte, end int64) error {
				frames := p.line(line)
				if len(frames) == 0 {
					return nil
				}
				anchor, err := anchorAt(f, end)
				if err != nil {
					return err
				}
				frames[len(frames)-1].Cursor = encodeRowsCursor(rowsCursor{P: c.P, O: end, A: anchor, S: cloneState(p.st)})
				for _, fr := range frames {
					pending = append(pending, fr)
					if fr.Type == "update" && fr.Row != nil && fr.Row.AgentID != "" && src.Harness == "claude" {
						for _, child := range claudeSidechainRows(ctx, c.P, fr.Row.AgentID, fr.Row.ID) {
							child := child
							pending = append(pending, RowFrame{Type: "row", Row: &child})
						}
					}
				}
				for _, fr := range pending {
					if emitErr = emit(fr); emitErr != nil {
						return emitErr
					}
				}
				pending = pending[:0]
				return nil
			})
			if err != nil {
				return err
			}
		}
		now := time.Now()
		if status != nil && now.Sub(lastStatusAt) >= time.Second {
			lastStatusAt = now
			if s := status(); s != nil {
				b, _ := json.Marshal(s)
				if !bytes.Equal(b, lastStatus) {
					lastStatus = b
					if err := emit(RowFrame{Type: "status", Status: s}); err != nil {
						return err
					}
				}
			}
		}
		if src.Resolve != nil && now.Sub(lastResolveAt) >= 2*time.Second {
			lastResolveAt = now
			if live, err := src.Resolve(); err == nil && live != "" && live != c.P {
				return emit(RowFrame{Type: "resync_required", Reason: "source_moved"})
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
