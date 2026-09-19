package reader

import (
	"bytes"
	"context"
	"encoding/json"
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

// claudeLayout: <cfgdir>/projects/**/*.jsonl, deduplicated at the projects
// level (the worker-scratch symlinks) so a tree reachable through many
// roots is walked once.
var claudeLayout = layout{
	harness: HarnessClaude,
	base:    "projects",
	skip:    skipDirs,
	keep:    isJSONL,
	ident: func(ref *SourceRef, _ string) bool {
		claudeIdentity(ref)
		return true
	},
}

// Discover walks each root's projects/ tree once.
func (Claude) Discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error {
	return claudeLayout.discover(ctx, roots, emit)
}

// Locate builds the SourceRef of one transcript under a root's projects/
// tree (the Stop hook's path). tool-results/ and workflows/ are not
// transcripts.
func (Claude) Locate(path string, roots []Root) (SourceRef, bool) {
	return claudeLayout.locate(path, roots)
}

// claudeIdentity derives the native id from the path: the file's uuid,
// prefixed by the parent session's for a subagents/ transcript.
func claudeIdentity(ref *SourceRef) {
	ref.NativeID = strings.TrimSuffix(filepath.Base(ref.Path), ".jsonl")
	if parent := filepath.Dir(ref.Path); filepath.Base(parent) == "subagents" {
		ref.IsSidechain = true
		ref.ParentNativeID = filepath.Base(filepath.Dir(parent))
		ref.NativeID = ref.ParentNativeID + "/" + ref.NativeID
	}
}

func isJSONL(name string) bool { return filepath.Ext(name) == ".jsonl" }

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

var interruptedTrue = []byte(`"interrupted":true`)

// Ingest streams src from byte offset from. It stops at a torn trailing
// line (the cursor never covers it), skips over-long lines whole, decodes
// only the record types that carry text or structure, and never holds more
// than one record in memory.
func (Claude) Ingest(ctx context.Context, src SourceRef, from int64, sink Sink, b *Budget) (int64, error) {
	st := &claudeState{emitter: newEmitter(src, sink)}
	return st.scan(ctx, from, 0, b, st.line)
}

type claudeState struct {
	emitter
	titleSent bool
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
	native := ""
	if !st.src.IsSidechain {
		native = rec.SessionID
	}
	st.session(Session{NativeID: native, CWD: rec.CWD, Branch: rec.GitBranch, Version: rec.Version})
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
		st.setModel(rec.Message.Model)
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
			joinText(&sb, blk.Text)
			if blk.Text != "" && classify.IsInterrupt(blk.Text) {
				m.IsInterrupt = true
			}
		case "tool_use":
			m.ToolNames = append(m.ToolNames, blk.Name)
			pc := pendingCall{name: blk.Name, ts: ts}
			pc.digest, pc.touches = digestArgs(blk.Name, blk.Input)
			st.openCall(blk.ID, pc)
		case "tool_result":
			m.IsToolResult = true
			if blk.IsError {
				m.IsError = true
			}
			st.closeCall(blk.ToolUseID, pendingCall{ts: ts}, ts, blk.IsError)
		}
	}
	return sb.String()
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
