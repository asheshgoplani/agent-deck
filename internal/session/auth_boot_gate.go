package session

import (
	"fmt"
	"log/slog"
	"math/rand"
	"time"
)

// BootSweep runs a bulk boot (restart/revive of many sessions) under three
// brakes that the 2026-07-26 fleet death showed were missing.
//
// WHAT WENT WRONG. `session restart --all` and the reviver sweep booted every
// session back-to-back with no brake of any kind. During a credential outage
// that is the worst possible behavior on two axes at once:
//
//   - Futility: a restart cannot fix an expired token, so every boot died again.
//   - Amplification: all sessions on a host share ONE rotating, single-use
//     refresh token. A burst of simultaneous cold starts all try to refresh it
//     at once; the losers of that race get a revoked token. The recovery sweep
//     was manufacturing the very failure it was recovering from.
//
// THE THREE BRAKES:
//
//  1. HOLD — a session already held for auth (see auth_hold.go) is skipped
//     outright, with its remedy reported. No point, and it would only add token
//     contention.
//  2. PACE — boots are staggered by BaseStagger plus a random jitter, and at
//     most MaxInFlight boots may be awaiting verification at any moment. Jitter
//     is what actually de-correlates concurrent token refreshes; a fixed
//     stagger just moves a synchronized burst, it does not break it up.
//  3. TRIP — if AuthTripThreshold consecutive boots land in auth-death, the
//     sweep STOPS and reports ONE loud message naming the remedy, instead of
//     burning the rest of the fleet against a token that cannot work.
//
// Everything slow or stateful is injectable so the whole policy is testable
// without spawning a single process.
type BootSweep struct {
	// EVERY brake below follows the same convention, deliberately: ZERO means
	// "use the safe default", and a NEGATIVE value means "explicitly disabled".
	// Leaving zero to mean "off" is what made this struct dangerous in review: a
	// partially-configured BootSweep{} would have had no stagger, no jitter, a
	// verify deadline of `now` (so no boot could ever be observed failing) and no
	// breaker — reconstructing precisely the unbraked sweep that caused the
	// outage. Under-configuration must fail SAFE, and turning a brake off must be
	// visible at the call site.

	// MaxInFlight caps how many booted-but-not-yet-verified sessions may exist
	// at once. This is the token-contention cap: it bounds how many fresh agents
	// are simultaneously racing the shared refresh token, which is the quantity
	// that actually caused harm. Minimum 1 — this brake cannot be disabled.
	MaxInFlight int
	// BaseStagger is the floor delay between consecutive boots.
	BaseStagger time.Duration
	// Jitter is the width of the random extra delay added to BaseStagger.
	Jitter time.Duration
	// AuthTripThreshold is how many CONSECUTIVE auth-deaths trip the circuit.
	AuthTripThreshold int
	// VerifyDelay is how long after a boot its outcome is judged. It must be
	// long enough for the agent to reach (and fail) authentication; a 401 is
	// reported within a second or two of process start.
	VerifyDelay time.Duration

	Log *slog.Logger

	// Sleep is the delay primitive (injectable so tests run instantly).
	Sleep func(time.Duration)
	// Now is the clock (injectable for deterministic verification scheduling).
	Now func() time.Time
	// JitterFor returns the random extra delay for one boot, given Jitter.
	JitterFor func(width time.Duration) time.Duration
	// AuthHeld reports whether an instance must be skipped, and why.
	AuthHeld func(*Instance) (bool, string)
	// AuthDeathAfterBoot judges a booted instance: did it land in auth-death?
	AuthDeathAfterBoot func(*Instance) bool
}

// Defaults. Tuned for the failure they exist to prevent, not for throughput: a
// bulk boot is a recovery operation, and a recovery that takes an extra minute
// is strictly better than one that takes the fleet down.
const (
	// DefaultBootMaxInFlight — how many unverified boots may race the shared
	// token at once. 2 keeps a sweep from serializing completely while staying
	// far below the burst size that caused harm.
	DefaultBootMaxInFlight = 2
	// DefaultBootBaseStagger is the floor gap between boots.
	DefaultBootBaseStagger = 750 * time.Millisecond
	// DefaultBootJitter is the random spread added on top. Comparable to the
	// stagger so start times are genuinely de-correlated rather than merely
	// offset.
	DefaultBootJitter = 750 * time.Millisecond
	// DefaultAuthTripThreshold — consecutive auth-deaths that stop the sweep.
	// 3 is enough to distinguish a fleet-wide credential outage from one
	// session with a broken local config.
	DefaultAuthTripThreshold = 3
	// DefaultBootVerifyDelay is how long after a boot its outcome is judged.
	DefaultBootVerifyDelay = 3 * time.Second
)

