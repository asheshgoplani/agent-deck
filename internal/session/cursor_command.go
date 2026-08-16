package session

import "strings"

// DefaultCursorCommand returns the host's preferred Cursor Agent CLI invocation.
//
// Cursor ships two entrypoints for the same agent TUI:
//   - standalone `agent` (cursor-agent install under ~/.local/bin)
//   - IDE shim `cursor agent` (Cursor.app's `cursor` binary on PATH)
//
// Prefer the standalone binary when present: many CLI-only installs never put
// the IDE shim on PATH, and launching the hardcoded `cursor agent` then exits
// immediately with "command not found: cursor".
func DefaultCursorCommand() string {
	if _, err := lookPathFn("agent"); err == nil {
		return "agent"
	}
	return "cursor agent"
}

// isDefaultCursorInvocation reports whether cmd is one of the known default
// Cursor launch forms (empty, tool id, or either stock entrypoint). Custom
// invocations with extra flags stay untouched.
func isDefaultCursorInvocation(cmd string) bool {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "", "cursor", "cursor agent", "agent":
		return true
	default:
		return false
	}
}

// cursorCommandInstalled reports whether Cursor is available for
// show_only_installed_tools.
//
// When [cursor].command is unset, either stock entrypoint (`agent` or `cursor`)
// counts. When the user set an explicit override, only that command is probed
// so a stock-looking override like command = "agent" cannot be marked installed
// just because the other entrypoint exists.
// (env_file does not affect install presence.)
func cursorCommandInstalled() bool {
	config, _ := LoadUserConfig()
	override := ""
	if config != nil {
		override = strings.TrimSpace(config.Cursor.Command)
	}
	if override == "" {
		return probeInstalled("agent") || probeInstalled("cursor")
	}
	return probeCursorOverride(override)
}

// probeCursorOverride probes an explicit [cursor].command. Multi-word values
// probe the first token so "cursor agent" does not inherit probeInstalled's
// whitespace-wrapper "always installed" heuristic.
func probeCursorOverride(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	if len(fields) > 1 {
		return probeInstalled(fields[0])
	}
	return probeInstalled(cmd)
}
