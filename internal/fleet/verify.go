package fleet

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// VerifyReport is what one boot proved about itself.
type VerifyReport struct {
	// PaneAlive is true when the tmux session came back and its pane is not dead.
	PaneAlive bool
	// ToolStarted is true when the pane additionally reached a state that only a
	// booted agent produces (see Verifier.Verify).
	ToolStarted bool
	// Status is the registry status observed at the end of verification.
	Status string
	// Substate is the Honest-Status-v2 refinement observed (may be "").
	Substate string
	// Elapsed is how long verification waited.
	Elapsed time.Duration
}

// Booted reports whether the boot is fully verified.
func (r VerifyReport) Booted() bool { return r.PaneAlive && r.ToolStarted }

// AuthFailed reports whether the pane came up showing an auth-failure banner.
func (r VerifyReport) AuthFailed() bool { return r.Substate == string(session.SubstateAuth401) }

// ProbeResult is one instantaneous reading of a session's pane.
type ProbeResult struct {
	PaneAlive bool
	Status    session.Status
	Substate  session.Substate
}

// Verifier polls a freshly restarted session until it proves it booted, or the
// timeout expires. The single Probe seam keeps every tmux call in one place so
// tests drive the polling logic with a scripted sequence of readings.
type Verifier struct {
	Probe func(*session.Instance) ProbeResult
	Sleep func(time.Duration)
	Now   func() time.Time

	// Timeout bounds one verification (<=0 uses DefaultVerifyTimeout).
	Timeout time.Duration
	// Poll is the interval between readings (<=0 uses DefaultVerifyPoll).
	Poll time.Duration
}

// NewVerifier returns a Verifier wired to real tmux/status probes.
func NewVerifier() *Verifier {
	return &Verifier{
		Probe:   defaultProbe,
		Sleep:   time.Sleep,
		Now:     time.Now,
		Timeout: DefaultVerifyTimeout,
		Poll:    DefaultVerifyPoll,
	}
}

// Verify blocks until the session is verified booted, is decisively broken, or
// the timeout expires. It always returns the last reading it took — an
// unverified boot is reported, never guessed at.
//
// "Booted" requires more than a live pane: a pane exists the instant tmux
// creates it, long before the agent's TUI is up. So verification waits for a
// state only a running agent produces — a registry status past `starting`
// (running/waiting/idle) or a substate the pane classifier only emits for a
// live agent TUI. A pane that is still `starting` when the timeout expires is
// reported as PaneAlive && !ToolStarted, which the recoverer surfaces as
// "unverified" rather than as a success or a failure.
//
// An auth-failure substate short-circuits the wait: it is a decisive verdict,
// and waiting out the full timeout on each of 60 sessions would turn a fast
// halt into a 30-minute one.
func (v *Verifier) Verify(inst *session.Instance) VerifyReport {
	now := v.now
	sleep := v.sleep
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = DefaultVerifyTimeout
	}
	poll := v.Poll
	if poll <= 0 {
		poll = DefaultVerifyPoll
	}

	start := now()
	for {
		p := ProbeResult{}
		if v.Probe != nil {
			p = v.Probe(inst)
		}
		rep := VerifyReport{
			PaneAlive: p.PaneAlive,
			Status:    string(p.Status),
			Substate:  string(p.Substate),
			Elapsed:   now().Sub(start),
		}
		if p.PaneAlive {
			if rep.AuthFailed() {
				return rep
			}
			if bootedState(p.Status, p.Substate) {
				rep.ToolStarted = true
				return rep
			}
		}
		if rep.Elapsed >= timeout {
			return rep
		}
		sleep(poll)
	}
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Verifier) sleep(d time.Duration) {
	if v.Sleep != nil {
		v.Sleep(d)
		return
	}
	time.Sleep(d)
}

// bootedState reports whether a (status, substate) pair can only come from a
// session whose agent process is actually up.
func bootedState(st session.Status, sub session.Substate) bool {
	switch st {
	case session.StatusRunning, session.StatusWaiting, session.StatusIdle:
		return true
	}
	switch sub {
	case session.SubstateRunning, session.SubstateIdleAtEmptyPrompt:
		return true
	}
	return false
}

// defaultProbe reads ground truth from tmux and the instance's own status
// machinery. It never creates or kills anything.
func defaultProbe(inst *session.Instance) ProbeResult {
	if inst == nil {
		return ProbeResult{}
	}
	if !tmux.HasSessionOnSocket(inst.TmuxSocketName, TmuxName(inst)) {
		return ProbeResult{}
	}
	if ts := inst.GetTmuxSession(); ts != nil && ts.IsPaneDead() {
		return ProbeResult{}
	}
	// UpdateStatus is the canonical pane-content classifier (the same one the
	// TUI and notify daemon use). Its error is advisory: a failed read just
	// leaves the previous status in place and we poll again.
	_ = inst.UpdateStatus()
	return ProbeResult{
		PaneAlive: true,
		Status:    inst.GetStatusThreadSafe(),
		Substate:  inst.Substate(),
	}
}
