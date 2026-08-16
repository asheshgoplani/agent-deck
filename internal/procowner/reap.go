package procowner

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Reap outcomes.
const (
	// OutcomeReaped: the process was ours, we signalled it, and it is gone.
	OutcomeReaped = "reaped"
	// OutcomeAlreadyGone: the process was already gone; nothing was signalled.
	OutcomeAlreadyGone = "already_gone"
	// OutcomeNotSignalled: the identity did not verify as ours, so it was left
	// strictly alone. This is the unknown / unreadable / not-ours case.
	OutcomeNotSignalled = "not_signalled"
	// OutcomeStillAlive: the process was ours and survived SIGTERM and SIGKILL.
	OutcomeStillAlive = "still_alive"
)

// Signaler delivers a signal to a pid. It is an interface so the escalation
// logic can be tested without killing anything.
type Signaler interface {
	Signal(pid int, sig syscall.Signal) error
}

// OSSignaler signals real processes.
type OSSignaler struct{}

// Signal implements Signaler.
func (OSSignaler) Signal(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}

// ReapOptions tunes the escalation. Zero values get sane defaults.
type ReapOptions struct {
	TermGrace time.Duration // how long SIGTERM is given before SIGKILL
	KillGrace time.Duration // how long SIGKILL is given before giving up
	Poll      time.Duration // liveness re-check interval
	Sleep     func(time.Duration)
}

