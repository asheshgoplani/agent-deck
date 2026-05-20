package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Issue #1102 (by @ddorman-dn against v1.9.23): insert mode silently no-op'd
// for remote sessions because the TUI's dispatch only ever resolved the
// selection to a local `*Instance` — remote sessions are
// `RemoteSessionInfo`, not Instance, so enterInsertMode bailed before any
// keys were forwarded. These tests cover the follow-up: a RemoteKeySender
// that uses the existing SSHRunner.Run RPC to invoke `agent-deck session
// send-keys <id>` on the remote host.

// captureSSHRunner returns an SSHRunner whose runFn records every invocation
// in the slice it returns. The runner doesn't actually fork ssh — the
// tests stay deterministic and fast.
func captureSSHRunner(t *testing.T) (*SSHRunner, func() [][]string) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls [][]string
	)
	r := &SSHRunner{
		Host:          "test-host",
		AgentDeckPath: "/usr/local/bin/agent-deck",
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			callCopy := make([]string, len(args))
			copy(callCopy, args)
			calls = append(calls, callCopy)
			return []byte(`{"success":true}`), nil
		},
	}
	return r, func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		out := make([][]string, len(calls))
		for i, c := range calls {
			out[i] = append([]string(nil), c...)
		}
		return out
	}
}

// TestIssue1102_RemoteKeySender_SendKeysRoutesViaSSH is the headline test:
// calling SendKeys on a RemoteKeySender must reach the SSHRunner with the
// canonical `session send-keys <id> --text <text>` argv. Previously this
// path didn't exist at all, so insert mode dropped the keystroke silently.
func TestIssue1102_RemoteKeySender_SendKeysRoutesViaSSH(t *testing.T) {
	runner, captured := captureSSHRunner(t)
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	if err := sender.SendKeys("hello"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	calls := captured()
	if len(calls) != 1 {
		t.Fatalf("expected 1 SSH call, got %d: %v", len(calls), calls)
	}
	want := []string{"session", "send-keys", "remote-sess-id", "--text", "hello"}
	if !sliceEqual(calls[0], want) {
		t.Errorf("SSH argv = %v, want %v", calls[0], want)
	}
}

// TestIssue1102_RemoteKeySender_SendNamedKeyRoutesViaSSH verifies the named-
// key dispatch path (Backspace, arrows, Tab, Ctrl-{C,D} — every key #1094
// taught insert mode about) reaches the remote with the right wire format.
func TestIssue1102_RemoteKeySender_SendNamedKeyRoutesViaSSH(t *testing.T) {
	runner, captured := captureSSHRunner(t)
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	if err := sender.SendNamedKey("BSpace"); err != nil {
		t.Fatalf("SendNamedKey: %v", err)
	}

	calls := captured()
	if len(calls) != 1 {
		t.Fatalf("expected 1 SSH call, got %d: %v", len(calls), calls)
	}
	want := []string{"session", "send-keys", "remote-sess-id", "--named-key", "BSpace"}
	if !sliceEqual(calls[0], want) {
		t.Errorf("SSH argv = %v, want %v", calls[0], want)
	}
}

// TestIssue1102_RemoteKeySender_SendEnterRoutesViaSSH covers the Enter
// dispatch — modeled as a discrete flag so the remote handler can apply the
// tmux bracketed-paste flush delay the local SendEnter uses (see
// internal/tmux/tmux.go:3984).
func TestIssue1102_RemoteKeySender_SendEnterRoutesViaSSH(t *testing.T) {
	runner, captured := captureSSHRunner(t)
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	if err := sender.SendEnter(); err != nil {
		t.Fatalf("SendEnter: %v", err)
	}

	calls := captured()
	if len(calls) != 1 {
		t.Fatalf("expected 1 SSH call, got %d: %v", len(calls), calls)
	}
	want := []string{"session", "send-keys", "remote-sess-id", "--enter"}
	if !sliceEqual(calls[0], want) {
		t.Errorf("SSH argv = %v, want %v", calls[0], want)
	}
}

// TestIssue1102_RemoteKeySender_EmptyTextIsNoOp verifies SendKeys("") doesn't
// produce an SSH call — buffer flushes after non-rune keys can hand an empty
// string here and we shouldn't pay an SSH round-trip for nothing.
func TestIssue1102_RemoteKeySender_EmptyTextIsNoOp(t *testing.T) {
	runner, captured := captureSSHRunner(t)
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	if err := sender.SendKeys(""); err != nil {
		t.Fatalf("SendKeys(\"\"): %v", err)
	}

	if calls := captured(); len(calls) != 0 {
		t.Errorf("empty SendKeys should be a no-op; got SSH call %v", calls)
	}
}

// TestIssue1102_RemoteKeySender_EmptyNamedKeyRejected guards against the
// dispatch sending a malformed `session send-keys --named-key` (with an
// empty value) — that would land on the remote and produce an opaque tmux
// error rather than failing fast on the local side.
func TestIssue1102_RemoteKeySender_EmptyNamedKeyRejected(t *testing.T) {
	runner, _ := captureSSHRunner(t)
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	err := sender.SendNamedKey("   ")
	if err == nil {
		t.Fatal("expected error for empty named key, got nil")
	}
	if !strings.Contains(err.Error(), "empty named key") {
		t.Errorf("error = %q, want one mentioning 'empty named key'", err)
	}
}

// TestIssue1102_RemoteKeySender_CloseStopsDispatch verifies the close path
// works idempotently and refuses further dispatches. Important because the
// TUI calls Close on insert-mode exit and then defer-closes on panic
// recovery; a panic on second close would brick the TUI.
func TestIssue1102_RemoteKeySender_CloseStopsDispatch(t *testing.T) {
	runner, captured := captureSSHRunner(t)
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	if err := sender.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sender.Close(); err != nil {
		t.Errorf("second Close should be a no-op; got %v", err)
	}

	err := sender.SendKeys("after-close")
	if err == nil {
		t.Error("SendKeys after Close should fail")
	} else if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error after Close = %v, want one mentioning 'closed'", err)
	}

	if calls := captured(); len(calls) != 0 {
		t.Errorf("no SSH calls expected after Close, got %v", calls)
	}
}

