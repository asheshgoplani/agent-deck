package main

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for verifyPromptConsumedAfterLaunch — the v1.7.64 fix for:
//
//   agent-deck launch -m "prompt" --no-wait
//
// where claude at the welcome screen occasionally eats the initial Enter and
// leaves the prompt typed-but-not-submitted. Existing 1.2s verify budget on
// the launch path (launch_cmd.go:403-414) was too short to observe and
// recover from this race on cold starts.
//
// Contract (per internal task spec, April 2026):
//   1. After initial send-keys with -m, poll the pane for up to maxWait.
//      "Consumed" = composer has rendered AND the message text is no longer
//      visible in the composer input line.
//   2. If still unconsumed after the first poll window, retry send-keys
//      exactly once.
//   3. Poll again for up to maxWait.
//   4. If still unconsumed, emit a stderr-style warning to the injected
//      writer and return (best-effort — do NOT fail the launch).
//
// All pane strings below are synthetic (no captured user data) per the
// sanitization rule.

const (
	// paneConsumed: composer rendered, input line empty → message was
	// submitted and claude is now at a clean prompt. This is the success
	// shape we poll for.
	paneConsumed = "some output above\n" +
		"─────────────────────────\n" +
		"❯\n" +
		"─────────────────────────\n"

	// paneUnsent: composer rendered with message still typed in the input
	// line → Enter was never accepted.
	paneUnsentTemplate = "welcome to claude\n" +
		"─────────────────────────\n" +
		"❯ %s\n" +
		"─────────────────────────\n"

	// paneWelcomeNoComposer: pre-TUI, no composer region visible at all →
	// must NOT be classified as consumed (prompt could still be pending).
	paneWelcomeNoComposer = "Claude is loading...\n"
)

func paneUnsent(msg string) string {
	return strings.Replace(paneUnsentTemplate, "%s", msg, 1)
}

func TestVerifyPromptConsumedAfterLaunch_ConsumedFirstPoll_NoRetry_NoWarning(t *testing.T) {
	// First capture already shows an empty composer → consumed on first poll.
	// No retry send-keys should fire. No warning.
	mock := &mockSendRetryTarget{
		panes: []string{paneConsumed},
	}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunch(mock, "do the thing", 50*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 0 {
		t.Fatalf("SendKeysAndEnter retry count: got %d, want 0 (prompt already consumed)", got)
	}
	if warn.Len() != 0 {
		t.Fatalf("unexpected warning written: %q", warn.String())
	}
}

func TestVerifyPromptConsumedAfterLaunch_UnsentFirstWindow_RetryThenConsumed_OneRetry_NoWarning(t *testing.T) {
	// Pane shows prompt typed-but-unconsumed through the entire first poll
	// window, then after the retry send-keys it shows the consumed shape.
	// Expect exactly one SendKeysAndEnter retry and no warning.
	msg := "explain this code"
	mock := &mockSendRetryTarget{
		panes: []string{
			paneUnsent(msg), paneUnsent(msg), paneUnsent(msg), paneUnsent(msg),
			paneUnsent(msg), paneUnsent(msg), paneUnsent(msg), paneUnsent(msg),
			paneUnsent(msg), paneUnsent(msg), paneUnsent(msg), paneUnsent(msg),
			// After the retry kicks in, subsequent captures show consumed.
			paneConsumed, paneConsumed, paneConsumed,
		},
	}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunch(mock, msg, 20*time.Millisecond, 2*time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("SendKeysAndEnter retry count: got %d, want exactly 1", got)
	}
	if warn.Len() != 0 {
		t.Fatalf("unexpected warning after successful retry: %q", warn.String())
	}
}

func TestVerifyPromptConsumedAfterLaunch_UnsentBothWindows_OneRetry_WarningEmitted(t *testing.T) {
	// Pane always shows prompt unconsumed. Expect: exactly one retry
	// send-keys + warning written to the injected writer + non-fatal return.
	msg := "launch stuck prompt"
	mock := &mockSendRetryTarget{
		panes: []string{paneUnsent(msg)}, // mock stays on last pane indefinitely
	}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunch(mock, msg, 10*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("SendKeysAndEnter retry count: got %d, want exactly 1", got)
	}
	if warn.Len() == 0 {
		t.Fatalf("expected warning on second no-op, got empty stderr writer")
	}
	if !strings.Contains(strings.ToLower(warn.String()), "prompt") {
		t.Fatalf("warning should mention the prompt; got %q", warn.String())
	}
}

func TestVerifyPromptConsumedAfterLaunch_WelcomeScreenNoComposer_NotConsumed_TriggersRetry(t *testing.T) {
	// No composer has rendered yet (welcome/loading screen). "Not unsent"
	// must NOT mean "consumed" — we require the composer to be present AND
	// empty. So this scenario should fall through to retry + warning, not
	// short-circuit as success.
	mock := &mockSendRetryTarget{
		panes: []string{paneWelcomeNoComposer},
	}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunch(mock, "ignored", 5*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("expected retry when composer never rendered; SendKeysAndEnter calls=%d want=1", got)
	}
	if warn.Len() == 0 {
		t.Fatalf("expected warning when composer never rendered")
	}
}

func TestVerifyPromptConsumedAfterLaunch_RespectsWallTimeBudget(t *testing.T) {
	// Poll windows must honour maxWait. With a 30ms budget per window and
	// an always-unsent pane, total wall time should be bounded by roughly
	// 2*maxWait + retry overhead. Anything approaching the production 10s
	// default inside a test means the budget enforcement is broken.
	msg := "budget guard"
	mock := &mockSendRetryTarget{
		panes: []string{paneUnsent(msg)},
	}
	var warn bytes.Buffer

	start := time.Now()
	verifyPromptConsumedAfterLaunch(mock, msg, 30*time.Millisecond, time.Millisecond, &warn)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("verify took %v, expected <500ms with 30ms budgets (unbounded poll bug)", elapsed)
	}
}
