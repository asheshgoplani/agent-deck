package session

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
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
	FieldPlugins            = "plugins"
	FieldExtraArgs          = "extra-args"
	FieldColor              = "color"
	FieldNotes              = "notes"
	FieldClaudeSessionID    = "claude-session-id"
	FieldGeminiSessionID    = "gemini-session-id"
	FieldOpenCodeSessionID  = "opencode-session-id"
	FieldCodexSessionID     = "codex-session-id"
	FieldTitleLocked        = "title-locked"
	FieldNoTransitionNotify = "no-transition-notify"
	FieldSkipPermissions    = "skip-permissions"
	FieldAutoMode           = "auto-mode"
	FieldAccount            = "account"      // #924 per-session named account slot
	FieldIdleTimeout        = "idle-timeout" // #1143 auto-stop dormant sessions
)

var ValidMutableFields = []string{
	FieldTitle,
	FieldPath,
	FieldCommand,
	FieldTool,
	FieldWrapper,
	FieldChannels,
	FieldPlugins,
	FieldExtraArgs,
	FieldColor,
	FieldNotes,
	FieldClaudeSessionID,
	FieldGeminiSessionID,
	FieldOpenCodeSessionID,
	FieldCodexSessionID,
	FieldTitleLocked,
	FieldNoTransitionNotify,
	FieldSkipPermissions,
	FieldAutoMode,
	FieldAccount,
	FieldIdleTimeout,
}

type FieldRestartPolicy int

const (
	FieldLive FieldRestartPolicy = iota
	FieldRestartRequired
)

