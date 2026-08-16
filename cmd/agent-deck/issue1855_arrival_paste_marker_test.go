package main

import (
	"strings"
	"testing"
)

// Round-2 regression tests for issue #1855: the arrival check must recognize
// agent-deck's OWN bracketed-paste collapse as evidence that the body reached
// the pane.
//
// The round-1 fix routed every multi-line body through `paste-buffer -p -r`.
// The `-p` frame is what makes the body survive into a TUI composer — but it
// also makes the composer render the payload as a "[Pasted text …]" marker
// instead of the verbatim text, for the first time. verifyContentArrival is
// the WHOLE delivery check for every non-Claude tool
// (skipClaudeDeliveryVerify), and its only content signal was
// "the message's distinctive token is visible in the pane". Against a
// paste-collapsing composer that token can never appear, so a send whose Enter
// was swallowed produced:
//
//	sawBody = false, riskyLine = false  ->  deliveryUnverified, nil  ->  exit 0
//
// i.e. `"success": true` for a message sitting unsent in the composer — the
// exact phantom success issues #1793 and #876 were written to close, reopened
// for the payload class the #1855 transport change retargets. Before the
// transport change the same send left the (fused) body verbatim in the pane
// and correctly reported deliveryTyped with a non-zero exit.
//
// The marker is measured as a TRANSITION from the pre-send baseline, exactly
// like the body-occurrence signal it joins: a marker already parked in the
// pane before the send says nothing about this send.

// crlfFreeMultiLineMessage is a small multi-line body — the #1855 shape —
// whose token cannot accidentally appear in any composer fixture below.
const pasteCollapseMessage = "/superpowers:writing-skills\n" +
	"Write a skill that writes cupcake flavors for seeded data instead of Lorem Ipsum."

// composerHoldingPasteMarker renders a composer whose input line holds Claude's
// (and codex's) collapsed-paste marker rather than the body: the on-screen
// shape of a framed multi-line delivery whose Enter was swallowed.
func composerHoldingPasteMarker() string {
	div := strings.Repeat("─", 25)
	return "some prior output\n" + div + "\n❯ [Pasted text #1 +2 lines]\n" + div + "\n"
}

// TestIssue1855_CollapsedPasteMarkerIsArrivalEvidence_NotAnUnverifiedSuccess is
// the regression proper: a non-Claude tool, a multi-line body delivered as a
// framed paste, the composer showing only the collapse marker, and an agent
// that never goes active. The body token is unobservable by construction, so
// without the marker signal this exits 0 claiming success.
func TestIssue1855_CollapsedPasteMarkerIsArrivalEvidence_NotAnUnverifiedSuccess(t *testing.T) {
	// panes[0] is the pre-send baseline (clean composer, no marker); every
	// capture after the send shows our own collapsed paste.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"❯ \n", composerHoldingPasteMarker()},
	}

	delivery, err := sendWithRetryTarget(mock, pasteCollapseMessage, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})

	if err == nil {
		t.Fatal("issue #1855: a framed multi-line send whose collapse marker sits unsubmitted in the composer must not report success")
	}
	if delivery != deliveryTyped {
		t.Fatalf("delivery: want %q, got %q", deliveryTyped, delivery)
	}
	if delivery == deliveryUnverified {
		t.Fatal("issue #1855: deliveryUnverified here is the #1793 phantom success, reopened by the -p frame")
	}
	if fields := (sendDeliveryResult{delivery: delivery}).jsonFields(); fields["submitted"] != false {
		t.Fatalf("typed must report submitted=false in --json, got %v", fields["submitted"])
	}
}

// TestIssue1855_PreExistingPasteMarkerIsNotArrivalEvidence guards the signal
// against the trap every other signal in this file already has to survive: a
// marker that was ALREADY on screen before the send is a foreign paste (or the
// scrollback of an earlier one), not proof that this send arrived. Only a
// marker that was not there before counts.
//
// This body's lines are all below arrivalSafeLineBytes, so the historical
// best-effort contract applies and the correct outcome is deliveryUnverified
// with no error (see TestIssue1793_SmallPayloadKeepsTheBestEffortContract).
// The assertion that matters is the one against deliveryTyped: reaching that
// would mean the stale marker had been counted as this send's arrival.
func TestIssue1855_PreExistingPasteMarkerIsNotArrivalEvidence(t *testing.T) {
	// The marker is present in the baseline capture and never changes.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{composerHoldingPasteMarker()},
	}

	delivery, err := sendWithRetryTarget(mock, pasteCollapseMessage, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})

	if delivery == deliveryTyped {
		t.Fatal("a paste marker that predates the send must not be counted as this send's arrival")
	}
	if err != nil {
		t.Fatalf("small unmatched sends must stay best-effort, got error: %v", err)
	}
	if delivery != deliveryUnverified {
		t.Fatalf("delivery: want %q, got %q", deliveryUnverified, delivery)
	}
}

// TestIssue1855_PreExistingPasteMarkerOnALongLineIsStillAFailure is the same
// trap at the size where "we could not tell" stops being an acceptable answer:
// a line long enough to be eaten outright by a canonical reader, a marker that
// was already parked in the composer, and nothing else changing. If the stale
// marker were accepted as evidence this would report typed instead of the
// honest no-evidence failure.
func TestIssue1855_PreExistingPasteMarkerOnALongLineIsStillAFailure(t *testing.T) {
	msg := bigMessage(4095)
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{composerHoldingPasteMarker()},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})

	if err == nil {
		t.Fatal("a paste marker that predates the send must not certify a send that vanished")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// TestIssue1855_CollapsedPasteMarkerDoesNotDowngradeARealSubmission: the marker
// signal is arrival evidence only. An idle agent going active is still the
// stronger signal and must still win, so a genuinely submitted framed paste is
// not demoted to the typed-but-not-submitted failure above.
func TestIssue1855_CollapsedPasteMarkerDoesNotDowngradeARealSubmission(t *testing.T) {
	mock := &mockSendRetryTarget{
		// Idle before the send, working after it: a transition attributable
		// to this delivery.
		statuses: []string{"waiting", "active"},
		panes:    []string{"❯ \n", composerHoldingPasteMarker()},
	}

	delivery, err := sendWithRetryTarget(mock, pasteCollapseMessage, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, delivery)
	}
}
