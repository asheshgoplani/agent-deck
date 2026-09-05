package main

import (
	"sync/atomic"
	"testing"
)

// Issue #2033: the residual of #1979/#1980. The #1980 gate on the
// Ctrl+C-and-resend recovery reads the VISIBLE pane, so its evidence expires
// the moment the body scrolls off the top. A target that is in fact generating
// — hook status "running" — whose spike-filtered GetStatus decayed to idle,
// with the body already out of view, still took the interrupt. The hook-driven
// busy signal (#1578) is the authoritative answer and must be consulted before
// any interrupt key is sent; pane content stays the secondary signal for tools
// whose hooks are absent or not firing.

// busyPaneBodyScrolledOff is the busy target after the body has scrolled out of
// the visible pane: only streaming output and the composer remain.
func busyPaneBodyScrolledOff() string {
	return "  ⎿  streaming output line 1\n" +
		"  ⎿  streaming output line 2\n" +
		"────────────────────────────────────────\n" +
		"❯ \n" +
		"────────────────────────────────────────"
}

func hookBusy() (bool, bool)    { return true, true }
func hookIdle() (bool, bool)    { return false, true }
func hookUnknown() (bool, bool) { return false, false }

// TestInterruptSuppressedWhenHookStatusBusy pins the fix (a): the hook says
// the target is mid-turn, the body is not on screen, the heuristic never
// reports active. No interrupt keys, exactly one delivery, `queued` verdict.
func TestInterruptSuppressedWhenHookStatusBusy(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"idle"},
		panes:    []string{busyPaneBodyScrolledOff()},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries:       25,
		checkDelay:       0,
		verifyDelivery:   true,
		targetBusyByHook: hookBusy,
	})

	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Errorf("SendCtrlC called %d times against a hook-busy target, want 0 — "+
			"Ctrl+C destroys the in-flight turn (#2033)", n)
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
		t.Errorf("SendKeysAndEnter called %d times, want exactly 1 — a resend duplicates the message (#2033)", n)
	}
	if delivery != deliveryQueued {
		t.Errorf("delivery = %q, want %q", delivery, deliveryQueued)
	}
	if err != nil {
		t.Errorf("unexpected error for a queued message: %v", err)
	}
}

// TestNoWaitClassifiesHookBusyAsQueued pins the sibling --no-wait path. Its
// resend budget is intentionally disabled, but that must not disable the busy
// classification or turn a safely queued message into a delivery failure.
func TestNoWaitClassifiesHookBusyAsQueued(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"idle"},
		panes:    []string{busyPaneBodyScrolledOff()},
	}
	opts := noWaitSendOptions()
	opts.checkDelay = 0
	opts.targetBusyByHook = hookBusy

	delivery, err := sendWithRetryTarget(mock, "queued no-wait message", false, opts)
	if err != nil {
		t.Fatalf("hook-busy --no-wait send returned error: %v", err)
	}
	if delivery != deliveryQueued {
		t.Fatalf("delivery = %q, want %q", delivery, deliveryQueued)
	}
	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Fatalf("SendCtrlC called %d times, want 0", n)
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
		t.Fatalf("SendKeysAndEnter called %d times, want 1", n)
	}
}

// TestInterruptUnchangedWhenHookStatusIdle pins (b): a hook-idle target with
// nothing on screen is the #876 TUI-init loss, and the recovery must still fire
// exactly as before.
func TestInterruptUnchangedWhenHookStatusIdle(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{emptyComposerPane()},
	}

	delivery, _ := sendWithRetryTarget(mock, "vanished message", false, sendRetryOptions{
		maxRetries:       25,
		checkDelay:       0,
		verifyDelivery:   true,
		targetBusyByHook: hookIdle,
	})

	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n == 0 {
		t.Error("SendCtrlC never called for a hook-idle target with nothing on screen — " +
			"the #876 TUI-init recovery must survive the #2033 gate")
	}
	if n := atomic.LoadInt32(&mock.sendKeysCalls); n < 2 {
		t.Errorf("SendKeysAndEnter called %d times, want >1 — the lost-message resend must still fire", n)
	}
	if delivery == deliveryQueued {
		t.Errorf("delivery = %q for an idle target, want the pre-#2033 verdict", delivery)
	}
}

// TestInterruptFallsBackToPaneCheckWithoutHooks pins (c): when the hook signal
// is unavailable the #1980 pane check is the gate — not the interrupt. With the
// body on screen the recovery stays suppressed; with nothing on screen it fires.
func TestInterruptFallsBackToPaneCheckWithoutHooks(t *testing.T) {
	const msg = "PROBE reply with only OK"

	onScreen := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneWithBody(msg)},
	}
	delivery, _ := sendWithRetryTarget(onScreen, msg, false, sendRetryOptions{
		maxRetries:       25,
		checkDelay:       0,
		verifyDelivery:   true,
		targetBusyByHook: hookUnknown,
	})
	if n := atomic.LoadInt32(&onScreen.sendCtrlCCalls); n != 0 {
		t.Errorf("SendCtrlC called %d times with hooks unavailable and the body on screen, want 0 — "+
			"the #1980 pane check must remain the fallback gate", n)
	}
	if delivery == deliveryQueued {
		t.Errorf("delivery = %q without a hook signal, want the #1980 verdict — only the hook may claim queued", delivery)
	}

	offScreen := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{emptyComposerPane()},
	}
	_, _ = sendWithRetryTarget(offScreen, "vanished message", false, sendRetryOptions{
		maxRetries:       25,
		checkDelay:       0,
		verifyDelivery:   true,
		targetBusyByHook: hookUnknown,
	})
	if n := atomic.LoadInt32(&offScreen.sendCtrlCCalls); n == 0 {
		t.Error("SendCtrlC never called with hooks unavailable and nothing on screen — " +
			"the TUI-init recovery must still fire when the pane check authorises it")
	}
}

// TestInterruptSuppressedWhenNilHookProbeAndBodyOnScreen guards the untouched
// callers (launch, tests) that pass no probe at all: behaviour is exactly #1980.
func TestInterruptSuppressedWhenNilHookProbeAndBodyOnScreen(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneWithBody(msg)},
	}
	_, _ = sendWithRetryTarget(mock, msg, false, sendRetryOptions{maxRetries: 25, checkDelay: 0})
	if n := atomic.LoadInt32(&mock.sendCtrlCCalls); n != 0 {
		t.Errorf("SendCtrlC called %d times with no hook probe and the body on screen, want 0", n)
	}
}

// TestInterruptSuppressedWhenHookStatusBusy_RemoteSession records the parity
// boundary explicitly: `session send` resolves local []*session.Instance and
// has no RemoteSessionInfo transport path. Remote sends execute this command
// over SSH on the owning host, where the same local hook gate is exercised.
func TestInterruptSuppressedWhenHookStatusBusy_RemoteSession(t *testing.T) {
	t.Skip("RemoteSessionInfo is a TUI cache row, not a session-send target; remote delivery invokes session send over SSH on the owning host")
}
