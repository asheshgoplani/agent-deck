package tmux

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// KeySender pushes keystrokes to a tmux pane over a persistent connection,
// amortizing the per-call fork+exec cost of `tmux send-keys` (#1102, follow-up
// to #1096). #1096 added 15ms rune batching, but at realistic typing speeds
// (>15ms between keys) every keystroke still triggers its own fork+exec — on
// macOS each one costs 10-50ms, so the user feels per-keystroke lag despite
// the batch window. KeySender opens one `tmux -L <socket> -C` subprocess at
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
// `tmux -L <socket> -C` subprocess. Each Send writes one command line to its
// stdin; tmux executes commands in-server without spawning new clients.
type localKeySender struct {
	target string
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	mu     sync.Mutex
	closed bool
}

// OpenKeySender starts a persistent tmux control-mode client on `socket`
// (empty = the user's default tmux server) and returns a KeySender bound to
// `target` (a tmux session/pane selector like "my-session" or "id:0.0").
//
// The subprocess does NOT attach to any session — it stays in control mode,
// reads commands from stdin, and emits status notifications on stdout (which
// are drained and discarded). At least one session must exist on the server
// for it to remain running; this is always true at the call site because
// insert mode targets an existing session.
//
// Returns a started KeySender on success. On any setup failure the
// subprocess is cleaned up and an error is returned, so callers can fall
// back to per-call fork+exec (the legacy path via Session.SendKeys).
func OpenKeySender(socket, target string) (KeySender, error) {
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("keysender: target required")
	}
	// Go through the sanctioned tmuxExec factory — it's the one place in
	// the codebase that knows how to assemble a tmux argv with the `-L
	// <socket>` selector, and the lint test in tmux_exec_lint_test.go
	// enforces this. Plain `exec.Command("tmux", ...)` would silently
	// defeat socket isolation when the user has opted in (#687).
	cmd := tmuxExec(socket, "-C")
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
		return nil, fmt.Errorf("keysender: start tmux -C: %w", err)
	}
	// Drain stdout so the OS pipe buffer never fills and blocks Send writes.
	// We don't parse %begin/%end responses — send-keys never returns useful
	// data, and any error is reflected when the next stdin write fails.
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	return &localKeySender{target: target, cmd: cmd, stdin: stdin}, nil
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

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		// stdin close should make tmux -C exit cleanly; kill as a backstop in
		// case the server is in an unresponsive state (rare; observed once on
		// a hung tmux 3.4 socket during the v1.5.x cascade incident).
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
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
