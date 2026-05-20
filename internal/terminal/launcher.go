// Package terminal provides a thin abstraction for spawning a new terminal
// window that attaches to an existing tmux session.
//
// This is used by the TUI's Shift+Enter binding to "pop out" an agent-deck
// session into its own native terminal window (e.g. a fresh iTerm2 window on
// macOS), leaving agent-deck running undisturbed in the original window.
//
// The cross-platform surface is intentionally tiny: callers pass the
// destination tmux session name (and optional `-L <socket>` selector) plus a
// hint at which terminal program they would like, and the platform-specific
// implementation does the rest. When a platform has no implementation, the
// stub returns ErrUnsupported so callers can show a friendly fallback.
package terminal

import (
	"errors"
	"strings"
)

// ErrUnsupported is returned by OpenSessionInNewWindow on platforms that have
// no native implementation yet. Callers should surface a non-fatal message
// rather than treating this as an error condition.
var ErrUnsupported = errors.New("terminal: opening a new window is not yet supported on this platform")

// AttachRequest describes the tmux session a new terminal window should
// attach to once spawned.
//
// Name is required. SocketName may be empty (meaning the default tmux
// server), matching the semantics of tmux.Session.SocketName.
type AttachRequest struct {
	// Name is the tmux session name (the `-t` argument of `tmux attach`).
	Name string

	// SocketName is the optional `-L <socket>` selector. Empty means the
	// default server.
	SocketName string

	// Terminal is an optional hint for which native terminal to use
	// (e.g. "iterm2"). Empty means "use the platform default".
	Terminal string
}

// BuildAttachCommand returns the shell command string that, when executed
// inside a fresh terminal window, attaches to the requested tmux session.
//
// It is exported (and pure) so platform implementations and tests can share
// the exact same string-building logic without depending on os/exec.
func BuildAttachCommand(req AttachRequest) string {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("tmux")
	if s := strings.TrimSpace(req.SocketName); s != "" {
		b.WriteString(" -L ")
		b.WriteString(shellQuote(s))
	}
	b.WriteString(" attach -t ")
	b.WriteString(shellQuote(name))
	return b.String()
}

// shellQuote single-quotes s for safe use in a /bin/sh command. It is
// intentionally simple: tmux session names are sanitized upstream (see
// internal/tmux.sanitizeName) so the input is already alphanumeric-ish, but
// we still quote defensively in case future names allow spaces.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
