package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
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

// tmuxSendKeysTimeoutDefault bounds a SINGLE `tmux send-keys` subprocess.
//
// The raw key-delivery primitives (sendKeysToTarget, sendEnterRawToTarget,
// SendNamedKey, ensureInsertModeOnTarget, SendCtrlC/SendCtrlU) historically ran
// cmd.Run() with NO deadline. Against a pane whose program is transiently not
// draining its input pty, `tmux send-keys` blocks inside Run() indefinitely.
//
// WaitDelay (tmuxSubprocessWaitDelay) does NOT save us here: it only bounds the
// stdio-drain race AFTER the process exits or its context is canceled, and a
// never-exiting send-keys never reaches that point. When an OUTER bound finally
// fires — the --no-wait wake-nudge's 5s context, or a daemon poll cycle — it
// kills the `agent-deck` process but the blocked `tmux send-keys` GRANDCHILD is
// reparented to launchd/init and hangs forever. Observed in production as 9h-old
// `tmux send-keys` zombies, one of which was a launchd heartbeat that held its
// launchd slot and killed that conductor's heartbeat entirely.
//
// 3s is far more than any healthy send-keys (<50ms) yet bounds the wedge so the
// caller fails fast and the durable pull model redelivers on the next turn.
const tmuxSendKeysTimeoutDefault = 3 * time.Second

// tmuxSendKeysTimeout is the live send-keys deadline. It is a var (seeded from
// the const above) ONLY so tests can shrink it to keep the timeout suite fast
// and deterministic; production never mutates it.
var tmuxSendKeysTimeout = tmuxSendKeysTimeoutDefault

// sendKeysReapGrace bounds how long runSendKeysBounded waits for a SIGKILL'd
// process group to be reaped before returning anyway. A process wedged in
// uninterruptible (D-state) sleep cannot be killed; the buffered wait channel
// lets the reaper goroutine exit on its own later, so this grace guarantees the
// caller is never blocked past tmuxSendKeysTimeout + sendKeysReapGrace.
const sendKeysReapGrace = 2 * time.Second

// errSendKeysTimeout is the benign sentinel returned when a send-keys exec is
// SIGKILL'd for exceeding tmuxSendKeysTimeout. It wraps context.DeadlineExceeded
// so callers can classify it with errors.Is(err, context.DeadlineExceeded). The
// wake-nudge wiring treats it like any dropped nudge (the durable record drains
// on the parent's next turn); the send-verify retry loop treats it as a failed
// attempt and retries within its budget — neither hard-fails or panics.
var errSendKeysTimeout = fmt.Errorf("tmux send-keys exceeded %s deadline: %w", tmuxSendKeysTimeoutDefault, context.DeadlineExceeded)

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

// tmuxFieldSep delimits the fields of the `-F` format strings that agent-deck
// both emits and parses (the session / pane / client probes that feed status
// detection). It MUST be a printable ASCII byte, and historically was a TAB —
// which turned out to be a latent bug:
//
// A tmux command invoked with NO attached client sanitizes non-printable bytes
// in its format output, rewriting TAB (0x09) — and every other control byte,
// including newline and the C0/UTF-8 separators — to "_". The launchd
// notify-daemon and conductor-heartbeat inherit no $TMUX, so every status probe
// they ran hit this path: the TAB field separators collapsed to "_", SplitN
// found a single field, parseListWindowsOutput skipped every line, the session
// cache came back empty, Session.Exists() reported false, and UpdateStatus
// stamped StatusError on every live session. That error then failed the
// idle/waiting gate on BOTH the wake-nudge and the heartbeat, so an idle
// conductor stopped being woken when a child finished (diagnosed 2026-06-18).
//
// "|" survives the no-client path. It can never collide with the non-trailing
// fields these formats carry: tmux session names are sanitized to [A-Za-z0-9-]
// (see sanitizeNameRe) and the rest are integers or a 0/1 flag. The genuinely
// free-text fields (window_name, pane_title, client_name) are always placed
// LAST and parsed with SplitN, so a stray "|" inside them is preserved intact.
//
// The control-mode pipe path (internal/tmux/pipemanager.go) is unaffected — a
// control client IS attached there, so it keeps its TAB formats.
const tmuxFieldSep = "|"

