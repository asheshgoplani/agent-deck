package tmux

import (
	"strings"
	"testing"
)

// TestCtrlQDetachScript_ScopedToSessionPrefixNotOneSessionName is a
// regression test for #1808.
//
// tmux key bindings live on the SERVER, one per socket, not on the session.
// The Ctrl+Q detach binding installed by Session.Start used to guard itself
// with `if-shell [ "#{session_name}" = "<one hardcoded session name>" ]`,
// re-installed by every Start() call. That only ever matched whichever
// session started most recently on the socket — every OTHER session sharing
// that socket hit the empty else-branch, tmux consumed nothing, and Ctrl+Q
// fell through to the running app (killing Claude Code instead of
// detaching) as soon as a socket hosted more than one session.
//
// The fix (ctrlQDetachScript) must not bake any single session's name into
// the script; it must re-check the CURRENT client's session via
// #{session_name} at keypress time and match on the shared SessionPrefix,
// so it stays correct for every agentdeck session sharing the socket
// regardless of Start() order.
func TestCtrlQDetachScript_ScopedToSessionPrefixNotOneSessionName(t *testing.T) {
	script := ctrlQDetachScript()

	if !strings.Contains(script, SessionPrefix) {
		t.Fatalf("script does not reference SessionPrefix, so it can't recognize any agentdeck session sharing the socket: %q", script)
	}
	if !strings.Contains(script, "#{session_name}") {
		t.Fatalf("script must re-check the CURRENT client's session via #{session_name} at keypress time, not a name baked in once at bind time: %q", script)
	}
	if !strings.Contains(script, "detach-client") {
		t.Fatalf("script must actually detach the invoking client: %q", script)
	}

	// The script must be identical no matter how many times it is generated
	// (Start() re-installs it for every session on the socket) — if a caller
	// ever starts interpolating a per-call session name again, the
	// socket-wide, single-session-guard bug (#1808) is back.
	if again := ctrlQDetachScript(); again != script {
		t.Fatalf("script must be stable across calls, not vary per session: first=%q second=%q", script, again)
	}
}
