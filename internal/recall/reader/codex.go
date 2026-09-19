package reader

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/classify"
)

// Codex reads Codex CLI rollouts: <home>/sessions/YYYY/MM/DD/rollout-*.jsonl
// (and archived_sessions/). Append-only JSONL, so it is byte-tailed like
// Claude, with one addition: Codex keeps its own cursor per thread in
// <home>/thread_history_1.sqlite (thread_history_projection_state
// .next_rollout_byte_offset), the offset up to which Codex has itself
// committed the rollout. A pass stops at min(cursor, size) for a file
// written in the last projectionGrace, so a turn still being projected is
// not indexed half way; a file older than that is tailed to its size in
// case the projection stopped following it (Codex crashed mid-turn). If
// the database, its table or the filename shape changes, the reader falls
// back to plain byte tailing.
type Codex struct{}

// HarnessCodex is the harness name stored on every Codex row.
const HarnessCodex = "codex"

// projectionGrace is how recently a rollout must have been written for
// Codex's projection cursor to bound the pass.
const projectionGrace = 10 * time.Minute

// codexProjectionDB is Codex's thread history database, opened read-only.
const codexProjectionDB = "thread_history_1.sqlite"

// codexTitleIndex is Codex's best-effort thread-name index (~10% of
// threads on the design machine; the rest fall back to the first prompt).
const codexTitleIndex = "session_index.jsonl"

var codexRolloutRE = regexp.MustCompile(`^rollout-.*-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

func (Codex) Harness() string { return HarnessCodex }

// codexLayout: <home>/{sessions,archived_sessions}/**/rollout-*.jsonl; the
// resolved home rides along as Aux for the projection database and the
// title index.
var codexLayout = layout{
	harness: HarnessCodex,
	subdirs: []string{"sessions", "archived_sessions"},
	keep:    isCodexRollout,
	ident: func(ref *SourceRef, home string) bool {
		ref.NativeID = codexNativeID(ref.Path)
		ref.Aux = home
		return true
	},
}

// Discover walks sessions/ and archived_sessions/ under every Codex home.
func (Codex) Discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error {
	return codexLayout.discover(ctx, roots, emit)
}

// Locate builds the SourceRef of one rollout under a Codex home.
func (Codex) Locate(path string, roots []Root) (SourceRef, bool) {
	return codexLayout.locate(path, roots)
}

func isCodexRollout(name string) bool { return codexRolloutRE.MatchString(name) }

// codexNativeID is the thread uuid in a rollout filename.
func codexNativeID(path string) string {
	return codexRolloutRE.FindStringSubmatch(filepath.Base(path))[1]
}

// Ingest tails src from byte offset from, bounded by Codex's own cursor
// when it applies (see the type comment).
func (Codex) Ingest(ctx context.Context, src SourceRef, from int64, sink Sink, b *Budget) (int64, error) {
	st := &codexState{newEmitter(src, sink)}
	return st.scan(ctx, from, codexProjectionStop(src), b, st.line)
}

// codexProjectionStop returns the byte offset a pass over src may not
// cross, or 0 for none: min(projection cursor, size) when the projection
// row exists and the file is fresh.
func codexProjectionStop(src SourceRef) int64 {
	if src.Aux == "" || src.NativeID == "" || src.Size <= 0 {
		return 0
	}
	if src.MtimeNS > 0 && time.Since(time.Unix(0, src.MtimeNS)) > projectionGrace {
		return 0
	}
	cursor, ok := codexProjectionCursor(filepath.Join(src.Aux, codexProjectionDB), src.NativeID)
	if !ok {
		return 0
	}
	if cursor > src.Size {
		cursor = src.Size
	}
	return cursor
}

// codexProjectionCursor reads one thread's next_rollout_byte_offset. Any
// failure (no database, different schema, no row) means "no cursor".
func codexProjectionCursor(dbPath, threadID string) (int64, bool) {
	if _, err := fsStat(dbPath); err != nil {
		return 0, false
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(2000)&_pragma=query_only(1)")
	if err != nil {
		return 0, false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var cursor int64
	if err := db.QueryRow(`SELECT next_rollout_byte_offset FROM thread_history_projection_state WHERE thread_id=?`, threadID).Scan(&cursor); err != nil || cursor < 0 {
		return 0, false
	}
	return cursor, true
}

// codexTitles caches session_index.jsonl per home, keyed on the file's
// size and mtime so a rewritten index is re-read.
var codexTitles struct {
	sync.Mutex
	byHome map[string]codexTitleCache
}

type codexTitleCache struct {
	size    int64
	mtimeNS int64
	titles  map[string]string
}

// codexTitle returns the thread_name Codex recorded for a thread, or "".
func codexTitle(home, threadID string) string {
	if home == "" {
		return ""
	}
	path := filepath.Join(home, codexTitleIndex)
	info, err := fsStat(path)
	if err != nil {
		return ""
	}
	codexTitles.Lock()
	defer codexTitles.Unlock()
	if codexTitles.byHome == nil {
		codexTitles.byHome = map[string]codexTitleCache{}
	}
	c, ok := codexTitles.byHome[home]
	if !ok || c.size != info.Size() || c.mtimeNS != info.ModTime().UnixNano() {
		c = codexTitleCache{size: info.Size(), mtimeNS: info.ModTime().UnixNano(), titles: map[string]string{}}
		if data, err := os.ReadFile(path); err == nil {
			for _, line := range bytes.Split(data, []byte{'\n'}) {
				var rec struct {
					ID   string `json:"id"`
					Name string `json:"thread_name"`
				}
				if json.Unmarshal(line, &rec) == nil && rec.ID != "" && rec.Name != "" {
					c.titles[rec.ID] = rec.Name
				}
			}
		}
		codexTitles.byHome[home] = c
	}
	return c.titles[threadID]
}

type codexRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMeta struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	CWD        string `json:"cwd"`
	CLIVersion string `json:"cli_version"`
	Git        struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

type codexTurnContext struct {
	Model string `json:"model"`
	CWD   string `json:"cwd"`
}

type codexResponseItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   []codexContent  `json:"content"`
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	Output    json.RawMessage `json:"output"`
	Action    struct {
		Command []string `json:"command"`
	} `json:"action"`
}

type codexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexEventMsg struct {
	Type string `json:"type"`
}

type codexUsageRecord struct {
	ResponseID string `json:"response_id"`
	TurnID     string `json:"turn_id"`
	Usage      struct {
		InputTokens        int64 `json:"input_tokens"`
		CachedInputTokens  int64 `json:"cached_input_tokens"`
		CacheWriteTokens   int64 `json:"cache_write_input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		ReasoningOutputTok int64 `json:"reasoning_output_tokens"`
	} `json:"usage"`
}

