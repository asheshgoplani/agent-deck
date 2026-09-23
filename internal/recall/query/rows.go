package query

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RowsSchema names the typed row model served by `recall timeline --rows`
// and `recall follow --rows`. The v1 Turn model stays the default output.
const RowsSchema = "agent-deck.recall.rows/v2"

// Row is one conversation row in the Mac app row model (conversation spec
// §1). Kinds: user, assistant, thinking, tool, bash, edit, read, subagent,
// todo, question, skill, command, system, compaction, turn_end, other.
// IDs are harness-native (Claude uuid[:block], tool_use id, Codex item or
// call id) so they survive window moves and reconnects.
type Row struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind,omitempty"`
	Timestamp  string          `json:"ts,omitempty"`
	Title      string          `json:"title,omitempty"`
	Text       string          `json:"text,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Command    string          `json:"command,omitempty"`
	Path       string          `json:"path,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Result     *RowResult      `json:"result,omitempty"`
	Delivery   string          `json:"delivery,omitempty"`
	ParentID   string          `json:"parent_id,omitempty"`
	AgentID    string          `json:"agent_id,omitempty"`
	Status     string          `json:"status,omitempty"`
	DurationMs int64           `json:"duration_ms,omitempty"`
	Images     int             `json:"images,omitempty"`
	Tokens     *RowTokens      `json:"tokens,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

// RowResult is a tool call's outcome, merged into the call row by id.
type RowResult struct {
	Text      string          `json:"text,omitempty"`
	Lines     int             `json:"lines,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	ExitCode  *int            `json:"exit_code,omitempty"`
	Added     int             `json:"added,omitempty"`
	Removed   int             `json:"removed,omitempty"`
	Answers   json.RawMessage `json:"answers,omitempty"`
}

// RowTokens carries the token numbers a turn footer shows when known.
type RowTokens struct {
	Input         int64 `json:"input,omitempty"`
	Output        int64 `json:"output,omitempty"`
	Total         int64 `json:"total,omitempty"`
	ContextWindow int64 `json:"context_window,omitempty"`
}

// RowFrame is one follow frame. Type is row (append), update (merge the
// non-empty fields into the row with the same id; unknown ids are ignored),
// remove (drop the row with that id), status (live session status, never
// persisted) or resync_required. Only the last frame produced by one native
// line carries Cursor, so every cursor is a clean resume point.
type RowFrame struct {
	Type   string      `json:"type"`
	Row    *Row        `json:"row,omitempty"`
	ID     string      `json:"id,omitempty"`
	Status *LiveStatus `json:"status,omitempty"`
	Cursor string      `json:"cursor,omitempty"`
	Reason string      `json:"reason,omitempty"`
}

