package main

import (
	"testing"
	"time"
)

// fakeSendClock is a hand-advanced clock for sendWaitBudget.
type fakeSendClock struct{ t time.Time }

func (c *fakeSendClock) now() time.Time { return c.t }

// TestSendWait_ReadinessAndCompletionShareOneDeadline_Issue3252: with
// --wait, `--timeout 300s` is the whole call. A bridge that kills the CLI at
// 300s+30s saw a send that spent 247s waiting for a busy Codex turn and then
// started a second, full 300s completion wait, so the CLI never printed its
// accepted-turn receipt.
func TestSendWait_ReadinessAndCompletionShareOneDeadline_Issue3252(t *testing.T) {
	clock := &fakeSendClock{t: time.Date(2026, 11, 22, 22, 25, 48, 0, time.UTC)}
	start := clock.t
	b := newSendWaitBudget(300*time.Second, true, clock.now)

	if got := b.readyTimeout(); got != 300*time.Second {
		t.Fatalf("readyTimeout at start = %v, want 300s", got)
	}

	// The busy turn ends 247s in; the message is submitted then.
	clock.t = start.Add(247 * time.Second)
	if got, want := b.completionDeadline(), start.Add(300*time.Second); !got.Equal(want) {
		t.Fatalf("completionDeadline after a 247s readiness wait = +%v, want +%v (one shared --timeout)",
			got.Sub(start), want.Sub(start))
	}

	// Readiness asked late gets only what is left, never less than a poll.
	if got := b.readyTimeout(); got != 53*time.Second {
		t.Fatalf("readyTimeout 247s in = %v, want 53s", got)
	}
	clock.t = start.Add(400 * time.Second)
	if got := b.readyTimeout(); got <= 0 || got > time.Second {
		t.Fatalf("readyTimeout past the deadline = %v, want a small positive budget", got)
	}
}

// TestSendNoWait_TimeoutStillBoundsReadinessOnly_Issue3252: without --wait the
// flag keeps its meaning (readiness budget), and --timeout 0 keeps the default.
func TestSendNoWait_TimeoutStillBoundsReadinessOnly_Issue3252(t *testing.T) {
	clock := &fakeSendClock{t: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)}
	b := newSendWaitBudget(80*time.Second, false, clock.now)
	clock.t = clock.t.Add(70 * time.Second)
	if got := b.readyTimeout(); got != 80*time.Second {
		t.Fatalf("no --wait readyTimeout = %v, want 80s", got)
	}
	z := newSendWaitBudget(0, true, clock.now)
	_ = z.readyTimeout()
	if got := z.readyTimeout(); got != 0 {
		t.Fatalf("--timeout 0 readyTimeout = %v, want 0 (callers apply the default)", got)
	}
}

// TestSendWait_NoReadinessWaitKeepsBudgetFromDelivery_Issue3252: a send that
// skips the readiness wait (--no-wait --wait) keeps the whole --timeout from
// delivery; CLI startup before it is not charged to the wait.
func TestSendWait_NoReadinessWaitKeepsBudgetFromDelivery_Issue3252(t *testing.T) {
	clock := &fakeSendClock{t: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)}
	b := newSendWaitBudget(300*time.Millisecond, true, clock.now)
	clock.t = clock.t.Add(200 * time.Millisecond) // startup and delivery
	if got, want := b.completionDeadline(), clock.t.Add(300*time.Millisecond); !got.Equal(want) {
		t.Fatalf("completionDeadline without a readiness wait = %v, want %v", got, want)
	}
}
