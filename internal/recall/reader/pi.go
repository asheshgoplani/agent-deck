package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/classify"
)

// Pi reads pi coding-agent sessions: <home>/agent/sessions/<enc-cwd>/
// <ts>_<uuid>.jsonl and agent-deck's per-instance <home>/agent-deck/
// <instance>/<ts>_<uuid>.jsonl. Version-3 records: a session header, then
// message / model_change / thinking_level_change / session_info /
// compaction / custom_message records chained by id and parentId.
// Append-only, so byte-tailed. The cheapest harness: files average 28 KB.
type Pi struct{}

// HarnessPi is the harness name stored on every pi row.
const HarnessPi = "pi"

var piSessionRE = regexp.MustCompile(`^.*_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

func (Pi) Harness() string { return HarnessPi }

// piLayout: <home>/agent/sessions/**/<ts>_<uuid>.jsonl and
// <home>/agent-deck/**/<ts>_<uuid>.jsonl.
var piLayout = layout{
	harness: HarnessPi,
	subdirs: []string{filepath.Join("agent", "sessions"), "agent-deck"},
	keep:    isPiSession,
	ident: func(ref *SourceRef, _ string) bool {
		ref.NativeID = piNativeID(ref.Path)
		return true
	},
}

// Discover walks agent/sessions and agent-deck under every pi home.
func (Pi) Discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error {
	return piLayout.discover(ctx, roots, emit)
}

// Locate builds the SourceRef of one pi session file under a pi home.
func (Pi) Locate(path string, roots []Root) (SourceRef, bool) {
	return piLayout.locate(path, roots)
}

func isPiSession(name string) bool { return piSessionRE.MatchString(name) }

// piNativeID is the uuid in a pi session filename, or the bare filename.
func piNativeID(path string) string {
	base := filepath.Base(path)
	if m := piSessionRE.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	return strings.TrimSuffix(base, ".jsonl")
}

// Ingest tails src from byte offset from.
func (Pi) Ingest(ctx context.Context, src SourceRef, from int64, sink Sink, b *Budget) (int64, error) {
	st := &piState{newEmitter(src, sink)}
	return st.scan(ctx, from, 0, b, st.line)
}

type piRecord struct {
	Type          string          `json:"type"`
	ID            string          `json:"id"`
	Timestamp     string          `json:"timestamp"`
	CWD           string          `json:"cwd"`
	ParentSession string          `json:"parentSession"`
	Version       json.RawMessage `json:"version"`
	ModelID       string          `json:"modelId"`
	Name          string          `json:"name"`
	Summary       string          `json:"summary"`
	Message       piMessage       `json:"message"`
}

type piMessage struct {
	Role     string          `json:"role"`
	Content  json.RawMessage `json:"content"`
	Model    string          `json:"model"`
	IsError  bool            `json:"isError"`
	ToolName string          `json:"toolName"`
	CallID   string          `json:"toolCallId"`
	RespID   string          `json:"responseId"`
	Usage    struct {
		Input      int64 `json:"input"`
		Output     int64 `json:"output"`
		CacheRead  int64 `json:"cacheRead"`
		CacheWrite int64 `json:"cacheWrite"`
	} `json:"usage"`
}

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type piState struct{ emitter }

func (st *piState) line(line []byte, off, n int64) {
	var rec piRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		st.sink.Count(CountBadJSON, 1)
		return
	}
	ts := parseTS(rec.Timestamp)
	switch rec.Type {
	case "session":
		forkOf := ""
		if rec.ParentSession != "" {
			forkOf = piNativeID(rec.ParentSession)
		}
		st.session(Session{NativeID: rec.ID, CWD: rec.CWD, ForkOf: forkOf})
	case "model_change":
		st.setModel(rec.ModelID)
	case "session_info":
		if rec.Name != "" {
			st.sink.Session(Session{Title: rec.Name, TitleSrc: "session_info"})
		}
	case "compaction":
		st.sink.Count(CountCompact, 1)
		if strings.TrimSpace(rec.Summary) != "" {
			st.session(Session{})
			st.fail(st.sink.Msg(Msg{Role: recall.RoleUser, TS: unixOrZero(ts), RecOff: off, RecLen: n, Text: rec.Summary, IsCompact: true}))
		}
	case "message":
		st.message(&rec, ts, off, n)
	case "thinking_level_change", "custom_message":
		// Settings and harness-injected notices: no conversation text.
	default:
		st.sink.Count(CountUnknownType, 1)
	}
}

func (st *piState) message(rec *piRecord, ts time.Time, off, n int64) {
	m := rec.Message
	switch m.Role {
	case "toolResult":
		st.session(Session{})
		st.closeCall(m.CallID, pendingCall{name: m.ToolName, ts: ts}, ts, m.IsError)
		return
	case "user", "assistant":
	default:
		return
	}
	st.session(Session{})
	msg := Msg{TS: unixOrZero(ts), RecOff: off, RecLen: n}
	if m.Role == "assistant" {
		msg.Role = recall.RoleAssistant
		st.setModel(m.Model)
		if m.Usage.Input+m.Usage.Output > 0 {
			st.fail(st.sink.Usage(Usage{UUID: firstNonEmpty(m.RespID, rec.ID), TS: ts, Model: firstNonEmpty(m.Model, st.model),
				In: m.Usage.Input, Out: m.Usage.Output, CacheR: m.Usage.CacheRead, CacheW: m.Usage.CacheWrite}))
		}
	} else {
		msg.Role = recall.RoleUser
	}
	msg.Text = st.content(m.Content, ts, &msg)
	if msg.Text == "" {
		return
	}
	if msg.Role == recall.RoleUser {
		msg.IsInterrupt = classify.IsInterrupt(msg.Text)
	}
	st.fail(st.sink.Msg(msg))
}

// content joins text blocks; toolCall blocks open pending calls.
func (st *piState) content(raw json.RawMessage, ts time.Time, m *Msg) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		return s
	}
	var blocks []piBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var sb strings.Builder
	for i := range blocks {
		blk := &blocks[i]
		switch blk.Type {
		case "text":
			joinText(&sb, blk.Text)
		case "toolCall":
			m.ToolNames = append(m.ToolNames, blk.Name)
			pc := pendingCall{name: blk.Name, ts: ts}
			pc.digest, pc.touches = digestArgs(blk.Name, blk.Arguments)
			st.openCall(blk.ID, pc)
		}
	}
	return sb.String()
}