type codexCompacted struct {
	Message string `json:"message"`
}

type codexState struct{ emitter }

// session emits the Session record once, with the best-effort title.
func (st *codexState) session(native, cwd, version, branch string) {
	if st.sessionSent {
		return
	}
	s := Session{NativeID: firstNonEmpty(native, st.src.NativeID), CWD: cwd, Version: version, Branch: branch}
	if title := codexTitle(st.src.Aux, s.NativeID); title != "" {
		s.Title, s.TitleSrc = title, "thread_name"
	}
	st.emitter.session(s)
}

func (st *codexState) line(line []byte, off, n int64) {
	var rec codexRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		st.sink.Count(CountBadJSON, 1)
		return
	}
	ts := parseTS(rec.Timestamp)
	switch rec.Type {
	case "session_meta":
		var meta codexSessionMeta
		_ = json.Unmarshal(rec.Payload, &meta)
		st.session(firstNonEmpty(meta.ID, meta.SessionID), meta.CWD, meta.CLIVersion, meta.Git.Branch)
	case "turn_context":
		var tc codexTurnContext
		_ = json.Unmarshal(rec.Payload, &tc)
		st.session("", tc.CWD, "", "")
		st.setModel(tc.Model)
	case "response_item":
		st.responseItem(rec.Payload, ts, off, n)
	case "token_usage_record":
		var u codexUsageRecord
		_ = json.Unmarshal(rec.Payload, &u)
		if u.Usage.InputTokens+u.Usage.OutputTokens > 0 {
			// The response id keys cost-event dedup across reparses.
			st.fail(st.sink.Usage(Usage{UUID: firstNonEmpty(u.ResponseID, u.TurnID+"@"+rec.Timestamp), TS: ts, Model: st.model,
				In: u.Usage.InputTokens, Out: u.Usage.OutputTokens, CacheR: u.Usage.CachedInputTokens, CacheW: u.Usage.CacheWriteTokens}))
		}
	case "event_msg":
		var ev codexEventMsg
		_ = json.Unmarshal(rec.Payload, &ev)
		if ev.Type == "turn_aborted" {
			st.sink.Count(CountInterrupt, 1)
		}
	case "compacted":
		// The summary replaces the history before it; replacement_history
		// repeats records already indexed from their own lines and is
		// never re-emitted.
		var c codexCompacted
		_ = json.Unmarshal(rec.Payload, &c)
		st.session("", "", "", "")
		st.sink.Count(CountCompact, 1)
		st.fail(st.sink.Msg(Msg{Role: recall.RoleUser, TS: unixOrZero(ts), RecOff: off, RecLen: n,
			Text: c.Message, IsCompact: true, SupersedesPrior: true}))
	case "world_state", "inter_agent_communication_metadata", "realtime_item":
		// Structural records without conversation text.
	default:
		st.sink.Count(CountUnknownType, 1)
	}
}

