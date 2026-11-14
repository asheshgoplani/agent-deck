package trace

import (
	"encoding/json"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HookContext is what the hook handler knows besides the payload.
type HookContext struct {
	Tool     string // AGENTDECK_TOOL ("claude", "codex", ...); "" = detect from payload
	Instance string
	Profile  string
	Host     string
	At       time.Time
	Level    Level
}

// hookPayload is the union of the fields Claude Code and Codex hooks send
// (both use the hook_event_name / session_id / tool_name / tool_input /
// tool_response shape; Codex adds turn_id). Unknown fields are ignored.
type hookPayload struct {
	HookEventName    string          `json:"hook_event_name"`
	SessionID        string          `json:"session_id"`
	ConversationID   string          `json:"conversation_id"`
	TurnID           json.RawMessage `json:"turn_id"`
	ToolName         string          `json:"tool_name"`
	ToolUseID        string          `json:"tool_use_id"`
	CallID           string          `json:"call_id"`
	ToolInput        json.RawMessage `json:"tool_input"`
	ToolResponse     json.RawMessage `json:"tool_response"`
	Prompt           json.RawMessage `json:"prompt"`
	Matcher          json.RawMessage `json:"matcher"`
	NotificationType string          `json:"notification_type"`
	Model            json.RawMessage `json:"model"`
	Error            json.RawMessage `json:"error"`
}

func rawStr(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func eventKey(event string) string {
	s := strings.ToLower(strings.TrimSpace(event))
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(s)
}

// NormalizeHook turns one Claude or Codex hook payload into a SpoolRecord.
// ok is false when the level is off, the payload is not decodable, or the
// event carries nothing to trace. The returned record is already redacted.
func NormalizeHook(raw []byte, ctx HookContext) (rec SpoolRecord, ok bool) {
	if ctx.Level == LevelOff || len(raw) == 0 {
		return SpoolRecord{}, false
	}
	var p hookPayload
	if json.Unmarshal(raw, &p) != nil || strings.TrimSpace(p.HookEventName) == "" {
		return SpoolRecord{}, false
	}
	turnID := rawStr(p.TurnID)
	tool := strings.ToLower(strings.TrimSpace(ctx.Tool))
	if tool == "" {
		tool = "claude"
		if turnID != "" {
			tool = "codex" // Codex hooks carry turn_id; Claude's never do
		}
	}
	sessionID := strings.TrimSpace(p.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(p.ConversationID)
	}
	at := ctx.At
	if at.IsZero() {
		at = time.Now()
	}
	rec = baseRecord(ctx, tool, at)
	rec.Event = cleanEventName(p.HookEventName)
	rec.SessionID = cleanID(sessionID)
	rec.TurnID = cleanID(turnID)
	rec.Model = cleanID(rawStr(p.Model))
	summaries := ctx.Level >= LevelSummaries

	switch eventKey(p.HookEventName) {
	case "sessionstart":
		rec.Kind = KindSessionStart
	case "sessionend":
		rec.Kind = KindSessionEnd
	case "userpromptsubmit", "beforesubmitprompt":
		rec.Kind = KindTurnStart
		if summaries {
			rec.Summary = "prompt_len=" + strconv.Itoa(len(rawStr(p.Prompt)))
		}
	case "stop":
		rec.Kind = KindTurnEnd
	case "pretooluse":
		rec.Kind = KindToolStart
		rec.ToolName = cleanToolName(p.ToolName)
		rec.ToolUseID = cleanID(firstNonEmpty(p.ToolUseID, p.CallID))
		if summaries {
			rec.Summary = Scrub(SummarizeToolInput(p.ToolName, p.ToolInput))
		}
	case "posttooluse", "posttoolusefailure":
		rec.Kind = KindToolEnd
		rec.ToolName = cleanToolName(p.ToolName)
		rec.ToolUseID = cleanID(firstNonEmpty(p.ToolUseID, p.CallID))
		rec.Status = StatusOK
		if eventKey(p.HookEventName) == "posttoolusefailure" || responseIsError(p.ToolResponse) || len(rawStr(p.Error)) > 0 {
			rec.Status = StatusError
		}
		if summaries {
			s := SummarizeToolInput(p.ToolName, p.ToolInput)
			if n := len(p.ToolResponse); n > 0 {
				s = strings.TrimSpace(s + " resp_bytes=" + strconv.Itoa(n))
			}
			rec.Summary = Scrub(s)
		}
	case "notification":
		m := firstNonEmpty(rawStr(p.Matcher), p.NotificationType)
		if m == "permission_prompt" || m == "elicitation_dialog" {
			rec.Kind = KindWaitStart
			rec.Summary = m // fixed vocabulary
		} else {
			rec.Kind = KindOther
		}
	case "permissionrequest":
		rec.Kind = KindWaitStart
		rec.ToolName = cleanToolName(p.ToolName)
		rec.Summary = "permission_request"
	case "precompact":
		rec.Kind = KindCompact
	default:
		rec.Kind = KindOther
	}
	return rec, true
}

// NormalizeCodexNotify turns a codex-notify edge (Codex's notify program:
// agent-turn-complete and friends) into a SpoolRecord. turnStarted /
// turnCompleted come from the caller's existing edge classifier.
func NormalizeCodexNotify(event, sessionID, turnID string, turnStarted, turnCompleted bool, ctx HookContext) (SpoolRecord, bool) {
	if ctx.Level == LevelOff || (!turnStarted && !turnCompleted) {
		return SpoolRecord{}, false
	}
	at := ctx.At
	if at.IsZero() {
		at = time.Now()
	}
	rec := baseRecord(ctx, "codex", at)
	rec.Event = cleanEventName(event)
	rec.SessionID = cleanID(sessionID)
	rec.TurnID = cleanID(turnID)
	if turnCompleted {
		rec.Kind = KindTurnEnd
		if l := strings.ToLower(event); strings.Contains(l, "fail") || strings.Contains(l, "abort") || strings.Contains(l, "cancel") {
			rec.Status = StatusError
		}
	} else {
		rec.Kind = KindTurnStart
	}
	return rec, true
}

func baseRecord(ctx HookContext, tool string, at time.Time) SpoolRecord {
	return SpoolRecord{
		V:        SpoolVersion,
		ID:       newRecordID(at),
		At:       at.UnixMilli(),
		Tool:     cleanID(tool),
		Profile:  cleanID(ctx.Profile),
		Instance: cleanID(ctx.Instance),
		Host:     cleanID(ctx.Host),
	}
}

func cleanEventName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	return cleanID(s)
}

// cleanToolName keeps tool names readable (MCP names are long:
// mcp__server__tool) while still refusing anything secret-shaped.
func cleanToolName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 96 {
		s = s[:96]
	}
	return cleanID(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// responseIsError reads the common failure markers of a tool response
// without keeping any of its content.
func responseIsError(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	isTrue := func(k string) bool {
		var b bool
		return json.Unmarshal(obj[k], &b) == nil && b
	}
	if isTrue("is_error") || isTrue("isError") || isTrue("interrupted") {
		return true
	}
	if v, ok := obj["success"]; ok {
		var b bool
		if json.Unmarshal(v, &b) == nil && !b {
			return true
		}
	}
	for _, k := range []string{"exit_code", "exitCode"} {
		var n int
		if json.Unmarshal(obj[k], &n) == nil && n != 0 {
			return true
		}
	}
	return false
}

// SummarizeToolInput returns the shape of a tool call's arguments, never
// their content: Bash/shell: first command word + length; file tools: file
// basename + sizes; apply_patch: file count + bytes; anything else: sorted
// key names. The caller scrubs the result.
func SummarizeToolInput(toolName string, input json.RawMessage) string {
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(input, &obj)
	switch strings.ToLower(toolName) {
	case "bash", "shell", "local_shell", "exec_command", "container.exec", "unified_exec":
		cmd := commandText(obj)
		return "cmd=" + firstWord(cmd) + " len=" + strconv.Itoa(len(cmd))
	case "edit", "multiedit", "write", "read", "notebookedit":
		file := rawStr(obj["file_path"])
		if file == "" {
			file = rawStr(obj["notebook_path"])
		}
		parts := []string{"file=" + path.Base(strings.ReplaceAll(file, "\\", "/"))}
		for _, k := range []string{"old_string", "new_string", "content", "new_source"} {
			if v, ok := obj[k]; ok {
				parts = append(parts, strings.TrimSuffix(k, "_string")+"="+strconv.Itoa(len(rawStr(v))))
			}
		}
		if v, ok := obj["edits"]; ok {
			var edits []json.RawMessage
			_ = json.Unmarshal(v, &edits)
			parts = append(parts, "edits="+strconv.Itoa(len(edits)))
		}
		return strings.Join(parts, " ")
	case "apply_patch":
		patch := firstNonEmpty(rawStr(obj["input"]), rawStr(obj["patch"]), commandText(obj))
		if patch == "" {
			patch = string(input)
		}
		files := strings.Count(patch, "*** Update File:") + strings.Count(patch, "*** Add File:") + strings.Count(patch, "*** Delete File:")
		return "files=" + strconv.Itoa(files) + " bytes=" + strconv.Itoa(len(patch))
	}
	if len(obj) == 0 {
		return ""
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 8 {
		keys = append(keys[:8], "...")
	}
	return "keys=" + strings.Join(keys, ",")
}

// commandText reads a shell command given as a string (Claude Bash) or an
// argv array (Codex shell: ["bash","-lc","<script>"]).
func commandText(obj map[string]json.RawMessage) string {
	for _, k := range []string{"command", "cmd"} {
		v, ok := obj[k]
		if !ok {
			continue
		}
		if s := rawStr(v); s != "" {
			return s
		}
		var argv []string
		if json.Unmarshal(v, &argv) == nil && len(argv) > 0 {
			if len(argv) >= 3 && (argv[1] == "-lc" || argv[1] == "-c") {
				return argv[2]
			}
			return strings.Join(argv, " ")
		}
	}
	return ""
}

// firstWord is the program a command runs: its first word, path stripped,
// capped. A leading NAME=value assignment is reported as "<env>" so its
// value never reaches the summary even before scrubbing.
func firstWord(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	f := fields[0]
	if strings.Contains(f, "=") {
		return "<env>"
	}
	w := path.Base(f)
	if len(w) > 32 {
		w = w[:32]
	}
	return w
}