// NewBootSweep returns a sweep wired to real primitives and the default brakes.
func NewBootSweep() *BootSweep {
	return &BootSweep{
		MaxInFlight:        DefaultBootMaxInFlight,
		BaseStagger:        DefaultBootBaseStagger,
		Jitter:             DefaultBootJitter,
		AuthTripThreshold:  DefaultAuthTripThreshold,
		VerifyDelay:        DefaultBootVerifyDelay,
		Log:                sessionLog,
		Sleep:              time.Sleep,
		Now:                time.Now,
		JitterFor:          defaultJitterFor,
		AuthHeld:           defaultBootAuthHeld,
		AuthDeathAfterBoot: defaultBootAuthDeath,
	}
}

func defaultJitterFor(width time.Duration) time.Duration {
	if width <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(width))) //nolint:gosec // jitter, not crypto
}

func defaultBootAuthHeld(inst *Instance) (bool, string) {
	if inst == nil {
		return false, ""
	}
	return inst.IsAuthHeld()
}

// defaultBootAuthDeath re-derives the instance's status and asks whether it is
// now held for auth. UpdateStatus is what observes the pane (or its absence) and
// arms the hold, so it must run before the question is asked.
func defaultBootAuthDeath(inst *Instance) bool {
	if inst == nil {
		return false
	}
	_ = inst.UpdateStatus()
	held, _ := inst.IsAuthHeld()
	return held
}

// BootAttempt is the per-instance outcome of a sweep.
type BootAttempt struct {
	InstanceID string
	Title      string
	// Skipped means the boot was not attempted. SkipReason says why: an auth
	// hold, or the circuit having tripped before we reached this instance.
	Skipped    bool
	SkipReason string
	// Err is the boot function's error, if it failed outright.
	Err error
	// AuthDeath means the boot ran but the session landed back in auth-death.
	AuthDeath bool
}

// BootSweepResult aggregates a sweep.
type BootSweepResult struct {
	Attempts []BootAttempt
	Booted   int
	Failed   int
	// SkippedHeld counts instances skipped because they were already auth-held.
	SkippedHeld int
	// Abandoned counts instances never attempted because the circuit tripped.
	Abandoned int
	// AuthDeaths counts verified boots that landed back in auth-death.
	AuthDeaths int
	// Tripped is true when the circuit breaker stopped the sweep.
	Tripped bool
	// TripMessage is the single loud message to show the user. Empty unless
	// Tripped.
	TripMessage string
}

// inFlightBoot is one booted instance awaiting its verdict.
type inFlightBoot struct {
	inst     *Instance
	attempt  int // index into result.Attempts
	verifyAt time.Time
}

// Run boots each instance through the three brakes and returns the outcome.
//
// Sequential by construction: every current bulk path is sequential, and a
// sequential sweep with a bounded verification window is both easier to reason
// about and easier to keep honest than a worker pool. Concurrency is bounded
// where it matters — MaxInFlight caps the boots simultaneously racing the shared
// credential, which is the quantity that caused the outage.
//
// boot must be the caller's real boot action (Restart, revive, start). Run never
// calls it for a held instance and never calls it after the circuit trips.
func (s *BootSweep) Run(instances []*Instance, boot func(*Instance) error) BootSweepResult {
	s.applyDefaults()

	result := BootSweepResult{}
	var inFlight []inFlightBoot
	consecutiveAuthDeaths := 0
	firstBoot := true

	// verify drains in-flight boots whose verdict is due (or all of them when
	// force is set), folding each into the breaker state.
	verify := func(force bool) {
		remaining := inFlight[:0]
		for _, f := range inFlight {
			if !force && s.Now().Before(f.verifyAt) {
				remaining = append(remaining, f)
				continue
			}
			if wait := f.verifyAt.Sub(s.Now()); force && wait > 0 {
				s.Sleep(wait)
			}
			if s.AuthDeathAfterBoot(f.inst) {
				result.Attempts[f.attempt].AuthDeath = true
				result.AuthDeaths++
				consecutiveAuthDeaths++
				f.inst.RecordAuthBootFailure()
				s.logf("boot_sweep_auth_death",
					slog.String("title", f.inst.Title),
					slog.String("instance_id", f.inst.ID),
					slog.Int("consecutive", consecutiveAuthDeaths))
			} else {
				// A boot that authenticated proves the credential is usable, so
				// the run of failures is broken.
				consecutiveAuthDeaths = 0
			}
		}
		inFlight = remaining
	}

	for idx, inst := range instances {
		if inst == nil {
			continue
		}

		// Drain due verdicts, and block while the in-flight cap is saturated so
		// no more than MaxInFlight unverified agents contend for the token.
		verify(false)
		for len(inFlight) >= s.MaxInFlight {
			verify(true)
		}

		if s.tripped(consecutiveAuthDeaths) {
			// Judge anything still in flight before reporting, so the summary
			// (and each session's auth boot counter) is complete. Bounded by
			// VerifyDelay, and inFlight is usually already empty here because the
			// drain above is what produced the trip.
			verify(true)
			result.Tripped = true
			result.Abandoned = countNonNil(instances[idx:])
			result.TripMessage = s.tripMessage(consecutiveAuthDeaths, result.Abandoned)
			s.logf("boot_sweep_circuit_tripped",
				slog.Int("consecutive_auth_deaths", consecutiveAuthDeaths),
				slog.Int("abandoned", result.Abandoned),
				slog.String("remedy", "run /login or repair credentials, then re-run"))
			for _, skipped := range instances[idx:] {
				if skipped == nil {
					continue
				}
				result.Attempts = append(result.Attempts, BootAttempt{
					InstanceID: skipped.ID,
					Title:      skipped.Title,
					Skipped:    true,
					SkipReason: "circuit tripped: preceding boots died on authentication",
				})
			}
			return result
		}

		if held, reason := s.AuthHeld(inst); held {
			result.SkippedHeld++
			result.Attempts = append(result.Attempts, BootAttempt{
				InstanceID: inst.ID,
				Title:      inst.Title,
				Skipped:    true,
				SkipReason: reason,
			})
			continue
		}

		// Pace: the first boot is immediate, every later one waits
		// BaseStagger + jitter. A negative BaseStagger is the explicit
		// "pacing disabled" signal, so skip rather than call Sleep with a
		// nonsensical duration.
		if !firstBoot {
			if gap := s.BaseStagger + s.JitterFor(s.Jitter); gap > 0 {
				s.Sleep(gap)
			}
		}
		firstBoot = false

		attemptIdx := len(result.Attempts)
		attempt := BootAttempt{InstanceID: inst.ID, Title: inst.Title}
		if err := boot(inst); err != nil {
			attempt.Err = err
			result.Failed++
			result.Attempts = append(result.Attempts, attempt)
			continue
		}
		result.Booted++
		result.Attempts = append(result.Attempts, attempt)
		inFlight = append(inFlight, inFlightBoot{
			inst:     inst,
			attempt:  attemptIdx,
			verifyAt: s.Now().Add(s.VerifyDelay),
		})
	}

	// Judge whatever is still in flight so the caller's summary is complete and
	// a trailing burst of auth-deaths is still reported.
	verify(true)
	if s.tripped(consecutiveAuthDeaths) {
		result.Tripped = true
		result.TripMessage = s.tripMessage(consecutiveAuthDeaths, 0)
	}
	return result
}

