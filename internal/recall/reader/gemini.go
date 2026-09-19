package reader

import (
	"context"
	"encoding/json"
	"os"
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

// Discover walks tmp/<hash>/chats/ under every Gemini home.
func (Gemini) Discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error {
	seenRoot := map[string]bool{}
	seen := dedup{}
	for _, r := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		home, err := fsEvalSymlinks(r.Dir)
		if err != nil || seenRoot[home] {
			continue
		}
		seenRoot[home] = true
		err = walkFiles(ctx, filepath.Join(home, "tmp"), nil, isGeminiChat, func(path string, info os.FileInfo) error {
			if filepath.Base(filepath.Dir(path)) != "chats" {
				return nil
			}
			ref := fileRef(HarnessGemini, r, path, info)
			if seen.seen(ref.Dev, ref.Ino) {
				return nil
			}
			ref.NativeID = strings.TrimSuffix(filepath.Base(path), ".json")
			return emit(ref)
		})
		if err != nil {
			return err
		}
	}
	return nil
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
	st := &geminiState{src: src, sink: sink}
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
	src         SourceRef
	sink        Sink
	sessionSent bool
	model       string
	title       string
	started     int64
	ended       int64
}

func (st *geminiState) session(native string) {
	if st.sessionSent {
		return
	}
	st.sessionSent = true
	if native == "" {
		native = st.src.NativeID
	}
	// Gemini records only a hash of the working directory; the cwd column
	// stays empty and project filters cannot match a Gemini session.
	st.sink.Session(Session{NativeID: native})
}

func (st *geminiState) top(key, v string) {
	switch key {
	case "sessionId":
		st.session(v)
	case "summary":
		st.title = v
	case "startTime":
		st.started = unixOrZero(parseTS(v))
	case "lastUpdated":
		st.ended = unixOrZero(parseTS(v))
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
		if m.Model != "" && m.Model != st.model {
			st.model = m.Model
			st.sink.Session(Session{Model: m.Model})
		}
		if m.Tokens.Input+m.Tokens.Output > 0 {
			_ = st.sink.Usage(Usage{UUID: m.ID, TS: ts, Model: m.Model, In: m.Tokens.Input, Out: m.Tokens.Output, CacheR: m.Tokens.Cached})
		}
		for _, tc := range m.ToolCalls {
			msg.ToolNames = append(msg.ToolNames, tc.Name)
			digest, touches := digestArgs(tc.Name, geminiArgs(tc.Args))
			call := ToolCall{Name: tc.Name, TS: unixOrZero(ts), IsError: tc.Status == "error", ArgDigest: digest, Touches: touches}
			if end := parseTS(tc.Timestamp); !end.IsZero() && !ts.IsZero() && end.After(ts) {
				call.DurationMS = end.Sub(ts).Milliseconds()
			}
			_ = st.sink.ToolCall(call)
		}
	}
	if strings.TrimSpace(msg.Text) == "" {
		return
	}
	_ = st.sink.Msg(msg)
}

// geminiText decodes content: a string, or a list of {text} parts.
func geminiText(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
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
		if p.Text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(p.Text)
	}
	return sb.String()
}

// geminiArgs normalises toolCalls[].args, which is a JSON object in most
// files and a Python-repr string in some, into JSON for digestArgs.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
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
