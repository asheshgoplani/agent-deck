package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/recall"
)

// Gemini reads Gemini CLI chats: <home>/tmp/<projectHash>/chats/
// session-*.json, one JSON document per session {sessionId, projectHash,
// startTime, lastUpdated, kind?, summary?, messages:[...]}. The document is
// rewritten on every turn, so it is not tailable (CursorNone: a change is a
// full reparse) and it is streamed, never unmarshalled whole: json.Decoder
// walks the top-level object and the messages array with Token()/More(),
// so one message is resident at a time and InputOffset() gives each
// message's real byte span. The worst case on the design machine is a
// 31,636,072 B file whose largest message is 10.5 MB; a message above
// MaxLineBytes is skipped and counted like an over-long JSONL line, which
// bounds the resident set at about 2x MaxLineBytes.
type Gemini struct{}

// HarnessGemini is the harness name stored on every Gemini row.
const HarnessGemini = "gemini"

func (Gemini) Harness() string { return HarnessGemini }

// Cursor: rewritten wholesale, reparsed whole.
func (Gemini) Cursor() CursorKind { return CursorNone }

// geminiLayout: <home>/tmp/<projectHash>/chats/session-*.json.
var geminiLayout = layout{
	harness: HarnessGemini,
	subdirs: []string{"tmp"},
	keep:    isGeminiChat,
	ident: func(ref *SourceRef, _ string) bool {
		if filepath.Base(filepath.Dir(ref.Path)) != "chats" {
			return false
		}
		ref.NativeID = strings.TrimSuffix(filepath.Base(ref.Path), ".json")
		return true
	},
}

// Discover walks tmp/<hash>/chats/ under every Gemini home.
func (Gemini) Discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error {
	return geminiLayout.discover(ctx, roots, emit)
}

func isGeminiChat(name string) bool {
	return strings.HasPrefix(name, "session-") && filepath.Ext(name) == ".json"
}

type geminiMessage struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	Model     string          `json:"model"`
	Tokens    struct {
		Input  int64 `json:"input"`
		Output int64 `json:"output"`
		Cached int64 `json:"cached"`
	} `json:"tokens"`
	ToolCalls []struct {
		Name      string          `json:"name"`
		Args      json.RawMessage `json:"args"`
		Status    string          `json:"status"`
		Timestamp string          `json:"timestamp"`
	} `json:"toolCalls"`
}

// Ingest streams the whole document (from is ignored: CursorNone) and
// returns the file size as the cursor.
func (Gemini) Ingest(ctx context.Context, src SourceRef, from int64, sink Sink, b *Budget) (int64, error) {
	f, err := fsOpen(src.Path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := newJSONScanner(f)
	st := &geminiState{emitter: newEmitter(src, sink)}
	if err := sc.expect('{'); err != nil {
		return 0, err
	}
	for {
		next, err := sc.skipWS()
		if err != nil {
			return 0, err
		}
		if next == '}' {
			break
		}
		rawKey, _, _, tooLong, err := sc.value(1 << 10)
		if err != nil {
			return 0, err
		}
		var key string
		if tooLong || json.Unmarshal(rawKey, &key) != nil {
			return 0, errJSONShape
		}
		if err := sc.expect(':'); err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if key == "messages" {
			if err := st.messages(ctx, sc, b); err != nil {
				return 0, err
			}
		} else {
			raw, _, _, tooLong, err := sc.value(MaxLineBytes)
			if err != nil {
				return 0, err
			}
			var v string
			if !tooLong && json.Unmarshal(raw, &v) == nil {
				st.top(key, v)
			}
		}
		if d, err := sc.delim(',', '}'); err != nil {
			return 0, err
		} else if d == '}' {
			break
		}
	}
	st.finish()
	return src.Size, nil
}

type geminiState struct {
	emitter
	title string
}

// session emits the Session record once. Gemini records only a hash of
// the working directory; the cwd column stays empty and project filters
// cannot match a Gemini session.
func (st *geminiState) session(native string) {
	st.emitter.session(Session{NativeID: native})
}

// top keeps the top-level keys that matter; startTime and lastUpdated
// add nothing the messages' own timestamps do not.
func (st *geminiState) top(key, v string) {
	switch key {
	case "sessionId":
		st.session(v)
	case "summary":
		st.title = v
	}
}

// finish emits what only the trailing keys could tell.
func (st *geminiState) finish() {
	st.session("")
	if st.title != "" {
		st.sink.Session(Session{Title: st.title, TitleSrc: "summary"})
	}
}

// messages walks the messages array one element at a time.
func (st *geminiState) messages(ctx context.Context, sc *jsonScanner, b *Budget) error {
	if err := sc.expect('['); err != nil {
		return err
	}
	for i := 0; ; i++ {
		if i&15 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if b.Expired() {
				return ErrBudget
			}
		}
		next, err := sc.skipWS()
		if err != nil {
			return err
		}
		if next == ']' {
			_, err := sc.readByte()
			return err
		}
		raw, start, n, tooLong, err := sc.value(MaxLineBytes)
		if err != nil {
			return err
		}
		if !b.Consume(n) {
			return ErrBudget
		}
		if tooLong {
			st.sink.Count(CountLineTooLong, 1)
		} else {
			st.message(raw, start, n)
		}
		if d, err := sc.delim(',', ']'); err != nil {
			return err
		} else if d == ']' {
			return nil
		}
	}
}

func (st *geminiState) message(raw json.RawMessage, off, n int64) {
	var m geminiMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		st.sink.Count(CountBadJSON, 1)
		return
	}
	st.session("")
	ts := parseTS(m.Timestamp)
	switch m.Type {
	case "info":
		if strings.HasPrefix(strings.TrimSpace(geminiText(m.Content)), "Request cancelled") {
			st.sink.Count(CountInterrupt, 1)
		}
		return
	case "error":
		st.sink.Count(CountAPIError, 1)
		return
	case "user", "gemini":
	default:
		st.sink.Count(CountUnknownType, 1)
		return
	}
	msg := Msg{Role: recall.RoleUser, TS: unixOrZero(ts), RecOff: off, RecLen: n, UUID: m.ID, Text: geminiText(m.Content)}
	if m.Type == "gemini" {
		msg.Role = recall.RoleAssistant
		st.setModel(m.Model)
		if m.Tokens.Input+m.Tokens.Output > 0 {
			_ = st.sink.Usage(Usage{UUID: m.ID, TS: ts, Model: m.Model, In: m.Tokens.Input, Out: m.Tokens.Output, CacheR: m.Tokens.Cached})
		}
		for _, tc := range m.ToolCalls {
			msg.ToolNames = append(msg.ToolNames, tc.Name)
			digest, touches := digestArgs(tc.Name, geminiArgs(tc.Args))
			pc := pendingCall{name: tc.Name, ts: ts, digest: digest, touches: touches}
			_ = st.sink.ToolCall(pc.call(parseTS(tc.Timestamp), tc.Status == "error"))
		}
	}
	if strings.TrimSpace(msg.Text) == "" {
		return
	}
	_ = st.sink.Msg(msg)
}

// geminiText decodes content: a string, or a list of {text} parts.
func geminiText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		joinText(&sb, p.Text)
	}
	return sb.String()
}

// geminiArgs normalises toolCalls[].args, which is a JSON object in most
// files and a Python-repr string in some, into JSON for digestArgs.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return raw
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	s = strings.ReplaceAll(s, `'`, `"`)
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return nil
}
