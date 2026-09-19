package reader

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/classify"
)

// Claude reads Claude Code transcripts: <cfgdir>/projects/<slug>/<uuid>.jsonl
// plus per-session <uuid>/subagents/agent-*.jsonl. tool-results/ and
// workflows/ are never opened. Ported from the Python prototype's reader
// (plans: proto/recall_proto.py) with the shapes observed on real files.
type Claude struct{}

// HarnessClaude is the harness name stored on every Claude row.
const HarnessClaude = "claude"

// MaxLineBytes caps one JSONL record; longer lines are skipped whole and
// counted, so a pathological record cannot grow the resident set.
const MaxLineBytes = 4 << 20

// ArgDigestChars is the tool_use argument preview kept per call (distill.py's
// ARG_PREVIEW_CHARS).
const ArgDigestChars = 200

func (Claude) Harness() string { return HarnessClaude }

// skipDirs are never descended into.
var skipDirs = map[string]bool{"tool-results": true, "workflows": true}

// Discover walks each root's projects/ tree once. Roots whose projects dir
// resolves to one already walked (the worker-scratch symlinks) are skipped
// at the directory level, and files are deduplicated by (dev, ino), so a
// transcript reachable through many paths is one source.
func (Claude) Discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error {
	seenRoot := map[string]bool{}
	seenFile := map[[2]uint64]bool{}
	for _, r := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		real, err := fsEvalSymlinks(filepath.Join(r.Dir, "projects"))
		if err != nil {
			continue // no projects dir: nothing to index
		}
		if seenRoot[real] {
			continue
		}
		seenRoot[real] = true
		if err := walkClaudeProjects(ctx, real, r, seenFile, emit); err != nil {
			return err
		}
	}
	return nil
}