func (o ReapOptions) withDefaults() ReapOptions {
	if o.TermGrace <= 0 {
		o.TermGrace = 3 * time.Second
	}
	if o.KillGrace <= 0 {
		o.KillGrace = 2 * time.Second
	}
	if o.Poll <= 0 {
		o.Poll = 50 * time.Millisecond
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	return o
}

// ReapOutcome is what happened to one recorded identity.
type ReapOutcome struct {
	Member  Member
	Outcome string
	Detail  string
}

// ReapReport is the result of reconciling a whole receipt.
type ReapReport struct {
	Outcomes []ReapOutcome
	Verdict  Verdict
	Reason   string
}

// Signalled reports how many identities were actually signalled.
func (r ReapReport) Signalled() int {
	n := 0
	for _, o := range r.Outcomes {
		if o.Outcome == OutcomeReaped || o.Outcome == OutcomeStillAlive {
			n++
		}
	}
	return n
}

// Describe renders the report for operator surfaces.
func (r ReapReport) Describe() string {
	out := fmt.Sprintf("verdict=%s (%s)", r.Verdict, r.Reason)
	for _, o := range r.Outcomes {
		out += fmt.Sprintf("\n  %s -> %s", o.Member.String(), o.Outcome)
		if o.Detail != "" {
			out += ": " + o.Detail
		}
	}
	return out
}

// Reap terminates every identity in the receipt that still verifies as ours,
// and nothing else.
//
// The rules, in order of importance:
//
//  1. Identity is re-verified immediately before every signal. A pid that has
//     been reused, cannot be read, or now belongs to another user is left
//     alone and reported — never signalled, not even with SIGTERM.
//  2. Descendants are signalled before the leader, so the tree is not
//     re-parented mid-reap.
//  3. Death is verified, not assumed: SIGTERM the whole set, wait, SIGKILL
//     whatever is left, wait again. A process that survives both is reported
//     still alive and the receipt is NOT cleared.
//
// The waiting is per PHASE, not per process. Reaping members one at a time
// would multiply the grace period by the size of the tree, and this runs on the
// teardown path — a session with a dozen stubborn children would take a minute
// to stop. Signalling the whole set first also gives a parent and its children
// the same window to shut down cleanly.
//
// The one window this cannot close is between the verify and the kill syscall:
// closing it entirely needs pidfd (Linux-only, and a syscall this package would
// have to hand-roll). The window is microseconds wide and requires the pid
// space to wrap inside it, and SIGTERM — the only signal that can land there
// first — is survivable. Everything after that first signal is re-verified.
func Reap(p Prober, s Signaler, r *Receipt, opts ReapOptions) ReapReport {
	opts = opts.withDefaults()
	if r == nil {
		return ReapReport{Verdict: VerdictClear, Reason: "no ownership receipt"}
	}
	if err := r.Validate(); err != nil {
		return ReapReport{Verdict: VerdictUnknown, Reason: err.Error()}
	}
	if p.Name() != r.Provider {
		return ReapReport{
			Verdict: VerdictUnknown,
			Reason: fmt.Sprintf("receipt was written by provider %q but this host verifies with %q; nothing was signalled",
				r.Provider, p.Name()),
		}
	}
	// A receipt from a previous boot names processes that cannot exist. There
	// is nothing to signal and nothing ambiguous about it.
	if boot, err := p.BootID(); err != nil {
		return ReapReport{Verdict: VerdictUnknown, Reason: "current boot id is unreadable: " + err.Error()}
	} else if boot != r.BootID {
		return ReapReport{
			Verdict: VerdictClear,
			Reason:  "receipt predates the current boot, so every recorded process is gone",
		}
	}

	// Descendants first, leader last.
	members := r.All()
	ordered := make([]Member, 0, len(members))
	for idx := len(members) - 1; idx >= 0; idx-- {
		ordered = append(ordered, members[idx])
	}

	outcomes := make(map[string]*ReapOutcome, len(ordered))
	self := os.Getpid()
	var pending []Member
	for _, m := range ordered {
		if m.PID <= 1 || m.PID == self {
			outcomes[m.Key()] = &ReapOutcome{
				Member:  m,
				Outcome: OutcomeNotSignalled,
				Detail:  fmt.Sprintf("refusing to signal pid %d", m.PID),
			}
			continue
		}
		pending = append(pending, m)
	}

	for _, step := range []struct {
		sig   syscall.Signal
		grace time.Duration
	}{
		{syscall.SIGTERM, opts.TermGrace},
		{syscall.SIGKILL, opts.KillGrace},
	} {
		if len(pending) == 0 {
			break
		}
		var signalled []Member
		for _, m := range pending {
			// Re-verify immediately before EVERY signal, including the
			// escalation: the grace period we just waited out is exactly when a
			// pid can change hands.
			if outcome := classifyBeforeSignal(p, m); outcome != nil {
				outcomes[m.Key()] = outcome
				continue
			}
			if err := s.Signal(m.PID, step.sig); err != nil {
				outcomes[m.Key()] = classifySignalError(m, err)
				continue
			}
			signalled = append(signalled, m)
		}

		pending = signalled
		if len(pending) == 0 {
			continue
		}
		deadline := time.Now().Add(step.grace)
		for {
			var stillOurs []Member
			for _, m := range pending {
				switch VerifyMember(p, m).State {
				case StateGone:
					outcomes[m.Key()] = &ReapOutcome{Member: m, Outcome: OutcomeReaped,
						Detail: "exited after " + step.sig.String()}
				case StateStranger:
					// The pid was reused after our signal, which means the
					// process we owned is gone. Stop here — signalling the new
					// occupant is exactly what this package exists to prevent.
					outcomes[m.Key()] = &ReapOutcome{Member: m, Outcome: OutcomeReaped,
						Detail: "exited; pid has since been reused"}
				case StateUnknown:
					outcomes[m.Key()] = &ReapOutcome{Member: m, Outcome: OutcomeNotSignalled,
						Detail: "identity became unverifiable after " + step.sig.String()}
				default:
					stillOurs = append(stillOurs, m)
				}
			}
			pending = stillOurs
			if len(pending) == 0 || !time.Now().Before(deadline) {
				break
			}
			opts.Sleep(opts.Poll)
		}
	}

	for _, m := range pending {
		outcomes[m.Key()] = &ReapOutcome{Member: m, Outcome: OutcomeStillAlive,
			Detail: "survived SIGTERM and SIGKILL"}
	}

	report := ReapReport{}
	var stillAlive, notSignalled int
	for _, m := range ordered {
		outcome := outcomes[m.Key()]
		if outcome == nil {
			// Unreachable in practice; recorded rather than dropped so a future
			// change cannot silently lose a member from the report.
			outcome = &ReapOutcome{Member: m, Outcome: OutcomeNotSignalled, Detail: "no outcome recorded"}
		}
		switch outcome.Outcome {
		case OutcomeStillAlive:
			stillAlive++
		case OutcomeNotSignalled:
			notSignalled++
		}
		report.Outcomes = append(report.Outcomes, *outcome)
	}
	switch {
	case stillAlive > 0:
		report.Verdict = VerdictOwned
		report.Reason = fmt.Sprintf("%d owned process(es) survived SIGTERM and SIGKILL", stillAlive)
	case notSignalled > 0:
		report.Verdict = VerdictUnknown
		report.Reason = fmt.Sprintf("%d recorded identity/identities could not be verified as ours and were left alone", notSignalled)
	default:
		report.Verdict = VerdictClear
		report.Reason = "every recorded process is gone"
	}
	return report
}

// classifyBeforeSignal returns a terminal outcome when a member must not be
// signalled, or nil when it verifies as ours.
func classifyBeforeSignal(p Prober, m Member) *ReapOutcome {
	status := VerifyMember(p, m)
	switch status.State {
	case StateGone:
		return &ReapOutcome{Member: m, Outcome: OutcomeAlreadyGone, Detail: status.Detail}
	case StateStranger:
		// The recorded process exited and something else now holds its pid.
		// Nothing to reap, and emphatically nothing to signal.
		return &ReapOutcome{Member: m, Outcome: OutcomeAlreadyGone, Detail: status.Detail}
	case StateUnknown:
		return &ReapOutcome{Member: m, Outcome: OutcomeNotSignalled, Detail: status.Detail}
	}
	return nil
}

// classifySignalError turns a failed kill into an outcome. A process that
// exited as we signalled it is reaped; a refusal is reported, never retried
// harder.
func classifySignalError(m Member, err error) *ReapOutcome {
	switch {
	case errors.Is(err, os.ErrProcessDone), errors.Is(err, syscall.ESRCH):
		return &ReapOutcome{Member: m, Outcome: OutcomeReaped, Detail: "exited before the signal landed"}
	case errors.Is(err, syscall.EPERM):
		return &ReapOutcome{
			Member:  m,
			Outcome: OutcomeNotSignalled,
			Detail:  "signal refused by the kernel (EPERM); the process is not ours to signal",
		}
	default:
		return &ReapOutcome{Member: m, Outcome: OutcomeNotSignalled, Detail: "signal failed: " + err.Error()}
	}
}
