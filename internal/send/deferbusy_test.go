package send

import (
	"errors"
	"testing"
	"time"
)

func TestStatusIsBusy(t *testing.T) {
	busy := []string{"running", "starting"}
	free := []string{"waiting", "idle", "error", "stopped", "queued", "unknown", ""}
	for _, s := range busy {
		if !StatusIsBusy(s) {
			t.Errorf("StatusIsBusy(%q) = false, want true", s)
		}
	}
	for _, s := range free {
		if StatusIsBusy(s) {
			t.Errorf("StatusIsBusy(%q) = true, want false", s)
		}
	}
}

// Holds while the target reports "running", then delivers the instant the
// hook-driven status flips to "waiting" (the Stop-hook turn-finished edge).
func TestWaitUntilNotBusy_HoldsThenDelivers(t *testing.T) {
	statuses := []string{"running", "running", "running", "waiting"}
	calls := 0
	fetch := func() (string, error) {
		s := statuses[calls]
		calls++
		return s, nil
	}
	sleeps := 0
	sleep := func(time.Duration) { sleeps++ }

	if err := WaitUntilNotBusy(fetch, 30*time.Minute, 10*time.Millisecond, sleep); err != nil {
		t.Fatalf("WaitUntilNotBusy returned error: %v", err)
	}
	if calls != 4 {
		t.Errorf("polled %d times, want 4 (held through 3 running polls, delivered on waiting)", calls)
	}
	if sleeps != 3 {
		t.Errorf("slept %d times, want 3 (once per busy poll, none after the delivering poll)", sleeps)
	}
}

// Already-idle target: returns immediately, never sleeps.
func TestWaitUntilNotBusy_ImmediateWhenIdle(t *testing.T) {
	calls := 0
	fetch := func() (string, error) {
		calls++
		return "waiting", nil
	}
	sleeps := 0
	sleep := func(time.Duration) { sleeps++ }

	if err := WaitUntilNotBusy(fetch, 30*time.Minute, 10*time.Millisecond, sleep); err != nil {
		t.Fatalf("WaitUntilNotBusy returned error: %v", err)
	}
	if calls != 1 {
		t.Errorf("polled %d times, want 1", calls)
	}
	if sleeps != 0 {
		t.Errorf("slept %d times, want 0", sleeps)
	}
}

// Target never leaves "running": the timeout elapses and the message is
// dropped (non-nil error), never delivered.
func TestWaitUntilNotBusy_TimeoutDrops(t *testing.T) {
	fetch := func() (string, error) { return "running", nil }
	// Injected sleep that advances a fake clock is unnecessary: a tiny real
	// timeout with a no-op sleep makes the deadline fire on the second poll.
	sleep := func(time.Duration) {}

	err := WaitUntilNotBusy(fetch, 1*time.Nanosecond, 10*time.Millisecond, sleep)
	if err == nil {
		t.Fatal("WaitUntilNotBusy returned nil, want timeout error (message must be dropped, not delivered)")
	}
	if got := err.Error(); got == "" || !contains(got, "still busy") {
		t.Errorf("error = %q, want it to mention the target is still busy", got)
	}
}

// Transient fetch errors are tolerated: fewer than the fail-fast threshold do
// not abort, and a subsequent good idle reading delivers.
func TestWaitUntilNotBusy_TransientErrorTolerated(t *testing.T) {
	seq := []struct {
		status string
		err    error
	}{
		{"", errors.New("transient")},
		{"", errors.New("transient")},
		{"waiting", nil},
	}
	calls := 0
	fetch := func() (string, error) {
		s := seq[calls]
		calls++
		return s.status, s.err
	}
	if err := WaitUntilNotBusy(fetch, 30*time.Minute, 10*time.Millisecond, func(time.Duration) {}); err != nil {
		t.Fatalf("WaitUntilNotBusy returned error on tolerable transient errors: %v", err)
	}
	if calls != 3 {
		t.Errorf("polled %d times, want 3", calls)
	}
}

// Persistent fetch errors (removed/renamed target) fail fast at the threshold
// instead of spinning for the full defer timeout.
func TestWaitUntilNotBusy_PersistentErrorFailsFast(t *testing.T) {
	calls := 0
	fetch := func() (string, error) {
		calls++
		return "", errors.New("session not found")
	}
	err := WaitUntilNotBusy(fetch, 30*time.Minute, 10*time.Millisecond, func(time.Duration) {})
	if err == nil {
		t.Fatal("WaitUntilNotBusy returned nil, want fail-fast error")
	}
	if calls != maxConsecutiveFetchErrors {
		t.Errorf("polled %d times, want %d (fail-fast threshold)", calls, maxConsecutiveFetchErrors)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
