package session

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
)

// RemoteKeySender dispatches insert-mode keystrokes to a session living on a
// remote agent-deck instance over the existing SSHRunner RPC (#1102 bug 2:
// previously, insert mode silently no-op'd for remote sessions because
// dispatch only saw `*Instance`, not `RemoteSessionInfo`). Each Send shells
// to the remote `agent-deck session send-keys <id>` subcommand; ControlMaster
// keeps the SSH multiplex channel warm so calls don't pay the full
// TCP+SSH handshake every time.
//
// Unlike the local path, this does NOT open a long-running subprocess —
// `ssh ... agent-deck session send-keys <id> -- <text>` is fork+exec per
// call. Acceptable because:
//   - Remote latency is dominated by network RTT, not local fork+exec.
//   - SSH ControlMaster (already set up in SSHRunner) amortizes the
//     connection cost across all calls during an insert-mode session.
//   - Keeping a persistent `ssh ... -t` interactive session would require
//     a custom on-remote shell protocol — out of scope for the bugfix.
//
// The remote side's `session send-keys` handler uses the existing tmux
// SendKeys / SendNamedKey / SendEnter on the remote Instance, so the
// behavior reaching the pane is bit-identical to a local insert-mode send.
type RemoteKeySender struct {
	runner    *SSHRunner
	sessionID string
	ctx       context.Context

	mu     sync.Mutex
	closed bool
}

// NewRemoteKeySender wires up a sender bound to a single remote session.
// The context governs every Send call's deadline; pass context.Background()
// to inherit SSHRunner.Run's default 10s timeout, or a derived context for
// the lifetime of the insert-mode session.
func NewRemoteKeySender(runner *SSHRunner, sessionID string, ctx context.Context) *RemoteKeySender {
	if ctx == nil {
		ctx = context.Background()
	}
	return &RemoteKeySender{
		runner:    runner,
		sessionID: sessionID,
		ctx:       ctx,
	}
}

// SendKeys forwards `text` as literal keystrokes to the remote session.
// Empty text is a no-op so the caller doesn't have to guard before flushing
// an empty rune buffer.
func (r *RemoteKeySender) SendKeys(text string) error {
	if text == "" {
		return nil
	}
	return r.run("--text", text)
}

// SendNamedKey forwards a tmux named key (BSpace/Up/Down/Left/Right/Tab/
// BTab/C-c/C-d — the same set #1094 added to the local path).
func (r *RemoteKeySender) SendNamedKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("remote keysender: empty named key")
	}
	return r.run("--named-key", key)
}

// SendEnter forwards a single Enter keystroke. Modeled as a discrete flag so
// the remote handler can preserve the existing tmux SendEnter semantics
// (with bracketed-paste flush delay; see internal/tmux/tmux.go:3984).
func (r *RemoteKeySender) SendEnter() error {
	return r.run("--enter")
}

// run invokes `agent-deck session send-keys <id> <flags…>` on the remote
// host. Each call goes through SSHRunner which honors ControlMaster, so
// after the first call the SSH channel is reused.
func (r *RemoteKeySender) run(extraArgs ...string) error {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return fmt.Errorf("remote keysender: closed")
	}

	args := []string{"session", "send-keys", r.sessionID}
	args = append(args, extraArgs...)
	if _, err := r.runner.Run(r.ctx, args...); err != nil {
		return fmt.Errorf("remote send-keys: %w", err)
	}
	return nil
}

// Close marks the sender as closed. Subsequent Send calls fail with
// "closed". Idempotent so panic-recovery flows can safely double-close.
// Does NOT tear down the SSH ControlMaster — that's shared across the
// process and persists per ssh -o ControlPersist=600.
func (r *RemoteKeySender) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

// StreamingRemoteKeySender amortizes the per-keystroke ssh fork+exec cost
// (#1112 bug 2) across an entire insert-mode session by holding one
// long-running `ssh ... agent-deck session send-keys <id> --stream`
// subprocess open. Each Send writes one line to that subprocess's stdin
// — sub-millisecond regardless of network RTT, because the SSH transport
// is already negotiated.
//
// Wire protocol (matches cmd/agent-deck/session_send_keys_cmd.go's
// runSendKeysStream):
//
//	T <hex-text>\n   — SendKeys(decode(<hex-text>))
//	N <name>\n       — SendNamedKey(<name>)
//	E\n              — SendEnter()
//
// Hex encoding for text means newlines/NULL/non-printables round-trip
// safely without escape-sequence gymnastics.
type StreamingRemoteKeySender struct {
	stdin   io.WriteCloser
	closeFn func() error

	mu     sync.Mutex
	closed bool
}

// OpenStreamingRemoteKeySender spawns the persistent SSH stream and
// returns a sender ready to dispatch. Falls back to a per-call
// RemoteKeySender if the stream can't open (caller can check via
// errors.Is — the streaming type implements the same insertKeySender
// interface as RemoteKeySender).
func OpenStreamingRemoteKeySender(runner *SSHRunner, sessionID string, ctx context.Context) (*StreamingRemoteKeySender, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	stdin, closeFn, err := runner.OpenStream(ctx, "session", "send-keys", sessionID, "--stream")
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	return &StreamingRemoteKeySender{stdin: stdin, closeFn: closeFn}, nil
}

// SendKeys writes a "T <hex>\n" line to the persistent stream. Empty
// text is a no-op (matches RemoteKeySender).
func (s *StreamingRemoteKeySender) SendKeys(text string) error {
	if text == "" {
		return nil
	}
	return s.write("T " + hex.EncodeToString([]byte(text)) + "\n")
}

// SendNamedKey writes "N <key>\n" to the stream. Empty key fails fast.
func (s *StreamingRemoteKeySender) SendNamedKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("streaming remote keysender: empty named key")
	}
	if strings.ContainsRune(key, '\n') {
		return fmt.Errorf("streaming remote keysender: named key must not contain newline")
	}
	return s.write("N " + key + "\n")
}

// SendEnter writes "E\n" to the stream.
func (s *StreamingRemoteKeySender) SendEnter() error {
	return s.write("E\n")
}

func (s *StreamingRemoteKeySender) write(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("streaming remote keysender: closed")
	}
	if _, err := io.WriteString(s.stdin, line); err != nil {
		return fmt.Errorf("streaming remote keysender: write: %w", err)
	}
	return nil
}

// Close terminates the persistent stream. Idempotent.
func (s *StreamingRemoteKeySender) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	closeFn := s.closeFn
	s.mu.Unlock()
	if closeFn != nil {
		return closeFn()
	}
	return nil
}
