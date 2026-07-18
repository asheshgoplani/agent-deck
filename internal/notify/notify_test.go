package notify

import "testing"

func TestBell_DoesNotPanic(t *testing.T) {
	// No controlling tty in CI: Bell must be a silent no-op, never panic.
	Bell()
}

func TestDesktop_DoesNotPanicOrBlock(t *testing.T) {
	// Best-effort; must return promptly and never panic even with no notifier.
	Desktop("agent-hopdeck test", "body")
}

func TestDesktop_DoesNotPanicOrBlock_WithBackslashesAndQuotes(t *testing.T) {
	// A title/body containing backslashes and quotes (e.g. a Windows-style
	// path or an escaped string) must not panic or block, even though on
	// darwin these characters are embedded in an AppleScript string literal.
	Desktop(`agent-hopdeck test \"quoted\"`, `C:\Users\test\path with "quotes"`)
}