// LiveStatus is the synthetic status row: the session's state plus what the
// terminal shows while a turn runs (spinner verb, elapsed, tokens, current
// tool, footer facts). It comes from state.db and a read-only pane capture
// done by agent-deck, so a client never reads tmux itself.
type LiveStatus struct {
	State       string `json:"state"`
	Since       string `json:"since,omitempty"`
	Verb        string `json:"verb,omitempty"`
	Elapsed     string `json:"elapsed,omitempty"`
	Tokens      string `json:"tokens,omitempty"`
	CurrentTool string `json:"current_tool,omitempty"`
	Queued      int    `json:"queued,omitempty"`
	Footer      string `json:"footer,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Notice      string `json:"notice,omitempty"`
}

const rowResultTextCap = 32 << 10

// rowParserState is the small state a parser carries across lines. It is
// serialized into the follow cursor so a resumed stream keeps queue ids.
type rowParserState struct {
	Pending map[string]string `json:"q,omitempty"` // content hash -> queued row id
	Recent  []string          `json:"r,omitempty"` // hashes of recently absorbed queue content
	Model   string            `json:"m,omitempty"` // Codex model, for model-change rows
}

// rowParser maps native lines to row frames for one harness.
type rowParser struct {
	harness string
	st      rowParserState
	tokens  *RowTokens
}

func newRowParser(harness string, st rowParserState) *rowParser {
	if st.Pending == nil {
		st.Pending = map[string]string{}
	}
	return &rowParser{harness: harness, st: st}
}

func contentHash(s string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(s)))
	return fmt.Sprintf("%x", h[:6])
}

func (p *rowParser) rememberAbsorbed(h string) {
	p.st.Recent = append(p.st.Recent, h)
	if len(p.st.Recent) > 16 {
		p.st.Recent = p.st.Recent[len(p.st.Recent)-16:]
	}
}

func (p *rowParser) wasQueued(h string) bool {
	if _, ok := p.st.Pending[h]; ok {
		return true
	}
	for _, r := range p.st.Recent {
		if r == h {
			return true
		}
	}
	return false
}

// line parses one complete native JSONL line.
func (p *rowParser) line(raw []byte) []RowFrame {
	var rec map[string]json.RawMessage
	if json.Unmarshal(raw, &rec) != nil {
		return nil
	}
	switch p.harness {
	case "claude":
		return p.claude(rec, raw)
	case "codex":
		return p.codex(rec, raw)
	}
	return nil
}

func rowFrame(r Row) RowFrame   { return RowFrame{Type: "row", Row: &r} }
func updateFrame(r Row) RowFrame { return RowFrame{Type: "update", Row: &r} }

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if max > 0 && len([]rune(s)) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

func newResult(text string, isError bool) *RowResult {
	r := &RowResult{Text: text, IsError: isError}
	if text != "" {
		r.Lines = strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
	}
	if len(r.Text) > rowResultTextCap {
		cut := rowResultTextCap
		for cut > 0 && !isRuneStart(r.Text[cut]) {
			cut--
		}
		r.Text, r.Truncated = r.Text[:cut], true
	}
	return r
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// claudeToolKind maps a Claude Code tool name to its row kind.
func claudeToolKind(name string) string {
	switch name {
	case "Bash", "BashOutput", "PowerShell":
		return "bash"
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "edit"
	case "Read", "Grep", "Glob", "LSP", "LS", "NotebookRead":
		return "read"
	case "Agent", "Task":
		return "subagent"
	case "TodoWrite", "TaskCreate", "TaskUpdate":
		return "todo"
	case "AskUserQuestion":
		return "question"
	case "Skill":
		return "skill"
	}
	return "tool"
}

var claudeDropTypes = map[string]bool{
	"file-history-snapshot": true, "last-prompt": true, "ai-title": true, "custom-title": true,
	"pr-link": true, "frame-link": true, "mode": true, "permission-mode": true, "atis-latch": true,
	"bridge-session": true, "history-suppression": true, "cost-state": true, "agent-name": true,
	"summary": true, "tag": true,
}

var claudeSystemAttachments = map[string]bool{
	"hook_blocking_error": true, "async_hook_response": true, "silent_turn_reminder": true,
	"hook_additional_context": true, "hook_non_blocking_error": true, "hook_error_during_execution": true,
}

func (p *rowParser) claude(rec map[string]json.RawMessage, raw []byte) []RowFrame {
	typ, ts, uuid := timelineString(rec["type"]), timelineString(rec["timestamp"]), timelineString(rec["uuid"])
	if uuid == "" {
		uuid = "line:" + ts + ":" + contentHash(string(raw))
	}
	switch {
	case typ == "user":
		return p.claudeUser(rec, ts, uuid)
	case typ == "assistant":
		return p.claudeAssistant(rec, ts, uuid)
	case typ == "queue-operation":
		return p.claudeQueue(rec, ts)
	case typ == "attachment":
		return p.claudeAttachment(rec, ts, uuid)
	case typ == "system":
		return p.claudeSystem(rec, ts, uuid, raw)
	case claudeDropTypes[typ] || strings.HasPrefix(typ, "artifact"):
		return nil
	}
	return []RowFrame{rowFrame(Row{ID: uuid, Kind: "other", Timestamp: ts, Title: typ, Raw: append(json.RawMessage(nil), raw...)})}
}

// claudeUserTextKind classifies a user text: what the person typed, a slash
// command, or an injection that the terminal shows as a faint line.
func claudeUserTextKind(text string, meta bool) string {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "<command-name>"), strings.HasPrefix(t, "<command-message>"):
		return "command"
	case meta,
		strings.HasPrefix(t, "[INBOX]"),
		strings.HasPrefix(t, "<system-reminder>"),
		strings.HasPrefix(t, "Stop hook feedback:"),
		strings.HasPrefix(t, "<task-notification>"),
		strings.HasPrefix(t, "<local-command-stdout>"),
		strings.HasPrefix(t, "<local-command-stderr>"),
		strings.HasPrefix(t, "<local-command-caveat>"),
		strings.HasPrefix(t, "Caveat: "),
		strings.HasPrefix(t, "Base directory for this skill"):
		return "system"
	}
	return "user"
}

func xmlTag(s, tag string) string {
	open, closeTag := "<"+tag+">", "</"+tag+">"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, closeTag)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

func commandTitle(text string) string {
	name := xmlTag(text, "command-name")
	if name == "" {
		name = xmlTag(text, "command-message")
	}
	if name != "" && !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	if args := xmlTag(text, "command-args"); args != "" {
		name += " " + args
	}
	return name
}

func systemTitle(text string) string {
	t := strings.TrimSpace(text)
	if s := xmlTag(t, "summary"); s != "" && strings.HasPrefix(t, "<task-notification>") {
		return s
	}
	for _, tag := range []string{"system-reminder", "local-command-stdout", "local-command-stderr"} {
		if s := xmlTag(t, tag); s != "" {
			return firstLine(s, 160)
		}
	}
	return firstLine(t, 160)
}

func (p *rowParser) claudeUser(rec map[string]json.RawMessage, ts, uuid string) []RowFrame {
	meta := string(rec["isMeta"]) == "true"
	msg := timelineObject(rec["message"])
	content := msg["content"]
	if string(rec["isCompactSummary"]) == "true" {
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", Timestamp: ts, Title: "Compaction summary", Text: timelineText(content)})}
	}
	if len(content) > 0 && content[0] == '"' {
		return p.claudeUserText(uuid, ts, timelineString(content), meta, 0)
	}
	var out []RowFrame
	var texts []string
	images := 0
	tur := timelineObject(rec["toolUseResult"])
	for _, part := range timelineArray(content) {
		b := timelineObject(part)
		switch timelineString(b["type"]) {
		case "text":
			texts = append(texts, timelineString(b["text"]))
		case "image":
			images++
		case "tool_result":
			out = append(out, p.claudeToolResult(b, rec["toolUseResult"], tur))
		}
	}
	if len(texts) > 0 || images > 0 {
		out = append(p.claudeUserText(uuid, ts, strings.Join(texts, "\n"), meta, images), out...)
	}
	return out
}

func (p *rowParser) claudeUserText(uuid, ts, text string, meta bool, images int) []RowFrame {
	kind := claudeUserTextKind(text, meta)
	row := Row{ID: uuid, Kind: kind, Timestamp: ts, Text: text, Images: images}
	switch kind {
	case "command":
		row.Title = commandTitle(text)
	case "system":
		row.Title = systemTitle(text)
	case "user":
		// A queued message that was dequeued lands as this user row: the
		// queued placeholder goes away (spec §2, queue-operation).
		h := contentHash(text)
		if id, ok := p.st.Pending[h]; ok {
			delete(p.st.Pending, h)
			return []RowFrame{{Type: "remove", ID: id}, rowFrame(row)}
		}
	}
	return []RowFrame{rowFrame(row)}
}

func (p *rowParser) claudeToolResult(b map[string]json.RawMessage, turRaw json.RawMessage, tur map[string]json.RawMessage) RowFrame {
	id := "tool:" + timelineString(b["tool_use_id"])
	text := timelineText(b["content"])
	if s := timelineString(tur["stdout"]); s != "" || len(tur["stdout"]) > 0 {
		text = s
		if e := timelineString(tur["stderr"]); e != "" {
			text = strings.TrimRight(text, "\n") + "\n" + e
		}
	}
	res := newResult(text, string(b["is_error"]) == "true")
	row := Row{ID: id, Result: res}
	if string(tur["interrupted"]) == "true" {
		row.Status = "interrupted"
	}
	if a := tur["answers"]; len(a) > 0 {
		res.Answers = append(json.RawMessage(nil), a...)
	}
	if agent := timelineString(tur["agentId"]); agent != "" {
		row.AgentID = agent
		row.Status = timelineString(tur["status"])
	}
	for _, hunk := range timelineArray(tur["structuredPatch"]) {
		for _, l := range timelineArray(timelineObject(hunk)["lines"]) {
			s := timelineString(l)
			if strings.HasPrefix(s, "+") {
				res.Added++
			} else if strings.HasPrefix(s, "-") {
				res.Removed++
			}
		}
	}
	_ = turRaw
	return updateFrame(row)
}

func (p *rowParser) claudeAssistant(rec map[string]json.RawMessage, ts, uuid string) []RowFrame {
	msg := timelineObject(rec["message"])
	var out []RowFrame
	for i, part := range timelineArray(msg["content"]) {
		b := timelineObject(part)
		id := fmt.Sprintf("%s:%d", uuid, i)
		switch timelineString(b["type"]) {
		case "text":
			out = append(out, rowFrame(Row{ID: id, Kind: "assistant", Timestamp: ts, Text: timelineString(b["text"])}))
		case "thinking", "redacted_thinking":
			out = append(out, rowFrame(Row{ID: id, Kind: "thinking", Timestamp: ts, Title: "Thinking", Text: timelineString(b["thinking"])}))
		case "tool_use":
			out = append(out, rowFrame(claudeToolRow(b, ts)))
		}
	}
	return out
}

func claudeToolRow(b map[string]json.RawMessage, ts string) Row {
	name := timelineString(b["name"])
	in := timelineObject(b["input"])
	row := Row{ID: "tool:" + timelineString(b["id"]), Kind: claudeToolKind(name), Timestamp: ts, ToolName: name}
	desc := timelineString(in["description"])
	switch row.Kind {
	case "bash":
		row.Command = timelineString(in["command"])
		row.Title = firstNonEmptyString(desc, firstLine(row.Command, 120), name)
	case "edit", "read":
		row.Path = firstNonEmptyString(timelineString(in["file_path"]), timelineString(in["notebook_path"]), timelineString(in["path"]))
		row.Title = name + " " + firstNonEmptyString(row.Path, timelineString(in["pattern"]))
		if row.Kind == "edit" || name == "Grep" || name == "Glob" {
			row.Input = append(json.RawMessage(nil), b["input"]...)
		}
	case "subagent":
		row.Title = firstNonEmptyString(desc, name)
		row.Text = timelineString(in["prompt"])
		row.Input = append(json.RawMessage(nil), b["input"]...)
	case "todo", "question":
		row.Title = name
		row.Input = append(json.RawMessage(nil), b["input"]...)
	case "skill":
		row.Title = "Skill: " + timelineString(in["skill"])
	default:
		row.Title = firstNonEmptyString(desc, name)
		row.Input = append(json.RawMessage(nil), b["input"]...)
	}
	return row
}

func firstNonEmptyString(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// claudeQueue handles messages typed while a turn runs. Claude Code writes
// no user row for an absorbed message, so the queue row is the only record.
func (p *rowParser) claudeQueue(rec map[string]json.RawMessage, ts string) []RowFrame {
	content := timelineString(rec["content"])
	h := contentHash(content)
	switch timelineString(rec["operation"]) {
	case "enqueue":
		id := "queue:" + ts + ":" + h
		p.st.Pending[h] = id
		kind := claudeUserTextKind(content, false)
		row := Row{ID: id, Kind: kind, Timestamp: ts, Text: content, Delivery: "queued"}
		if kind == "system" {
			row.Title = systemTitle(content)
		}
		return []RowFrame{rowFrame(row)}
	case "remove", "dequeue", "popAll":
		id, ok := p.st.Pending[h]
		delete(p.st.Pending, h)
		reason := timelineString(rec["reason"])
		if strings.HasPrefix(reason, "absorbed") {
			p.rememberAbsorbed(h)
			if !ok {
				kind := claudeUserTextKind(content, false)
				row := Row{ID: "queue:" + ts + ":" + h, Kind: kind, Timestamp: ts, Text: content, Delivery: "absorbed"}
				if kind == "system" {
					row.Title = systemTitle(content)
				}
				return []RowFrame{rowFrame(row)}
			}
			// The terminal prints an absorbed message at the remove time, so
			// the row moves there: remove, then append under the same id.
			kind := claudeUserTextKind(content, false)
			row := Row{ID: id, Kind: kind, Timestamp: ts, Text: content, Delivery: "absorbed"}
			if kind == "system" {
				row.Title = systemTitle(content)
			}
			return []RowFrame{{Type: "remove", ID: id}, rowFrame(row)}
		}
		if !ok {
			return nil
		}
		if timelineString(rec["operation"]) == "dequeue" {
			// The next user row with this text replaces the placeholder.
			p.st.Pending[h] = id
			return nil
		}
		return []RowFrame{{Type: "remove", ID: id}}
	}
	return nil
}

func (p *rowParser) claudeAttachment(rec map[string]json.RawMessage, ts, uuid string) []RowFrame {
	a := timelineObject(rec["attachment"])
	typ := timelineString(a["type"])
	if typ == "queued_command" {
		prompt := timelineString(a["prompt"])
		if p.wasQueued(contentHash(prompt)) {
			return nil
		}
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", Timestamp: ts, Title: systemTitle(prompt), Text: prompt})}
	}
	if !claudeSystemAttachments[typ] {
		return nil
	}
	text := timelineText(a["content"])
	if text == "" {
		text = firstNonEmptyString(timelineString(a["stderr"]), timelineString(a["stdout"]), timelineString(a["blockingError"]))
	}
	title := typ
	if hook := timelineString(a["hookName"]); hook != "" {
		title = hook + " · " + typ
	}
	return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", Timestamp: ts, Title: title, Text: text})}
}

func (p *rowParser) claudeSystem(rec map[string]json.RawMessage, ts, uuid string, raw []byte) []RowFrame {
	sub := timelineString(rec["subtype"])
	switch sub {
	case "turn_duration":
		var ms int64
		_ = json.Unmarshal(rec["durationMs"], &ms)
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "turn_end", Timestamp: ts, Title: "Worked for " + formatDurationMs(ms), DurationMs: ms})}
	case "stop_hook_summary":
		var parts []string
		for _, key := range []string{"hookErrors", "hookAdditionalContext"} {
			for _, e := range timelineArray(rec[key]) {
				if s := timelineText(e); s != "" {
					parts = append(parts, s)
				} else if s := timelineString(timelineObject(e)["error"]); s != "" {
					parts = append(parts, s)
				}
			}
		}
		if s := timelineString(rec["stopReason"]); s != "" {
			parts = append(parts, s)
		}
		if len(parts) == 0 {
			return nil
		}
		text := strings.Join(parts, "\n")
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", Timestamp: ts, Title: "Stop hook: " + firstLine(text, 120), Text: text})}
	case "compact_boundary":
		meta := timelineObject(rec["compactMetadata"])
		var pre, post int64
		_ = json.Unmarshal(meta["preTokens"], &pre)
		_ = json.Unmarshal(meta["postTokens"], &post)
		title := "Context compacted"
		if pre > 0 {
			title += " · " + formatTokens(pre)
			if post > 0 {
				title += " → " + formatTokens(post)
			}
		}
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "compaction", Timestamp: ts, Title: title})}
	case "local_command":
		text := timelineString(rec["content"])
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "command", Timestamp: ts, Title: firstNonEmptyString(commandTitle(text), systemTitle(text)), Text: text})}
	case "scheduled_task_fire", "informational", "api_error", "away_summary", "bridge_status":
		text := firstNonEmptyString(timelineString(rec["content"]), sub)
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", Timestamp: ts, Title: systemTitle(text), Text: text})}
	}
	return []RowFrame{rowFrame(Row{ID: uuid, Kind: "other", Timestamp: ts, Title: "system/" + sub, Raw: append(json.RawMessage(nil), raw...)})}
}

func formatDurationMs(ms int64) string {
	s := ms / 1000
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %dm", s/3600, (s%3600)/60)
}

func formatTokens(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// codexBoilerplate reports the injected user messages Codex writes before the
// first real prompt (AGENTS.md, environment context).
func codexBoilerplate(text string) bool {
	t := strings.TrimSpace(text)
	for _, prefix := range []string{"# AGENTS.md instructions", "<environment_context>", "<INSTRUCTIONS>", "<user_instructions>", "<turn_aborted>", "<user_shell_command>"} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// codexReadOnlyCommands are the parsed_cmd types Codex groups as "Explored".
var codexReadOnlyCommands = map[string]bool{"read": true, "list_files": true, "search": true}

func (p *rowParser) codex(rec map[string]json.RawMessage, raw []byte) []RowFrame {
	typ, ts := timelineString(rec["type"]), timelineString(rec["timestamp"])
	pl := timelineObject(rec["payload"])
	ptype := timelineString(pl["type"])
	// Rows without a native id are keyed by content, never by position, so
	// a tail window or a resumed follow names them the same way.
	fallbackID := "codex:" + ts + ":" + contentHash(string(raw))
	switch typ {
	case "response_item":
		return p.codexResponseItem(pl, ptype, ts, fallbackID)
	case "event_msg":
		return p.codexEvent(pl, ptype, ts, fallbackID)
	case "compacted":
		return []RowFrame{rowFrame(Row{ID: fallbackID, Kind: "compaction", Timestamp: ts, Title: "Context compacted"})}
	case "turn_context":
		model := timelineString(pl["model"])
		prev := p.st.Model
		p.st.Model = model
		if prev != "" && model != "" && model != prev {
			return []RowFrame{rowFrame(Row{ID: fallbackID, Kind: "system", Timestamp: ts, Title: "Model changed to " + model})}
		}
		return nil
	case "session_meta", "thread_settings_applied", "token_usage_record", "world_state", "inter_agent_communication_metadata", "realtime_item":
		return nil
	}
	return []RowFrame{rowFrame(Row{ID: fallbackID, Kind: "other", Timestamp: ts, Title: typ, Raw: append(json.RawMessage(nil), raw...)})}
}

func codexID(pl map[string]json.RawMessage, fallback string) string {
	if id := timelineString(pl["id"]); id != "" {
		return "codex:" + id
	}
	return fallback
}

func (p *rowParser) codexResponseItem(pl map[string]json.RawMessage, ptype, ts, fallbackID string) []RowFrame {
	switch ptype {
	case "message":
		text := timelineText(pl["content"])
		switch timelineString(pl["role"]) {
		case "user":
			if codexBoilerplate(text) {
				return nil
			}
			images := 0
			for _, part := range timelineArray(pl["content"]) {
				if strings.Contains(timelineString(timelineObject(part)["type"]), "image") {
					images++
				}
			}
			return []RowFrame{rowFrame(Row{ID: codexID(pl, fallbackID), Kind: "user", Timestamp: ts, Text: text, Images: images})}
		case "assistant":
			return []RowFrame{rowFrame(Row{ID: codexID(pl, fallbackID), Kind: "assistant", Timestamp: ts, Text: text})}
		}
		return nil
	case "reasoning":
		var parts []string
		for _, s := range timelineArray(pl["summary"]) {
			if t := timelineString(timelineObject(s)["text"]); t != "" {
				parts = append(parts, t)
			}
		}
		text := strings.Join(parts, "\n")
		return []RowFrame{rowFrame(Row{ID: codexID(pl, fallbackID), Kind: "thinking", Timestamp: ts, Title: firstNonEmptyString(firstLine(text, 120), "Thinking"), Text: text})}
	case "function_call", "custom_tool_call", "local_shell_call":
		name := timelineString(pl["name"])
		callID := firstNonEmptyString(timelineString(pl["call_id"]), timelineString(pl["id"]))
		args := pl["arguments"]
		if len(args) == 0 {
			args = pl["input"]
		}
		if ptype == "custom_tool_call" && name == "exec" {
			// A code-mode script: the commands and patches it runs arrive as
			// item_completed CommandExecution/FileChange rows of their own.
			in := timelineString(args)
			if strings.Contains(in, "exec_command") || strings.Contains(in, "apply_patch") || strings.Contains(in, "write_stdin") {
				return nil
			}
		}
		row := Row{ID: "tool:" + callID, Timestamp: ts, ToolName: name, Status: timelineString(pl["status"])}
		switch {
		case ptype == "local_shell_call" || name == "shell" || name == "exec_command" || name == "container.exec":
			row.Kind = "bash"
			row.ToolName = firstNonEmptyString(name, "shell")
			row.Command = codexCommandText(args, pl["action"])
			row.Title = firstLine(row.Command, 120)
		case name == "apply_patch":
			row.Kind = "edit"
			row.Title = "apply_patch"
			row.Input = rawJSONValue(args)
		case name == "spawn_agent" || name == "wait_agent":
			row.Kind = "subagent"
			a := timelineObject(json.RawMessage(timelineString(args)))
			row.Title = firstNonEmptyString(timelineString(a["task_name"]), timelineString(a["description"]), name)
		case name == "update_plan":
			row.Kind = "todo"
			row.Title = "Plan"
			row.Input = rawJSONValue(args)
		case name == "request_user_input":
			row.Kind = "question"
			row.Title = "Question"
			row.Input = rawJSONValue(args)
		default:
			row.Kind = "tool"
			row.Title = name
			row.Input = rawJSONValue(args)
		}
		return []RowFrame{rowFrame(row)}
	case "function_call_output", "custom_tool_call_output":
		out := pl["output"]
		text := timelineText(out)
		if text == "" {
			text = timelineString(out)
		}
		res := newResult(text, false)
		// Some outputs are JSON with exit_code/output.
		if obj := timelineObject(json.RawMessage(text)); obj != nil {
			if o := timelineString(obj["output"]); o != "" {
				res = newResult(o, false)
			}
			if code, ok := jsonInt(obj["exit_code"]); ok {
				res.ExitCode = &code
				res.IsError = code != 0
			}
		}
		return []RowFrame{updateFrame(Row{ID: "tool:" + timelineString(pl["call_id"]), Result: res})}
	case "agent_message":
		text := timelineText(pl["content"])
		author := timelineString(pl["author"])
		return []RowFrame{rowFrame(Row{ID: codexID(pl, fallbackID), Kind: "system", Timestamp: ts, Title: "Message from " + firstNonEmptyString(author, "agent"), Text: text})}
	}
	return nil
}

func rawJSONValue(args json.RawMessage) json.RawMessage {
	if len(args) > 0 && args[0] == '"' {
		s := timelineString(args)
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
		b, _ := json.Marshal(map[string]string{"input": s})
		return b
	}
	return append(json.RawMessage(nil), args...)
}

func codexCommandText(args, action json.RawMessage) string {
	a := timelineObject(rawJSONValue(args))
	if len(action) > 0 {
		a = timelineObject(action)
	}
	cmd := a["command"]
	if len(cmd) == 0 {
		cmd = a["cmd"]
	}
	return commandString(cmd)
}

// commandString renders an argv or a string command, dropping the shell
// wrapper (`/bin/zsh -lc "..."`).
func commandString(cmd json.RawMessage) string {
	if len(cmd) > 0 && cmd[0] == '"' {
		return timelineString(cmd)
	}
	var argv []string
	_ = json.Unmarshal(cmd, &argv)
	if len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
		return argv[2]
	}
	return strings.Join(argv, " ")
}

func jsonInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	return int(n), true
}

func (p *rowParser) codexEvent(pl map[string]json.RawMessage, ptype, ts, fallbackID string) []RowFrame {
	switch ptype {
	case "item_completed":
		return p.codexItem(timelineObject(pl["item"]), pl, ts, fallbackID)
	case "task_complete", "turn_aborted":
		var started, completed int64
		_ = json.Unmarshal(pl["started_at"], &started)
		_ = json.Unmarshal(pl["completed_at"], &completed)
		row := Row{ID: "turn:" + firstNonEmptyString(timelineString(pl["turn_id"]), fallbackID), Kind: "turn_end", Timestamp: ts, Tokens: p.tokens}
		if durMs, ok := jsonInt(pl["duration_ms"]); ok && durMs > 0 {
			row.DurationMs = int64(durMs)
		} else if started > 0 {
			end := completed
			if end == 0 {
				if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					end = t.Unix()
				}
			}
			if end >= started {
				row.DurationMs = (end - started) * 1000
			}
		}
		row.Title = "Worked for " + formatDurationMs(row.DurationMs)
		if ptype == "turn_aborted" {
			row.Title = "Interrupted"
			row.Status = "interrupted"
		}
		p.tokens = nil
		return []RowFrame{rowFrame(row)}
	case "token_count":
		info := timelineObject(pl["info"])
		total := timelineObject(info["total_token_usage"])
		t := &RowTokens{}
		_ = json.Unmarshal(total["input_tokens"], &t.Input)
		_ = json.Unmarshal(total["output_tokens"], &t.Output)
		_ = json.Unmarshal(total["total_tokens"], &t.Total)
		_ = json.Unmarshal(info["model_context_window"], &t.ContextWindow)
		if t.Total > 0 || t.Input > 0 {
			p.tokens = t
		}
		return nil
	case "context_compacted":
		return []RowFrame{rowFrame(Row{ID: fallbackID, Kind: "compaction", Timestamp: ts, Title: "Context compacted"})}
	}
	return nil
}

func (p *rowParser) codexItem(item, pl map[string]json.RawMessage, ts, fallbackID string) []RowFrame {
	id := codexID(item, fallbackID)
	var startMs, endMs int64
	_ = json.Unmarshal(pl["started_at_ms"], &startMs)
	_ = json.Unmarshal(pl["completed_at_ms"], &endMs)
	dur := int64(0)
	if endMs > startMs && startMs > 0 {
		dur = endMs - startMs
	}
	switch timelineString(item["type"]) {
	case "AgentMessage", "UserMessage", "Reasoning":
		return nil // duplicates of the response_item rows
	case "CommandExecution":
		row := Row{ID: id, Kind: "bash", Timestamp: ts, ToolName: "shell", Command: commandString(item["command"]), DurationMs: dur, Status: timelineString(item["status"])}
		readOnly := true
		parsed := timelineArray(item["parsed_cmd"])
		for _, pc := range parsed {
			if !codexReadOnlyCommands[timelineString(timelineObject(pc)["type"])] {
				readOnly = false
			}
		}
		if readOnly && len(parsed) > 0 {
			row.Kind = "read"
		}
		row.Title = firstLine(row.Command, 120)
		out := firstNonEmptyString(timelineString(item["aggregated_output"]), timelineString(item["stdout"]))
		row.Result = newResult(out, false)
		if code, ok := jsonInt(item["exit_code"]); ok {
			row.Result.ExitCode = &code
			row.Result.IsError = code != 0
		}
		return []RowFrame{rowFrame(row)}
	case "FileChange":
		row := Row{ID: id, Kind: "edit", Timestamp: ts, ToolName: "apply_patch", Status: timelineString(item["status"]), DurationMs: dur}
		changes := timelineObject(item["changes"])
		var paths []string
		for path := range changes {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		if len(paths) > 0 {
			row.Path = paths[0]
		}
		row.Title = "Edited " + strings.Join(paths, ", ")
		row.Input = append(json.RawMessage(nil), item["changes"]...)
		return []RowFrame{rowFrame(row)}
	case "McpToolCall":
		name := timelineString(item["server"]) + "." + timelineString(item["tool"])
		row := Row{ID: id, Kind: "tool", Timestamp: ts, ToolName: name, Title: name, Status: timelineString(item["status"]), DurationMs: dur}
		row.Input = append(json.RawMessage(nil), item["arguments"]...)
		if r := item["result"]; len(r) > 0 {
			row.Result = newResult(timelineText(timelineObject(r)["content"]), timelineString(item["status"]) == "failed")
		}
		return []RowFrame{rowFrame(row)}
	case "ContextCompaction":
		return []RowFrame{rowFrame(Row{ID: id, Kind: "compaction", Timestamp: ts, Title: "Context compacted"})}
	case "SubAgentActivity":
		agent := timelineString(item["agent_thread_id"])
		return []RowFrame{updateFrame(Row{ID: "tool:" + timelineString(item["id"]), AgentID: agent, Status: timelineString(item["kind"])})}
	case "CollabAgentToolCall":
		return []RowFrame{updateFrame(Row{ID: "tool:" + timelineString(item["id"]), Status: timelineString(item["status"]), DurationMs: dur})}
	case "WebSearch":
		q := timelineString(item["query"])
		return []RowFrame{rowFrame(Row{ID: id, Kind: "tool", Timestamp: ts, ToolName: "web_search", Title: "Web search: " + q})}
	}
	return nil
}

// rowsFromTurns maps v1 turns (Pi, Gemini, OpenCode, Hermes) onto the row
// model so every harness answers `--rows`. IDs are positional there because
// those formats carry no stable per-event ids in the v1 parser.
func rowsFromTurns(turns []Turn) []Row {
	out := make([]Row, 0, len(turns))
	for i, t := range turns {
		id := fmt.Sprintf("seq:%d", i+1)
		r := Row{ID: id, Timestamp: t.Timestamp, ToolName: t.ToolName, Text: t.Text}
		switch t.Kind {
		case "message":
			r.Kind = t.Role
			if r.Kind != "user" && r.Kind != "assistant" {
				r.Kind = "system"
			}
		case "tool_call", "permission":
			r.Kind, r.Title = "tool", t.ToolName
		case "bash", "edit", "todo", "subagent", "skill", "compaction":
			r.Kind, r.Title = t.Kind, firstLine(t.Text, 120)
		case "tool_result":
			// Keep results as their own faint rows: v1 turns carry no call id.
			r.Kind, r.Title = "system", "Result: "+firstLine(t.Text, 120)
		case "system":
			if t.Text == "" {
				continue
			}
			r.Kind, r.Title = "system", firstLine(t.Text, 120)
		default:
			r.Kind, r.Raw = "other", t.Raw
		}
		out = append(out, r)
	}
	return out
}
