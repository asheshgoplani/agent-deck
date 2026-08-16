package main

import (
	"strings"
	"testing"
)

// Round-3 regression tests for issue #1855. The round-2 arrival signal was a
// whole-pane BOOLEAN ("a [Pasted text …] marker is visible somewhere in the
// pane") compared against a boolean baseline, while the body signal beside it
// in the same block was a COUNT compared as `n > baseline.occurrences`. Two
// distinct defects followed from that, and neither of the round-2 fixtures
// could see either one, because they park the stale marker IN the composer —
// the one place where a whole-pane look and a composer-scoped look agree.
//
// The distinction both tests below turn on: a collapsed paste marker in the
// TRANSCRIPT is the ordinary on-screen trace of a multi-line send that
// SUCCEEDED, and it stays there. A marker in the COMPOSER is payload that has
// not been submitted. Only the second is evidence about the send in flight.

// paneTranscriptMarkerEmptyComposer renders the pane AFTER a successful framed
// multi-line send: the composer collapsed the payload to a marker, Enter was
// accepted, and the marker now sits in the scrollback above an empty composer.
func paneTranscriptMarkerEmptyComposer() string {
	div := strings.Repeat("─", 25)
	return "some prior output\n" +
		"> [Pasted text #1 +2 lines]\n" +
		"  working on it…\n" +
		div + "\n❯ \n" + div + "\n"
}

// paneTranscriptMarkerPlusComposerMarker renders a SECOND framed multi-line
// send to the same pane whose Enter was swallowed: the first send's marker is
// still in the transcript, and the new payload sits collapsed and unsubmitted
// in the composer.
func paneTranscriptMarkerPlusComposerMarker() string {
	div := strings.Repeat("─", 25)
	return "some prior output\n" +
		"> [Pasted text #1 +2 lines]\n" +
		"  working on it…\n" +
		div + "\n❯ [Pasted text #2 +2 lines]\n" + div + "\n"
}

// TestIssue1855_TranscriptMarkerWithEmptyComposerIsNotArrivalEvidence pins the
// false FAILURE. The recipient was already busy at baseline (the common fleet
// case — a worker mid-turn), which disables the status signal and leaves the
// content signal as the whole delivery check. The send lands AND is submitted:
// the composer collapses it, accepts Enter, and the marker ends up in the
// transcript with the composer empty.
//
// Read whole-pane, that transcript marker is "a marker that was not there
// before" and the send is reported as deliveryTyped with
// "submission was never confirmed … Treat this as NOT delivered" — exit 1 for a
// send that worked. A false "not delivered" is the input to the
// double-delivery class (#876), so this is the more dangerous of the two
// directions.
//
// The honest answer for a pane that shows neither the body nor an unsubmitted
// composer is deliveryUnverified with no error: the historical best-effort
// contract for a body whose lines are all below arrivalSafeLineBytes
// (see TestIssue1793_SmallPayloadKeepsTheBestEffortContract).
func TestIssue1855_TranscriptMarkerWithEmptyComposerIsNotArrivalEvidence(t *testing.T) {
	mock := &mockSendRetryTarget{
		// Busy before the send and busy after it: no attributable transition,
		// so the status signal proves nothing and is switched off.
		statuses: []string{"active"},
		// panes[0] is the pre-send baseline (clean, no marker anywhere);
		// afterwards the payload has been submitted and its marker sits in
		// the transcript above an EMPTY composer.
		panes: []string{"❯ \n", paneTranscriptMarkerEmptyComposer()},
	}

	delivery, err := sendWithRetryTarget(mock, pasteCollapseMessage, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})

	if delivery == deliveryTyped {
		t.Fatal("issue #1855: a marker in the TRANSCRIPT above an empty composer is a submitted send, not unsent bytes; reporting it as typed fails a delivery that worked and invites a double send (#876)")
	}
	if err != nil {
		t.Fatalf("small unmatched sends must stay best-effort, got error: %v", err)
	}
	if delivery != deliveryUnverified {
		t.Fatalf("delivery: want %q, got %q", deliveryUnverified, delivery)
	}
}

// TestIssue1855_SecondSendWithStaleTranscriptMarkerIsStillTyped pins the false
// SUCCESS on the other side of the same defect. This is the SECOND multi-line
// send to a pane: the first one's marker is permanently on screen in the
// transcript, so a boolean baseline is armed before this send even starts and
// "marker && !baseline.pasteMarker" can never fire again. This send's Enter is
// swallowed and its payload sits collapsed in the composer — the #1793/#876
// phantom, which round 2 closed only for the first multi-line send per pane.
func TestIssue1855_SecondSendWithStaleTranscriptMarkerIsStillTyped(t *testing.T) {
	mock := &mockSendRetryTarget{
		// Idle throughout: the agent never takes the message up.
		statuses: []string{"waiting"},
		// Baseline already carries the earlier send's transcript marker; the
		// composer is empty. After the send the composer holds a NEW marker.
		panes: []string{paneTranscriptMarkerEmptyComposer(), paneTranscriptMarkerPlusComposerMarker()},
	}

	delivery, err := sendWithRetryTarget(mock, pasteCollapseMessage, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})

	if err == nil {
		t.Fatal("issue #1855: the second multi-line send to a pane must still be verifiable — a marker left in the transcript by an earlier send must not disarm the signal")
	}
	if delivery != deliveryTyped {
		t.Fatalf("delivery: want %q, got %q", deliveryTyped, delivery)
	}
}

// TestIssue1855_StaleMarkerCountsWhereNoComposerCanBeScoped is why the signal
// is a COUNT and not merely composer-scoped. On a pane offering no
// introspectable composer at all (codex, cursor, a plain shell) the scoping
// falls back to the whole pane, where an earlier send's marker never goes
// away. Comparing counts keeps the signal alive there; a boolean would be dead
// from the first multi-line send onwards, exactly as in the test above.
func TestIssue1855_StaleMarkerCountsWhereNoComposerCanBeScoped(t *testing.T) {
	before := "session log\n[Pasted text #1 +2 lines]\nwork finished\n"
	after := before + "[Pasted text #2 +2 lines]\n"

	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{before, after},
	}

	delivery, err := sendWithRetryTarget(mock, pasteCollapseMessage, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})

	if err == nil {
		t.Fatal("issue #1855: on a pane with no introspectable composer, one MORE paste marker than before is still this send's payload arriving")
	}
	if delivery != deliveryTyped {
		t.Fatalf("delivery: want %q, got %q", deliveryTyped, delivery)
	}
}
