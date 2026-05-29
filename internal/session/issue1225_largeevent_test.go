package session

// Audit B6 — oversized events must neither be silently truncated by the scanner
// nor fail the whole drain. The producer caps DoneSummary to a sane bound so a
// worker that dumps a large log into its summary cannot produce a line that
// trips the scanner, and the drain reads it back intact.

import (
	"strings"
	"testing"
	"time"
)

func TestB6_OversizedDoneSummary_CappedAndDrains(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-b6-1777200000"

	// A worker dumps a 2 MB log into its summary — larger than the old 1 MB
	// scanner cap that used to fail the entire drain.
	huge := strings.Repeat("x", 2*1024*1024)
	if err := CommitToInbox(parent, TransitionNotificationEvent{
		ChildSessionID: "child-fat",
		ChildTitle:     "worker",
		Profile:        "personal",
		Kind:           transitionKindFinished,
		DoneStatus:     "success",
		DoneSummary:    huge,
		Timestamp:      time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	events, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatalf("drain must not fail on a large event: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("oversized event lost: expected 1 drained, got %d", len(events))
	}
	got := events[0]
	if got.ChildSessionID != "child-fat" {
		t.Fatalf("wrong event drained: %+v", got)
	}
	// The producer must have capped the summary to a bounded size (not silently
	// truncated mid-scan, not unbounded).
	if len(got.DoneSummary) > maxDoneSummaryBytes {
		t.Fatalf("DoneSummary not capped: got %d bytes, cap is %d", len(got.DoneSummary), maxDoneSummaryBytes)
	}
	if len(got.DoneSummary) == 0 {
		t.Fatalf("DoneSummary should retain a (truncated) prefix, got empty")
	}
}
