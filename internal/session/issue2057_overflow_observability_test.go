package session

// The pending-turn bound must be visible to an operator once per child. Since
// issue #2481 item 7 a saturated child's turns fold into one overflow digest
// record (committed, never `failed` and retried every poll); the warning marks
// the moment the folding starts.

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

	// Issue #2481 item 7: past the bound the turn folds into the child's
	// overflow digest (committed, never failed); the warning still fires once.
	for attempt := 0; attempt < 3; attempt++ {
		ev := event
		ev.DoneSummary = fmt.Sprintf("past the bound %d", attempt)
		if res := n.NotifyFinished(ev); res.DeliveryResult != transitionDeliveryCommitted {
			t.Fatalf("attempt %d: delivery_result = %q, want a digest commit", attempt, res.DeliveryResult)
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
	if res := n.NotifyFinished(event); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("first saturation: %q, want a digest commit", res.DeliveryResult)
	}
	if _, err := DrainInboxForParent(parentID); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res := n.NotifyFinished(event); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("commit after drain = %q", res.DeliveryResult)
	}
	// Replaying the consumed turn is a no-op (issue #2481 item 3), so
	// it occupies no slot. The next saturation needs a full fresh queue.
	if pending, err := ReadInboxEvents(parentID); err != nil || len(pending) != 0 {
		t.Fatalf("consumed replay: pending=%d err=%v, want no record", len(pending), err)
	}
	fillPendingTurns(t, parentID, event.ChildSessionID, maxPendingTurnsPerChild)
	event.DoneSummary = "second stall"
	if res := n.NotifyFinished(event); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("second saturation: %q, want a digest commit", res.DeliveryResult)
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