func RestartPolicyFor(field string) FieldRestartPolicy {
	switch field {
	case FieldCommand, FieldWrapper, FieldTool, FieldChannels, FieldPlugins, FieldExtraArgs, FieldPath,
		FieldSkipPermissions, FieldAutoMode, FieldAccount:
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

var (
	openCodeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	codexSessionIDPattern    = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
)

func normalizeToolSessionID(field, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	switch field {
	case FieldOpenCodeSessionID:
		if !openCodeSessionIDPattern.MatchString(trimmed) {
			return "", &MutationError{
				Field: field,
				Msg:   fmt.Sprintf("invalid opencode session id %q — expected a shell-safe identifier", trimmed),
			}
		}
	case FieldCodexSessionID:
		if !codexSessionIDPattern.MatchString(trimmed) {
			return "", &MutationError{
				Field: field,
				Msg:   fmt.Sprintf("invalid codex session id %q — expected UUID format", trimmed),
			}
		}
	}
	return trimmed, nil
}

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
		// Leaving claude → drop encoded ClaudeOptions so a same-submit
		// skip/auto toggle (Tool applies last) doesn't leave ghost flags
		// for a future shell→claude switch. UnmarshalClaudeOptions
		// returns nil for non-claude wrappers, so other tools' options
		// pass through.
		if !IsClaudeCompatible(value) {
			if opts, _ := UnmarshalClaudeOptions(inst.ToolOptionsJSON); opts != nil {
				inst.ToolOptionsJSON = nil
			}
		}

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

	case FieldPlugins:
		// RFC docs/rfc/PLUGIN_ATTACH.md §4.5. Catalog-only validation:
		// every name must resolve via GetPluginDef. Telegram-official
		// is filtered at catalog load (§6) so this branch will reject
		// it as "not in catalog".
		if inst.Tool != "claude" {
			return "", nil, &MutationError{
				Field: field,
				Msg:   fmt.Sprintf("plugins only supported for claude sessions (this session's tool is %q); plugins are Claude Code enabledPlugins entries applied per-session via the worker scratch settings.json", inst.Tool),
			}
		}
		oldValue = strings.Join(inst.Plugins, ",")
		parsed := []string{}
		for _, raw := range strings.Split(value, ",") {
			s := strings.TrimSpace(raw)
			if s == "" {
				continue
			}
			if IsTelegramOfficialRefusal(s, telegramOfficialRefusalSource) || s == "telegram@"+telegramOfficialRefusalSource {
				return oldValue, nil, &MutationError{
					Field: field,
					Msg:   fmt.Sprintf("plugin %q refused in v1: telegram@claude-plugins-official cannot be enabled via plugins. Use channels instead. See docs/rfc/PLUGIN_TELEGRAM_RETROFIT.md (planned) for the deferred refactor", s),
				}
			}
			if def := GetPluginDef(s); def == nil {
				available := GetAvailablePluginNames()
				if len(available) == 0 {
					return oldValue, nil, &MutationError{
						Field: field,
						Msg:   fmt.Sprintf("plugin %q: catalog is empty. Add a [plugins.%s] table to ~/.agent-deck/config.toml", s, s),
					}
				}
				return oldValue, nil, &MutationError{
					Field: field,
					Msg:   fmt.Sprintf("plugin %q: not in catalog. Available: %s", s, strings.Join(available, ", ")),
				}
			}
			parsed = append(parsed, s)
		}
		inst.Plugins = parsed

		// Channel auto-link reconciliation (RFC §4.7, fixes G4+C2).
		// syncPluginChannels handles the opt-out case internally — when
		// PluginChannelLinkDisabled is true, it still removes stale
		// auto-linked channels (otherwise toggling the flag mid-session
		// would leak channels). Always call.
		syncPluginChannels(inst)

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
		// Issue #923 (reporter @bautrey): when the user explicitly clears
		// the session id, the hook .sid sidecar at
		// `~/.agent-deck/hooks/<id>.sid` must also be removed. Otherwise
		// the next restart's spawn-env construction reads the stale anchor
		// via ReadHookSessionAnchor and re-injects the old id, undoing the
		// clear. DB is authoritative for the empty case; empty means
		// abandon, not "fall back to last seen".
		if value == "" {
			ClearHookSessionAnchor(inst.ID)
		}

	case FieldGeminiSessionID:
		oldValue = inst.GeminiSessionID
		inst.GeminiSessionID = value
		inst.GeminiDetectedAt = time.Now()
		postCommit = makeSessionEnvPostCommit(inst, "GEMINI_SESSION_ID", value)

	case FieldOpenCodeSessionID:
		oldValue = inst.OpenCodeSessionID
		normalized, err := normalizeToolSessionID(field, value)
		if err != nil {
			return oldValue, nil, err
		}
		inst.OpenCodeSessionID = normalized
		inst.OpenCodeDetectedAt = time.Now()

	case FieldCodexSessionID:
		oldValue = inst.CodexSessionID
		normalized, err := normalizeToolSessionID(field, value)
		if err != nil {
			return oldValue, nil, err
		}
		inst.CodexSessionID = normalized
		inst.CodexDetectedAt = time.Now()

	case FieldTitleLocked:
		oldValue = strconv.FormatBool(inst.TitleLocked)
		b, perr := parseFieldBool(value)
		if perr != nil {
			return oldValue, nil, &MutationError{Field: field, Msg: perr.Error()}
		}
		inst.TitleLocked = b

	case FieldNoTransitionNotify:
		oldValue = strconv.FormatBool(inst.NoTransitionNotify)
		b, perr := parseFieldBool(value)
		if perr != nil {
			return oldValue, nil, &MutationError{Field: field, Msg: perr.Error()}
		}
		inst.NoTransitionNotify = b

	case FieldSkipPermissions:
		oldValue, err = setClaudeOptionBool(inst, field, value,
			func(o *ClaudeOptions) bool { return o.SkipPermissions },
			func(o *ClaudeOptions, b bool) { o.SkipPermissions = b })
		if err != nil {
			return oldValue, nil, err
		}

	case FieldAutoMode:
		oldValue, err = setClaudeOptionBool(inst, field, value,
			func(o *ClaudeOptions) bool { return o.AutoMode },
			func(o *ClaudeOptions, b bool) { o.AutoMode = b })
		if err != nil {
			return oldValue, nil, err
		}

	case FieldAccount:
		// #924 per-session named account slot. Stored verbatim; an
		// unconfigured name silently falls through the resolver chain.
		// Empty string clears the override (back to conductor/group/env).
		// Restart required (see RestartPolicyFor) — the in-flight
		// conversation is lost, that's the documented Option 1 tradeoff.
		oldValue = inst.Account
		inst.Account = strings.TrimSpace(value)

	case FieldIdleTimeout:
		// #1143: parses a Go duration like "30m"; 0 (or "0", "") disables.
		// Live: the next watcher tick reads the new value.
		oldValue = strconv.FormatInt(inst.IdleTimeoutSecs, 10)
		secs, perr := ParseIdleTimeoutFlag(strings.TrimSpace(value))
		if perr != nil {
			return oldValue, nil, &MutationError{Field: field, Msg: perr.Error()}
		}
		inst.IdleTimeoutSecs = secs

	default:
		return "", nil, &MutationError{
			Field: field,
			Msg:   fmt.Sprintf("invalid field: %s\nValid fields: %s", field, strings.Join(ValidMutableFields, ", ")),
		}
	}
	return oldValue, postCommit, nil
}

// setClaudeOptionBool flips a single bool inside the ClaudeOptions JSON
// wrapper. Empty wrapper → fresh ClaudeOptions{}, so legacy sessions
// (created before any options panel touched them) get a populated blob.
// Rejects on non-claude tools, since the launcher would never read it.
func setClaudeOptionBool(inst *Instance, field, value string, get func(*ClaudeOptions) bool, set func(*ClaudeOptions, bool)) (string, error) {
	if !IsClaudeCompatible(inst.Tool) {
		return "", &MutationError{
			Field: field,
			Msg:   fmt.Sprintf("%s only supported for claude-compatible tools (this session's tool is %q)", field, inst.Tool),
		}
	}
	opts, err := UnmarshalClaudeOptions(inst.ToolOptionsJSON)
	if err != nil {
		return "", &MutationError{Field: field, Msg: fmt.Sprintf("failed to read existing claude options: %v", err)}
	}
	if opts == nil {
		opts = &ClaudeOptions{}
	}
	oldVal := strconv.FormatBool(get(opts))
	b, perr := parseFieldBool(value)
	if perr != nil {
		return oldVal, &MutationError{Field: field, Msg: perr.Error()}
	}
	set(opts, b)
	raw, merr := MarshalToolOptions(opts)
	if merr != nil {
		return oldVal, &MutationError{Field: field, Msg: fmt.Sprintf("failed to serialize claude options: %v", merr)}
	}
	inst.ToolOptionsJSON = json.RawMessage(raw)
	return oldVal, nil
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