// TestIssue1102_RemoteKeySender_PropagatesSSHError verifies the error from
// SSHRunner.Run is surfaced verbatim (wrapped) so the TUI's setError shows
// the user what's actually wrong — network down, remote agent-deck binary
// missing, session ID stale, etc. Previously, with no dispatch path at all,
// the user got silence.
func TestIssue1102_RemoteKeySender_PropagatesSSHError(t *testing.T) {
	wantErr := errors.New("ssh: connection refused")
	runner := &SSHRunner{
		Host:          "test-host",
		AgentDeckPath: "/usr/local/bin/agent-deck",
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			return nil, wantErr
		},
	}
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	err := sender.SendKeys("hello")
	if err == nil {
		t.Fatal("expected error to surface from SSH layer")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error chain does not include SSH error; got %v", err)
	}
}

// TestIssue1102_RemoteKeySender_ConcurrentSendsSerialize covers the
// concurrency contract: the dispatch path stores no mutable state per call
// and the underlying SSHRunner.runFn is goroutine-safe, so 100 concurrent
// SendKeys calls must all reach the remote without losing any or
// corrupting state.
func TestIssue1102_RemoteKeySender_ConcurrentSendsSerialize(t *testing.T) {
	runner, captured := captureSSHRunner(t)
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	var wg sync.WaitGroup
	var failures atomic.Int32
	const n = 100
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := sender.SendKeys("x"); err != nil {
				failures.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := failures.Load(); got != 0 {
		t.Errorf("%d/100 concurrent SendKeys failed", got)
	}
	if calls := captured(); len(calls) != n {
		t.Errorf("got %d SSH calls, want %d (each concurrent call must reach the remote)", len(calls), n)
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
