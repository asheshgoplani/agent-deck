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
// Name is required for local sessions. For remote sessions, Remote must
// be populated instead (Name then refers to the remote agent-deck session
// ID — what `agent-deck session attach <id>` expects on the other side).
// SocketName may be empty (meaning the default tmux server), matching the
// semantics of tmux.Session.SocketName.
type AttachRequest struct {
	// Name is the tmux session name (the `-t` argument of `tmux attach`)
	// for local sessions, or the remote agent-deck session ID when
	// Remote != nil.
	Name string

	// SocketName is the optional `-L <socket>` selector. Empty means the
	// default server. Ignored when Remote != nil.
	SocketName string

	// Terminal is an optional hint for which native terminal to use
	// (e.g. "iterm2"). Empty means "use the platform default".
	Terminal string

	// OpenAs controls whether the platform launcher opens a new tab or a
	// new window when both are supported (currently: iTerm2 on macOS).
	// Valid values: "tab", "window". Empty falls through to the platform
	// default, which is "tab" on macOS — matching iTerm's natural UX.
	// Issue #1100.
	OpenAs string

	// Remote, when non-nil, switches BuildAttachCommand from a local
	// `tmux attach` invocation to an `ssh` invocation that runs
	// `agent-deck session attach <Name>` on the remote host. Issue #1100,
	// follow-up to #1098 — Shift+Enter for remote sessions.
	Remote *RemoteAttach
}

// RemoteAttach carries the SSH and agent-deck details needed to attach
// to a remote agent-deck session from a freshly-spawned terminal window.
//
// Fields mirror the runtime values used by session.SSHRunner so the
// generated ssh command is byte-for-byte equivalent to what the in-TUI
// remote-attach path runs — same control-socket flags, same remote
// binary, same profile selector.
type RemoteAttach struct {
	// Host is the SSH destination (e.g. "user@host" or "user@host:port").
	Host string

	// AgentDeckPath is the agent-deck binary path on the remote host.
	// Empty defaults to "agent-deck".
	AgentDeckPath string

	// Profile is the remote profile selector. Empty or "default" omits
	// the -p flag.
	Profile string
}

// SSHControlDir is the directory the launcher tells SSH to keep its
// ControlMaster sockets in. Mirrors internal/session.sshControlDir so a
// remote attach launched via Shift+Enter shares the same multiplexed
// connection as a normal in-TUI remote attach.
const SSHControlDir = "/tmp/agent-deck-ssh"

// BuildAttachCommand returns the shell command string that, when executed
// inside a fresh terminal window, attaches to the requested session.
//
// For local requests (Remote == nil) it produces a `tmux attach …`
// invocation. For remote requests it produces an `ssh -tt …
// '<agent-deck-path> [-p profile] session attach <name>'` invocation
// that mirrors session.SSHRunner.Attach.
//
// It is exported (and pure) so platform implementations and tests can
// share the exact same string-building logic without depending on
// os/exec.
func BuildAttachCommand(req AttachRequest) string {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return ""
	}
	if req.Remote != nil {
		return buildRemoteAttachCommand(name, req.Remote)
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

// buildRemoteAttachCommand renders the ssh invocation that attaches to
// a remote agent-deck session. The remote command pieces mirror
// session.SSHRunner.buildRemoteCommand so the user lands in the same
// session regardless of which path opened it.
//
// We rely on ssh's documented behavior of joining trailing args with
// spaces before sending them to the remote shell, so each piece only
// needs to be locally shell-safe — no nested re-quoting of the whole
// command string.
func buildRemoteAttachCommand(remoteName string, r *RemoteAttach) string {
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	agentDeckPath := strings.TrimSpace(r.AgentDeckPath)
	if agentDeckPath == "" {
		agentDeckPath = "agent-deck"
	}
	profile := strings.TrimSpace(r.Profile)

	// ssh with -tt (force remote PTY) and the same ControlMaster flags
	// as the in-TUI remote attach, so the multiplexed connection is
	// reused.
	var b strings.Builder
	b.WriteString("ssh -tt")
	b.WriteString(" -o ControlMaster=auto")
	b.WriteString(" -o ControlPath=")
	b.WriteString(shellQuote(SSHControlDir + "/%r@%h:%p"))
	b.WriteString(" -o ControlPersist=600")
	b.WriteString(" ")
	b.WriteString(shellQuote(host))
	b.WriteString(" ")
	b.WriteString(shellQuote(agentDeckPath))
	if profile != "" && profile != "default" {
		b.WriteString(" -p ")
		b.WriteString(shellQuote(profile))
	}
	b.WriteString(" session attach ")
	b.WriteString(shellQuote(remoteName))
	return b.String()
}

// shellQuote single-quotes s for safe use in a /bin/sh command. It is
// intentionally simple: tmux session names are sanitized upstream (see
// internal/tmux.sanitizeName) so the input is already alphanumeric-ish, but
// we still quote defensively in case future names allow spaces.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
