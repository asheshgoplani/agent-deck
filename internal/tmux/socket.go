package tmux

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// tmuxSubprocessWaitDelay is the deadline cmd.Wait() waits for stdio I/O
// goroutines to finish AFTER the tmux process exits (or its context is
// canceled). It backstops the EOF hang where a forked child of tmux
// inherits the subprocess's stdout pipe fd and never closes it — most
// commonly the tmux server's terminal pass-through dups under bridged
// stdio (Claude Code /remote-control, ssh ControlMaster, certain
// container runtimes).
//
// Without this, cmd.Output() blocks indefinitely on the read goroutine
// waiting for an EOF that never comes, even after the tmux client
// process is dead and the context has fired. Two seconds is comfortably
// more than any successful tmux subcommand takes (typically <50ms) but
// well under the 5-second symptom threshold reported by users running
// agent-deck CLI under /remote-control.
//
// Contract for callers using cmd.Output() / cmd.CombinedOutput(): when
// errors.Is(err, exec.ErrWaitDelay) and the captured stdout looks valid
// (non-empty, parses cleanly), treat it as success. The bytes were
// written to the buffer before the I/O goroutine was abandoned.
const tmuxSubprocessWaitDelay = 2 * time.Second

// defaultSocketName is the process-wide socket used by package-level tmux
// probes (version checks, list-all-sessions, duplicate-session reaping)
// that have no Session receiver in scope. Populated once at program start
// from [tmux].socket_name in config.toml, or left empty when the user has
// not opted in (pre-v1.7.50 default).
//
// Per-Session calls never consult this value — they read Session.SocketName
// directly, which is captured at session-creation time so sessions remain
// reachable even if the installation-wide config is later edited.
var (
	defaultSocketName   string
	defaultSocketNameMu sync.RWMutex
)

// SetDefaultSocketName seeds the process-wide socket used by package-level
// tmux calls. Called once from main.go after config load and CLI flag
// parsing. Whitespace is trimmed; a blank or whitespace-only input clears
// the default (falls back to pre-v1.7.50 behavior).
func SetDefaultSocketName(name string) {
	defaultSocketNameMu.Lock()
	defer defaultSocketNameMu.Unlock()
	defaultSocketName = strings.TrimSpace(name)
}

// DefaultSocketName returns the process-wide default socket name, or ""
// when the user has not configured isolation. Safe for concurrent use.
func DefaultSocketName() string {
	defaultSocketNameMu.RLock()
	defer defaultSocketNameMu.RUnlock()
	return defaultSocketName
}

// tmuxArgs builds the full `tmux …` argv for a command, inserting the
// `-L <name>` socket selector at the front when socketName is non-empty
// and non-whitespace. An empty socket name is the pre-v1.7.50 default and
// produces an unmodified argv — zero behavior change for users who do not
// opt in to socket isolation (scope decision 1: empty default).
//
// The returned slice is always freshly allocated; the caller's args slice
// is never mutated or aliased.
//
// See CHANGELOG v1.7.50 and docs/README socket-isolation section.
func tmuxArgs(socketName string, args ...string) []string {
	name := strings.TrimSpace(socketName)
	if name == "" {
		out := make([]string, len(args))
		copy(out, args)
		return out
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "-L", name)
	out = append(out, args...)
	return out
}

// tmuxExec constructs an *exec.Cmd that invokes `tmux` with the given
// subcommand, honoring the configured socket name. It is the package-level
// counterpart to (*Session).tmuxCmd — use this when there is no Session
// receiver handy (e.g. list-sessions probes, revival lookups).
//
// When socketName is empty, the produced command is indistinguishable from
// `exec.Command("tmux", args...)`, preserving the contract of every
// pre-v1.7.50 call site that was rewritten in #697.
func tmuxExec(socketName string, args ...string) *exec.Cmd {
	cmd := exec.Command("tmux", tmuxArgs(socketName, args...)...)
	cmd.WaitDelay = tmuxSubprocessWaitDelay
	return cmd
}

// tmuxExecContext is the context-aware variant of tmuxExec. Several
// tmux.go call sites already use exec.CommandContext for cancellation +
// timeout (e.g. SetEnvironment at internal/tmux/tmux.go:1412); this keeps
// the -L plumbing centralised for them too.
func tmuxExecContext(ctx context.Context, socketName string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "tmux", tmuxArgs(socketName, args...)...)
	cmd.WaitDelay = tmuxSubprocessWaitDelay
	return cmd
}

// tmuxCmd is the per-Session convenience wrapper. Every tmux subprocess
// spawned for a specific session must target the socket that session was
// created under — even if the installation-wide config later changes.
// Mixing sockets would leave stored sessions unreachable.
//
// NOTE: Session.SocketName is immutable after session creation (set once
// by the CLI/config path that minted the Instance). Mutating it in-place
// would lie about where the tmux server lives.
func (s *Session) tmuxCmd(args ...string) *exec.Cmd {
	return tmuxExec(s.SocketName, args...)
}

// tmuxCmdContext mirrors tmuxCmd for the context-aware call sites.
func (s *Session) tmuxCmdContext(ctx context.Context, args ...string) *exec.Cmd {
	return tmuxExecContext(ctx, s.SocketName, args...)
}

// Exec is the public package counterpart to tmuxExec. Call sites outside
// internal/tmux (the session package, CLI helpers, web terminal bridge) use
// this when they have a socket name — typically Instance.TmuxSocketName —
// and need to spawn a one-off tmux subprocess. Pass "" for the user's
// default server.
//
// This keeps the `-L <name>` plumbing centralised: there is exactly one
// place in the codebase that knows how to assemble a tmux argv, so a future
// socket-selection change (phase 2/3 — per-conductor sockets, env var
// fallback) only needs to be made here.
func Exec(socketName string, args ...string) *exec.Cmd {
	return tmuxExec(socketName, args...)
}

// ExecContext is the context-aware variant of Exec.
func ExecContext(ctx context.Context, socketName string, args ...string) *exec.Cmd {
	return tmuxExecContext(ctx, socketName, args...)
}

// buildInnerTmuxArgs is the systemd-run-aware variant of tmuxArgs. When the
// session is launched via `systemd-run --user tmux <args…>`, the socket
// selector has to live INSIDE the inner tmux argv — after the literal
// "tmux" that systemd-run execs, before the subcommand. This helper
// returns just the inner `[-L <name>] <args…>` slice; callers splice it
// after "tmux" in their systemd-run argv (or use it directly when launcher
// is "tmux").
//
// Empty / whitespace-only socket name returns the input args unchanged, so
// pre-v1.7.50 call sites see byte-identical argv.
func buildInnerTmuxArgs(socketName string, args ...string) []string {
	return tmuxArgs(socketName, args...)
}
