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
