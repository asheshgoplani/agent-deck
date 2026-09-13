package session

// builtinTool is the single source of truth for one canonical built-in tool.
//
// Before this file, the built-in list lived split across two hand-synced
// functions (issue #1258):
//   - detectTool()        in cmd/agent-deck/main.go  — the strings.Contains
//     heuristic dispatcher (command string -> tool name).
//   - isBuiltinToolName()  in internal/session/userconfig.go — the strict
//     allowlist used to stop custom [tools.<name>] entries
//     from shadowing a built-in.
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

	// Color is the brand color slot the TUI paints this tool with. It names a
	// theme palette slot ("orange", "purple", "cyan", "accent", "yellow",
	// "red") rather than a literal color so it follows live theme switches.
	// Empty means "no brand color": the UI falls back to its default dim text.
	// Mirrors the arms of the former ToolColor() switch in internal/ui/styles.go.
	Color string

	// detectSubstrings are case-insensitive strings.Contains fragments against
	// the command string — the exact arms of the legacy detectTool() switch.
	detectSubstrings []string

	// detectTokens are case-insensitive whitespace-delimited token matches, used
	// for short ambiguous names like "pi" where Contains would false-match
	// "epic"/"tapioca". Mirrors detectTool()'s hasCommandToken() arm.
	detectTokens []string

	// Installed reports whether this tool's command resolved on the host PATH at
	// registry-init time. It is ONLY populated when the show_only_installed_tools
	// filter is on (issue #1259); with the filter off the probe is skipped
	// entirely and this stays false (unused), so the default path is byte-identical
	// to before. "shell" is always marked installed regardless of the probe.
	Installed bool
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
		{Name: "claude", Icon: "🤖", Color: "orange", detectSubstrings: []string{"claude"}},
		{Name: "opencode", Icon: "🌐", detectSubstrings: []string{"opencode", "open-code"}},
		{Name: "gemini", Icon: "✨", Color: "purple", detectSubstrings: []string{"gemini"}},
		{Name: "codex", Icon: "💻", Color: "cyan", detectSubstrings: []string{"codex"}},
		{Name: "pi", Icon: "π", Color: "accent", detectTokens: []string{"pi"}},
		{Name: "copilot", Icon: "🐙", Color: "accent", detectSubstrings: []string{"copilot"}},
		{Name: "crush", Icon: "💘", Color: "purple", detectSubstrings: []string{"crush"}},
		// detectTokens includes bare "agent" (standalone Cursor Agent CLI).
		// Token match only: substring "agent" would false-match "agent-deck".
		{Name: "cursor", Icon: "📝", Color: "accent", detectSubstrings: []string{"cursor"}, detectTokens: []string{"agent"}},
		{Name: "hermes", Icon: "☤", Color: "yellow", detectSubstrings: []string{"hermes"}},
		// DeepSeek Harness. The tool is named for the vendor; the binary is
		// `dsh`, so the substring alone would never match a real command line.
		// "dsh" is a token match, not a substring: as a substring it would
		// false-match "dshell", "fdsh", and any path containing those three
		// letters — the same reason "pi" is token-matched.
		{Name: "deepseek", Icon: "🐋", Color: "cyan", detectSubstrings: []string{"deepseek"}, detectTokens: []string{"dsh"}},
		{Name: "aider", Icon: "🐚", Color: "red"},
		{Name: "shell", Icon: "🐚"},
	}
}
