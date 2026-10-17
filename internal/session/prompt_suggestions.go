// Claude Code prompt suggestions in sessions agent-deck launches.
//
// Claude Code can paint a predicted next user message as dim placeholder
// text in an empty composer; Tab or Right copies it into the input and the
// next Enter submits it. Nobody reads a worker's composer, so in a session
// agent-deck launches under a parent (a worker, a conductor's child) the
// suggestion is only a hazard for automated keys. Those sessions start with
// CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false, which Claude Code honours ahead
// of its own promptSuggestionEnabled setting. Root sessions, the ones the
// owner attaches to, are left alone. The send guard in internal/send stays on
// regardless: a plugin prompt.suggest hook can still paint ghost text.
//
// Precedence: session (`session set <id> prompt-suggestions`) >
// [profiles.<profile>.claude].prompt_suggestions > [launch].prompt_suggestions
// > children-off. Profiles have no launch section, so the profile override
// stays under [profiles.<profile>.claude]. The session override lives in the
// tool_data extras zone, like context_level, so no schema migration is needed.
package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	PromptSuggestionsChildrenOff = "children-off" // off for parented sessions, Claude's own setting for root sessions
	PromptSuggestionsOn          = "on"           // never touch the env; Claude's own setting decides
	PromptSuggestionsOff         = "off"          // always start with suggestions disabled

	promptSuggestionEnvVar       = "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"
	promptSuggestionOffEnv       = promptSuggestionEnvVar + "=false"
	toolDataPromptSuggestionsKey = "prompt_suggestions"
)

// NormalizePromptSuggestions canonicalizes a prompt-suggestions value
// (case-insensitive; true/false, yes/no and 1/0 are accepted as on/off).
// An empty value is rejected; callers that treat "" as unset check first.
func NormalizePromptSuggestions(s string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case PromptSuggestionsChildrenOff:
		return v, nil
	case PromptSuggestionsOn, "true", "yes", "1":
		return PromptSuggestionsOn, nil
	case PromptSuggestionsOff, "false", "no", "0":
		return PromptSuggestionsOff, nil
	default:
		return "", fmt.Errorf("invalid prompt-suggestions value %q: expected one of children-off, on, off", s)
	}
}

// firstPromptSuggestionsMode returns the first value, in precedence order,
// that normalizes to a mode, or children-off. An unset or malformed value
// (hand-edited config) falls through to the next.
func firstPromptSuggestionsMode(values ...string) string {
	for _, v := range values {
		if mode, err := NormalizePromptSuggestions(v); err == nil {
			return mode
		}
	}
	return PromptSuggestionsChildrenOff
}

// promptSuggestionsMode resolves the effective mode for this session. The
// config is read only when the session has no valid override of its own.
func (i *Instance) promptSuggestionsMode() string {
	if mode, err := NormalizePromptSuggestions(i.PromptSuggestions); err == nil {
		return mode
	}
	cfg, _ := LoadUserConfig()
	if cfg == nil {
		return PromptSuggestionsChildrenOff
	}
	profile := cfg.Profiles[resolvedProcessProfile()].Claude.PromptSuggestions
	return firstPromptSuggestionsMode(profile, cfg.Launch.PromptSuggestions)
}

// PromptSuggestionsDisabled reports whether this session's Claude is started
// with prompt suggestions turned off.
func (i *Instance) PromptSuggestionsDisabled() bool {
	if i == nil || !IsClaudeCompatible(i.Tool) {
		return false
	}
	switch i.promptSuggestionsMode() {
	case PromptSuggestionsOff:
		return true
	case PromptSuggestionsOn:
		return false
	default:
		return i.ParentSessionID != ""
	}
}

// promptSuggestionExport is the launch-env export for the shared bash prefix
// (start, restart, resume and fork all build through buildBashExportPrefix).
func (i *Instance) promptSuggestionExport() string {
	if !i.PromptSuggestionsDisabled() {
		return ""
	}
	return "export " + promptSuggestionOffEnv + "; "
}

// WritePromptSuggestionsToToolData merges the per-session override into the
// tool_data blob, removing the key when mode is empty. Sibling keys survive.
func WritePromptSuggestionsToToolData(td json.RawMessage, mode string) json.RawMessage {
	return writeToolDataString(td, toolDataPromptSuggestionsKey, mode)
}

// ReadPromptSuggestionsFromToolData returns the per-session override, or ""
// (unset) for missing, malformed or legacy rows.
func ReadPromptSuggestionsFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		PromptSuggestions string `json:"prompt_suggestions"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.PromptSuggestions
}
