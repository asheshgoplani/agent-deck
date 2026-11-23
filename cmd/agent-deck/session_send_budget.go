package main

import "time"

// sendWaitBudget hands out the --timeout budget of one `session send` call:
// how long the readiness wait may take, and when the --wait/--stream phases
// after delivery must stop.
type sendWaitBudget struct {
	timeout time.Duration
	shared  bool // --wait or --stream
	start   time.Time
	now     func() time.Time
}

func newSendWaitBudget(timeout time.Duration, shared bool, now func() time.Time) sendWaitBudget {
	return sendWaitBudget{timeout: timeout, shared: shared, start: now(), now: now}
}

// readyTimeout is the budget for the readiness wait.
func (b sendWaitBudget) readyTimeout() time.Duration {
	return b.timeout
}

// completionDeadline is when the phases after delivery (turn identity,
// completion, reply read) must stop.
func (b sendWaitBudget) completionDeadline() time.Time {
	return b.now().Add(b.timeout)
}
