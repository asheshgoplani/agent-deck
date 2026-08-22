package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// #1238: the #876 delivery-verify keys off Claude-specific signals (an "active"
// transition, composer glyph, unsent-paste markers). Non-Claude tools
// (codewhale, gemini, codex) never surface those, so a *delivered* message gets
// reported as "dropped silently". The fix routes the post-send verify through
// session.UsesClaudeDeliveryVerify(tool): Claude tools keep the verify, all
// non-Claude tools skip it — the general superset of #1228's codex-only skip.

// nonClaudeShapedTarget returns a mock in the shape that breaks the verifier:
// status stays "waiting" and the pane never renders a Claude composer/paste
// marker, even though (in the real world) the inner agent is processing.
func nonClaudeShapedTarget() *mockSendRetryTarget {
	return &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{""}}
}

// Non-Claude tools use the same confirmation contract: without a pane or
// turn-start transition they fail loudly rather than claiming delivery.
func TestSend_NonClaudeTool_UnconfirmedFails(t *testing.T) {
	for _, tool := range []string{"codex", "codewhale", "gemini", "opencode"} {
		t.Run(tool, func(t *testing.T) {
			mock := nonClaudeShapedTarget()
			_, err := sendWithRetryTarget(mock, "do the multi-line task please", skipClaudeDeliveryVerify(tool), sendRetryOptions{
				maxRetries: 50, checkDelay: 0, verifyDelivery: true,
			})
			if err == nil {
				t.Fatalf("%s: unconfirmed send returned success", tool)
			}
			if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
				t.Fatalf("%s: expected exactly 1 atomic send, got %d", tool, got)
			}
			if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 0 {
				t.Fatalf("%s: must never receive destructive Ctrl+C recovery, got %d", tool, got)
			}
			if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
				t.Fatalf("%s: must not Enter-spam a non-Claude composer, got %d", tool, got)
			}
		})
	}
}

// TestSend_ClaudeTool_VerifyPreserved guards against over-skipping: Claude tools
// must still run the #876 verify, so a genuinely silent drop (no markers, never
// active) is still surfaced as an error.
func TestSend_ClaudeTool_VerifyPreserved(t *testing.T) {
	mock := nonClaudeShapedTarget()
	_, err := sendWithRetryTarget(mock, "do the multi-line task please", skipClaudeDeliveryVerify("claude"), sendRetryOptions{
		maxRetries: 12, checkDelay: 0, verifyDelivery: true,
	})
	if err == nil {
		t.Fatal("claude must keep the #876 verify: a silent drop should still error")
	}
	if !strings.Contains(err.Error(), "dropped silently") {
		t.Fatalf("expected #876 silent-drop error, got: %v", err)
	}
}