func (st *codexState) responseItem(payload json.RawMessage, ts time.Time, off, n int64) {
	var it codexResponseItem
	if json.Unmarshal(payload, &it) != nil {
		st.sink.Count(CountBadJSON, 1)
		return
	}
	switch it.Type {
	case "message":
		var role int
		switch it.Role {
		case "user":
			role = recall.RoleUser
		case "assistant":
			role = recall.RoleAssistant
		default:
			return // developer / system instructions are not conversation
		}
		var sb strings.Builder
		for _, c := range it.Content {
			if c.Type == "input_text" || c.Type == "output_text" {
				joinText(&sb, c.Text)
			}
		}
		text := sb.String()
		if strings.TrimSpace(text) == "" {
			return
		}
		st.session("", "", "", "")
		m := Msg{Role: role, TS: unixOrZero(ts), RecOff: off, RecLen: n, Text: text}
		if role == recall.RoleUser {
			m.IsMeta = codexInjectedText(text)
			m.IsInterrupt = classify.IsInterrupt(text)
		}
		st.fail(st.sink.Msg(m))
	case "function_call", "custom_tool_call", "local_shell_call":
		name := it.Name
		args := firstNonEmpty(it.Arguments, it.Input)
		if it.Type == "local_shell_call" {
			name = firstNonEmpty(name, "shell")
			args = strings.Join(it.Action.Command, " ")
		}
		st.openCall(it.CallID, pendingCall{name: name, ts: ts, digest: clipRunes(strings.TrimSpace(args), ArgDigestChars)})
	case "function_call_output", "custom_tool_call_output":
		st.closeCall(it.CallID, pendingCall{ts: ts}, ts, codexOutputIsError(it.Output))
	}
}

// codexInjectedText reports harness-injected user text: Codex wraps
// environment context, AGENTS.md instructions and permission notes in a
// leading XML-ish tag (<environment_context>, <user_instructions>, ...).
func codexInjectedText(text string) bool {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "<") {
		return false
	}
	end := strings.IndexByte(t, '>')
	return end > 1 && !strings.ContainsAny(t[1:end], " \n\t")
}

// codexOutputIsError reports a failed tool call from its output: Codex has
// no is_error flag, so the shell wrapper's own prefix is the signal.
func codexOutputIsError(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	var s string
	if raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return codexErrorText(s)
	}
	var blocks []codexContent
	if raw[0] == '[' && json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if codexErrorText(b.Text) {
				return true
			}
		}
	}
	return false
}

func codexErrorText(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "Error:") || strings.HasPrefix(t, "error:") || strings.HasPrefix(t, "failed:") ||
		strings.Contains(t, "\nExit code: ") && !strings.Contains(t, "\nExit code: 0")
}
