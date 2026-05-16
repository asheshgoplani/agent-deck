package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// Phase 1 v1.9 regression coverage for issue #876 (silent send drop) —
// cases send-001 and send-002.
//
// messageDeliveryToken (session_cmd.go:2123) is the helper that gates the
// "did the message body show up in the captured pane?" branch of
// sendWithRetryTarget's verifyDelivery loop. It has zero direct tests
// today; the branch is only exercised indirectly via
// TestSendWithRetryTarget_VerifyDelivery_AcceptsMessageInPane with a
// 19-character token. Conductor / scripted callers (S-CLI-4) routinely
// send 100KB-class prompts — exactly the size where the 64-char truncation
// matters and where a regression that returned "" for "long" inputs would
// silently disable body-in-pane verification, re-opening the #876 hole.

// send-001: messageDeliveryToken contract.
//
// The function MUST:
//   - Return "" for short or whitespace-only messages (avoids false-positive
//     matches on common short strings like "hi" / "ok" appearing in any pane).
//   - Return the trimmed prefix capped at 64 chars for longer messages.
//
// Constants pinned (minTokenLen=12, maxTokenLen=64) so a refactor that
// flipped them invalidates the calibration that callers depend on.
func TestMessageDeliveryToken_Contract(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty → empty", in: "", want: ""},
		{name: "whitespace-only → empty", in: "   \n\t  ", want: ""},
		{name: "below minTokenLen → empty", in: "short msg", want: ""},
		{name: "exactly minTokenLen-1 → empty", in: strings.Repeat("a", 11), want: ""},
		{name: "exactly minTokenLen → kept", in: strings.Repeat("a", 12), want: strings.Repeat("a", 12)},
		{name: "between min and max → kept verbatim", in: "DELIVERY_TOKEN_876_marker", want: "DELIVERY_TOKEN_876_marker"},
		{name: "exactly maxTokenLen → kept verbatim", in: strings.Repeat("x", 64), want: strings.Repeat("x", 64)},
		{name: "above maxTokenLen → truncated", in: strings.Repeat("y", 100), want: strings.Repeat("y", 64)},
		{
			// 100KB prompt — the S-CLI-4 conductor surface from USE-CASES-AND-TESTS.md.
			// MUST NOT return "" (that would silently disable body-in-pane verify);
			// must truncate to exactly 64 chars.
			name: "100KB prompt truncates to 64 not empty",
			in:   "PROMPT_HEAD_DIAGTOKEN_99__" + strings.Repeat("z", 100*1024),
			want: ("PROMPT_HEAD_DIAGTOKEN_99__" + strings.Repeat("z", 100*1024))[:64],
		},
		{
			// Leading/trailing whitespace must be trimmed BEFORE the cap so the
			// returned token is always content, not whitespace.
			name: "whitespace trimmed before cap",
			in:   "    " + strings.Repeat("z", 70) + "    ",
			want: strings.Repeat("z", 64),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageDeliveryToken(tc.in); got != tc.want {
				if len(got) > 80 || len(tc.want) > 80 {
					t.Errorf("messageDeliveryToken: len(got)=%d len(want)=%d "+
						"(short prefix: got=%q want=%q)",
						len(got), len(tc.want),
						truncForLog(got, 32), truncForLog(tc.want, 32))
				} else {
					t.Errorf("messageDeliveryToken(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
}

// send-002: large-prompt verification end-to-end.
//
// Drives sendWithRetryTarget with verifyDelivery=true, a multi-KB message,
// and a pane that contains the message's first 64 chars (mirroring how a
// real Claude composer renders a wrapped paste). MUST return nil — i.e. the
// truncated body match is recognized as positive delivery evidence.
//
// This is the test that closes the conductor / S-CLI-4 silent-drop gap:
// without messageDeliveryToken's 64-char prefix, the verifyDelivery loop
// would never observe the message body for any prompt larger than the
// pane's visible surface, and #876 would re-open for large prompts.
func TestSendWithRetryTarget_VerifyDelivery_LargePromptBodyMatch(t *testing.T) {
	// 8KB prompt with a distinctive 32-char head; well past maxTokenLen (64).
	const head = "DIAGTOKEN_876_LARGE_PROMPT_HEAD_"
	body := head + strings.Repeat("payload-", 1024) // ~8KB
	if len(body) < 4096 {
		t.Fatalf("test setup: body too short (%d)", len(body))
	}

	// Pane shows the first 64 chars of the body (what messageDeliveryToken
	// captures and looks for). Real Claude composers wrap and truncate but
	// always preserve the leading content; a 64-char head match is the
	// canonical positive signal.
	paneAfterPaste := head + strings.Repeat("payload-", 4) // 32 + 32 = 64 chars of head+payload
	if len(paneAfterPaste) < 64 {
		t.Fatalf("test setup: paneAfterPaste must include >=64 chars of body, got %d", len(paneAfterPaste))
	}

	statuses := []string{"waiting", "waiting", "waiting", "waiting", "waiting"}
	panes := []string{"", paneAfterPaste, paneAfterPaste, paneAfterPaste, paneAfterPaste}
	mock := &mockSendRetryTarget{statuses: statuses, panes: panes}

	err := sendWithRetryTarget(mock, body, false, sendRetryOptions{
		maxRetries: 5, checkDelay: 0, verifyDelivery: true,
	})
	if err != nil {
		t.Fatalf("verifyDelivery on large prompt must accept body-prefix match: got error %v "+
			"(this is the S-CLI-4 / #876 conductor large-prompt silent-drop scenario)", err)
	}
	// Sanity: the initial send fired exactly once. A regression that decided
	// "large body → just retry harder" would inflate this and re-open #479.
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("expected 1 SendKeysAndEnter call (initial only), got %d", got)
	}
}

// truncForLog avoids dumping a 100KB string in test failures. Returns
// content-bearing prefix + length.
func truncForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(" + itoa(len(s)) + " chars)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
