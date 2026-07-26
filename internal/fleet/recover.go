package fleet

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Outcome is what happened to one candidate during a sweep.
type Outcome string

const (
	// OutcomeRecovered — restarted and verified booted.
	OutcomeRecovered Outcome = "recovered"
	// OutcomeUnverified — the restart call succeeded but the session did not
	// prove it booted within the verify timeout. Deliberately distinct from
	// "recovered": the sweep continues, but the summary never claims a session
	// is back when it only claims a pane exists.
	OutcomeUnverified Outcome = "unverified"
	// OutcomeFailed — the restart call itself returned an error.
	OutcomeFailed Outcome = "failed"
	// OutcomeSkipped — never attempted (halt, --limit, or dry run).
	OutcomeSkipped Outcome = "skipped"
	// OutcomePlanned — dry run: this is what a real sweep would restart.
	OutcomePlanned Outcome = "planned"
)

// Result is the per-session record of a sweep.
type Result struct {
	ID      string
	Title   string
	Status  string // registry status before the restart
	Outcome Outcome
	// Err is the restart error for OutcomeFailed (nil otherwise).
	Err error
	// Report is the verification reading (zero value for skipped/planned).
	Report VerifyReport
	// WaitedBefore is the spacing actually slept before this boot.
	WaitedBefore time.Duration
	// Reason explains a skip.
	Reason string
}

// Summary is the whole sweep, suitable for both the human one-liner and the
// --json payload.
type Summary struct {
	Assessment  Assessment
	DryRun      bool
	Attempted   int
	Recovered   int
	Unverified  int
	Failed      int
	Skipped     int
	Halted      bool
	HaltReason  string
	Results     []Result
	TotalWaited time.Duration
}

// Format returns the stable one-line summary. Key order and spelling are a
// contract — the CLI tests assert on this substring.
func (s Summary) Format() string {
	base := fmt.Sprintf("down=%d attempted=%d recovered=%d unverified=%d failed=%d skipped=%d",
		s.Assessment.Down, s.Attempted, s.Recovered, s.Unverified, s.Failed, s.Skipped)
	if s.DryRun {
		return "dry-run " + base
	}
	if s.Halted {
		return base + " halted=true"
	}
	return base
}

// Recoverer restarts down sessions one at a time, verifying each before moving
// on. Every side effect is an injectable field; see the package doc for why.
type Recoverer struct {
	// Restart performs the actual restart of one session.
	Restart func(*session.Instance) error
	// StillDown re-probes a candidate immediately before its restart. A sweep
	// over 65 sessions runs for minutes, so the assessment is stale by the time
	// the last candidates come up: the operator (or the TUI's own recovery) may
	// have brought a session back in the meantime, and restarting it then would
	// KILL a live session — the one genuinely destructive thing this command
	// could do. nil disables the re-check.
	StillDown func(*session.Instance) bool
	// Verify proves (or fails to prove) that a restarted session booted.
	Verify func(*session.Instance) VerifyReport
	// Persist durably records the sessions a boot mutated. It is called with a
	// ONE-element slice after each attempt so a sweep interrupted at session 40
	// of 65 leaves the first 39 persisted. Wire it to a targeted, sweep-free
	// write (see session.Storage.PersistRecoveredInstances) — never to a
	// whole-table save from a stale snapshot (2026-06-04).
	Persist func([]*session.Instance) error
	// Progress, when set, is called before each attempt (1-based index) so the
	// CLI can stream a line per session during a multi-minute sweep.
	Progress func(index, total int, c Candidate)
	// Sleep implements spacing.
	Sleep func(time.Duration)
	// Rand returns a value in [0,1) for jitter (see defaultJitterSource).
	Rand func() float64
	Log  *slog.Logger

	// Spacing is the base gap between consecutive boots. <=0 means "unset" and
	// falls back to DefaultSpacing — spacing is the safety property of this
	// command, so an unset field must never silently mean "no spacing".
	Spacing time.Duration
	// NoSpacing is the ONLY way to disable the gap: tests, and an operator who
	// explicitly passed --spacing 0.
	NoSpacing bool
	// Jitter is the ± fraction of Spacing applied per gap (0 disables).
	Jitter float64
	// Limit caps how many sessions the sweep attempts (0 = all).
	Limit int
	// MaxFailures halts the sweep after this many CONSECUTIVE failed restarts
	// (<=0 uses DefaultMaxFailures).
	MaxFailures int
	// MaxDeadBoots halts the sweep after this many CONSECUTIVE boots whose pane
	// was gone again by the time verification looked — the signature of sessions
	// exiting immediately on boot (see DefaultMaxDeadBoots).
	//
	// Zero means "use the safe default"; a NEGATIVE value is the explicit
	// opt-out. Zero must not mean "off": a partially-configured Recoverer that
	// silently lost this brake would restart a whole fleet against a dead
	// credential, which is the outage itself.
	MaxDeadBoots int
	// AuthGate halts the sweep when further boots would deepen an auth
	// cascade. nil means no auth gating.
	AuthGate AuthGate
	// DryRun plans without restarting anything.
	DryRun bool
}

