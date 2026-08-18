package ui

import (
	"fmt"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock for the backoff tests: the escalation
// is measured in tens of seconds, which no test should sleep through.
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// failingConnector models the observed pathology: a session whose tmux session
// does not exist, so every dial fails. It counts dials so a test can assert the
// reconciler is not spinning.
type failingConnector struct {
	dials int
}

func (f *failingConnector) IsConnected(string) bool { return false }
func (f *failingConnector) Connect(name, _ string) error {
	f.dials++
	return fmt.Errorf("connect pipe for %s: no such session", name)
}
func (f *failingConnector) Disconnect(string)           {}
func (f *failingConnector) ConnectedSessions() []string { return nil }

// The bug: reconcilePipes ran every livePipeReconcileInterval (500ms) and
// dialled every desired-but-unconnected session with no failure memory, so a
// session whose tmux session was gone was dialled ~2x/sec forever — 15k
// pipe_closed and 10k pipe_connect_retry events in a 50-minute window.
//
// Ungated (pipeReconcileGate{}), 60s of ticks is 120 dials. With the backoff it
// must be a small handful.
func TestReconcilePipes_BackoffStopsTheDialStorm(t *testing.T) {
	const window = 60 * time.Second
	ticks := int(window / livePipeReconcileInterval)

	ungated := &failingConnector{}
	for i := 0; i < ticks; i++ {
		reconcilePipes(ungated, []string{"ghost"}, func(string) string { return "" }, pipeReconcileGate{})
	}
	if ungated.dials != ticks {
		t.Fatalf("sanity: ungated reconcile dialled %d times over %v, want %d", ungated.dials, window, ticks)
	}

	clock := newFakeClock()
	gated := &failingConnector{}
	gate := pipeReconcileGate{backoff: newPipeConnectBackoff(clock.now)}
	for i := 0; i < ticks; i++ {
		reconcilePipes(gated, []string{"ghost"}, func(string) string { return "" }, gate)
		clock.advance(livePipeReconcileInterval)
	}
	// 2s + 4s + 8s + 16s + 32s exhausts the 60s window after 6 dials.
	if gated.dials > 6 {
		t.Fatalf("backed-off reconcile dialled %d times over %v, want <= 6 (ungated was %d)",
			gated.dials, window, ungated.dials)
	}
	if gated.dials < 1 {
		t.Fatal("backed-off reconcile never dialled at all; the first attempt must always go through")
	}
	t.Logf("dials over %v: ungated=%d gated=%d", window, ungated.dials, gated.dials)
}

// A session the liveness cache positively reports as absent must not be dialled
// at the tick rate either. It still gets ONE dial — a default-socket cache miss
// can make a live session read absent (see Session.Exists), so a dead verdict
// throttles rather than retires.
func TestReconcilePipes_KnownDeadSessionIsParkedNotSpun(t *testing.T) {
	clock := newFakeClock()
	f := &failingConnector{}
	gate := pipeReconcileGate{
		liveness: func(string) (bool, bool) { return false, true }, // known dead
		backoff:  newPipeConnectBackoff(clock.now),
	}
	// 29s of ticks: inside pipeConnectDeadDelay, so only the first dial lands.
	for i := 0; i < int(29*time.Second/livePipeReconcileInterval); i++ {
		reconcilePipes(f, []string{"ghost"}, func(string) string { return "" }, gate)
		clock.advance(livePipeReconcileInterval)
	}
	if f.dials != 1 {
		t.Fatalf("known-dead session dialled %d times in 29s, want exactly 1", f.dials)
	}
	// Past the park, exactly one more probe is allowed.
	clock.advance(pipeConnectDeadDelay)
	reconcilePipes(f, []string{"ghost"}, func(string) string { return "" }, gate)
	if f.dials != 2 {
		t.Fatalf("known-dead session dialled %d times after the park expired, want 2", f.dials)
	}
}

// known=false is "no evidence", not "dead": a cold cache must never suppress a
// session's dial, or a fresh focus would silently never get a live pipe.
func TestReconcilePipes_UnknownLivenessStillDials(t *testing.T) {
	f := &failingConnector{}
	gate := pipeReconcileGate{
		liveness: func(string) (bool, bool) { return false, false }, // cold cache
		backoff:  newPipeConnectBackoff(newFakeClock().now),
	}
	reconcilePipes(f, []string{"fresh"}, func(string) string { return "" }, gate)
	if f.dials != 1 {
		t.Fatalf("cold-cache session dialled %d times, want 1", f.dials)
	}
}

// A pipe that stays up must not inherit a stale penalty: once it has been
// connected for pipeConnectStableWindow, its streak is forgiven, so a later
// genuine drop reconnects promptly instead of waiting out a 60s cap.
func TestPipeConnectBackoff_StablePipeForgivesTheStreak(t *testing.T) {
	clock := newFakeClock()
	b := newPipeConnectBackoff(clock.now)

	// Three failed dials push the streak to an 8s delay.
	for i := 0; i < 3; i++ {
		if !b.allow("s") {
			t.Fatalf("dial %d should have been allowed", i)
		}
		clock.advance(pipeConnectDelay(i + 1))
	}
	// It finally connects and stays up past the stable window.
	clock.advance(pipeConnectStableWindow)
	b.observeConnected("s")
	if b.tracked() != 0 {
		t.Fatal("a pipe stable past pipeConnectStableWindow must have its streak forgiven")
	}
	// The next drop reconnects immediately, not after the escalated delay.
	if !b.allow("s") {
		t.Fatal("a forgiven session must be dialled immediately on its next drop")
	}
}

// A pipe that dies inside the stable window is exactly the churn the backoff
// exists to damp; it must keep escalating. Connect returns nil in that case, so
// a Connect-error-only backoff would never fire.
func TestPipeConnectBackoff_FlappingPipeKeepsEscalating(t *testing.T) {
	clock := newFakeClock()
	b := newPipeConnectBackoff(clock.now)

	if !b.allow("s") {
		t.Fatal("first dial must be allowed")
	}
	clock.advance(time.Second) // pipe connected, then died 1s later
	b.observeConnected("s")
	if b.tracked() != 1 {
		t.Fatal("a pipe that died inside the stable window must keep its streak")
	}
	if b.allow("s") {
		t.Fatal("a flapping session must not be re-dialled inside its backoff")
	}
	clock.advance(pipeConnectBackoffBase)
	if !b.allow("s") {
		t.Fatal("dial must be allowed once the backoff elapses")
	}
}

// Backoff state must not outlive the desired set, or the map grows without
// bound and a session that returns to focus inherits an old penalty.
func TestPipeConnectBackoff_RetainPrunesUndesired(t *testing.T) {
	b := newPipeConnectBackoff(newFakeClock().now)
	b.allow("a")
	b.allow("b")
	if b.tracked() != 2 {
		t.Fatalf("tracked = %d, want 2", b.tracked())
	}
	b.retain(map[string]bool{"a": true})
	if b.tracked() != 1 {
		t.Fatalf("tracked after retain = %d, want 1", b.tracked())
	}
}

// The escalation shape must match watchPipe's discipline and stop at the cap.
func TestPipeConnectDelay_Escalation(t *testing.T) {
	want := []time.Duration{2, 4, 8, 16, 32, 60, 60, 60}
	for i, w := range want {
		if got := pipeConnectDelay(i + 1); got != w*time.Second {
			t.Errorf("pipeConnectDelay(%d) = %v, want %v", i+1, got, w*time.Second)
		}
	}
}

// A nil backoff (alternate/test Home constructors never build one) must
// reconcile exactly like the original unthrottled loop.
func TestPipeConnectBackoff_NilReceiverAllowsEverything(t *testing.T) {
	var b *pipeConnectBackoff
	if !b.allow("s") {
		t.Fatal("nil backoff must allow every dial")
	}
	b.park("s", time.Hour)
	b.observeConnected("s")
	b.retain(nil)
	if !b.allow("s") {
		t.Fatal("nil backoff must stay permissive after every no-op method")
	}
}
