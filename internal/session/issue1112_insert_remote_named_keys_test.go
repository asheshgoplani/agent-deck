package session

// Issue #1112 — bug 3 (by @ddorman-dn on v1.9.24): arrow keys + Enter for
// menu navigation work in insert mode against LOCAL sessions but appear
// broken against REMOTE sessions. The hypothesis was "RPC for named keys
// not extending to remote." These tests are the regression fence proving
// every menu-nav key the user can press lands on the remote with the
// correct argv shape — Up/Down/Left/Right/Tab/Shift-Tab/Enter.
//
// Distinct from #1102's RemoteKeySender tests: those covered the
// general dispatch verbs. These bind the SPECIFIC menu-nav keys named
// in the bug report so a future refactor can't silently drop one of
// them.

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// captureRemoteCalls is a local helper so this file stays independent of
// issue1102_insert_remote_test.go's captureSSHRunner — same idea, but a
// distinct fixture so #1112 regressions are caught even if #1102's
// fixture is later changed.
func captureRemoteCalls() (*SSHRunner, func() [][]string) {
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

// TestIssue1112_RemoteKeySender_ForwardsAllArrowKeys asserts the four
// arrow keys forward to the remote with the canonical tmux names. If a
// regression turns "Up" into something tmux doesn't recognize (e.g.
// "ArrowUp"), the remote pane sees garbage and the menu picker freezes.
func TestIssue1112_RemoteKeySender_ForwardsAllArrowKeys(t *testing.T) {
	cases := []string{"Up", "Down", "Left", "Right"}
	for _, key := range cases {
		t.Run(key, func(t *testing.T) {
			runner, captured := captureRemoteCalls()
			sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

			if err := sender.SendNamedKey(key); err != nil {
				t.Fatalf("SendNamedKey(%q): %v", key, err)
			}
			calls := captured()
			if len(calls) != 1 {
				t.Fatalf("expected 1 SSH call for %s, got %d: %v", key, len(calls), calls)
			}
			got := strings.Join(calls[0], " ")
			want := "session send-keys remote-sess-id --named-key " + key
			if got != want {
				t.Errorf("argv = %q, want %q (#1112 bug 3)", got, want)
			}
		})
	}
}

// TestIssue1112_RemoteKeySender_ForwardsTabAndShiftTab covers Tab and
// BTab (tmux's name for Shift-Tab). Claude's pickers use Tab to advance
// between fields and Shift-Tab to step back; both must round-trip.
func TestIssue1112_RemoteKeySender_ForwardsTabAndShiftTab(t *testing.T) {
	cases := []string{"Tab", "BTab"}
	for _, key := range cases {
		t.Run(key, func(t *testing.T) {
			runner, captured := captureRemoteCalls()
			sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

			if err := sender.SendNamedKey(key); err != nil {
				t.Fatalf("SendNamedKey(%q): %v", key, err)
			}
			calls := captured()
			if len(calls) != 1 || calls[0][len(calls[0])-1] != key {
				t.Errorf("argv = %v, want last token %q", calls, key)
			}
		})
	}
}

// TestIssue1112_RemoteKeySender_ForwardsEnter covers the Enter dispatch.
// Enter is its own verb (--enter), not a named key, so the remote
// handler can apply the bracketed-paste flush delay that local
// SendEnter uses.
func TestIssue1112_RemoteKeySender_ForwardsEnter(t *testing.T) {
	runner, captured := captureRemoteCalls()
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	if err := sender.SendEnter(); err != nil {
		t.Fatalf("SendEnter: %v", err)
	}
	calls := captured()
	if len(calls) != 1 {
		t.Fatalf("expected 1 SSH call, got %d: %v", len(calls), calls)
	}
	want := []string{"session", "send-keys", "remote-sess-id", "--enter"}
	if !sliceEqualLocal(calls[0], want) {
		t.Errorf("argv = %v, want %v", calls[0], want)
	}
}

// TestIssue1112_RemoteKeySender_MenuNavSequence simulates a realistic
// menu-nav sequence (DownDownEnter — pick the third item in a picker)
// and asserts every key reached the remote, in order. Catches dispatch
// reordering bugs that single-key tests would miss.
func TestIssue1112_RemoteKeySender_MenuNavSequence(t *testing.T) {
	runner, captured := captureRemoteCalls()
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	if err := sender.SendNamedKey("Down"); err != nil {
		t.Fatalf("Down 1: %v", err)
	}
	if err := sender.SendNamedKey("Down"); err != nil {
		t.Fatalf("Down 2: %v", err)
	}
	if err := sender.SendEnter(); err != nil {
		t.Fatalf("Enter: %v", err)
	}

	calls := captured()
	if len(calls) != 3 {
		t.Fatalf("expected 3 SSH calls, got %d: %v", len(calls), calls)
	}
	wantSequence := [][]string{
		{"session", "send-keys", "remote-sess-id", "--named-key", "Down"},
		{"session", "send-keys", "remote-sess-id", "--named-key", "Down"},
		{"session", "send-keys", "remote-sess-id", "--enter"},
	}
	for i, want := range wantSequence {
		if !sliceEqualLocal(calls[i], want) {
			t.Errorf("call %d argv = %v, want %v", i, calls[i], want)
		}
	}
}

// TestIssue1112_RemoteKeySender_RejectsUnknownNamedKey is the failure-
// mode guard: an empty named key must fail fast on the local side, not
// land on the remote and produce an opaque tmux error.
func TestIssue1112_RemoteKeySender_RejectsUnknownNamedKey(t *testing.T) {
	runner, captured := captureRemoteCalls()
	sender := NewRemoteKeySender(runner, "remote-sess-id", context.Background())

	if err := sender.SendNamedKey(""); err == nil {
		t.Fatal("SendNamedKey(\"\") should fail")
	}
	if calls := captured(); len(calls) != 0 {
		t.Errorf("empty key should not produce an SSH call; got %v", calls)
	}
}

// sliceEqualLocal duplicates sliceEqual from issue1102_insert_remote_test.go
// — keeping #1112 tests free of cross-file fixture coupling so renames in
// one file don't cascade.
func sliceEqualLocal(a, b []string) bool {
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