// NewRecoverer returns a Recoverer wired to real restarts, real verification,
// and the built-in auth breaker. Persist is intentionally left nil: the caller
// owns storage and must opt in.
func NewRecoverer() *Recoverer {
	v := NewVerifier()
	return &Recoverer{
		Restart: func(inst *session.Instance) error { return inst.Restart() },
		StillDown: func(inst *session.Instance) bool {
			return !tmux.HasSessionOnSocket(inst.TmuxSocketName, TmuxName(inst))
		},
		Verify:       v.Verify,
		Sleep:        time.Sleep,
		Rand:         defaultJitterSource,
		Log:          slog.Default(),
		Spacing:      DefaultSpacing,
		Jitter:       DefaultJitter,
		MaxFailures:  DefaultMaxFailures,
		MaxDeadBoots: DefaultMaxDeadBoots,
		AuthGate:     &SubstateAuthGate{HaltAfter: DefaultAuthHaltAfter},
	}
}

// Recover runs the sweep over an assessment's candidates.
//
// Sequencing contract (this is the whole point of the command):
//
//  1. One session at a time, in assessment order. Never concurrent — the
//     failure this recovers from is partly caused by too many agents booting
//     at once.
//  2. The auth gate is consulted BEFORE each boot; a closed circuit halts and
//     marks every remaining candidate skipped rather than pretending it tried.
//  3. Spacing (+ jitter) is slept BEFORE every boot except the first.
//  4. Every candidate is re-probed immediately before its restart, so a session
//     that recovered on its own during the sweep is never killed by it.
//  5. Each boot is verified before the next one starts. A single verification
//     failure does not abort the sweep; a run of failed RESTARTS does
//     (MaxFailures), and so does a run of boots that came up with the pane
//     already gone (MaxDeadBoots) — sessions exiting on boot is how a
//     credential outage actually presents.
//  6. Each mutated session is persisted immediately, individually.
func (r *Recoverer) Recover(as Assessment) Summary {
	sum := Summary{Assessment: as, DryRun: r.DryRun}

	total := len(as.Candidates)
	limit := total
	if r.Limit > 0 && r.Limit < limit {
		limit = r.Limit
	}

	consecutiveFailures := 0
	consecutiveDeadBoots := 0
	attempts := 0

	for i, c := range as.Candidates {
		res := Result{ID: c.ID(), Title: c.Title(), Status: c.Status}

		if sum.Halted {
			res.Outcome = OutcomeSkipped
			res.Reason = sum.HaltReason
			sum.Skipped++
			sum.Results = append(sum.Results, res)
			continue
		}
		if attempts >= limit {
			res.Outcome = OutcomeSkipped
			res.Reason = fmt.Sprintf("beyond --limit %d", limit)
			sum.Skipped++
			sum.Results = append(sum.Results, res)
			continue
		}
		if c.Instance == nil {
			res.Outcome = OutcomeSkipped
			res.Reason = "no instance"
			sum.Skipped++
			sum.Results = append(sum.Results, res)
			continue
		}

		if r.AuthGate != nil {
			if ok, reason := r.AuthGate.Allow(); !ok {
				sum.Halted = true
				sum.HaltReason = reason
				res.Outcome = OutcomeSkipped
				res.Reason = reason
				sum.Skipped++
				sum.Results = append(sum.Results, res)
				r.logHalt(reason)
				continue
			}
		}

		if r.DryRun {
			res.Outcome = OutcomePlanned
			res.WaitedBefore = r.plannedWait(attempts)
			sum.TotalWaited += res.WaitedBefore
			attempts++
			sum.Attempted++
			sum.Results = append(sum.Results, res)
			continue
		}

		if r.Progress != nil {
			r.Progress(i+1, total, c)
		}

		wait := r.spacingFor(attempts)
		if wait > 0 {
			r.sleep(wait)
		}
		res.WaitedBefore = wait
		sum.TotalWaited += wait

		// Freshness re-check, AFTER the wait so the reading is as close to the
		// restart as possible. A session that came back on its own is left
		// alone: restarting a live session is the only way this command could
		// destroy work. It does not consume an attempt — spacing paces BOOTS,
		// and no boot happened here.
		if r.StillDown != nil && !r.StillDown(c.Instance) {
			res.Outcome = OutcomeSkipped
			res.Reason = "came back on its own before the sweep reached it"
			sum.Skipped++
			sum.Results = append(sum.Results, res)
			r.logf("fleet_recover_skipped_recovered_itself", c)
			continue
		}

		attempts++
		sum.Attempted++

		if err := r.restart(c.Instance); err != nil {
			res.Outcome = OutcomeFailed
			res.Err = err
			sum.Failed++
			consecutiveFailures++
			r.logf("fleet_recover_restart_failed", c, slog.String("error", err.Error()))
			if r.AuthGate != nil {
				r.AuthGate.Observe(c.Instance, VerifyReport{}, err)
			}
			sum.Results = append(sum.Results, res)
			if consecutiveFailures >= r.maxFailures() {
				sum.Halted = true
				sum.HaltReason = fmt.Sprintf(
					"%d consecutive restarts failed — halting instead of failing through the rest of the fleet (last error: %v)",
					consecutiveFailures, err)
				r.logHalt(sum.HaltReason)
			}
			continue
		}

		// The restart succeeded, so the session mutated (status, tmux name,
		// tool session id). Persist before verifying: verification can take
		// tens of seconds and an interrupt in that window must not lose the
		// fact that this session was restarted.
		r.persist(c.Instance)

		rep := r.verify(c.Instance)
		res.Report = rep
		if r.AuthGate != nil {
			r.AuthGate.Observe(c.Instance, rep, nil)
		}

		if rep.Booted() {
			res.Outcome = OutcomeRecovered
			sum.Recovered++
			// A session that booted and reached a live-agent state proves the
			// host and the credential still work, so both runs are broken.
			consecutiveFailures = 0
			consecutiveDeadBoots = 0
			r.logf("fleet_recover_booted", c, slog.Duration("waited", rep.Elapsed))
		} else {
			res.Outcome = OutcomeUnverified
			sum.Unverified++
			// An unverified boot is not a restart failure, so it does not feed
			// the consecutive-failure brake — but it does not reset it either:
			// a fleet that keeps coming up unverified should still trip on the
			// next real failure.
			r.logf("fleet_recover_unverified", c,
				slog.Bool("pane_alive", rep.PaneAlive),
				slog.String("status", rep.Status),
				slog.String("substate", rep.Substate))

			// Pane gone after a successful restart = the session started and
			// exited. One is a crash; a run of them is systemic (dead
			// credential, wedged tmux, missing binary) and every further boot
			// makes it worse — this is the brake the 2026-07-26 auth cascade
			// needed, since an exiting session leaves no substate for the auth
			// breaker to read.
			if !rep.PaneAlive {
				consecutiveDeadBoots++
				if limit := r.maxDeadBoots(); limit > 0 && consecutiveDeadBoots >= limit {
					sum.Halted = true
					sum.HaltReason = fmt.Sprintf(
						"%d consecutive sessions restarted and then died immediately (pane gone before verification) — "+
							"that is a host- or credential-level fault, and each further boot deepens it; "+
							"attach one of them by hand, re-authenticate if it shows a 401, then re-run",
						consecutiveDeadBoots)
					r.logHalt(sum.HaltReason)
				}
			}
		}

		// Verification may have refined the status (starting → running/waiting);
		// persist again so storage reflects the settled state.
		r.persist(c.Instance)

		sum.Results = append(sum.Results, res)
	}

	return sum
}

