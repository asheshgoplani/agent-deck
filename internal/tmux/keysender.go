package tmux

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// keySenderHandshakeTimeout bounds the wait for tmux's first control-mode
	// response block (%begin … %end) after `attach-session`. Matches the
	// control-pipe handshake budget; a slow server is not a failure, so a
	// timeout falls through to "assume attached" rather than erroring.
	keySenderHandshakeTimeout = 4 * time.Second
	// keySenderEOFExitGrace is how long Close waits for the child to exit on
	// its own after stdin EOF before escalating to a process-group kill.
	keySenderEOFExitGrace = 200 * time.Millisecond
)

// KeySender pushes keystrokes to a tmux pane over a persistent connection,
// amortizing the per-call fork+exec cost of `tmux send-keys` (#1102, follow-up
// to #1096). #1096 added 15ms rune batching, but at realistic typing speeds
// (>15ms between keys) every keystroke still triggers its own fork+exec — on
// macOS each one costs 10-50ms, so the user feels per-keystroke lag despite
// the batch window. KeySender opens one `tmux -C -u attach-session` subprocess at
// the start of an insert-mode session and streams send-keys commands over
// stdin for the lifetime of that mode, dropping per-call dispatch to a stdin
// write (<1ms regardless of platform).
type KeySender interface {
	// SendKeys forwards `text` as literal keystrokes (tmux send-keys -l).
	SendKeys(text string) error
	// SendNamedKey forwards a tmux named key (e.g. "Up", "BSpace", "C-c").
	SendNamedKey(key string) error
	// SendEnter forwards a single Enter keystroke.
	SendEnter() error
	// Close releases the underlying subprocess. Subsequent Send calls fail.
	Close() error
}

// localKeySender is the in-process KeySender backed by a long-running
// `tmux -L <socket> -C -u attach-session -t <target>` subprocess. Each Send
// writes one command line to its stdin; tmux executes commands in-server
// without spawning new clients.
// It is deliberately headless: its stdin/stdout are pipes and it never opens
// /dev/tty, so detached callers without a controlling terminal (#1114) work.
type localKeySender struct {
	target string
	cmd    *exec.Cmd
	// pgid is the process group cmd was given at spawn, captured while it was
	// certainly unreaped. See ownProcessGroupID and reapWithEOFGrace.
	pgid  int
	stdin io.WriteCloser

	waitOnce sync.Once

	mu     sync.Mutex
	closed bool
}

// OpenKeySender starts a persistent tmux control-mode client on `socket`
// (empty = the user's default tmux server) and returns a KeySender bound to
// `target` (the tmux session insert mode is typing into).
//
// The client attaches EXPLICITLY to `target`:
//
//	tmux [-L <socket>] -C -u attach-session -t <target>
//
// The explicit `attach-session` is load-bearing twice over, and both reasons
// are incident findings — do not "simplify" it back to a bare `tmux -C`:
//
//  1. No implicit session. A bare `tmux -C` carries no command, so tmux
//     falls back to `new-session`: every call minted a fresh session with a
//     live shell pane that Close() never killed (each holding a pty). ~26 of
//     those orphans helped exhaust the macOS pty pool on 2026-07-18, after
//     which no process on the machine could attach to anything.
//
//  2. Honest argv. On macOS a process keeps the argv it was exec'd with, so
//     a DEFAULT-socket server auto-started by a bare client is itself named
//     exactly "tmux -C" — indistinguishable from the leaked clients. On
//     2026-07-26 the hourly reaper matched `pgrep -fx "tmux -C"`, hit the
//     main server, and killed all ~65 live sessions at once. Nothing this
//     package spawns may ever carry that argv again; scripts/reap-stale-tmux.sh
//     now identifies servers by socket path for the same reason.
//
// Attaching does not resize the target: control-mode clients impose no size
// unless they ask for one (`refresh-client -C`), which this one never does.
//
// Returns a started KeySender on success. On any setup failure — including a
// target that no longer exists — the subprocess is cleaned up and an error is
// returned, so callers fall back to per-call fork+exec (the legacy path via
// Session.SendKeys).
func OpenKeySender(socket, target string) (KeySender, error) {
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("keysender: target required")
	}
	// Go through the sanctioned tmuxExec factory — it's the one place in
	// the codebase that knows how to assemble a tmux argv with the `-L
	// <socket>` selector, and the lint test in tmux_exec_lint_test.go
	// enforces this. Plain `exec.Command("tmux", ...)` would silently
	// defeat socket isolation when the user has opted in (#687).
	cmd := tmuxExec(socket, "-u", "-C", "attach-session", "-t", target)
	// Own process group so Close can take down the whole subtree, matching
	// ControlPipe. Without it a wedged child's descendants outlive Close.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("keysender: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("keysender: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("keysender: start tmux -C -u attach-session: %w", err)
	}
	k := &localKeySender{target: target, cmd: cmd, pgid: ownProcessGroupID(cmd), stdin: stdin}

	// Drain stdout so the OS pipe buffer never fills and blocks Send writes,
	// and settle the attach handshake on the way past. tmux answers the
	// attach with one %begin…%end block on success, or %begin…%error…%exit
	// when the target is gone.
	handshake := make(chan error, 1)
	go k.drain(stdout, handshake)

	select {
	case err := <-handshake:
		if err != nil {
			_ = k.Close()
			return nil, err
		}
	case <-time.After(keySenderHandshakeTimeout):
		// A slow server is not a failed attach. Proceed; a genuinely dead
		// client surfaces as a write error on the first Send, which is the
		// same signal callers already fall back on.
	}
	return k, nil
}

