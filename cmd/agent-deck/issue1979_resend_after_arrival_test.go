package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// Issue #1979: the Ctrl+C-then-resend recovery exists for a message lost during
// TUI init, where nothing ever reached the pane. It fired on a different case
// too — a target that is busy and has the message queued, whose working state
// simply is not being observed — and there it interrupted the in-flight turn
// and delivered the body a second time, at exit 0.
//
// #479 established that this path double-sends and disabled it for --no-wait
// (noWaitSendOptions sets maxFullResends: -1). The verified path kept it.
//
// The distinguishing signal is already in the loop: the body appearing in the
// pane is arrival evidence. Once arrival is observed, a resend cannot be a
// recovery — it can only duplicate.

// busyPaneWithBody is what a queued-but-unobserved target looks like: the body
// is present above the composer rules, and the status never reads active.
func busyPaneWithBody(msg string) string {
	return strings.Join([]string{
		"  ⎿  earlier output",
		"❯ " + msg,
		"────────────────────────────────────────",
		"❯ Press up to edit queued messages",
		"────────────────────────────────────────",
	}, "\n")
}

// TestResendSuppressedOnceBodyHasArrived pins the fix: with the body already
// visible, the loop must never Ctrl+C the target nor retype the message, no
// matter how long it sits without an observable "active".
func TestResendSuppressedOnceBodyHasArrived(t *testing.T) {
	const msg = "PROBE reply with only OK"
	pane := busyPaneWithBody(msg)

	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{pane},
	}

	// Budget well past fullResendThreshold (8) so the resend has every chance.
	_, _ = sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 25,
		checkDelay: 0,
	})

	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Errorf("SendCtrlC called %d times after the body had already arrived, want 0 — "+
			"Ctrl+C interrupts whatever the target is doing (#1979)", n)
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
		t.Errorf("SendKeysAndEnter called %d times, want exactly 1 — "+
			"a resend after arrival duplicates the message (#1979, #479)", n)
	}
}

// TestResendStillFiresWhenNothingArrived guards the other direction: the #876
// TUI-init case, where the body never reached the pane, must still be
// recovered. A fix that suppressed the resend unconditionally would break it.
func TestResendStillFiresWhenNothingArrived(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"❯ \n────────────────────────────────────────"}, // empty composer, no body
	}

	_, _ = sendWithRetryTarget(mock, "vanished message", false, sendRetryOptions{
		maxRetries: 25,
		checkDelay: 0,
	})

	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n == 0 {
		t.Error("SendCtrlC never called although nothing ever arrived — " +
			"the #876 TUI-init recovery must survive the #1979 fix")
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n < 2 {
		t.Errorf("SendKeysAndEnter called %d times, want >1 — the lost-message resend must still fire", n)
	}
}

// composerHoldingBody is the first step of the TUI-init loss the resend exists
// for: the keys land in the composer before the input handler is ready.
func composerHoldingBody(msg string) string {
	return strings.Join([]string{
		"  earlier output",
		"────────────────────────────────────────",
		"❯ " + msg,
		"────────────────────────────────────────",
	}, "\n")
}

func emptyComposerPane() string {
	return strings.Join([]string{
		"  earlier output",
		"────────────────────────────────────────",
		"❯ ",
		"────────────────────────────────────────",
	}, "\n")
}

// TestResendStillFiresAfterComposerMarkerCleared is the regression guard for the
// review finding on the first attempt at this fix. That attempt gated the resend
// on sawDeliveryEvidence, which latches — and one of its sources is the composer
// merely HOLDING the message, which is the opening step of the TUI-init loss
// this recovery exists for. Gating on the latch therefore suppressed the resend
// exactly when it was needed, losing the message; and because the same flag
// suppresses the #876 error at the end of the budget, the loss was reported as
// success. Gating on a per-iteration observation instead keeps the recovery.
//
// Marker first, then a cleared composer: nothing was ever submitted.
func TestResendStillFiresAfterComposerMarkerCleared(t *testing.T) {
	const msg = "recover me please"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{composerHoldingBody(msg), emptyComposerPane(), emptyComposerPane()},
	}

	_, _ = sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 25,
		checkDelay: 0,
	})

	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n == 0 {
		t.Error("resend never fired after the composer marker cleared — the message " +
			"landed in the composer, was discarded, and was not recovered (#876 regression)")
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n < 2 {
		t.Errorf("SendKeysAndEnter called %d times, want >1 — the lost message was never re-sent", n)
	}
	// Note: on this path the call still returns success, both before and after
	// this change, because the loop treats held-then-cleared as submission
	// evidence. That is pre-existing and out of scope here; asserting on it
	// would fail on base for a reason this change does not address.
}

// TestResendSuppressedWhenPaneCannotBeRead pins the capture-failure case. The
// resend is destructive — it Ctrl+Cs the target — so it must require positive
// evidence that nothing is on screen. A failed capture is not that evidence:
// bodyInPaneNow is only assigned when the capture succeeded, so without the
// paneNow.OK term it would read false by absence of an observation rather than
// by an observation of absence, and an unreadable pane would re-authorize the
// interrupt against a target that is working fine.
func TestResendSuppressedWhenPaneCannotBeRead(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{""},
		paneErrs: []error{errors.New("capture failed")},
	}

	_, _ = sendWithRetryTarget(mock, "some message", false, sendRetryOptions{
		maxRetries: 25,
		checkDelay: 0,
	})

	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Errorf("SendCtrlC called %d times while the pane could not be read, want 0 — "+
			"an unreadable pane is not evidence that the message is absent (#1979)", n)
	}
}

// TestResendSuppressedForShortQueuedMessage covers messages below the delivery
// token's 12-byte floor. messageDeliveryToken returns "" for those, so
// bodyInPaneNow can never become true no matter what is on screen — which would
// leave a short queued message ("OK") firing the very Ctrl+C-and-resend this
// change exists to prevent. With no usable needle we cannot distinguish arrival
// from absence, so the destructive branch must not fire.
func TestResendSuppressedForShortQueuedMessage(t *testing.T) {
	const msg = "OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneWithBody(msg)},
	}

	_, _ = sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 25,
		checkDelay: 0,
	})

	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Errorf("SendCtrlC called %d times for a short queued message, want 0 — "+
			"a body below the delivery-token floor is still a body (#1979)", n)
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
		t.Errorf("SendKeysAndEnter called %d times, want exactly 1 — a short message was duplicated", n)
	}
}
