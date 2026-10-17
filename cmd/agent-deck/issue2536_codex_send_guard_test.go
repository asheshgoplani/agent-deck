package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Issue #2536: `session send` to a Codex session bypassed the composer draft
// guard (#1409). executeSend gated GuardComposerDraft on IsClaudeCompatible,
// so an automated message was typed straight into a Codex composer that held
// a half-written operator draft, merging the two into one prompt.
//
// Codex must get the same hold-then-refuse behavior Claude has: hold while
// the composer holds operator input, refuse at the bound without typing,
// pressing Enter, or sending Ctrl+C, and send normally when the composer only
// shows Codex's dim placeholder.
// ---------------------------------------------------------------------------

const issue2536CodexFooter = "  gpt-5.5-codex high · ~/work/lane · Context 82% left · weekly 91% left"

// issue2536CodexPane renders a Codex 0.160 frame as `tmux capture-pane -e`
// returns it: an earlier user message and agent reply in the transcript,
// then the composer and footer. composerBody is the raw text after the "›"
// glyph, ANSI included, so callers can render the dim placeholder.
func issue2536CodexPane(composerBody string) string {
	return strings.Join([]string{
		"› run the lint step",
		"",
		"• Ran make lint",
		"  └ ok",
		"",
		"─ Worked for 12s ─────────────────────────────────────",
		"",
		"\x1b[1m›\x1b[0m " + composerBody,
		"",
		issue2536CodexFooter,
		"",
	}, "\n")
}

func issue2536CodexDraft(text string) string { return issue2536CodexPane(text) }

func issue2536CodexEmpty() string {
	return issue2536CodexPane("\x1b[2mAsk Codex to do anything\x1b[0m")
}

func TestIssue2536_CodexSendRefusesOperatorDraftWithoutTyping(t *testing.T) {
	mock := &guardedSendMock{
		draftPane:  issue2536CodexDraft("halfway through writing a refactor plan for"),
		chunkedErr: errors.New("typing unavailable"),
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 4, checkDelay: 0})
	res, err := executeSend(mock, "codex", "[from conductor] status please", false, tun)
	if err == nil || res.delivery != deliveryComposerBlocked {
		t.Fatalf("issue #2536: Codex send into an occupied composer must refuse with %q, got delivery=%q err=%v",
			deliveryComposerBlocked, res.delivery, err)
	}
	if mock.sendKeysCalls != 0 || mock.enterCalls != 0 || mock.ctrlCCalls != 0 || mock.chunkedCalls != 0 {
		t.Fatalf("issue #2536: refusal must not touch the Codex composer: keys=%d enter=%d ctrlc=%d chunked=%d",
			mock.sendKeysCalls, mock.enterCalls, mock.ctrlCCalls, mock.chunkedCalls)
	}
}

func TestIssue2536_CodexNoWaitSendAlsoGuarded(t *testing.T) {
	mock := &guardedSendMock{draftPane: issue2536CodexDraft("draft in progress")}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 4, checkDelay: 0})
	res, err := executeSend(mock, "codex", "AUTOMATED", true, tun)
	if err == nil || res.delivery != deliveryComposerBlocked {
		t.Fatalf("issue #2536: --no-wait Codex send must still guard the composer, got delivery=%q err=%v", res.delivery, err)
	}
	if mock.sendKeysCalls != 0 || mock.enterCalls != 0 || mock.ctrlCCalls != 0 {
		t.Fatalf("issue #2536: refusal must not touch the Codex composer: %+v", mock)
	}
}

func TestIssue2536_CodexSendHoldsUntilDraftClears(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes: []string{
			issue2536CodexDraft("let me finish this sentence"), // guard: busy
			issue2536CodexDraft("let me finish this sentence"), // guard: still busy
			issue2536CodexEmpty(),                              // guard: operator submitted it
			issue2536CodexEmpty(),
		},
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 2, checkDelay: 0})
	tun.guardHold = 500 * time.Millisecond
	res, err := executeSend(mock, "codex", "run tests", false, tun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("expected exactly one send once the draft cleared, got %d", got)
	}
	if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 0 {
		t.Fatalf("the guard must never Ctrl+C a Codex composer, got %d", got)
	}
	if res.held <= 0 {
		t.Fatalf("expected the guard to hold while the draft was present, held=%v", res.held)
	}
	if got := mock.paneIdx.Load(); got < 3 {
		t.Fatalf("expected the guard to poll the Codex composer, captured %d times", got)
	}
}

func TestIssue2536_CodexPlaceholderDoesNotBlock(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{issue2536CodexEmpty()},
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 2, checkDelay: 0})
	res, err := executeSend(mock, "codex", "run tests", false, tun)
	if err != nil {
		t.Fatalf("an empty Codex composer (dim placeholder) must not block: %v", err)
	}
	if res.delivery == deliveryComposerBlocked {
		t.Fatalf("placeholder read as an operator draft")
	}
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("expected one send, got %d", got)
	}
}
