package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Tool is one MCP tool (the HTTP routes mirror the same set).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// Scope is the scope the tool needs; "" means any valid token.
	Scope Scope `json:"-"`
	// Write marks tools counted against the write rate limit.
	Write bool `json:"-"`
}

// Tool names.
const (
	ToolWhoami        = "whoami"
	ToolListSessions  = "list_sessions"
	ToolSessionStatus = "session_status"
	ToolReadOutput    = "read_output"
	ToolSendMessage   = "send_message"
	ToolApprove       = "approve"
	ToolLaunchSession = "launch_session"
)

// LaunchTools are the agent tools launch_session accepts (-c). A free-form
// command is never accepted.
var LaunchTools = []string{"claude", "codex", "gemini", "opencode", "pi", "omp", "copilot", "crush", "cursor", "hermes", "deepseek"}

// maxMessageBytes bounds a send_message / launch_session message.
const maxMessageBytes = 64 << 10

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func objSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// Tools is the full tool list in a stable order. There is no stop, remove,
// restart or kill tool, on purpose.
func Tools() []Tool {
	sessionProp := strProp("Session id or title, as returned by list_sessions")
	return []Tool{
		{
			Name:        ToolWhoami,
			Description: "Who this connection is: its name, the scopes it was granted and its rate limits. Call it first if unsure what you may do.",
			InputSchema: objSchema(map[string]any{}),
		},
		{
			Name:        ToolListSessions,
			Description: "List agent-deck sessions (id, title, tool, status, group, path). Status is one of running, waiting, idle, error, stopped. Needs scope read.",
			InputSchema: objSchema(map[string]any{
				"group":  strProp("Only sessions in this group (or its subgroups)"),
				"status": strProp("Only sessions with this status: running, waiting, idle, error, stopped"),
			}),
			Scope: ScopeRead,
		},
		{
			Name:        ToolSessionStatus,
			Description: "Details of one session: status, tool, path, group, parent, queued sends. Needs scope read.",
			InputSchema: objSchema(map[string]any{"session": sessionProp}, "session"),
			Scope:       ScopeRead,
		},
		{
			Name:        ToolReadOutput,
			Description: "The last response of a session's agent (long output keeps its start and end). Needs scope read.",
			InputSchema: objSchema(map[string]any{"session": sessionProp}, "session"),
			Scope:       ScopeRead,
		},
		{
			Name:        ToolSendMessage,
			Description: "Send a message to a running session, as if the user typed it. ASK THE USER FIRST and show them the message. Needs scope send.",
			InputSchema: objSchema(map[string]any{"session": sessionProp, "message": strProp("The text to send")}, "session", "message"),
			Scope:       ScopeSend,
			Write:       true,
		},
		{
			Name:        ToolApprove,
			Description: "Answer the approval prompt currently visible in a session (Codex). choice: once (default), always, session, or a displayed option number. ASK THE USER FIRST. Needs scope approve.",
			InputSchema: objSchema(map[string]any{"session": sessionProp, "choice": strProp("once, always, session, or an option number 1-9")}, "session"),
			Scope:       ScopeApprove,
			Write:       true,
		},
		{
			Name:        ToolLaunchSession,
			Description: "Start a new session in a project folder, optionally with a first message. ASK THE USER FIRST. tool is one of " + strings.Join(LaunchTools, ", ") + " (default claude). Needs scope launch.",
			InputSchema: objSchema(map[string]any{
				"path":    strProp("Absolute path of the project folder"),
				"title":   strProp("Session title"),
				"tool":    strProp("Agent to run: " + strings.Join(LaunchTools, ", ")),
				"message": strProp("Optional first message"),
			}, "path", "title"),
			Scope: ScopeLaunch,
			Write: true,
		},
	}
}

// LookupTool finds a tool by name.
func LookupTool(name string) (Tool, bool) {
	for _, t := range Tools() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// toolArgs are the decoded arguments of a call.
type toolArgs map[string]any

func decodeArgs(raw json.RawMessage) (toolArgs, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return toolArgs{}, nil
	}
	var a toolArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
	}
	if a == nil {
		a = toolArgs{}
	}
	return a, nil
}

func (a toolArgs) str(key string) (string, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

// sessionRef validates a session id or title: required, single line, not
// flag-shaped, at most 256 bytes.
func sessionRef(a toolArgs) (string, error) {
	s, err := a.str("session")
	if err != nil {
		return "", err
	}
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", errors.New("session is required")
	case strings.HasPrefix(s, "-"):
		return "", errors.New("session must not start with '-'")
	case strings.ContainsAny(s, "\r\n\x00"):
		return "", errors.New("session must be one line")
	case len(s) > 256:
		return "", errors.New("session is too long")
	}
	return s, nil
}

func validMessage(s string, required bool) (string, error) {
	if strings.TrimSpace(s) == "" {
		if required {
			return "", errors.New("message is required")
		}
		return "", nil
	}
	if len(s) > maxMessageBytes {
		return "", fmt.Errorf("message is larger than %d bytes", maxMessageBytes)
	}
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return "", errors.New("message must be valid UTF-8 text")
	}
	return s, nil
}

// sessionSummary is the trimmed list_sessions row.
type sessionSummary struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Tool   string `json:"tool"`
	Status string `json:"status"`
	Group  string `json:"group"`
	Path   string `json:"path"`
}

// invalidArgs marks an error as the caller's mistake.
type invalidArgs struct{ error }

func badArgs(err error) error { return invalidArgs{err} }

