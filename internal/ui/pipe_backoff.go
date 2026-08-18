package ui

import (
	"sync"
	"time"
)

const (
	// pipeConnectBackoffBase and pipeConnectBackoffMax bound how fast the
	// 500ms reconciler may re-dial a session whose control pipe will not stay
	// up. Without a backoff, reconcilePipes called pm.Connect on every tick for
	// every desired-but-unconnected session forever: each call costs one
	// `tmux list-clients` (killStaleControlClients) plus up to three
	// `tmux -C attach-session` spawns (controlPipeConnectAttempts), which was
	// measured in the wild at ~5 connect cycles/sec — ~20 tmux processes per
	// second, indefinitely, for two sessions whose tmux server had no such
	// session at all.
	//
	// The shape mirrors PipeManager.watchPipe's own 2s -> 30s escalation; the
	// cap is higher here because this loop ticks 4x faster and never gives up.
	pipeConnectBackoffBase = 2 * time.Second
	pipeConnectBackoffMax  = 60 * time.Second

	// pipeConnectDeadDelay is the retry floor for a session the liveness cache
	// reports as gone. Deliberately shorter than pipeConnectBackoffMax: a
	// default-socket cache miss can make a LIVE session read as absent (see
	// Session.Exists), so a dead verdict throttles the dial rather than
	// retiring the session.
	pipeConnectDeadDelay = 30 * time.Second

	// pipeConnectStableWindow is how long a pipe must stay continuously
	// connected before its failure streak is forgiven. A pipe that connects and
	// dies inside this window is exactly the churn the backoff exists to damp,
	// and it never surfaces as a Connect error — Connect returns nil, the pipe
	// emits %exit, and the next tick finds it disconnected again.
	pipeConnectStableWindow = 10 * time.Second
)

// pipeConnectState is one session's dial history.
type pipeConnectState struct {
	streak      int
	lastAttempt time.Time
	nextAttempt time.Time
}

// pipeConnectBackoff throttles per-session control-pipe dials from the
// reconciler. The clock is injectable so the escalation is unit-testable
// without sleeping; pass nil for time.Now.
//
// Every method is nil-receiver safe: a Home built by an alternate/test
// constructor never initializes the backoff, and must still reconcile.
type pipeConnectBackoff struct {
	mu    sync.Mutex
	now   func() time.Time
	state map[string]*pipeConnectState
}

func newPipeConnectBackoff(now func() time.Time) *pipeConnectBackoff {
	if now == nil {
		now = time.Now
	}
	return &pipeConnectBackoff{now: now, state: map[string]*pipeConnectState{}}
}

// pipeConnectDelay is the wait after the streak'th consecutive dial:
// 2s, 4s, 8s, ... capped at pipeConnectBackoffMax.
func pipeConnectDelay(streak int) time.Duration {
	if streak < 1 {
		streak = 1
	}
	d := pipeConnectBackoffBase
	for i := 1; i < streak; i++ {
		if d >= pipeConnectBackoffMax {
			break
		}
		d *= 2
	}
	if d > pipeConnectBackoffMax {
		d = pipeConnectBackoffMax
	}
	return d
}

// allow reports whether name may be dialled now, and records the attempt when
// it says yes. The first dial for a name is always allowed — the backoff damps
// repetition, it never blocks a session's first chance at a pipe.
func (b *pipeConnectBackoff) allow(name string) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	st, ok := b.state[name]
	if !ok {
		st = &pipeConnectState{}
		b.state[name] = st
	}
	if !st.nextAttempt.IsZero() && now.Before(st.nextAttempt) {
		return false
	}
	st.streak++
	st.lastAttempt = now
	st.nextAttempt = now.Add(pipeConnectDelay(st.streak))
	return true
}

// park raises name's retry floor to d after its LAST dial. Anchoring on
// lastAttempt rather than now is what makes this a throttle instead of a veto:
// parking on every 500ms tick from "now" would slide the deadline forward
// forever and the session would never be retried at all.
//
// It deliberately does NOT create state for a name that has never been dialled:
// a session the cache calls dead still gets one real attempt, so a transiently
// wrong cache negative cannot starve a healthy pipe.
func (b *pipeConnectBackoff) park(name string, d time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.state[name]
	if !ok || st.lastAttempt.IsZero() {
		return
	}
	if next := st.lastAttempt.Add(d); next.After(st.nextAttempt) {
		st.nextAttempt = next
	}
}

// observeConnected forgives name's streak once its pipe has been up for
// pipeConnectStableWindow since the dial that established it. Called on every
// reconcile for every already-connected desired session.
func (b *pipeConnectBackoff) observeConnected(name string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.state[name]
	if !ok {
		return
	}
	if b.now().Sub(st.lastAttempt) >= pipeConnectStableWindow {
		delete(b.state, name)
	}
}

// retain drops state for sessions no longer in the desired set, so a session
// that leaves the live set and later returns starts clean and the map cannot
// grow without bound.
func (b *pipeConnectBackoff) retain(desired map[string]bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for name := range b.state {
		if !desired[name] {
			delete(b.state, name)
		}
	}
}

// tracked reports how many sessions currently carry backoff state. Test-only
// visibility into retain's pruning.
func (b *pipeConnectBackoff) tracked() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.state)
}
