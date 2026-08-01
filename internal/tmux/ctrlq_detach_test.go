package tmux

import (
	"strings"
	"testing"
)

// TestCtrlQDetachIfShellFormat_ScopedToSessionPrefixNotOneSessionName is a
// regression test for #1808 (and its #1820 shell-injection fix).
//
// tmux key bindings live on the SERVER, one per socket, not on the session.
// The Ctrl+Q detach binding installed by Session.Start used to guard itself
// with `if-shell [ "#{session_name}" = "<one hardcoded session name>" ]`,
// re-installed by every Start() call. Because the binding is "-n -T root",
// tmux's root key table consumes Ctrl+Q on every client on the socket before
// it reaches the pane — so that guard only ever matched whichever session
// started most recently; every OTHER session sharing the socket hit the
// empty else-branch and Ctrl+Q was silently swallowed instead of detaching.
//
// The fix must not bake any single session's name into the guard, and the
// guard itself must be a pure tmux FORMAT (never a shell script — see
// TestCtrlQDetachBindArgs_UsesIfShellDetachClientDirectly): re-checked
// against the CURRENT client's session at keypress time and matched on the
// shared SessionPrefix, so (a) it stays correct for every agentdeck session
// sharing the socket regardless of Start() order, and (b) a session name can
// never reach a shell, so it can't be used to inject commands (#1820).
func TestCtrlQDetachIfShellFormat_ScopedToSessionPrefixNotOneSessionName(t *testing.T) {
	format := ctrlQDetachIfShellFormat()

	if !strings.Contains(format, SessionPrefix) {
		t.Fatalf("format does not reference SessionPrefix, so it can't recognize any agentdeck session sharing the socket: %q", format)
	}
	if !strings.Contains(format, "#{session_name}") {
		t.Fatalf("format must re-check the CURRENT client's session via #{session_name} at keypress time, not a name baked in once at bind time: %q", format)
	}
	if strings.ContainsAny(format, `'"`) {
		t.Fatalf("format must never quote the session name for a shell — it is a tmux FORMAT evaluated by the tmux server, not shell input; quoting here is exactly the #1820 injection shape: %q", format)
	}

	// The format must be identical no matter how many times it is generated
	// (Start() re-installs the binding for every session on the socket) — if
	// a caller ever starts interpolating a per-call session name again, the
	// socket-wide, single-session-guard bug (#1808) is back.
	if again := ctrlQDetachIfShellFormat(); again != format {
		t.Fatalf("format must be stable across calls, not vary per session: first=%q second=%q", format, again)
	}
}

// TestCtrlQDetachBindArgs_UsesIfShellDetachClientDirectly locks in that the
// binding is installed as a plain `if-shell -F ... detach-client` command —
// never routed through run-shell / an embedded shell script.
//
// A shell script re-introduces the #1820 injection surface: tmux expands
// FORMATS into the script text before /bin/sh ever parses it, so a session
// name containing shell metacharacters can break out of the substitution.
// Routing detach-client through a run-shell subprocess also makes tmux
// resolve "the client to detach" via a most-recently-active-client
// heuristic instead of the client that actually pressed the key — this
// asserts on the real args Session.Start feeds to bind-key so a regression
// back to run-shell can't slip past review unnoticed.
func TestCtrlQDetachBindArgs_UsesIfShellDetachClientDirectly(t *testing.T) {
	args := ctrlQDetachBindArgs()

	// Exact argv, in order — not just substring presence — so a reordered or
	// malformed arg list (e.g. -F landing after the format, or a stray extra
	// element) fails the test instead of slipping through on a loose
	// strings.Contains check.
	want := []string{"if-shell", "-F", ctrlQDetachIfShellFormat(), "detach-client", ""}
	if len(args) != len(want) {
		t.Fatalf("bind-key args have wrong shape: got %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("bind-key args[%d] = %q, want %q (full: got %v, want %v)", i, args[i], want[i], args, want)
		}
	}

	for _, a := range args {
		if strings.Contains(a, "run-shell") {
			t.Fatalf("binding must not use run-shell (reintroduces the #1820 shell-injection surface): %v", args)
		}
	}
}
