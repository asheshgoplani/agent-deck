package main

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Gated-composer recovery.
//
// Failure this closes (observed live 2026-07-24): a Claude session hit
// "API Error: Unable to connect to API (ConnectionRefused)" and its turn state
// machine never returned to idle. The input handler kept accepting and
// rendering keystrokes — typed text appeared in the composer — but the submit
// handler stayed gated, so Enter was a no-op. `session send` typed the message,
// re-pressed Enter for its whole budget, and correctly reported
// typed_not_submitted (#1413)... after which the session sat wedged for an
// hour because nothing ever tried the one key that releases the gate.
//
// Escape releases it; the following Enter submits. These tests pin that the
// send path escalates to Escape+Enter after plain Enter has demonstrably
// failed, that the escalation is bounded, and that it never fires on a
// composer that is submitting normally.
// ---------------------------------------------------------------------------

// TestSendRetry_GatedComposer_EscalatesToEscapeThenEnter is the recovery
// succeeding: the composer holds the message across three checks (plain Enter
// refused each time), Escape fires, and the message then submits.
func TestSendRetry_GatedComposer_EscalatesToEscapeThenEnter(t *testing.T) {
	const msg = "resume supervising, three children are still running"
	mock := &mockSendRetryTarget{
		// checks 1-5: composer still holds the message (Enter is being
		// swallowed). check 5 crosses escapeRecoveryThreshold -> Escape.
		// checks 6-7: gate released, composer empty and the turn is active.
		panes: []string{
			claudeComposer(msg),
			claudeComposer(msg),
			claudeComposer(msg),
			claudeComposer(msg),
			claudeComposer(msg),
			claudeComposer(""),
			claudeComposer(""),
		},
		statuses: []string{"waiting", "waiting", "waiting", "waiting", "waiting", "active", "active"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 10, checkDelay: 0, verifyDelivery: true,
	})
	if err != nil {
		t.Fatalf("recovery should have submitted the message, got error: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, delivery)
	}
	if got := mock.namedKeyCount("Escape"); got != 1 {
		t.Errorf("Escape presses: want exactly 1 after the threshold, got %d", got)
	}
}

// TestSendRetry_GatedComposer_EscapeIsBounded pins that a composer which never
// releases gets a bounded number of Escape attempts and still reports an
// honest typed_not_submitted — the escalation must not become an infinite
// Escape loop against a pane it cannot fix.
func TestSendRetry_GatedComposer_EscapeIsBounded(t *testing.T) {
	const msg = "this message never submits"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{claudeComposer(msg)},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 20, checkDelay: 0, verifyDelivery: true,
	})
	if err == nil {
		t.Fatal("a permanently gated composer must still surface an error")
	}
	if delivery != deliveryTypedNotSubmitted {
		t.Fatalf("delivery: want %q, got %q", deliveryTypedNotSubmitted, delivery)
	}
	// 20 checks would be 4 Escapes at a threshold of 5 without the bound.
	if got := mock.namedKeyCount("Escape"); got != 2 {
		t.Errorf("Escape presses: want exactly maxEscapeRecoveries (2), got %d", got)
	}
	// The operator-facing error must name the recovery that was already tried,
	// so a human reading it does not retry the same thing by hand.
	if !strings.Contains(err.Error(), "Escape") {
		t.Errorf("error should mention the attempted Escape recovery, got: %v", err)
	}
}

// TestSendRetry_NormalSubmit_NeverPressesEscape guards the blast radius: a
// message that submits normally must never see an Escape, which in other TUI
// states can cancel an in-flight turn.
func TestSendRetry_NormalSubmit_NeverPressesEscape(t *testing.T) {
	const msg = "a message that submits on the first try"
	mock := &mockSendRetryTarget{
		panes:    []string{claudeComposer("")},
		statuses: []string{"active", "active"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 10, checkDelay: 0, verifyDelivery: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, delivery)
	}
	if got := mock.namedKeyCount("Escape"); got != 0 {
		t.Errorf("Escape must not be pressed on a healthy submit, got %d presses", got)
	}
}

// TestSendRetry_TransientUnsent_NoEscape pins the threshold: an ordinary
// bracketed-paste timing race clears within one or two Enter retries and must
// NOT trigger the escalation.
func TestSendRetry_TransientUnsent_NoEscape(t *testing.T) {
	const msg = "briefly unsent, then accepted"
	mock := &mockSendRetryTarget{
		// Four unsent checks only — below escapeRecoveryThreshold (5).
		panes: []string{
			claudeComposer(msg),
			claudeComposer(msg),
			claudeComposer(msg),
			claudeComposer(msg),
			claudeComposer(""),
			claudeComposer(""),
		},
		statuses: []string{"waiting", "waiting", "waiting", "waiting", "active", "active"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 10, checkDelay: 0, verifyDelivery: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, delivery)
	}
	if got := mock.namedKeyCount("Escape"); got != 0 {
		t.Errorf("a transient unsent race must not escalate to Escape, got %d presses", got)
	}
}
