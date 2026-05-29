package session

// Issue #1225 case 2 (wake-nudge half) — Tier-2 latency optimization. A
// write-triggered nudge fires ONE debounced `send-keys` into the parent's pane
// to wake it sooner than the heartbeat, but ONLY when the pane is idle.
// send-keys is safe HERE because the failure mode (a busy pane only queues the
// keystroke) only exists when busy. A dropped nudge is harmless: the durable
// record is drained on the next heartbeat/turn regardless — wake ≠ deliver.

import (
	"errors"
	"testing"
	"time"
)

func TestIssue1225_WakeNudge_OnlyWhenIdle(t *testing.T) {
	w := NewWakeNudger(100 * time.Millisecond)
	now := time.Unix(1000, 0)

	sent := 0
	send := func() error { sent++; return nil }

	// Busy pane: never nudge (the exact failure the design forbids).
	if did, _ := w.Nudge("p", now, func() bool { return false }, send); did {
		t.Fatalf("must not nudge a busy pane")
	}
	if sent != 0 {
		t.Fatalf("busy pane: send called %d times, want 0", sent)
	}

	// Idle pane: nudge once.
	if did, _ := w.Nudge("p", now, func() bool { return true }, send); !did {
		t.Fatalf("idle pane: expected a nudge")
	}
	if sent != 1 {
		t.Fatalf("idle pane: send called %d times, want 1", sent)
	}
}

func TestIssue1225_WakeNudge_DebouncesRapidWrites(t *testing.T) {
	w := NewWakeNudger(100 * time.Millisecond)
	idle := func() bool { return true }
	sent := 0
	send := func() error { sent++; return nil }

	base := time.Unix(2000, 0)
	if did, _ := w.Nudge("p", base, idle, send); !did {
		t.Fatalf("first nudge expected")
	}
	// Second write 50ms later — within the debounce window → suppressed.
	if did, _ := w.Nudge("p", base.Add(50*time.Millisecond), idle, send); did {
		t.Fatalf("nudge within debounce window must be suppressed")
	}
	// Third write past the window → allowed again.
	if did, _ := w.Nudge("p", base.Add(150*time.Millisecond), idle, send); !did {
		t.Fatalf("nudge past debounce window expected")
	}
	if sent != 2 {
		t.Fatalf("debounce: send called %d times, want 2", sent)
	}
}

// A nudge that fails to send is harmless and non-fatal — the durable record is
// still drained by the heartbeat/turn path (correctness never depends on wake).
func TestIssue1225_WakeNudge_SendErrorIsNonFatal(t *testing.T) {
	w := NewWakeNudger(0)
	did, err := w.Nudge("p", time.Unix(3000, 0), func() bool { return true }, func() error {
		return errors.New("pane gone")
	})
	if !did {
		t.Fatalf("attempted nudge should report it tried")
	}
	if err == nil {
		t.Fatalf("send error should be surfaced (best-effort, caller ignores)")
	}
	// Distinct parents debounce independently.
	if did2, _ := w.Nudge("q", time.Unix(3000, 0), func() bool { return true }, func() error { return nil }); !did2 {
		t.Fatalf("distinct parent must nudge independently")
	}
}
