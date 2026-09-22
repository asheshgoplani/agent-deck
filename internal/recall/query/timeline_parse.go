package query

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// parseTimelineSource reads native records directly. The search index
// deliberately omits tool output and metadata, so it cannot reconstruct a
// conversation or expose a newly appended line before the next index sweep.
func parseTimelineSource(ctx context.Context, src TimelineSource, nativeID string) ([]Turn, error) {
	switch src.Harness {
	case "claude", "codex", "pi":
		return parseTimelineJSONL(ctx, src)
	case "gemini":
		return parseTimelineGemini(ctx, src)
	case "opencode":
		return parseTimelineOpenCode(ctx, src, nativeID)
	case "hermes":
		return parseTimelineHermes(ctx, src, nativeID)
	default:
		return nil, fmt.Errorf("recall timeline: unknown harness %q", src.Harness)
	}
}

func parseTimelineJSONL(ctx context.Context, src TimelineSource) ([]Turn, error) {
	f, err := os.Open(src.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var out []Turn
	// Fix the read boundary at open time. A line appended during this read is
	// handled by the next poll; a torn line at this boundary is ignored.
	r := bufio.NewReader(io.NewSectionReader(f, 0, info.Size()))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			break // a torn final record is outside the indexed cursor
		}
		if err != nil {
			return nil, err
		}
		if !json.Valid(line) {
			continue
		}
		var record map[string]json.RawMessage
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		var turns []Turn
		switch src.Harness {
		case "claude":
			turns = claudeTimelineRecord(record, line)
		case "codex":
			turns = codexTimelineRecord(record, line)
		case "pi":
			turns = piTimelineRecord(record, line)
		}
		out = append(out, turns...)
	}
	return out, nil
}

func timelineString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func timelineObject(raw json.RawMessage) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	return obj
}

func timelineArray(raw json.RawMessage) []json.RawMessage {
	var array []json.RawMessage
	_ = json.Unmarshal(raw, &array)
	return array
}

