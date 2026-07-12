package session

// Wiring-side contract for the tmux-layer send-keys timeout fix: a wake-nudge
// whose send is SIGKILL'd for exceeding the per-call send-keys deadline (the
// production hang this fix bounds) surfaces as an error that wraps
// context.DeadlineExceeded. That error must be treated as a BENIGN dropped nudge
// — it must NOT fail the durable commit, and the record must stay pending so the
// parent drains it on its next turn/heartbeat (the pull-model safety net). This
// complements the generic TestIssue1225_NudgeSendErrorIsHarmless by pinning the
// specific deadline-classified case introduced by the send-keys timeout.

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestWakeNudge_SendKeysTimeoutIsBenignDrop(t *testing.T) {
	n, parentID, event := newWakeNudgeFixture(t)

	// Mirror the tmux-layer errSendKeysTimeout shape: a deadline-wrapped error.
	timeoutErr := fmt.Errorf("tmux send-keys exceeded deadline: %w", context.DeadlineExceeded)

	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    func() time.Time { return time.Unix(4000, 0) },
		isIdle: func(p *Instance) bool { return true },
		send:   func(p *Instance, profile string) error { return timeoutErr },
	}

	res := n.NotifyFinished(event)
	if res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("a timed-out send-keys nudge must NOT fail the commit; got %q", res.DeliveryResult)
	}
	got := readInboxLines(t, parentID)
	if len(got) != 1 {
		t.Fatalf("durable record count = %d, want 1 (drain-on-next-turn safety net)", len(got))
	}
}
