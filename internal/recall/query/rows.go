package query

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RowsSchema names the typed row model of `recall timeline` / `recall
// follow` (docs/recall-timeline.md). `--v1` keeps the slice-6 turn shape.
const RowsSchema = "agent-deck.recall.rows/v2"

// Row is one conversation row, one shape for every harness (Mac app
// conversation spec §1, macapp-core-needs §1). Kinds: user, assistant,
// thinking, tool, bash, edit, read, subagent, todo, question, skill,
// command, system, compaction, turn_end, other. IDs are native: Claude
// `uuid` / `uuid#block`, the tool_use id for a tool row, Codex item or call
// ids; never line numbers, so they survive tail windows and reconnects.
type Row struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind,omitempty"`
	TS       string         `json:"ts,omitempty"`
	Title    string         `json:"title,omitempty"`
	Body     string         `json:"body,omitempty"`
	Summary  string         `json:"summary,omitempty"`
	Detail   string         `json:"detail,omitempty"`
	ToolID   string         `json:"tool_id,omitempty"`
	Finished *bool          `json:"finished,omitempty"`
	IsError  *bool          `json:"is_error,omitempty"`
	Queued   *bool          `json:"queued,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
	Children []Row          `json:"children,omitempty"`
	RawType  string         `json:"raw_type,omitempty"`
}

// RowFrame is one `recall follow` line. Frame is row (append, or replace
// the row with that id), update (merge the fields present into the row with
// that id; unknown ids are ignored), remove (drop the row with that id),
// status (live status; see LiveStatus), delivery (a queued send changed
// state) or resync_required. Only the last frame produced by one native
// line carries Cursor, so every cursor is a clean resume point.
type RowFrame struct {
	Frame  string `json:"frame"`
	Row    *Row   `json:"row,omitempty"`
	ID     string `json:"id,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Reason string `json:"reason,omitempty"`
	*LiveStatus
	*Delivery
}

// LiveStatus is the synthetic status frame: what the terminal shows about a
// running turn that no transcript carries. agent-deck reads state.db and a
// read-only pane capture, so a client never shells tmux.
type LiveStatus struct {
	SessionID      string            `json:"session_id,omitempty"`
	Running        bool              `json:"running"`
	SessionStatus  string            `json:"session_status,omitempty"`
	Verb           string            `json:"verb,omitempty"`
	ElapsedS       int               `json:"elapsed_s,omitempty"`
	Tokens         string            `json:"tokens,omitempty"`
	CurrentTool    string            `json:"current_tool,omitempty"`
	Facts          map[string]string `json:"facts,omitempty"`
	Permission     string            `json:"permission,omitempty"`
	AutoCompactPct *int              `json:"auto_compact_pct,omitempty"`
	Queued         []string          `json:"queued,omitempty"`
	Notice         string            `json:"notice,omitempty"`
}

// Delivery is the payload of a delivery frame, mirroring `session
// send-status` for one queued send.
type Delivery struct {
	SendID string `json:"send_id"`
	State  string `json:"state"`
}

const rowBodyCap = 32 << 10

func boolPtr(b bool) *bool { return &b }

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
	tokens  map[string]any
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

func rowFrame(r Row) RowFrame    { return RowFrame{Frame: "row", Row: &r} }
func updateFrame(r Row) RowFrame { return RowFrame{Frame: "update", Row: &r} }
func removeFrame(id string) RowFrame {
	return RowFrame{Frame: "remove", ID: id}
}

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

// capBody bounds tool output in a row; meta.lines keeps the full count.
func capBody(text string) (string, bool) {
	if len(text) <= rowBodyCap {
		return text, false
	}
	cut := rowBodyCap
	for cut > 0 && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut], true
}