func (r *Recoverer) restart(inst *session.Instance) error {
	if r.Restart == nil {
		return fmt.Errorf("recoverer has no Restart action configured")
	}
	return r.Restart(inst)
}

func (r *Recoverer) verify(inst *session.Instance) VerifyReport {
	if r.Verify == nil {
		return VerifyReport{}
	}
	return r.Verify(inst)
}

// persist is best-effort and never aborts a sweep: a storage hiccup on session
// 3 must not strand the other 62 down sessions. The failure is logged loudly.
func (r *Recoverer) persist(inst *session.Instance) {
	if r.Persist == nil || inst == nil {
		return
	}
	if err := r.Persist([]*session.Instance{inst}); err != nil && r.Log != nil {
		r.Log.Warn("fleet_recover_persist_failed",
			slog.String("instance_id", inst.ID),
			slog.String("title", inst.Title),
			slog.String("error", err.Error()))
	}
}

// spacingFor returns the gap to sleep before the attempt with the given
// zero-based index. The first boot never waits.
func (r *Recoverer) spacingFor(attemptIndex int) time.Duration {
	if attemptIndex == 0 {
		return 0
	}
	base := r.baseSpacing()
	if base <= 0 {
		return 0
	}
	j := r.Jitter
	if j <= 0 {
		return base
	}
	if j > 1 {
		j = 1
	}
	rnd := 0.5
	if r.Rand != nil {
		rnd = r.Rand()
	}
	// Map [0,1) → [-1,1) so the gap spreads symmetrically around base.
	factor := 1 + j*(2*rnd-1)
	out := time.Duration(float64(base) * factor)
	if out < 0 {
		return 0
	}
	return out
}