// runTool executes tool for rec. The returned value is JSON-encodable data,
// or a string for plain-text output.
func (s *Service) runTool(ctx context.Context, rec Record, tool Tool, a toolArgs) (any, string, error) {
	switch tool.Name {
	case ToolWhoami:
		return map[string]any{
			"name":      rec.Name,
			"assistant": rec.Assistant,
			"scopes":    rec.Scopes,
			"profile":   rec.Profile,
			"limits":    s.Limits,
			"tools":     toolNamesFor(rec.Scopes),
		}, "", nil

	case ToolListSessions:
		group, err := a.str("group")
		if err != nil {
			return nil, "", badArgs(err)
		}
		status, err := a.str("status")
		if err != nil {
			return nil, "", badArgs(err)
		}
		out, err := s.Exec.Run(ctx, rec.Profile, []string{"list", "--json"}, nil)
		if err != nil {
			return nil, "", err
		}
		var rows []sessionSummary
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, "", fmt.Errorf("parsing list --json output: %w", err)
		}
		filtered := make([]sessionSummary, 0, len(rows))
		for _, r := range rows {
			if group != "" && r.Group != group && !strings.HasPrefix(r.Group, group+"/") {
				continue
			}
			if status != "" && !strings.EqualFold(r.Status, status) {
				continue
			}
			filtered = append(filtered, r)
		}
		return map[string]any{"sessions": filtered, "count": len(filtered)}, "", nil

	case ToolSessionStatus:
		ref, err := sessionRef(a)
		if err != nil {
			return nil, "", badArgs(err)
		}
		out, err := s.Exec.Run(ctx, rec.Profile, []string{"session", "show", "--json", ref}, nil)
		if err != nil {
			return nil, ref, err
		}
		var v map[string]any
		if err := json.Unmarshal(out, &v); err != nil {
			return nil, ref, fmt.Errorf("parsing session show --json output: %w", err)
		}
		return v, ref, nil

	case ToolReadOutput:
		ref, err := sessionRef(a)
		if err != nil {
			return nil, "", badArgs(err)
		}
		out, err := s.Exec.Run(ctx, rec.Profile, []string{"session", "output", ref}, nil)
		if err != nil {
			return nil, ref, err
		}
		return string(out), ref, nil

	case ToolSendMessage:
		ref, err := sessionRef(a)
		if err != nil {
			return nil, "", badArgs(err)
		}
		msg, err := a.str("message")
		if err == nil {
			msg, err = validMessage(msg, true)
		}
		if err != nil {
			return nil, ref, badArgs(err)
		}
		out, err := s.Exec.Run(ctx, rec.Profile, []string{"session", "send", "--json", "--message-file", "-", ref}, []byte(msg))
		if err != nil {
			return nil, ref, err
		}
		return jsonOrText(out), ref, nil

	case ToolApprove:
		ref, err := sessionRef(a)
		if err != nil {
			return nil, "", badArgs(err)
		}
		choice, err := a.str("choice")
		if err != nil {
			return nil, ref, badArgs(err)
		}
		choice = strings.TrimSpace(choice)
		if choice == "" {
			choice = "once"
		}
		if !validChoice(choice) {
			return nil, ref, badArgs(errors.New("choice must be once, always, session or a number 1-9"))
		}
		out, err := s.Exec.Run(ctx, rec.Profile, []string{"session", "approve", "--json", "--choice", choice, ref}, nil)
		if err != nil {
			return nil, ref, err
		}
		return jsonOrText(out), ref, nil

	case ToolLaunchSession:
		path, err := a.str("path")
		if err != nil {
			return nil, "", badArgs(err)
		}
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
			return nil, "", badArgs(errors.New("path must be an absolute folder path"))
		}
		title, err := a.str("title")
		if err != nil {
			return nil, "", badArgs(err)
		}
		title = strings.TrimSpace(title)
		if title == "" || strings.HasPrefix(title, "-") || strings.ContainsAny(title, "\r\n\x00") || len(title) > 200 {
			return nil, "", badArgs(errors.New("title is required: one line, not starting with '-', at most 200 bytes"))
		}
		toolName, err := a.str("tool")
		if err != nil {
			return nil, "", badArgs(err)
		}
		toolName = strings.ToLower(strings.TrimSpace(toolName))
		if toolName == "" {
			toolName = "claude"
		}
		if !contains(LaunchTools, toolName) {
			return nil, "", badArgs(fmt.Errorf("tool must be one of %s", strings.Join(LaunchTools, ", ")))
		}
		msg, err := a.str("message")
		if err == nil {
			msg, err = validMessage(msg, false)
		}
		if err != nil {
			return nil, "", badArgs(err)
		}
		args := []string{"launch", "--json", "--no-parent", "-t", title, "-c", toolName}
		var stdin []byte
		if msg != "" {
			args = append(args, "--message-file", "-")
			stdin = []byte(msg)
		}
		args = append(args, filepath.Clean(path))
		out, err := s.Exec.Run(ctx, rec.Profile, args, stdin)
		if err != nil {
			return nil, title, err
		}
		return jsonOrText(out), title, nil
	}
	return nil, "", badArgs(fmt.Errorf("unknown tool %q", tool.Name))
}

func validChoice(c string) bool {
	switch c {
	case "once", "always", "session":
		return true
	}
	return len(c) == 1 && c[0] >= '1' && c[0] <= '9'
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// jsonOrText returns decoded JSON when out is JSON, else the trimmed text.
func jsonOrText(out []byte) any {
	var v any
	if err := json.Unmarshal(out, &v); err == nil {
		return v
	}
	return strings.TrimSpace(string(out))
}

// toolNamesFor lists the tools scopes allow.
func toolNamesFor(scopes []Scope) []string {
	var names []string
	for _, t := range Tools() {
		if t.Scope == "" || HasScope(scopes, t.Scope) {
			names = append(names, t.Name)
		}
	}
	return names
}