func lineCount(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// resultRow builds the update that finishes a tool row with its output.
func resultRow(id, text string, isError bool) Row {
	body, truncated := capBody(text)
	r := Row{ID: id, Body: body, Finished: boolPtr(true), IsError: boolPtr(isError), Meta: map[string]any{"lines": lineCount(text)}}
	if truncated {
		r.Meta["truncated"] = true
	}
	return r
}

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

func otherRow(id, ts, typ string, raw []byte) Row {
	return Row{ID: id, Kind: "other", TS: ts, Title: typ, RawType: typ, Meta: map[string]any{"raw": json.RawMessage(append([]byte(nil), raw...))}}
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
	return []RowFrame{rowFrame(otherRow(uuid, ts, typ, raw))}
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

// textRow builds a user/command/system row from typed or injected text.
func textRow(id, ts, text string, meta bool) Row {
	kind := claudeUserTextKind(text, meta)
	row := Row{ID: id, Kind: kind, TS: ts, Body: text}
	switch kind {
	case "command":
		row.Title = commandTitle(text)
	case "system":
		row.Title = systemTitle(text)
	}
	return row
}

func (p *rowParser) claudeUser(rec map[string]json.RawMessage, ts, uuid string) []RowFrame {
	meta := string(rec["isMeta"]) == "true"
	msg := timelineObject(rec["message"])
	content := msg["content"]
	if string(rec["isCompactSummary"]) == "true" {
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", TS: ts, Title: "Compaction summary", Body: timelineText(content), RawType: "user"})}
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
			out = append(out, p.claudeToolResult(b, tur))
		}
	}
	if len(texts) > 0 || images > 0 {
		out = append(p.claudeUserText(uuid, ts, strings.Join(texts, "\n"), meta, images), out...)
	}
	return out
}

func (p *rowParser) claudeUserText(uuid, ts, text string, meta bool, images int) []RowFrame {
	row := textRow(uuid, ts, text, meta)
	row.RawType = "user"
	if images > 0 {
		row.Meta = map[string]any{"images": images}
	}
	if row.Kind == "user" {
		// A dequeued message lands as this user row: the queued copy goes.
		h := contentHash(text)
		if id, ok := p.st.Pending[h]; ok {
			delete(p.st.Pending, h)
			return []RowFrame{removeFrame(id), rowFrame(row)}
		}
	}
	return []RowFrame{rowFrame(row)}
}

func (p *rowParser) claudeToolResult(b map[string]json.RawMessage, tur map[string]json.RawMessage) RowFrame {
	id := timelineString(b["tool_use_id"])
	text := timelineText(b["content"])
	if _, ok := tur["stdout"]; ok {
		text = timelineString(tur["stdout"])
		if e := timelineString(tur["stderr"]); e != "" {
			text = strings.TrimRight(text, "\n") + "\n" + e
		}
	}
	row := resultRow(id, text, string(b["is_error"]) == "true")
	if string(tur["interrupted"]) == "true" {
		row.Meta["status"] = "interrupted"
		row.Summary = "Interrupted"
	}
	if a := tur["answers"]; len(a) > 0 {
		row.Meta["answers"] = json.RawMessage(append([]byte(nil), a...))
		var answers map[string]any
		_ = json.Unmarshal(a, &answers)
		var parts []string
		for q, v := range answers {
			parts = append(parts, fmt.Sprintf("%s → %v", q, v))
		}
		sort.Strings(parts)
		row.Summary = strings.Join(parts, "; ")
	}
	if agent := timelineString(tur["agentId"]); agent != "" {
		row.Meta["agent_id"] = agent
		status := timelineString(tur["status"])
		row.Meta["status"] = status
		if status == "async_launched" {
			row.Summary = "Backgrounded agent"
		} else {
			var uses int
			_ = json.Unmarshal(tur["totalToolUseCount"], &uses)
			row.Summary = "Done (" + plural(uses, "tool use") + ")"
		}
	}
	if file := timelineObject(tur["file"]); file != nil {
		var n int
		if json.Unmarshal(file["numLines"], &n) == nil && n > 0 {
			row.Summary = "Read " + plural(n, "line")
		}
	}
	if n, ok := jsonInt(tur["numFiles"]); ok {
		row.Summary = "Found " + plural(n, "file")
	}
	added, removed := 0, 0
	for _, hunk := range timelineArray(tur["structuredPatch"]) {
		for _, l := range timelineArray(timelineObject(hunk)["lines"]) {
			s := timelineString(l)
			if strings.HasPrefix(s, "+") {
				added++
			} else if strings.HasPrefix(s, "-") {
				removed++
			}
		}
	}
	if added+removed > 0 {
		row.Meta["added"], row.Meta["removed"] = added, removed
		row.Summary = "Added " + plural(added, "line") + ", removed " + plural(removed, "line")
	}
	if n, _ := row.Meta["lines"].(int); row.Summary == "" && n > 3 {
		row.Summary = fmt.Sprintf("… +%d lines", n-3)
	}
	return updateFrame(row)
}

func (p *rowParser) claudeAssistant(rec map[string]json.RawMessage, ts, uuid string) []RowFrame {
	msg := timelineObject(rec["message"])
	blocks := timelineArray(msg["content"])
	var out []RowFrame
	for i, part := range blocks {
		b := timelineObject(part)
		id := uuid
		if len(blocks) > 1 {
			id = fmt.Sprintf("%s#%d", uuid, i)
		}
		switch timelineString(b["type"]) {
		case "text":
			out = append(out, rowFrame(Row{ID: id, Kind: "assistant", TS: ts, Body: timelineString(b["text"]), RawType: "assistant"}))
		case "thinking", "redacted_thinking":
			out = append(out, rowFrame(Row{ID: id, Kind: "thinking", TS: ts, Title: "Thinking", Body: timelineString(b["thinking"]), RawType: "assistant"}))
		case "tool_use":
			out = append(out, rowFrame(claudeToolRow(b, ts)))
		}
	}
	return out
}

func claudeToolRow(b map[string]json.RawMessage, ts string) Row {
	name := timelineString(b["name"])
	in := timelineObject(b["input"])
	toolID := timelineString(b["id"])
	row := Row{ID: toolID, ToolID: toolID, Kind: claudeToolKind(name), TS: ts, Finished: boolPtr(false), RawType: "assistant", Meta: map[string]any{"tool_name": name}}
	desc := timelineString(in["description"])
	input := json.RawMessage(append([]byte(nil), b["input"]...))
	switch row.Kind {
	case "bash":
		row.Detail = timelineString(in["command"])
		row.Title = firstNonEmptyString(desc, firstLine(row.Detail, 120), name)
	case "edit", "read":
		path := firstNonEmptyString(timelineString(in["file_path"]), timelineString(in["notebook_path"]), timelineString(in["path"]))
		row.Detail = firstNonEmptyString(path, timelineString(in["pattern"]))
		row.Title = name + " " + row.Detail
		if path != "" {
			row.Meta["path"] = path
		}
		if row.Kind == "edit" || name == "Grep" || name == "Glob" {
			row.Meta["input"] = input
		}
	case "subagent":
		row.Title = firstNonEmptyString(desc, name)
		row.Body = timelineString(in["prompt"])
		row.Detail = timelineString(in["subagent_type"])
		row.Meta["input"] = input
	case "todo":
		row.Title = name
		row.Meta["input"] = input
		var todos []struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(in["todos"], &todos) == nil && len(todos) > 0 {
			done := 0
			for _, td := range todos {
				if td.Status == "completed" {
					done++
				}
			}
			row.Summary = fmt.Sprintf("%d of %d done", done, len(todos))
		}
	case "question":
		row.Title = name
		row.Meta["input"] = input
		if qs := timelineArray(in["questions"]); len(qs) > 0 {
			row.Title = firstNonEmptyString(timelineString(timelineObject(qs[0])["question"]), name)
		}
	case "skill":
		row.Title = "Skill: " + timelineString(in["skill"])
	default:
		row.Title = firstNonEmptyString(desc, name)
		row.Meta["input"] = input
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
	op := timelineString(rec["operation"])
	switch op {
	case "enqueue":
		id := "queue:" + ts + ":" + h
		p.st.Pending[h] = id
		row := textRow(id, ts, content, false)
		row.Queued, row.RawType = boolPtr(true), "queue-operation"
		row.Meta = map[string]any{"delivery": "queued"}
		return []RowFrame{rowFrame(row)}
	case "remove", "dequeue", "popAll":
		id, ok := p.st.Pending[h]
		delete(p.st.Pending, h)
		if strings.HasPrefix(timelineString(rec["reason"]), "absorbed") {
			p.rememberAbsorbed(h)
			if !ok {
				row := textRow("queue:"+ts+":"+h, ts, content, false)
				row.Queued, row.RawType = boolPtr(false), "queue-operation"
				row.Meta = map[string]any{"delivery": "absorbed"}
				return []RowFrame{rowFrame(row)}
			}
			// The terminal prints an absorbed message at the remove time:
			// the update carries that ts, and a client moves the row there.
			return []RowFrame{updateFrame(Row{ID: id, TS: ts, Queued: boolPtr(false), Meta: map[string]any{"delivery": "absorbed"}})}
		}
		if !ok {
			return nil
		}
		if op == "dequeue" {
			// The next user row with this text replaces the queued copy.
			p.st.Pending[h] = id
			return nil
		}
		return []RowFrame{removeFrame(id)}
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
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", TS: ts, Title: systemTitle(prompt), Body: prompt, RawType: "attachment/" + typ})}
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
	return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", TS: ts, Title: title, Body: text, RawType: "attachment/" + typ})}
}

func (p *rowParser) claudeSystem(rec map[string]json.RawMessage, ts, uuid string, raw []byte) []RowFrame {
	sub := timelineString(rec["subtype"])
	rawType := "system/" + sub
	switch sub {
	case "turn_duration":
		var ms int64
		_ = json.Unmarshal(rec["durationMs"], &ms)
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "turn_end", TS: ts, Title: "Worked for " + formatDurationMs(ms), RawType: rawType, Meta: map[string]any{"duration_ms": ms}})}
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
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", TS: ts, Title: "Stop hook: " + firstLine(text, 120), Body: text, RawType: rawType})}
	case "compact_boundary":
		meta := timelineObject(rec["compactMetadata"])
		var pre, post int64
		_ = json.Unmarshal(meta["preTokens"], &pre)
		_ = json.Unmarshal(meta["postTokens"], &post)
		title := "Context compacted"
		m := map[string]any{}
		if pre > 0 {
			title += " · " + formatTokens(pre)
			m["pre_tokens"] = pre
			if post > 0 {
				title += " → " + formatTokens(post)
				m["post_tokens"] = post
			}
		}
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "compaction", TS: ts, Title: title, RawType: rawType, Meta: m})}
	case "local_command":
		text := timelineString(rec["content"])
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "command", TS: ts, Title: firstNonEmptyString(commandTitle(text), systemTitle(text)), Body: text, RawType: rawType})}
	case "scheduled_task_fire", "informational", "api_error", "away_summary", "bridge_status":
		text := firstNonEmptyString(timelineString(rec["content"]), sub)
		return []RowFrame{rowFrame(Row{ID: uuid, Kind: "system", TS: ts, Title: systemTitle(text), Body: text, RawType: rawType})}
	}
	return []RowFrame{rowFrame(otherRow(uuid, ts, rawType, raw))}
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
		return []RowFrame{rowFrame(Row{ID: fallbackID, Kind: "compaction", TS: ts, Title: "Context compacted", RawType: typ})}
	case "turn_context", "thread_settings_applied":
		model := firstNonEmptyString(timelineString(pl["model"]), timelineString(timelineObject(pl["settings"])["model"]))
		prev := p.st.Model
		if model != "" {
			p.st.Model = model
		}
		if prev != "" && model != "" && model != prev {
			return []RowFrame{rowFrame(Row{ID: fallbackID, Kind: "system", TS: ts, Title: "Model changed to " + model, RawType: typ, Meta: map[string]any{"model": model}})}
		}
		return nil
	case "session_meta", "token_usage_record", "world_state", "inter_agent_communication_metadata", "realtime_item":
		return nil
	}
	return []RowFrame{rowFrame(otherRow(fallbackID, ts, typ, raw))}
}