func timelineText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		return timelineString(raw)
	}
	var parts []string
	for _, block := range timelineArray(raw) {
		b := timelineObject(block)
		if s := timelineString(b["text"]); s != "" {
			parts = append(parts, s)
		} else if s := timelineString(b["content"]); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

func timelineToolKind(name string) string {
	switch strings.ToLower(name) {
	case "bash", "shell", "terminal", "run_shell_command", "local_shell_call":
		return "bash"
	case "edit", "multiedit", "write", "notebookedit", "apply_patch":
		return "edit"
	case "todowrite", "taskcreate", "taskupdate", "tasklist":
		return "todo"
	case "agent", "task", "taskoutput", "taskstop", "listagents", "sendmessage":
		return "subagent"
	case "skill":
		return "skill"
	case "askuserquestion", "enterplanmode", "exitplanmode":
		return "permission"
	default:
		return "tool_call"
	}
}

func timelineTurn(role, kind, ts, tool, body string, raw json.RawMessage) Turn {
	return Turn{Role: role, Kind: kind, Timestamp: ts, ToolName: tool, Text: body, Raw: append(json.RawMessage(nil), raw...)}
}

func claudeTimelineRecord(rec map[string]json.RawMessage, raw json.RawMessage) []Turn {
	typ, ts := timelineString(rec["type"]), timelineString(rec["timestamp"])
	role := typ
	if typ != "user" && typ != "assistant" {
		kind := "system"
		if typ == "system" && timelineString(rec["subtype"]) == "compact_boundary" {
			kind = "compaction"
		} else if typ == "permission-mode" {
			kind = "permission"
		} else if typ != "system" && typ != "summary" && typ != "custom-title" && typ != "ai-title" && typ != "agent-name" && typ != "mode" {
			kind = "other"
		}
		return []Turn{timelineTurn("system", kind, ts, "", timelineString(rec["content"]), raw)}
	}
	msg := timelineObject(rec["message"])
	content := msg["content"]
	if len(content) == 0 || content[0] == '"' {
		kind := "message"
		if string(rec["isCompactSummary"]) == "true" {
			kind = "compaction"
		}
		return []Turn{timelineTurn(role, kind, ts, "", timelineText(content), raw)}
	}
	var out []Turn
	for _, part := range timelineArray(content) {
		b := timelineObject(part)
		switch timelineString(b["type"]) {
		case "text":
			kind := "message"
			if string(rec["isCompactSummary"]) == "true" {
				kind = "compaction"
			}
			out = append(out, timelineTurn(role, kind, ts, "", timelineString(b["text"]), part))
		case "tool_use":
			name := timelineString(b["name"])
			out = append(out, timelineTurn("assistant", timelineToolKind(name), ts, name, timelineToolText(name, b["input"]), part))
		case "tool_result":
			out = append(out, timelineTurn("user", "tool_result", ts, "", timelineText(b["content"]), part))
		default:
			out = append(out, timelineTurn(role, "other", ts, "", "", part))
		}
	}
	return out
}

func timelineToolText(name string, args json.RawMessage) string {
	if len(args) > 0 && args[0] == '"' {
		decoded := timelineString(args)
		if json.Valid([]byte(decoded)) && strings.HasPrefix(strings.TrimSpace(decoded), "{") {
			return timelineToolText(name, json.RawMessage(decoded))
		}
		return decoded
	}
	a := timelineObject(args)
	for _, key := range []string{"command", "file_path", "path", "prompt", "skill", "query", "description"} {
		if s := timelineString(a[key]); s != "" {
			return s
		}
	}
	return name
}

func codexTimelineRecord(rec map[string]json.RawMessage, raw json.RawMessage) []Turn {
	typ, ts := timelineString(rec["type"]), timelineString(rec["timestamp"])
	p := timelineObject(rec["payload"])
	switch typ {
	case "response_item":
		switch timelineString(p["type"]) {
		case "message":
			role := timelineString(p["role"])
			if role != "user" && role != "assistant" {
				return []Turn{timelineTurn("system", "system", ts, "", timelineText(p["content"]), raw)}
			}
			return []Turn{timelineTurn(role, "message", ts, "", timelineText(p["content"]), raw)}
		case "function_call", "custom_tool_call", "local_shell_call":
			name := timelineString(p["name"])
			if timelineString(p["type"]) == "local_shell_call" {
				name = "shell"
			}
			args := p["arguments"]
			if len(args) == 0 {
				args = p["input"]
			}
			body := timelineToolText(name, args)
			if timelineString(p["type"]) == "local_shell_call" {
				body = timelineText(p["action"])
			}
			return []Turn{timelineTurn("assistant", timelineToolKind(name), ts, name, body, raw)}
		case "function_call_output", "custom_tool_call_output":
			return []Turn{timelineTurn("tool", "tool_result", ts, "", timelineText(p["output"]), raw)}
		default:
			return []Turn{timelineTurn("system", "other", ts, "", "", raw)}
		}
	case "compacted":
		return []Turn{timelineTurn("system", "compaction", ts, "", timelineString(p["message"]), raw)}
	case "session_meta", "turn_context", "token_usage_record", "event_msg", "world_state", "inter_agent_communication_metadata", "realtime_item":
		return []Turn{timelineTurn("system", "system", ts, "", timelineString(p["type"]), raw)}
	default:
		return []Turn{timelineTurn("system", "other", ts, "", "", raw)}
	}
}

func piTimelineRecord(rec map[string]json.RawMessage, raw json.RawMessage) []Turn {
	typ, ts := timelineString(rec["type"]), timelineString(rec["timestamp"])
	switch typ {
	case "message":
		m := timelineObject(rec["message"])
		role := timelineString(m["role"])
		if role == "toolResult" {
			return []Turn{timelineTurn("tool", "tool_result", ts, timelineString(m["toolName"]), timelineText(m["content"]), raw)}
		}
		if role != "user" && role != "assistant" {
			return []Turn{timelineTurn("system", "other", ts, "", "", raw)}
		}
		content := m["content"]
		if len(content) == 0 || content[0] == '"' {
			return []Turn{timelineTurn(role, "message", ts, "", timelineText(content), raw)}
		}
		var out []Turn
		for _, part := range timelineArray(content) {
			b := timelineObject(part)
			switch timelineString(b["type"]) {
			case "text":
				out = append(out, timelineTurn(role, "message", ts, "", timelineString(b["text"]), part))
			case "toolCall":
				name := timelineString(b["name"])
				out = append(out, timelineTurn(role, timelineToolKind(name), ts, name, timelineToolText(name, b["arguments"]), part))
			default:
				out = append(out, timelineTurn(role, "other", ts, "", "", part))
			}
		}
		return out
	case "compaction":
		return []Turn{timelineTurn("system", "compaction", ts, "", timelineString(rec["summary"]), raw)}
	case "session", "session_info", "model_change", "thinking_level_change", "custom_message":
		kind := "system"
		if typ == "custom_message" && strings.Contains(timelineString(rec["customType"]), "subagent") {
			kind = "subagent"
		}
		return []Turn{timelineTurn("system", kind, ts, "", "", raw)}
	default:
		return []Turn{timelineTurn("system", "other", ts, "", "", raw)}
	}
}

func parseTimelineGemini(ctx context.Context, src TimelineSource) ([]Turn, error) {
	data, err := os.ReadFile(src.Path)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var out []Turn
	for _, raw := range timelineArray(doc["messages"]) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m := timelineObject(raw)
		typ, ts := timelineString(m["type"]), timelineString(m["timestamp"])
		role, kind := "system", "other"
		switch typ {
		case "user":
			role, kind = "user", "message"
		case "gemini":
			role, kind = "assistant", "message"
		case "info", "error":
			kind = "system"
		}
		out = append(out, timelineTurn(role, kind, ts, "", timelineText(m["content"]), raw))
		for _, call := range timelineArray(m["toolCalls"]) {
			c := timelineObject(call)
			name := timelineString(c["name"])
			callTS := timelineString(c["timestamp"])
			if callTS == "" {
				callTS = ts
			}
			out = append(out, timelineTurn("assistant", timelineToolKind(name), callTS, name, timelineToolText(name, c["args"]), call))
			if result := c["result"]; len(result) > 0 {
				out = append(out, timelineTurn("tool", "tool_result", callTS, name, string(result), result))
			}
		}
	}
	return out, nil
}