// tmuxFmt joins tmux format fields with tmuxFieldSep. Producer and consumer
// (strings.SplitN(line, tmuxFieldSep, n)) reference the same constant so the
// delimiter can never drift between the two halves.
func tmuxFmt(fields ...string) string {
	return strings.Join(fields, tmuxFieldSep)
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
	// #nosec G204,G702 -- "tmux" is a fixed binary and every dynamic value is
	// passed as a distinct argv element, never through a shell. Call sites may
	// supply user-selected paths, but those cannot alter the executable or argv
	// boundaries.
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

// runSendKeysBounded runs a pre-built `tmux send-keys` command under
// tmuxSendKeysTimeout, in its OWN process group, SIGKILL-ing the WHOLE group on
// timeout so a wedged send-keys — and any grandchild it forked — is reaped
// instead of being orphaned to launchd/init. This is the deadline + group-kill
// that the historical bare cmd.Run() lacked (see tmuxSendKeysTimeoutDefault).
//
// The command is built by the caller (via the keySenderExec seam or tmuxCmd) so
// the existing argv-recording test seams keep working unchanged; this wrapper
// only owns the lifecycle. Mirrors the Setpgid + negative-pid SIGKILL pattern
// already used by controlpipe.go / softKillProcessGroup.
//
// Returns nil on clean exit, the process's own error on non-timeout failure
// (preserving the previous cmd.Run() contract), or errSendKeysTimeout (which
// wraps context.DeadlineExceeded) when the deadline fired.
func runSendKeysBounded(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	// Own process group so the timeout can SIGKILL the entire subtree, not just
	// the immediate send-keys child.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan error, 1) // buffered: the waiter can always send & exit
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(tmuxSendKeysTimeout)
	defer timer.Stop()

	select {
	case err := <-done:
		return err
	case <-timer.C:
		// The process may have completed in the scheduling window between the
		// timer firing and this branch being selected. Drain non-blockingly
		// first: if it already finished, return its result rather than
		// SIGKILL'ing a group whose leader PID may be reaped (and reused).
		select {
		case err := <-done:
			return err
		default:
		}
		killSendKeysGroup(cmd)
		// Best-effort reap so we don't leak a zombie, but bounded: a process
		// wedged in uninterruptible sleep must not make us hang past the grace.
		select {
		case <-done:
		case <-time.After(sendKeysReapGrace):
		}
		return errSendKeysTimeout
	}
}

// killSendKeysGroup SIGKILLs the process group led by cmd's process. Setpgid
// (set in runSendKeysBounded before Start) made the child a group leader, so the
// negative-pid signal reaps the child plus any grandchild it forked.
func killSendKeysGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// tmuxPollTimeout bounds the short, read-only tmux queries and option-set
// commands agent-deck fires on a cadence: status-bar refreshes, pane-path and
// dead-state probes, and client enumeration. These commands target a server
// that may be wedged, and a tmux client whose target session was destroyed
// mid-exchange can spin on its poll loop at 100% CPU instead of exiting
// (observed on tmux 3.0a). Bare tmuxExec(...).Run()/.Output() has no deadline,
// so the client hangs forever; if the owning TUI later dies the kernel
// reparents the spinning client to init/systemd and nothing reaps it — the
// exact orphan-busy-loop class that leaks whole CPU cores. Wrapping each poll
// in a context lets exec.CommandContext SIGKILL a stuck client. 3s is ~60x the
// typical <50ms runtime and mirrors hasSessionProbeTimeout.
var tmuxPollTimeout = 3 * time.Second

// runBoundedOutput runs a short, read-only tmux query under tmuxPollTimeout and
// returns its stdout. It is the timeout-guarded replacement for the bare
// tmuxExec(socket, args...).Output() pattern at status/poll call sites.
func runBoundedOutput(socketName string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxPollTimeout)
	defer cancel()
	return tmuxExecContext(ctx, socketName, args...).Output()
}

// runBoundedRun runs a short tmux command (typically a status set-option batch)
// under tmuxPollTimeout, discarding stdout. Timeout-guarded replacement for the
// bare tmuxExec(socket, args...).Run() / s.tmuxCmd(args...).Run() status sites.
func runBoundedRun(socketName string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxPollTimeout)
	defer cancel()
	return tmuxExecContext(ctx, socketName, args...).Run()
}

// runBoundedOutput is the per-Session convenience wrapper, targeting the
// session's own socket (see tmuxCmd for why the socket must not drift).
func (s *Session) runBoundedOutput(args ...string) ([]byte, error) {
	return runBoundedOutput(s.SocketName, args...)
}

// runBoundedRun is the per-Session convenience wrapper for status/option
// commands that produce no output.
func (s *Session) runBoundedRun(args ...string) error {
	return runBoundedRun(s.SocketName, args...)
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