// plannedWait is the spacing a dry run REPORTS without sleeping. Jitter is
// excluded so the plan is deterministic and reviewable.
func (r *Recoverer) plannedWait(attemptIndex int) time.Duration {
	if attemptIndex == 0 {
		return 0
	}
	base := r.baseSpacing()
	if base < 0 {
		return 0
	}
	return base
}

func (r *Recoverer) baseSpacing() time.Duration {
	if r.NoSpacing {
		return 0
	}
	if r.Spacing <= 0 {
		return DefaultSpacing
	}
	return r.Spacing
}

func (r *Recoverer) maxFailures() int {
	if r.MaxFailures <= 0 {
		return DefaultMaxFailures
	}
	return r.MaxFailures
}

// maxDeadBoots resolves the dead-boot brake: unset (0) takes the default, a
// negative value is the explicit opt-out, and 0 is never "off" (see the field
// doc). A non-positive return disables the brake.
func (r *Recoverer) maxDeadBoots() int {
	if r.MaxDeadBoots == 0 {
		return DefaultMaxDeadBoots
	}
	return r.MaxDeadBoots
}

// defaultJitterSource returns a fraction in [0,1) derived from the wall clock's
// sub-millisecond digits.
//
// Deliberately not math/rand: the jitter here is scheduling noise — it spreads
// boots so a recovery never lands on a perfectly periodic cadence — not a
// security or statistical primitive, and this keeps a one-shot CLI from seeding
// a global RNG (and keeps gosec's weak-RNG rule honest, since a weak RNG here is
// the correct engineering choice rather than a suppressed warning).
func defaultJitterSource() float64 {
	return float64(time.Now().UnixNano()%1_000) / 1_000
}

func (r *Recoverer) sleep(d time.Duration) {
	if r.Sleep != nil {
		r.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (r *Recoverer) logf(msg string, c Candidate, attrs ...any) {
	if r.Log == nil {
		return
	}
	base := []any{slog.String("instance_id", c.ID()), slog.String("title", c.Title())}
	r.Log.Info(msg, append(base, attrs...)...)
}

func (r *Recoverer) logHalt(reason string) {
	if r.Log == nil {
		return
	}
	r.Log.Warn("fleet_recover_halted", slog.String("reason", reason))
}
