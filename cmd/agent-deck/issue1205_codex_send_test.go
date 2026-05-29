package main

import (
	"sync/atomic"
	"testing"
)

// #1205: codex's TUI exposes no "active" transition or composer/paste markers,
// so the #876 delivery-verify false-negatives and its Ctrl+C recovery kills the
// pane. Codex sends are routed through skipVerify instead.

// codexShapedTarget returns a mock whose status never reaches "active" and whose
// pane shows no composer marker — the shape that broke the verifier for codex.
func codexShapedTarget() *mockSendRetryTarget {
	return &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{""}}
}

// TestSendWithRetryTarget_CodexSkipVerify_NoDestructiveRecovery asserts the codex
// path (skipVerify) is a single atomic send: no Ctrl+C, no Enter spam, no error,
// even though the target never transitions to "active".
func TestSendWithRetryTarget_CodexSkipVerify_NoDestructiveRecovery(t *testing.T) {
	mock := codexShapedTarget()
	err := sendWithRetryTarget(mock, "hello codex", true, sendRetryOptions{
		maxRetries: 50, checkDelay: 0, verifyDelivery: true,
	})
	if err != nil {
		t.Fatalf("codex skipVerify send should succeed, got: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("expected 1 SendKeysAndEnter, got %d", got)
	}
	if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 0 {
		t.Fatalf("codex must never receive Ctrl+C, got %d", got)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("expected no Enter nudges, got %d", got)
	}
}

// TestSendWithRetryTarget_CodexShapedWithoutSkip_TriggersCtrlC guards the
// regression: without skipVerify the same codex-shaped state drives the
// destructive Ctrl+C recovery the fix avoids.
func TestSendWithRetryTarget_CodexShapedWithoutSkip_TriggersCtrlC(t *testing.T) {
	mock := codexShapedTarget()
	_ = sendWithRetryTarget(mock, "hello codex", false, sendRetryOptions{
		maxRetries: 12, checkDelay: 0, maxFullResends: 1,
	})
	if atomic.LoadInt32(&mock.sendCtrlCCalls) == 0 {
		t.Fatal("expected Ctrl+C recovery on codex-shaped waiting state")
	}
}

// TestSendWithRetryTarget_Codex_RemoteSessionNotApplicable documents, per the
// cmd/agent-deck RemoteSession guideline, why this change needs no
// RemoteSession-specific coverage.
func TestSendWithRetryTarget_Codex_RemoteSessionNotApplicable(t *testing.T) {
	t.Skip("RemoteSession N/A: `agent-deck session send` has no remote branch — it " +
		"drives the local tmux session via sendWithRetryTarget. RemoteSession is a " +
		"TUI-over-SSH viewer (remote_keysender) with no post-send verify loop, so the " +
		"codex skip-verify applies to the local/CLI send path only.")
}
