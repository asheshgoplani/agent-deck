package session

// builtinTool is the single source of truth for one canonical built-in tool.
//
// Before this file, the built-in list lived split across two hand-synced
// functions (issue #1258):
//   - detectTool()        in cmd/agent-deck/main.go  — the strings.Contains
//                          heuristic dispatcher (command string -> tool name).
//   - isBuiltinToolName()  in internal/session/userconfig.go — the strict
//                          allowlist used to stop custom [tools.<name>] entries
//                          from shadowing a built-in.
//
// Both are now derived from the single builtinTools() slice below.
//
// Designed so issue #1259 (show-only-installed-tools) is a trivial follow-up:
// adding an `Installed bool` field here plus one os/exec.LookPath at registry
// init is all it takes — no second function to keep in sync.
type builtinTool struct {
	// Name is the canonical tool identity (what callers store as Instance.Tool).
	Name string

	// Icon is the emoji/symbol shown in the dialogs. Mirrors GetToolIcon()'s
	// built-in switch exactly (kept here as data for the registry; GetToolIcon
	// itself is intentionally not retargeted in this prototype to keep the diff
	// focused — see PR open questions).
	Icon string

	// detectSubstrings are case-insensitive strings.Contains fragments against
	// the command string — the exact arms of the legacy detectTool() switch.
	detectSubstrings []string

	// detectTokens are case-insensitive whitespace-delimited token matches, used
	// for short ambiguous names like "pi" where Contains would false-match
	// "epic"/"tapioca". Mirrors detectTool()'s hasCommandToken() arm.
	detectTokens []string
}

// builtinTools returns the canonical built-ins in the EXACT precedence order of
// the legacy detectTool() switch. Order is load-bearing: Registry.Match() walks
// this slice top-to-bottom and returns the first hit, so a command string that
// contains two tool names resolves identically to the old switch.
//
// Two deliberate asymmetries preserved verbatim from the legacy code:
//   - "aider" is a valid built-in NAME (isBuiltinToolName allowed it) but had NO
//     arm in detectTool() — so detectTool("aider") returned "shell". It carries
//     no detect patterns here, so Match() never returns "aider". (Open question
//     for upstream: is that intended, or should aider gain a detect arm?)
//   - "shell" is the catch-all fallback, never matched by a pattern.
func builtinTools() []builtinTool {
	return []builtinTool{
		{Name: "claude", Icon: "🤖", detectSubstrings: []string{"claude"}},
		{Name: "opencode", Icon: "🌐", detectSubstrings: []string{"opencode", "open-code"}},
		{Name: "gemini", Icon: "✨", detectSubstrings: []string{"gemini"}},
		{Name: "codex", Icon: "💻", detectSubstrings: []string{"codex"}},
		{Name: "pi", Icon: "π", detectTokens: []string{"pi"}},
		{Name: "copilot", Icon: "🐙", detectSubstrings: []string{"copilot"}},
		{Name: "crush", Icon: "💘", detectSubstrings: []string{"crush"}},
		{Name: "cursor", Icon: "📝", detectSubstrings: []string{"cursor"}},
		{Name: "hermes", Icon: "☤", detectSubstrings: []string{"hermes"}},
		{Name: "aider", Icon: "🐚"},
		{Name: "shell", Icon: "🐚"},
	}
}
