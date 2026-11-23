package main

import "time"

// sendWaitBudget hands out the --timeout budget of one `session send` call:
// how long the readiness wait may take, and when the --wait/--stream phases
// after delivery must stop.
//
// With --wait or --stream the two share one deadline (issue #3252): the clock
// starts when the readiness wait starts, so time spent waiting for a busy
// target is never granted a second time after delivery. A caller that kills
// the CLI shortly after --timeout (the conductor bridge allows 30s) then still
// receives its structured result, including an accepted-turn receipt for a
// reply that is still running. A send that skips the readiness wait keeps the
// whole budget from delivery.
type sendWaitBudget struct {
	timeout time.Duration
	shared  bool // --wait or --stream
	now     func() time.Time
	start   *time.Time // set when the readiness wait starts
}

func newSendWaitBudget(timeout time.Duration, shared bool, now func() time.Time) *sendWaitBudget {
	return &sendWaitBudget{timeout: timeout, shared: shared, now: now}
}

// minReadyTimeout keeps one readiness poll when the budget is spent.
const minReadyTimeout = time.Millisecond

// sharedDeadline reports whether readiness and the --wait phases draw from one
// --timeout. Without --wait the flag bounds readiness alone, and --timeout 0
// keeps each phase's own default.
func (b *sendWaitBudget) sharedDeadline() bool {
	return b.shared && b.timeout > 0
}

// readyTimeout is the budget for a readiness wait about to start: what is
// left of --timeout with --wait or --stream, the whole --timeout otherwise.
// The first call starts the shared clock.
func (b *sendWaitBudget) readyTimeout() time.Duration {
	if !b.sharedDeadline() {
		return b.timeout
	}
	if b.start == nil {
		start := b.now()
		b.start = &start
	}
	return max(b.start.Add(b.timeout).Sub(b.now()), minReadyTimeout)
}

// completionDeadline is when the phases after delivery (turn identity,
// completion, reply read) must stop: the end of the shared --timeout once a
// readiness wait ran, otherwise --timeout from now.
func (b *sendWaitBudget) completionDeadline() time.Time {
	if !b.sharedDeadline() || b.start == nil {
		return b.now().Add(b.timeout)
	}
	return b.start.Add(b.timeout)
}
