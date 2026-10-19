package session

import "github.com/asheshgoplani/agent-deck/internal/sandboxguard"

// sandboxedToolDir returns dir unchanged, except under go test, where a tool
// config dir that resolves inside the real home panics instead of letting the
// test read or write the live ~/.claude, ~/.codex, ~/.gemini, ... (the test
// isolation class of #2001 and the CLAUDE_CONFIG_DIR leak). No-op in the real
// binary. See internal/sandboxguard.
func sandboxedToolDir(kind, dir string) string {
	if dir == "" {
		return dir
	}
	return sandboxguard.Must(kind, dir)
}
