package session

import "strings"

// hookStatusToolNames is the registry of exact tool names whose session status
// is driven by lifecycle hook events written to ~/.agent-deck/hooks/<id>.json
// by `agent-deck hook-handler`. The Claude- and Codex-compatible families are
// not listed here: they stay predicate-based because a user-defined tool can
// join them via compatible_with = "claude"/"codex".
//
// This list used to be spelled out inline at each call site, so adding a tool
// meant finding every copy. #2222 was exactly that miss: pi emitted hook
// events that nothing consumed. New hook-emitting tools belong here and
// nowhere else.
var hookStatusToolNames = map[string]bool{
	"gemini": true,
	"hermes": true,
	"cursor": true,
	// pi (pi-coding-agent) emits through the agent-deck extension that
	// `agent-deck pi-hooks install` writes into pi's extensions dir (#2222).
	"pi": true,
}

// HookStatusTool reports whether tool publishes lifecycle hook events that
// agent-deck consumes as an authoritative status signal. When it does, the
// hook fast path in UpdateStatus (and the CLI cold load that feeds it) prefers
// a FRESH hook sample over pane-regex content detection; when the sample is
// missing or stale, control falls through to content detection as before.
func HookStatusTool(tool string) bool {
	if IsClaudeCompatible(tool) || IsCodexCompatible(tool) {
		return true
	}
	return hookStatusToolNames[strings.ToLower(strings.TrimSpace(tool))]
}