// drain consumes the control-mode stream for the client's lifetime and
// reports the outcome of the initial attach on `handshake` exactly once.
func (k *localKeySender) drain(stdout io.ReadCloser, handshake chan<- error) {
	// Reap before anyone can observe EOF so the child never lingers as a
	// zombie when the server drops the client on its own (#677).
	defer k.reap()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	settled := false
	settle := func(err error) {
		if !settled {
			settled = true
			handshake <- err
		}
	}
	// tmux puts the human-readable reason on the line before %error.
	lastDetail := ""

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "%end"):
			settle(nil)
		case strings.HasPrefix(line, "%error"):
			settle(fmt.Errorf("keysender: attach %q: %s", k.target, lastDetail))
		case strings.HasPrefix(line, "%exit"):
			settle(fmt.Errorf("keysender: attach %q: tmux exited during attach", k.target))
		case !strings.HasPrefix(line, "%"):
			lastDetail = line
		}
	}
	settle(fmt.Errorf("keysender: attach %q: tmux exited before handshake", k.target))
}

// reap harvests the child's exit status exactly once, from whichever of
// drain / Close gets there first.
func (k *localKeySender) reap() {
	k.waitOnce.Do(func() { _ = k.cmd.Wait() })
}

func (k *localKeySender) SendKeys(text string) error {
	if text == "" {
		return nil
	}
	return k.writeCmd("send-keys -l -t " + tmuxQuote(k.target) + " -- " + tmuxQuote(text))
}

func (k *localKeySender) SendNamedKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("keysender: empty named key")
	}
	return k.writeCmd("send-keys -t " + tmuxQuote(k.target) + " " + tmuxQuote(key))
}

func (k *localKeySender) SendEnter() error {
	return k.writeCmd("send-keys -t " + tmuxQuote(k.target) + " Enter")
}

func (k *localKeySender) writeCmd(line string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return fmt.Errorf("keysender: closed")
	}
	if _, err := io.WriteString(k.stdin, line+"\n"); err != nil {
		return fmt.Errorf("keysender: write %q: %w", line, err)
	}
	return nil
}

func (k *localKeySender) Close() error {
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return nil
	}
	k.closed = true
	stdin := k.stdin
	cmd := k.cmd
	k.mu.Unlock()

	// Stage 1: stdin EOF makes the control client detach and exit cleanly.
	if stdin != nil {
		_ = stdin.Close()
	}
	// Stage 2: reap it, escalating to a process-group kill if EOF alone
	// doesn't land (rare; observed once on a hung tmux 3.4 socket during the
	// v1.5.x cascade incident). Killing the GROUP — not just the pid — is
	// what guarantees Close tears down everything Open spawned.
	if cmd != nil && cmd.Process != nil {
		_ = reapWithEOFGrace(k.reap, cmd.Process, k.pgid, keySenderEOFExitGrace, controlClientKillGrace)
	}
	return nil
}

// tmuxQuote wraps s in single quotes for tmux's command parser, escaping
// embedded single quotes by closing the quoted region, inserting a
// backslash-escaped single quote, then re-opening. Single quotes treat
// every other byte as literal, so this is safe for arbitrary user input
// (e.g., insert-mode rune bursts that may contain ", $, \, backticks).
func tmuxQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