func (s *BootSweep) tripped(consecutive int) bool {
	return s.AuthTripThreshold > 0 && consecutive >= s.AuthTripThreshold
}

// tripMessage is the ONE loud line the user gets instead of a wall of failures.
// It names the cause, the reason further attempts are harmful (not merely
// useless), the blast radius avoided, and the exact remedy.
func (s *BootSweep) tripMessage(consecutive, abandoned int) string {
	msg := fmt.Sprintf(
		"STOPPED after %d consecutive sessions died on authentication (401). "+
			"The shared credential is not working, so further restarts cannot succeed — "+
			"and each one races the single rotating refresh token, which is what turns "+
			"one expired token into a fleet-wide outage.",
		consecutive,
	)
	if abandoned > 0 {
		msg += fmt.Sprintf(" %d session(s) were left untouched on purpose.", abandoned)
	}
	return msg + " Fix: run /login in a working session (or repair the credentials file), then re-run this command."
}

func (s *BootSweep) logf(event string, attrs ...any) {
	if s.Log == nil {
		return
	}
	s.Log.Warn(event, attrs...)
}

// applyDefaults makes a partially-configured BootSweep safe: every unset brake
// (zero) takes its default, and only an explicitly NEGATIVE value disables one.
// A caller may therefore construct BootSweep{} with just the fields it cares
// about — or just the clock, in tests — without silently losing the brakes.
func (s *BootSweep) applyDefaults() {
	if s.MaxInFlight < 1 {
		// Not disableable: zero or negative would mean "unbounded boots racing
		// the shared token", which is the failure itself. Clamp to fully serial.
		s.MaxInFlight = 1
	}
	if s.BaseStagger == 0 {
		s.BaseStagger = DefaultBootBaseStagger
	}
	if s.Jitter == 0 {
		s.Jitter = DefaultBootJitter
	}
	if s.AuthTripThreshold == 0 {
		s.AuthTripThreshold = DefaultAuthTripThreshold
	}
	if s.VerifyDelay == 0 {
		// A zero delay would judge every boot before the agent could reach (and
		// fail) authentication, so no auth-death would ever be observed and the
		// breaker could never trip.
		s.VerifyDelay = DefaultBootVerifyDelay
	}
	if s.Sleep == nil {
		s.Sleep = time.Sleep
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.JitterFor == nil {
		s.JitterFor = defaultJitterFor
	}
	if s.AuthHeld == nil {
		s.AuthHeld = defaultBootAuthHeld
	}
	if s.AuthDeathAfterBoot == nil {
		s.AuthDeathAfterBoot = defaultBootAuthDeath
	}
}

func countNonNil(instances []*Instance) int {
	n := 0
	for _, inst := range instances {
		if inst != nil {
			n++
		}
	}
	return n
}