func codexID(pl map[string]json.RawMessage, fallback string) string {
	if id := timelineString(pl["id"]); id != "" {
		return id
	}
	return fallback
}

func (p *rowParser) codexResponseItem(pl map[string]json.RawMessage, ptype, ts, fallbackID string) []RowFrame {
	rawType := "response_item/" + ptype
	switch ptype {
	case "message":
		text := timelineText(pl["content"])
		switch timelineString(pl["role"]) {
		case "user":
			if codexBoilerplate(text) {
				return nil
			}
			row := Row{ID: codexID(pl, fallbackID), Kind: "user", TS: ts, Body: text, RawType: rawType}
			images := 0
			for _, part := range timelineArray(pl["content"]) {
				if strings.Contains(timelineString(timelineObject(part)["type"]), "image") {
					images++
				}
			}
			if images > 0 {
				row.Meta = map[string]any{"images": images}
			}
			return []RowFrame{rowFrame(row)}
		case "assistant":
			return []RowFrame{rowFrame(Row{ID: codexID(pl, fallbackID), Kind: "assistant", TS: ts, Body: text, RawType: rawType})}
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
		return []RowFrame{rowFrame(Row{ID: codexID(pl, fallbackID), Kind: "thinking", TS: ts, Title: firstNonEmptyString(firstLine(text, 120), "Thinking"), Body: text, RawType: rawType})}
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
		row := Row{ID: callID, ToolID: callID, TS: ts, Finished: boolPtr(false), RawType: rawType, Meta: map[string]any{"tool_name": firstNonEmptyString(name, "shell")}}
		switch {
		case ptype == "local_shell_call" || name == "shell" || name == "exec_command" || name == "container.exec":
			row.Kind = "bash"
			row.Detail = codexCommandText(args, pl["action"])
			row.Title = firstLine(row.Detail, 120)
		case name == "apply_patch":
			row.Kind, row.Title = "edit", "apply_patch"
			row.Meta["input"] = rawJSONValue(args)
		case name == "spawn_agent" || name == "wait_agent":
			row.Kind = "subagent"
			a := timelineObject(json.RawMessage(timelineString(args)))
			row.Title = firstNonEmptyString(timelineString(a["task_name"]), timelineString(a["description"]), name)
		case name == "update_plan":
			row.Kind, row.Title = "todo", "Plan"
			row.Meta["input"] = rawJSONValue(args)
		case name == "request_user_input":
			row.Kind, row.Title = "question", "Question"
			row.Meta["input"] = rawJSONValue(args)
		default:
			row.Kind, row.Title = "tool", name
			row.Meta["input"] = rawJSONValue(args)
		}
		return []RowFrame{rowFrame(row)}
	case "function_call_output", "custom_tool_call_output":
		out := pl["output"]
		text := timelineText(out)
		if text == "" {
			text = timelineString(out)
		}
		row := resultRow(timelineString(pl["call_id"]), text, false)
		// Some outputs are JSON with exit_code/output.
		if obj := timelineObject(json.RawMessage(text)); obj != nil {
			if o := timelineString(obj["output"]); o != "" {
				row = resultRow(row.ID, o, false)
			}
			if code, ok := jsonInt(obj["exit_code"]); ok {
				row.Meta["exit_code"] = code
				row.IsError = boolPtr(code != 0)
			}
		}
		return []RowFrame{updateFrame(row)}
	case "agent_message":
		text := timelineText(pl["content"])
		author := timelineString(pl["author"])
		return []RowFrame{rowFrame(Row{ID: codexID(pl, fallbackID), Kind: "system", TS: ts, Title: "Message from " + firstNonEmptyString(author, "agent"), Body: text, RawType: rawType})}
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
	rawType := "event_msg/" + ptype
	switch ptype {
	case "item_completed":
		return p.codexItem(timelineObject(pl["item"]), pl, ts, fallbackID)
	case "task_complete", "turn_aborted":
		var started, completed int64
		_ = json.Unmarshal(pl["started_at"], &started)
		_ = json.Unmarshal(pl["completed_at"], &completed)
		row := Row{ID: "turn:" + firstNonEmptyString(timelineString(pl["turn_id"]), fallbackID), Kind: "turn_end", TS: ts, RawType: rawType, Meta: map[string]any{}}
		var dur int64
		if ms, ok := jsonInt(pl["duration_ms"]); ok && ms > 0 {
			dur = int64(ms)
		} else if started > 0 {
			end := completed
			if end == 0 {
				if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					end = t.Unix()
				}
			}
			if end >= started {
				dur = (end - started) * 1000
			}
		}
		if dur > 0 {
			row.Meta["duration_ms"] = dur
		}
		if p.tokens != nil {
			row.Meta["tokens"] = p.tokens
		}
		row.Title = "Worked for " + formatDurationMs(dur)
		if ptype == "turn_aborted" {
			row.Title = "Interrupted"
			row.Meta["status"] = "interrupted"
		}
		p.tokens = nil
		return []RowFrame{rowFrame(row)}
	case "token_count":
		info := timelineObject(pl["info"])
		total := timelineObject(info["total_token_usage"])
		t := map[string]any{}
		for key, field := range map[string]json.RawMessage{"input": total["input_tokens"], "output": total["output_tokens"], "total": total["total_tokens"], "context_window": info["model_context_window"]} {
			if n, ok := jsonInt(field); ok && n > 0 {
				t[key] = n
			}
		}
		if len(t) > 0 {
			p.tokens = t
		}
		return nil
	case "context_compacted":
		return []RowFrame{rowFrame(Row{ID: fallbackID, Kind: "compaction", TS: ts, Title: "Context compacted", RawType: rawType})}
	}
	return nil
}

func (p *rowParser) codexItem(item, pl map[string]json.RawMessage, ts, fallbackID string) []RowFrame {
	id := codexID(item, fallbackID)
	itype := timelineString(item["type"])
	rawType := "item_completed/" + itype
	var startMs, endMs int64
	_ = json.Unmarshal(pl["started_at_ms"], &startMs)
	_ = json.Unmarshal(pl["completed_at_ms"], &endMs)
	meta := map[string]any{}
	if endMs > startMs && startMs > 0 {
		meta["duration_ms"] = endMs - startMs
	}
	if s := timelineString(item["status"]); s != "" {
		meta["status"] = s
	}
	switch itype {
	case "AgentMessage", "UserMessage", "Reasoning":
		return nil // duplicates of the response_item rows
	case "CommandExecution":
		command := commandString(item["command"])
		out := firstNonEmptyString(timelineString(item["aggregated_output"]), timelineString(item["stdout"]))
		row := resultRow(id, out, false)
		row.Kind, row.TS, row.ToolID, row.RawType = "bash", ts, id, rawType
		row.Title, row.Detail = firstLine(command, 120), command
		for k, v := range meta {
			row.Meta[k] = v
		}
		row.Meta["tool_name"] = "shell"
		parsed := timelineArray(item["parsed_cmd"])
		readOnly := len(parsed) > 0
		for _, pc := range parsed {
			if !codexReadOnlyCommands[timelineString(timelineObject(pc)["type"])] {
				readOnly = false
			}
		}
		if readOnly {
			row.Kind = "read"
		}
		if code, ok := jsonInt(item["exit_code"]); ok {
			row.Meta["exit_code"] = code
			row.IsError = boolPtr(code != 0)
		}
		lines, _ := row.Meta["lines"].(int)
		switch {
		case row.Kind == "read":
			row.Summary = "Read " + plural(lines, "line")
		case *row.IsError:
			row.Summary = fmt.Sprintf("Exit code %v", row.Meta["exit_code"])
		case lines > 3:
			row.Summary = fmt.Sprintf("… +%d lines", lines-3)
		}
		return []RowFrame{rowFrame(row)}
	case "FileChange":
		changes := timelineObject(item["changes"])
		var paths []string
		for path := range changes {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		meta["tool_name"] = "apply_patch"
		meta["input"] = json.RawMessage(append([]byte(nil), item["changes"]...))
		added, removed := 0, 0
		for _, c := range changes {
			for _, l := range strings.Split(timelineString(timelineObject(c)["unified_diff"]), "\n") {
				switch {
				case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
					added++
				case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
					removed++
				}
			}
		}
		row := Row{ID: id, ToolID: id, Kind: "edit", TS: ts, Title: "Edited " + strings.Join(paths, ", "), Finished: boolPtr(true), RawType: rawType, Meta: meta}
		if len(paths) > 0 {
			row.Detail = paths[0]
		}
		if added+removed > 0 {
			meta["added"], meta["removed"] = added, removed
			row.Summary = "Added " + plural(added, "line") + ", removed " + plural(removed, "line")
		}
		return []RowFrame{rowFrame(row)}
	case "McpToolCall":
		name := timelineString(item["server"]) + "." + timelineString(item["tool"])
		meta["tool_name"] = name
		meta["input"] = json.RawMessage(append([]byte(nil), item["arguments"]...))
		row := Row{ID: id, ToolID: id, Kind: "tool", TS: ts, Title: name, Finished: boolPtr(true), RawType: rawType, Meta: meta}
		if r := item["result"]; len(r) > 0 {
			res := resultRow(id, timelineText(timelineObject(r)["content"]), timelineString(item["status"]) == "failed")
			row.Body, row.IsError, meta["lines"] = res.Body, res.IsError, res.Meta["lines"]
		}
		return []RowFrame{rowFrame(row)}
	case "ContextCompaction":
		return []RowFrame{rowFrame(Row{ID: id, Kind: "compaction", TS: ts, Title: "Context compacted", RawType: rawType})}
	case "SubAgentActivity":
		agent := timelineString(item["agent_thread_id"])
		kind := timelineString(item["kind"])
		return []RowFrame{updateFrame(Row{ID: timelineString(item["id"]), Summary: "Agent " + kind, Meta: map[string]any{"agent_id": agent, "status": kind, "agent_path": timelineString(item["agent_path"])}})}
	case "CollabAgentToolCall":
		return []RowFrame{updateFrame(Row{ID: timelineString(item["id"]), Finished: boolPtr(true), Meta: meta})}
	case "WebSearch":
		q := timelineString(item["query"])
		return []RowFrame{rowFrame(Row{ID: id, ToolID: id, Kind: "tool", TS: ts, Title: "Web search: " + q, Finished: boolPtr(true), RawType: rawType, Meta: map[string]any{"tool_name": "web_search"}})}
	}
	return nil
}

// rowsFromTurns maps v1 turns (Pi, Gemini, OpenCode, Hermes) onto the row
// model so every harness answers with rows. IDs are positional there
// because the v1 parsers keep no per-event ids for those formats.
func rowsFromTurns(turns []Turn) []Row {
	out := make([]Row, 0, len(turns))
	for i, t := range turns {
		r := Row{ID: fmt.Sprintf("seq:%d", i+1), TS: t.Timestamp, Body: t.Text, RawType: t.Kind}
		if t.ToolName != "" {
			r.Meta = map[string]any{"tool_name": t.ToolName}
		}
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
			r.Kind = "other"
			if len(t.Raw) > 0 {
				r.Meta = map[string]any{"raw": t.Raw}
			}
		}
		out = append(out, r)
	}
	return out
}
