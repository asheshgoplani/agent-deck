package session

// The pending-turn bound is documented as an observable backpressure outcome,
// but commitEventToInbox maps every CommitToInbox error to "transient". A
// parent that stopped draining therefore makes its child re-observe the same
// transition on every poll with no operator signal at all — the ~1/sec runaway
// class the dead-letter work removed. Backpressure stays retryable; it just has
// to be visible once per child.

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func fillPendingTurns(t *testing.T, parentID, childID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ev := TransitionNotificationEvent{
			ChildSessionID: childID, FromStatus: "running", ToStatus: "waiting",
			LastOutputHash: fmt.Sprintf("saturating-turn-%d", i), Timestamp: time.Unix(int64(i+1), 0),
		}
		if err := CommitToInbox(parentID, ev); err != nil {
			t.Fatalf("seed commit %d: %v", i, err)
		}
	}
}

func TestIssue2057_OverflowBackpressureIsLoggedOncePerChild(t *testing.T) {
	n, parentID, event := newWakeNudgeFixture(t)
	buf := captureWarnings(t)
	fillPendingTurns(t, parentID, event.ChildSessionID, maxPendingTurnsPerChild)

	for attempt := 0; attempt < 3; attempt++ {
		res := n.NotifyFinished(event)
		if res.DeliveryResult == transitionDeliveryCommitted {
			t.Fatalf("attempt %d committed past the bound", attempt)
		}
	}

	if got := strings.Count(buf.String(), "inbox_turn_overflow"); got != 1 {
		t.Fatalf("overflow warnings = %d, want exactly 1 per child\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), event.ChildSessionID) {
		t.Fatalf("overflow warning does not name the child:\n%s", buf.String())
	}
}

// Once the parent drains, the same child must be able to warn again if it
// saturates a second time; otherwise a long-lived child reports only its first
// stall ever.
func TestIssue2057_OverflowWarningRearmsAfterDrain(t *testing.T) {
	n, parentID, event := newWakeNudgeFixture(t)
	buf := captureWarnings(t)
	fillPendingTurns(t, parentID, event.ChildSessionID, maxPendingTurnsPerChild)
	if res := n.NotifyFinished(event); res.DeliveryResult == transitionDeliveryCommitted {
		t.Fatal("first saturation committed past the bound")
	}
	if _, err := DrainInboxForParent(parentID); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res := n.NotifyFinished(event); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("commit after drain = %q", res.DeliveryResult)
	}
	// the commit above already occupies one slot
	fillPendingTurns(t, parentID, event.ChildSessionID, maxPendingTurnsPerChild-1)
	event.DoneSummary = "second stall"
	if res := n.NotifyFinished(event); res.DeliveryResult == transitionDeliveryCommitted {
		t.Fatal("second saturation committed past the bound")
	}
	if got := strings.Count(buf.String(), "inbox_turn_overflow"); got != 2 {
		t.Fatalf("overflow warnings = %d, want 2 (once per stall)\n%s", got, buf.String())
	}
}

// The legacy 90s window is the floor for any pair where turn identity cannot be
// compared. Requiring BOTH sides to be empty leaves a hole: a child whose
// signal is momentarily unavailable (transcript mid-rotation, a Codex turn
// whose start/completion identity did not bind) re-emits the identical
// transition seconds later — the issue #1187 duplicate-[EVENT] class.
func TestIssue2057_ShortWindowStillGuardsOneSidedSignal(t *testing.T) {
	inboxTestHome(t)
	base := time.Unix(5_000_000, 0)
	child := "one-sided-child"

	t.Run("signal lost after a signalled turn", func(t *testing.T) {
		n := NewTransitionNotifier()
		n.markNotified(TransitionNotificationEvent{
			ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
			LastOutputHash: "jsonl:5000", Timestamp: base,
		})
		unsignalled := TransitionNotificationEvent{
			ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
			Timestamp: base.Add(3 * time.Second),
		}
		if !n.isDuplicate(unsignalled) {
			t.Fatal("re-emitted an identical transition whose signal was unavailable")
		}
	})

	t.Run("signal gained after an unsignalled turn", func(t *testing.T) {
		n := NewTransitionNotifier()
		n.markNotified(TransitionNotificationEvent{
			ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
			Timestamp: base,
		})
		signalled := TransitionNotificationEvent{
			ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
			LastOutputHash: "jsonl:5000", Timestamp: base.Add(3 * time.Second),
		}
		if !n.isDuplicate(signalled) {
			t.Fatal("re-emitted an identical transition when only the record lacked a signal")
		}
	})

	// Two proven-distinct turns inside the window must still both fire; that is
	// the whole point of the Codex turn identity work.
	t.Run("distinct signals are never suppressed by the window", func(t *testing.T) {
		n := NewTransitionNotifier()
		n.markNotified(TransitionNotificationEvent{
			ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
			LastOutputHash: "codex-completion:4", Timestamp: base,
		})
		next := TransitionNotificationEvent{
			ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
			LastOutputHash: "codex-completion:5", Timestamp: base.Add(3 * time.Second),
		}
		if n.isDuplicate(next) {
			t.Fatal("suppressed a proven-distinct turn inside the legacy window")
		}
	})
}
