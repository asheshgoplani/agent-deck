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

// TestSend_NonClaudeTool_NotReportedDropped is the issue #1238 regression: a
// successful send to a non-Claude tool must NOT return the "dropped silently"
// error, for every non-Claude tool — not just codex (#1205).
func TestSend_NonClaudeTool_NotReportedDropped(t *testing.T) {
	for _, tool := range []string{"codex", "codewhale", "gemini", "opencode"} {
		t.Run(tool, func(t *testing.T) {
			mock := nonClaudeShapedTarget()
			_, err := sendWithRetryTarget(mock, "do the multi-line task please", skipClaudeDeliveryVerify(tool), sendRetryOptions{
				maxRetries: 50, checkDelay: 0, verifyDelivery: true,
			})
			if err != nil {
				t.Fatalf("%s: delivered send must not be reported dropped, got: %v", tool, err)
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

func TestSend_PiComposerDistinguishesSubmittedFromTyped(t *testing.T) {
	const message = "Reply with exactly this text and nothing else"
	border := strings.Repeat("\u2500", 40)
	cases := []struct {
		name    string
		pane    string
		want    string
		wantErr bool
	}{
		{
			name: "submitted",
			pane: "transcript\n" + message + "\n\nHello, world!\n" + border + "\n  \n" + border + "\n~/src/project",
			want: deliverySubmitted,
		},
		{
			name:    "still in composer",
			pane:    "transcript\n" + border + "\n" + message + "\n" + border + "\n~/src/project",
			want:    deliveryTyped,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockSendRetryTarget{
				statuses: []string{"waiting"},
				panes:    []string{"transcript", tc.pane},
			}
			res, err := executeSend(mock, "pi", message, false, sendExecTuning{
				retry: sendRetryOptions{maxRetries: 1, checkDelay: 0, verifyDelivery: true},
			})
			delivery := res.delivery
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if delivery != tc.want {
				t.Fatalf("delivery = %q, want %q", delivery, tc.want)
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