func parseTimelineOpenCode(ctx context.Context, src TimelineSource, nativeID string) ([]Turn, error) {
	storage := filepath.Dir(filepath.Dir(filepath.Dir(src.Path)))
	if nativeID == "" {
		nativeID = strings.TrimSuffix(filepath.Base(src.Path), ".json")
	}
	var out []Turn
	if data, err := os.ReadFile(src.Path); err == nil && json.Valid(data) {
		out = append(out, timelineTurn("system", "system", "", "", "", data))
	} else if err != nil {
		return nil, err
	}
	dir := filepath.Join(storage, "message", nativeID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	type orderedMessage struct {
		raw     json.RawMessage
		id      string
		created int64
	}
	var messages []orderedMessage
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		m := timelineObject(data)
		var timeObj struct {
			Created int64 `json:"created"`
		}
		_ = json.Unmarshal(m["time"], &timeObj)
		messages = append(messages, orderedMessage{data, strings.TrimSuffix(e.Name(), ".json"), timeObj.Created})
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].created < messages[j].created })
	for _, om := range messages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m := timelineObject(om.raw)
		role := timelineString(m["role"])
		if role != "user" && role != "assistant" {
			role = "system"
		}
		out = append(out, timelineTurn(role, "system", "", "", "", om.raw))
		partsDir := filepath.Join(storage, "part", om.id)
		parts, err := os.ReadDir(partsDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].Name() < parts[j].Name() })
		for _, p := range parts {
			if p.IsDir() || filepath.Ext(p.Name()) != ".json" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(partsDir, p.Name()))
			if err != nil {
				return nil, err
			}
			part := timelineObject(data)
			kind, name, body := "other", "", ""
			switch timelineString(part["type"]) {
			case "text":
				kind, body = "message", timelineString(part["text"])
			case "tool":
				name = timelineString(part["tool"])
				kind = timelineToolKind(name)
				state := timelineObject(part["state"])
				body = timelineToolText(name, state["input"])
			case "step-start", "step-finish":
				kind = "system"
			}
			out = append(out, timelineTurn(role, kind, "", name, body, data))
			if timelineString(part["type"]) == "tool" {
				state := timelineObject(part["state"])
				if result := state["output"]; len(result) > 0 {
					out = append(out, timelineTurn("tool", "tool_result", "", name, timelineText(result), result))
				}
			}
		}
	}
	return out, nil
}

func parseTimelineHermes(ctx context.Context, src TimelineSource, nativeID string) ([]Turn, error) {
	path, id, _ := strings.Cut(src.Path, "#")
	if nativeID != "" {
		id = nativeID
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id,role,COALESCE(content,''),COALESCE(tool_calls,''),COALESCE(tool_name,''),COALESCE(tool_call_id,''),timestamp,COALESCE(compacted,0) FROM messages WHERE session_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		var rowID, compact int64
		var role, content, calls, name, callID string
		var ts float64
		if err := rows.Scan(&rowID, &role, &content, &calls, &name, &callID, &ts, &compact); err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(map[string]any{"id": rowID, "role": role, "content": content, "tool_calls": calls, "tool_name": name, "tool_call_id": callID, "timestamp": ts, "compacted": compact})
		stamp := fmt.Sprintf("%.3f", ts)
		kind := "message"
		if compact == 1 {
			kind = "compaction"
		}
		if role == "tool" {
			kind = "tool_result"
		}
		if role != "user" && role != "assistant" && role != "tool" {
			role, kind = "system", "other"
		}
		out = append(out, timelineTurn(role, kind, stamp, name, content, raw))
		if calls != "" {
			for _, call := range timelineArray(json.RawMessage(calls)) {
				c := timelineObject(call)
				fn := timelineObject(c["function"])
				tool := timelineString(fn["name"])
				out = append(out, timelineTurn("assistant", timelineToolKind(tool), stamp, tool, timelineToolText(tool, fn["arguments"]), call))
			}
		}
	}
	return out, rows.Err()
}
