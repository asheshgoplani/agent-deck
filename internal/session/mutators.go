package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Field names accepted by SetField. Kept as raw strings to match the
// `agent-deck session set <field>` CLI surface verbatim.
const (
	FieldTitle              = "title"
	FieldPath               = "path"
	FieldCommand            = "command"
	FieldTool               = "tool"
	FieldWrapper            = "wrapper"
	FieldChannels           = "channels"
	FieldExtraArgs          = "extra-args"
	FieldColor              = "color"
	FieldNotes              = "notes"
	FieldClaudeSessionID    = "claude-session-id"
	FieldGeminiSessionID    = "gemini-session-id"
	FieldTitleLocked        = "title-locked"
	FieldNoTransitionNotify = "no-transition-notify"
)

var ValidMutableFields = []string{
	FieldTitle,
	FieldPath,
	FieldCommand,
	FieldTool,
	FieldWrapper,
	FieldChannels,
	FieldExtraArgs,
	FieldColor,
	FieldNotes,
	FieldClaudeSessionID,
	FieldGeminiSessionID,
	FieldTitleLocked,
	FieldNoTransitionNotify,
}

type FieldRestartPolicy int

const (
	FieldLive FieldRestartPolicy = iota
	FieldRestartRequired
)

func RestartPolicyFor(field string) FieldRestartPolicy {
	switch field {
	case FieldCommand, FieldWrapper, FieldTool, FieldChannels, FieldExtraArgs, FieldPath:
		return FieldRestartRequired
	default:
		return FieldLive
	}
}

type MutationError struct {
	Field string
	Msg   string
}

func (e *MutationError) Error() string { return e.Msg }

// SetField is the single source of truth for session metadata edits — both
// `agent-deck session set` and the TUI EditSessionDialog call it.
//
// postCommit is non-nil for fields that need a slow tmux subprocess
// (claude/gemini session-id env propagation). TUI callers must drop
// instancesMu before invoking it so the subprocess doesn't stall background
// readers; CLI callers run it inline.
//
// extraArgsTokens supplies pre-tokenized argv for FieldExtraArgs (CLI path);
// when nil, FieldExtraArgs falls back to strings.Fields(value) (TUI path).
//
// Persistence is the caller's responsibility.
func SetField(inst *Instance, field, value string, extraArgsTokens []string) (oldValue string, postCommit func(), err error) {
	switch field {
	case FieldTitle:
		oldValue = inst.Title
		inst.Title = value
		inst.SyncTmuxDisplayName()

	case FieldPath:
		oldValue = inst.ProjectPath
		inst.ProjectPath = value

	case FieldCommand:
		oldValue = inst.Command
		inst.Command = value

	case FieldTool:
		oldValue = inst.Tool
		inst.Tool = value

	case FieldWrapper:
		oldValue = inst.Wrapper
		inst.Wrapper = value

	case FieldNotes:
		oldValue = inst.Notes
		inst.Notes = value

	case FieldColor:
		oldValue = inst.Color
		trimmed := strings.TrimSpace(value)
		if !IsValidSessionColor(trimmed) {
			return oldValue, nil, &MutationError{
				Field: field,
				Msg:   fmt.Sprintf("invalid color %q — expected '#RRGGBB', ANSI '0'..'255', or '' to clear", trimmed),
			}
		}
		inst.Color = trimmed

	case FieldChannels:
		if inst.Tool != "claude" {
			return "", nil, &MutationError{
				Field: field,
				Msg:   fmt.Sprintf("channels only supported for claude sessions (this session's tool is %q); requires --channels on the claude binary", inst.Tool),
			}
		}
		oldValue = strings.Join(inst.Channels, ",")
		parsed := []string{}
		for _, raw := range strings.Split(value, ",") {
			if s := strings.TrimSpace(raw); s != "" {
				parsed = append(parsed, s)
			}
		}
		inst.Channels = parsed

	case FieldExtraArgs:
		if inst.Tool != "claude" {
			return "", nil, &MutationError{
				Field: field,
				Msg:   fmt.Sprintf("extra-args only supported for claude sessions (this session's tool is %q); claude is the only tool whose builder appends user extra args", inst.Tool),
			}
		}
		oldValue = strings.Join(inst.ExtraArgs, " ")
		tokens := extraArgsTokens
		if tokens == nil && value != "" {
			tokens = strings.Fields(value)
		}
		cleaned := make([]string, 0, len(tokens))
		for _, tok := range tokens {
			if tok != "" {
				cleaned = append(cleaned, tok)
			}
		}
		if len(cleaned) == 0 {
			inst.ExtraArgs = nil
		} else {
			inst.ExtraArgs = cleaned
		}

	case FieldClaudeSessionID:
		oldValue = inst.ClaudeSessionID
		inst.ClaudeSessionID = value
		inst.ClaudeDetectedAt = time.Now()
		postCommit = makeSessionEnvPostCommit(inst, "CLAUDE_SESSION_ID", value)

	case FieldGeminiSessionID:
		oldValue = inst.GeminiSessionID
		inst.GeminiSessionID = value
		inst.GeminiDetectedAt = time.Now()
		postCommit = makeSessionEnvPostCommit(inst, "GEMINI_SESSION_ID", value)

	case FieldTitleLocked:
		oldValue = fmt.Sprintf("%t", inst.TitleLocked)
		b, perr := parseFieldBool(value)
		if perr != nil {
			return oldValue, nil, &MutationError{Field: field, Msg: perr.Error()}
		}
		inst.TitleLocked = b

	case FieldNoTransitionNotify:
		oldValue = fmt.Sprintf("%t", inst.NoTransitionNotify)
		b, perr := parseFieldBool(value)
		if perr != nil {
			return oldValue, nil, &MutationError{Field: field, Msg: perr.Error()}
		}
		inst.NoTransitionNotify = b

	default:
		return "", nil, &MutationError{
			Field: field,
			Msg:   fmt.Sprintf("invalid field: %s\nValid fields: %s", field, strings.Join(ValidMutableFields, ", ")),
		}
	}
	return oldValue, postCommit, nil
}

// makeSessionEnvPostCommit returns a closure that propagates the new session
// ID to a running tmux session via `tmux set-environment`. nil when no
// tmux session is bound; captures sess+socket+value so the closure can run
// after the caller drops instancesMu.
func makeSessionEnvPostCommit(inst *Instance, envName, value string) func() {
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return nil
	}
	socket := inst.TmuxSocketName
	return func() {
		if tmuxSess.Exists() {
			_ = tmux.Exec(socket, "set-environment", "-t", tmuxSess.Name, envName, value).Run()
		}
	}
}

// IsValidSessionColor validates a per-session color tint (issue #391).
// Accepts "", "#RRGGBB" hex, or ANSI 256-palette decimal "0".."255".
func IsValidSessionColor(v string) bool {
	if v == "" {
		return true
	}
	if len(v) == 7 && v[0] == '#' {
		for i := 1; i < 7; i++ {
			c := v[i]
			ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !ok {
				return false
			}
		}
		return true
	}
	if len(v) == 0 || len(v) > 3 {
		return false
	}
	n := 0
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n >= 0 && n <= 255
}

func parseFieldBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off", "":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q — expected true/false", v)
}
