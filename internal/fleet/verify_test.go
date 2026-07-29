package fleet

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeClock advances only when the verifier sleeps, so timeout behavior is
// deterministic and the test suite never actually waits.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(d time.Duration) {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
}

func newTestVerifier(probes []ProbeResult) (*Verifier, *fakeClock, *int) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	v := &Verifier{
		Probe: func(*session.Instance) ProbeResult {
			idx := calls
			if idx > len(probes)-1 {
				idx = len(probes) - 1
			}
			calls++
			return probes[idx]
		},
		Sleep:   clock.Sleep,
		Now:     clock.Now,
		Timeout: 3 * time.Second,
		Poll:    time.Second,
	}
	return v, clock, &calls
}

// A pane exists the instant tmux creates it — long before the agent is up. So a
// freshly created pane sitting in `starting` must NOT count as booted; the
// verifier has to keep polling until the agent's own state shows up.
func TestVerifyWaitsForTheAgentNotJustThePane(t *testing.T) {
	v, clock, calls := newTestVerifier([]ProbeResult{
		{}, // tmux session not back yet
		{PaneAlive: true, Status: session.StatusStarting}, // pane up, agent not
		{PaneAlive: true, Status: session.StatusRunning},  // agent up
		{PaneAlive: true, Status: session.StatusRunning},  // (unused)
	})

	rep := v.Verify(nil)

	if !rep.Booted() {
		t.Fatalf("report = %+v, want booted", rep)
	}
	if *calls != 3 {
		t.Errorf("probes = %d, want 3", *calls)
	}
	if len(clock.sleeps) != 2 {
		t.Errorf("sleeps = %v, want 2 polls", clock.sleeps)
	}
	if rep.Elapsed != 2*time.Second {
		t.Errorf("Elapsed = %s, want 2s", rep.Elapsed)
	}
}

func TestVerifyTimesOutUnverifiedRatherThanGuessing(t *testing.T) {
	v, _, _ := newTestVerifier([]ProbeResult{{PaneAlive: true, Status: session.StatusStarting}})

	rep := v.Verify(nil)

	if rep.Booted() {
		t.Fatal("a session stuck in `starting` must not verify as booted")
	}
	if !rep.PaneAlive {
		t.Error("PaneAlive = false, want the pane fact preserved in the report")
	}
	if rep.Status != string(session.StatusStarting) {
		t.Errorf("Status = %q, want the last observed status", rep.Status)
	}
	if rep.Elapsed < 3*time.Second {
		t.Errorf("Elapsed = %s, want at least the timeout", rep.Elapsed)
	}
}

// An auth failure is decisive. Waiting out the full timeout on each of 65
// sessions would turn a fast halt into a half-hour one.
func TestVerifyShortCircuitsOnAuthFailure(t *testing.T) {
	v, clock, calls := newTestVerifier([]ProbeResult{
		{PaneAlive: true, Status: session.StatusRunning, Substate: session.SubstateAuth401},
	})

	rep := v.Verify(nil)

	if !rep.AuthFailed() {
		t.Fatalf("report = %+v, want AuthFailed", rep)
	}
	if rep.ToolStarted {
		t.Error("ToolStarted = true, want false for an auth-failed boot")
	}
	if *calls != 1 || len(clock.sleeps) != 0 {
		t.Errorf("auth failure took %d probes and %v sleeps, want 1 probe and no wait", *calls, clock.sleeps)
	}
}

// A dead pane never satisfies verification even when the stale status looks fine.
func TestVerifyIgnoresStatusWhenThePaneIsGone(t *testing.T) {
	v, _, _ := newTestVerifier([]ProbeResult{{PaneAlive: false, Status: session.StatusRunning}})

	if rep := v.Verify(nil); rep.Booted() {
		t.Fatalf("report = %+v, want not booted", rep)
	}
}

func TestVerifyAcceptsSubstateEvidenceOfALiveAgent(t *testing.T) {
	v, _, _ := newTestVerifier([]ProbeResult{
		{PaneAlive: true, Status: session.StatusStarting, Substate: session.SubstateIdleAtEmptyPrompt},
	})

	if rep := v.Verify(nil); !rep.Booted() {
		t.Fatalf("report = %+v, want booted (agent prompt is up)", rep)
	}
}

func TestVerifyZeroConfigUsesDefaults(t *testing.T) {
	v := &Verifier{
		Probe: func(*session.Instance) ProbeResult {
			return ProbeResult{PaneAlive: true, Status: session.StatusRunning}
		},
		Sleep: func(time.Duration) { t.Fatal("a booted session must not poll again") },
	}
	if rep := v.Verify(nil); !rep.Booted() {
		t.Fatalf("report = %+v, want booted", rep)
	}
}

func TestVerifyWithoutProbeIsNotBooted(t *testing.T) {
	v := &Verifier{Sleep: func(time.Duration) {}, Now: time.Now, Timeout: time.Nanosecond}
	if rep := v.Verify(nil); rep.Booted() {
		t.Fatalf("report = %+v, want not booted", rep)
	}
}

func TestBootedState(t *testing.T) {
	tests := []struct {
		status session.Status
		sub    session.Substate
		want   bool
	}{
		{status: session.StatusRunning, want: true},
		{status: session.StatusWaiting, want: true},
		{status: session.StatusIdle, want: true},
		{status: session.StatusStarting, want: false},
		{status: session.StatusError, want: false},
		{status: session.StatusStopped, want: false},
		{status: session.StatusStarting, sub: session.SubstateRunning, want: true},
		{status: session.StatusStarting, sub: session.SubstateIdleAtEmptyPrompt, want: true},
		{status: session.StatusError, sub: session.SubstateAuth401, want: false},
		{status: session.StatusError, sub: session.SubstateModelUnavailable, want: false},
	}
	for _, tc := range tests {
		if got := bootedState(tc.status, tc.sub); got != tc.want {
			t.Errorf("bootedState(%q, %q) = %t, want %t", tc.status, tc.sub, got, tc.want)
		}
	}
}