func walkClaudeProjects(ctx context.Context, dir string, r Root, seen map[[2]uint64]bool, emit func(SourceRef) error) error {
	entries, err := fsReadDir(dir)
	if err != nil {
		return nil // vanished or unreadable: skip, the next sweep sees it
	}
	for _, d := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := d.Name()
		path := filepath.Join(dir, name)
		switch {
		case d.IsDir():
			if skipDirs[name] {
				continue
			}
			if err := walkClaudeProjects(ctx, path, r, seen, emit); err != nil {
				return err
			}
		case filepath.Ext(name) == ".jsonl":
			path, info, ok := regularFile(d, path)
			if !ok {
				continue
			}
			dev, ino := FileIdentity(info)
			key := [2]uint64{dev, ino}
			if ino != 0 {
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			ref := SourceRef{
				Harness:       HarnessClaude,
				Profile:       r.Profile,
				Path:          path,
				Dev:           dev,
				Ino:           ino,
				Size:          info.Size(),
				MtimeNS:       info.ModTime().UnixNano(),
				RetentionDays: r.RetentionDays,
				NativeID:      strings.TrimSuffix(name, ".jsonl"),
			}
			if filepath.Base(dir) == "subagents" {
				ref.IsSidechain = true
				ref.ParentNativeID = filepath.Base(filepath.Dir(dir))
				ref.NativeID = ref.ParentNativeID + "/" + ref.NativeID
			}
			if err := emit(ref); err != nil {
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

// noiseTypes never carry conversation text and are roughly 41% of bytes;
// they are skipped on the raw line before any decode.
var noiseTypes = map[string]bool{
	"attachment": true, "file-history-snapshot": true, "file-history-delta": true,
	"progress": true, "queue-operation": true, "pr-link": true, "permission-mode": true,
	"last-prompt": true, "mode": true, "frame-link": true,
}

var typeKey = []byte(`"type":"`)

// recordType returns the record's top-level "type" value without decoding
// the line: a byte walk that tracks string state and nesting depth, since
// real attachment and progress records put a nested "type" (and any text
// may quote one) before the top-level key. "" when there is none; a miss
// only costs a full decode.
func recordType(line []byte) string {
	depth, inStr := 0, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inStr {
			switch c {
			case '\\':
				i++
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			if depth == 1 && bytes.HasPrefix(line[i:], typeKey) {
				rest := line[i+len(typeKey):]
				if j := bytes.IndexByte(rest, '"'); j >= 0 {
					return string(rest[:j])
				}
				return ""
			}
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return ""
}

type claudeRecord struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	UUID             string          `json:"uuid"`
	SessionID        string          `json:"sessionId"`
	AgentID          string          `json:"agentId"`
	CWD              string          `json:"cwd"`
	GitBranch        string          `json:"gitBranch"`
	Version          string          `json:"version"`
	Timestamp        string          `json:"timestamp"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	Message          claudeMessage   `json:"message"`
	ToolUseResult    json.RawMessage `json:"toolUseResult"`
	CustomTitle      string          `json:"customTitle"`
	AITitle          string          `json:"aiTitle"`
	AgentName        string          `json:"agentName"`
	Summary          string          `json:"summary"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

// toolArgs are the keys worth a digest; anything else falls back to the
// compact JSON prefix.
type toolArgs struct {
	Command      string `json:"command"`
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Path         string `json:"path"`
	Pattern      string `json:"pattern"`
	Prompt       string `json:"prompt"`
	Description  string `json:"description"`
	Query        string `json:"query"`
	URL          string `json:"url"`
	Skill        string `json:"skill"`
}

type pendingCall struct {
	name    string
	ts      time.Time
	digest  string
	touches []FileTouch
}

// Ingest streams src from byte offset from. It stops at a torn trailing
// line (the cursor never covers it), skips over-long lines whole, decodes
// only the record types that carry text or structure, and never holds more
// than one record in memory.
func (Claude) Ingest(ctx context.Context, src SourceRef, from int64, sink Sink, b *Budget) (int64, error) {
	f, err := fsOpen(src.Path)
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
	st := &claudeState{src: src, sink: sink, pending: map[string]pendingCall{}}
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
		if tooLong {
			sink.Count(CountLineTooLong, 1)
		} else {
			st.line(line, off, int64(n))
			if st.err != nil {
				return off, st.err // the sink refused (quarantine): the cursor stays put
			}
		}
		off += int64(n)
		if !b.Consume(int64(n)) {
			st.flush()
			return off, ErrBudget
		}
	}
	st.flush()
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

type claudeState struct {
	src         SourceRef
	sink        Sink
	pending     map[string]pendingCall
	sessionSent bool
	titleSent   bool
	model       string
	err         error // first sink error; stops the pass
}

func (st *claudeState) line(line []byte, off, n int64) {
	typ := recordType(line)
	if noiseTypes[typ] {
		return
	}
	var rec claudeRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		st.sink.Count(CountBadJSON, 1)
		return
	}
	switch rec.Type {
	case "user", "assistant":
		st.message(&rec, off, n)
	case "system":
		switch rec.Subtype {
		case "compact_boundary":
			st.sink.Count(CountCompact, 1)
		case "api_error":
			st.sink.Count(CountAPIError, 1)
		}
	case "summary":
		if rec.Summary != "" && !st.titleSent {
			st.sink.Session(Session{Title: rec.Summary, TitleSrc: "summary"})
		}
	case "custom-title":
		st.title(rec.CustomTitle, "custom-title")
	case "ai-title":
		st.title(rec.AITitle, "ai-title")
	case "agent-name":
		st.title(rec.AgentName, "agent-name")
	default:
		st.sink.Count(CountUnknownType, 1)
	}
}

func (st *claudeState) title(title, src string) {
	if title == "" {
		return
	}
	st.titleSent = true
	st.sink.Session(Session{Title: title, TitleSrc: src})
}

func (st *claudeState) message(rec *claudeRecord, off, n int64) {
	if !st.sessionSent {
		st.sessionSent = true
		native := st.src.NativeID
		if !st.src.IsSidechain && rec.SessionID != "" {
			native = rec.SessionID
		}
		st.sink.Session(Session{NativeID: native, CWD: rec.CWD, Branch: rec.GitBranch, Version: rec.Version})
	}
	ts := parseTS(rec.Timestamp)
	m := Msg{
		TS:        unixOrZero(ts),
		RecOff:    off,
		RecLen:    n,
		UUID:      rec.UUID,
		IsMeta:    rec.IsMeta,
		IsCompact: rec.IsCompactSummary,
	}
	if rec.Type == "assistant" {
		m.Role = recall.RoleAssistant
		if rec.Message.Model != "" && rec.Message.Model != st.model {
			st.model = rec.Message.Model
			st.sink.Session(Session{Model: st.model})
		}
		u := rec.Message.Usage
		if u.InputTokens+u.OutputTokens > 0 {
			st.fail(st.sink.Usage(Usage{UUID: rec.UUID, TS: ts, Model: rec.Message.Model,
				In: u.InputTokens, Out: u.OutputTokens, CacheR: u.CacheReadInputTokens, CacheW: u.CacheCreationInputTokens}))
		}
	} else {
		m.Role = recall.RoleUser
	}
	// One Escape press is one interrupt, carried by the marker message's
	// IsInterrupt flag; the toolUseResult.interrupted record that precedes
	// the marker is the same press and adds nothing.
	m.Text = st.content(rec.Message.Content, ts, &m)
	if m.Text == "" {
		return
	}
	st.fail(st.sink.Msg(m))
}

func (st *claudeState) fail(err error) {
	if err != nil && st.err == nil {
		st.err = err
	}
}

// content decodes a message body: a plain string or an array of typed
// blocks. Text blocks join into the body; tool_use starts a pending call;
// tool_result closes one.
func (st *claudeState) content(raw json.RawMessage, ts time.Time, m *Msg) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		m.IsInterrupt = classify.IsInterrupt(s)
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var sb strings.Builder
	for i := range blocks {
		blk := &blocks[i]
		switch blk.Type {
		case "text":
			if blk.Text == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(blk.Text)
			if classify.IsInterrupt(blk.Text) {
				m.IsInterrupt = true
			}
		case "tool_use":
			m.ToolNames = append(m.ToolNames, blk.Name)
			pc := pendingCall{name: blk.Name, ts: ts}
			pc.digest, pc.touches = digestArgs(blk.Name, blk.Input)
			if blk.ID != "" {
				st.pending[blk.ID] = pc
			} else {
				st.emitCall(pc, ts, false)
			}
		case "tool_result":
			m.IsToolResult = true
			if blk.IsError {
				m.IsError = true
			}
			if pc, ok := st.pending[blk.ToolUseID]; ok {
				delete(st.pending, blk.ToolUseID)
				st.emitCall(pc, ts, blk.IsError)
			} else {
				st.emitCall(pendingCall{name: "", ts: ts}, ts, blk.IsError)
			}
		}
	}
	return sb.String()
}

func (st *claudeState) emitCall(pc pendingCall, resultTS time.Time, isError bool) {
	tc := ToolCall{Name: pc.name, TS: unixOrZero(pc.ts), IsError: isError, ArgDigest: pc.digest, Touches: pc.touches}
	if !pc.ts.IsZero() && !resultTS.IsZero() && resultTS.After(pc.ts) {
		tc.DurationMS = resultTS.Sub(pc.ts).Milliseconds()
	}
	st.fail(st.sink.ToolCall(tc))
}

// flush emits calls whose result never arrived in this pass (the result
// may land in the next append; it is then an unmatched result).
func (st *claudeState) flush() {
	for id, pc := range st.pending {
		delete(st.pending, id)
		st.emitCall(pc, time.Time{}, false)
	}
}

// digestArgs picks the one argument that identifies a call (the command,
// the path, the pattern) and the file touches it implies.
func digestArgs(name string, input json.RawMessage) (string, []FileTouch) {
	if len(input) == 0 {
		return "", nil
	}
	var a toolArgs
	_ = json.Unmarshal(input, &a)
	path := firstNonEmpty(a.FilePath, a.NotebookPath)
	var touches []FileTouch
	if path != "" {
		switch name {
		case "Read":
			touches = []FileTouch{{Path: path, Op: "read"}}
		case "Write":
			touches = []FileTouch{{Path: path, Op: "write"}}
		case "Edit", "MultiEdit", "NotebookEdit":
			touches = []FileTouch{{Path: path, Op: "edit"}}
		}
	}
	digest := firstNonEmpty(a.Command, path, joinNonEmpty(a.Path, a.Pattern), a.Prompt, a.Query, a.URL, a.Skill, a.Description)
	if digest == "" {
		digest = string(bytes.TrimSpace(input))
	}
	return clipRunes(digest, ArgDigestChars), touches
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinNonEmpty(a, b string) string {
	switch {
	case a != "" && b != "":
		return a + " " + b
	case a != "":
		return a
	default:
		return b
	}
}

// clipRunes truncates s to at most n runes, never inside a UTF-8 sequence.
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// unixOrZero is t.Unix(), with 0 (not the year-one epoch) for a missing
// timestamp.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
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

// usageSink keeps only Usage events.
type usageSink struct{ out []Usage }

func (usageSink) Session(Session)         {}
func (usageSink) Msg(Msg) error           { return nil }
func (usageSink) ToolCall(ToolCall) error { return nil }
func (usageSink) Count(Counter, int64)    {}
func (s *usageSink) Usage(u Usage) error  { s.out = append(s.out, u); return nil }

// ScanClaudeUsage returns every assistant usage record in one transcript
// file and in its subagents/ directory, in file order. It is the one parse
// path internal/costs uses, so a transcript is read once per purpose with
// the same decoder recall indexes with.
func ScanClaudeUsage(ctx context.Context, path string) ([]Usage, error) {
	var sink usageSink
	if _, err := (Claude{}).Ingest(ctx, SourceRef{Path: path}, 0, &sink, nil); err != nil {
		return nil, err
	}
	subDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	entries, err := fsReadDir(subDir)
	if err != nil {
		return sink.out, nil
	}
	for _, d := range entries {
		if d.IsDir() || filepath.Ext(d.Name()) != ".jsonl" {
			continue
		}
		sub := filepath.Join(subDir, d.Name())
		if _, err := (Claude{}).Ingest(ctx, SourceRef{Path: sub, IsSidechain: true}, 0, &sink, nil); err != nil {
			return sink.out, err
		}
	}
	return sink.out, nil
}
