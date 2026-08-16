package ui

import (
	"testing"
)

// Round-2 regression tests for issue #1855 on the conductor/watcher dispatch
// path.
//
// deliverToConductorPaneTuned was the only one of agent-deck's four post-send
// verify loops with no "[Pasted text …]" arm — compare sendWithRetryTarget
// (session_cmd.go) and Instance.sendMessageWhenReady (instance.go), which both
// check send.HasUnsentPastedPrompt. Its switch read:
//
//	case send.HasUnsentComposerPrompt(content, msg):        // marker != msg -> false
//	case sawUnsent || send.HasCurrentComposerPrompt(content):
//	    return nil                                          // "submitted"
//
// so a composer holding agent-deck's OWN collapse marker matched the second
// arm — "a composer is rendered without our message" — and the function
// returned success on the FIRST check, with zero recovery Enters, for a
// message still sitting unsent.
//
// The #1855 transport change is what put our own marker into that composer:
// `paste-buffer -p` frames every multi-line body, and every payload above
// canonicalSafeBytes, which is well within reach of both callers here (the
// #1410 prompt dialog accepts 2000 bytes; formatWatcherDispatchMsg inlines a
// full watcher event Body).

// markerComposer renders a composer whose input line holds the collapsed-paste
// marker instead of the body.
func markerComposer() string {
	return composerWith("[Pasted text #1 +4 lines]")
}

// conductorDispatchMessage is a multi-line body of the shape the transport now
// frames.
const conductorDispatchMessage = "[slack] alice: please re-check the delivery path\nand report back with the transcript"

// TestIssue1855_ConductorDispatch_OwnPasteMarkerIsNotSubmitted is the
// regression proper: an unattributable paste marker in the composer must never
// be reported as delivered. Before the fix this returned nil immediately.
func TestIssue1855_ConductorDispatch_OwnPasteMarkerIsNotSubmitted(t *testing.T) {
	// No status ever goes active and the marker never clears.
	p := &fakeConductorPane{captures: []string{markerComposer()}}

	err := deliverToConductorPaneTuned(p, conductorDispatchMessage, 5, 0)

	if err == nil {
		t.Fatal("issue #1855: a composer holding an unattributable paste marker must not be reported as submitted")
	}
	if p.enterCalls != 0 {
		t.Fatalf("#1777: an unattributable marker must not be nudged, got %d Enter presses", p.enterCalls)
	}
}

// TestIssue1855_ConductorDispatch_AttributedPasteMarkerIsRecovered: with the
// guard's pre-send provenance, the collapse is ours, so the loop nudges Enter
// and confirms submission once the composer clears — the recovery the missing
// arm was silently skipping.
func TestIssue1855_ConductorDispatch_AttributedPasteMarkerIsRecovered(t *testing.T) {
	p := &fakeConductorPane{captures: []string{
		markerComposer(), // our collapsed body, Enter swallowed -> nudge
		markerComposer(), // still unsent -> nudge again
		emptyComposer(),  // accepted
	}}

	err := deliverToConductorPaneAttributed(p, conductorDispatchMessage, true, 40, 0)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.enterCalls != 2 {
		t.Fatalf("recovery Enter presses: want 2, got %d", p.enterCalls)
	}
}

// TestIssue1855_ConductorDispatch_AttributedMarkerNeverClearing_StillFails:
// attribution buys a recovery attempt, not a success. If the marker never
// leaves the composer the drop is still reported, so the caller logs it.
func TestIssue1855_ConductorDispatch_AttributedMarkerNeverClearing_StillFails(t *testing.T) {
	p := &fakeConductorPane{captures: []string{markerComposer()}}

	err := deliverToConductorPaneAttributed(p, conductorDispatchMessage, true, 5, 0)

	if err == nil {
		t.Fatal("a message whose marker never leaves the composer must be reported as not confirmed")
	}
	if p.enterCalls != 5 {
		t.Fatalf("want one recovery Enter per check (5), got %d", p.enterCalls)
	}
}

// TestIssue1855_ConductorDispatch_PlainComposerBehaviorUnchanged guards the
// new arm against overreach: a composer with no paste marker must behave
// exactly as before — the ordinary "rendered composer, not our message"
// reading still means submitted.
func TestIssue1855_ConductorDispatch_PlainComposerBehaviorUnchanged(t *testing.T) {
	p := &fakeConductorPane{captures: []string{emptyComposer()}}

	if err := deliverToConductorPaneAttributed(p, conductorDispatchMessage, true, 40, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.enterCalls != 0 {
		t.Fatalf("no retry Enter expected when the composer is clear, got %d", p.enterCalls)
	}
}
