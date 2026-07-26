// Package fleet implements first-class recovery from a fleet-wide session
// death: the failure mode where every managed tmux pane on the host dies at
// once and agent-deck is left holding a registry full of sessions whose
// processes are gone.
//
// # Why this exists
//
// Twice on 2026-07-26 the maintainer's ~65-session fleet died at once (a
// process reaper matching tmux by argv took down the shared server; separately
// an auth-token cascade made sessions exit on their own). Both times recovery
// was a hand-rolled shell sweep: list the error sessions, restart them one at a
// time with a few seconds of spacing so the OAuth refresh-token rotation race
// does not fork the token, eyeball each pane, keep going. That sweep is the
// thing this package productizes, so the next fleet death is one command
// instead of an improvised loop under pressure.
//
// The two halves
//
//   - Detector (detect.go) answers "did the fleet die?" — it finds sessions the
//     registry believes are alive whose tmux session is gone, and reports
//     whether the shape is a mass death or a one-off.
//   - Recoverer (recover.go) restarts those sessions SEQUENTIALLY with spacing
//     plus jitter, verifies each boot before starting the next, and halts the
//     sweep when an auth circuit breaker says further boots would make things
//     worse.
//
// Safety posture (2026-06-04 data-loss lessons)
//
//   - Nothing in this package deletes or sweeps rows. The persist seam takes
//     ONLY the instances a boot actually mutated, and the CLI wires it to a
//     targeted per-row write (no DELETE-NOT-IN, no whole-table rewrite from a
//     stale snapshot).
//   - Recovery is opt-in at the CLI: `fleet recover` plans without acting
//     unless the operator passes --yes, and DryRun is a first-class field here
//     so the planning path runs the exact same code as the acting path.
//   - Every side effect (restart, verify, sleep, jitter, clock, persist) is an
//     injectable function field, so the unit tests exercise the real sequencing
//     logic without spawning a single tmux server.
package fleet

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Health is a recovery-oriented liveness classification for one session.
type Health int

const (
	// HealthAlive means the session's tmux session is present. Nothing to do:
	// a live pane whose control pipe is broken is `session revive`'s job, not
	// ours (see internal/session/reviver.go).
	HealthAlive Health = iota

	// HealthDown means the registry believes the session should be alive
	// (status running/waiting/starting/error) but its tmux session is gone.
	// This is the fleet-death signature and the only class we recover.
	HealthDown

	// HealthSkipped means the session is legitimately not running — stopped or
	// queued by the operator, or archived. Recovery never touches these: a
	// mass restart that also "recovers" every session the user deliberately
	// stopped would be a footgun, not a fix.
	HealthSkipped
)

func (h Health) String() string {
	switch h {
	case HealthAlive:
		return "alive"
	case HealthDown:
		return "down"
	case HealthSkipped:
		return "skipped"
	}
	return "unknown"
}

// Candidate is one classified session.
type Candidate struct {
	Instance *session.Instance
	Health   Health
	// Status is the registry status at classification time, kept as a string
	// so summaries and JSON payloads do not have to re-read the (mutating)
	// instance after a restart.
	Status string
}

// ID returns the candidate's instance id ("" when the instance is nil).
func (c Candidate) ID() string {
	if c.Instance == nil {
		return ""
	}
	return c.Instance.ID
}

// Title returns the candidate's title ("" when the instance is nil).
func (c Candidate) Title() string {
	if c.Instance == nil {
		return ""
	}
	return c.Instance.Title
}

// Defaults for the detector and the recoverer. Kept in one block so a reviewer
// can retune the whole tool without hunting through the logic.
const (
	// DefaultConfirmProbes is how many independent tmux probes must agree that
	// a session is gone before it becomes a recovery candidate. Two, because a
	// single has-session miss is NOT proof of death: right after a tmux server
	// restart an existing session can briefly probe as absent (the race called
	// out in reviver.go's TODO(revive-liveness)), and acting on one miss would
	// restart sessions that are in fact alive — the destructive direction of
	// this tool's error budget.
	DefaultConfirmProbes = 2

	// DefaultConfirmDelay separates those probes.
	DefaultConfirmDelay = 750 * time.Millisecond

	// DefaultMinDead is the smallest number of down sessions that can be called
	// a mass death rather than an isolated crash.
	DefaultMinDead = 3

	// DefaultDeadFraction is the share of should-be-alive sessions that must be
	// down for the shape to read as fleet-wide.
	DefaultDeadFraction = 0.5

	// DefaultSpacing is the gap between two consecutive boots. ~5s is what the
	// hand-rolled 2026-07-26 sweeps used: enough that N Claude processes do not
	// all refresh the single rotating OAuth refresh token inside one window
	// (the fork-the-token race that produces the 401 cascade).
	DefaultSpacing = 5 * time.Second

	// DefaultJitter spreads each gap by ±20% so a recovery never lands on a
	// perfectly periodic cadence that could resonate with a remote rate limit.
	DefaultJitter = 0.2

	// DefaultVerifyTimeout bounds how long one boot may take to prove itself.
	DefaultVerifyTimeout = 30 * time.Second

	// DefaultVerifyPoll is the verification poll interval.
	DefaultVerifyPoll = 500 * time.Millisecond

	// DefaultMaxFailures is how many CONSECUTIVE failed boots halt the sweep.
	// A fleet where the first three restarts all fail is a fleet with a common
	// cause (tmux wedged, disk full, binary missing); grinding through the
	// remaining 60 just multiplies the damage.
	DefaultMaxFailures = 3

	// DefaultAuthHaltAfter is how many boots showing an auth failure banner
	// halt the sweep. Two rather than one so a single stale-credential session
	// cannot stop a recovery that is otherwise working.
	DefaultAuthHaltAfter = 2

	// DefaultMaxDeadBoots is how many CONSECUTIVE boots may come up with their
	// pane already gone before the sweep halts.
	//
	// This is the brake for the failure actually observed on 2026-07-26: a
	// session whose credential is dead does not sit in the pane showing a
	// banner, it EXITS — the agent quits on the 401 and tmux tears the pane
	// down with it. Such a boot produces no auth substate for the auth breaker
	// to see and no restart error for the failure brake to count, so without
	// this brake a credential outage would let the sweep restart all 65
	// sessions, each burning a full verify timeout — the exact amplification
	// this command exists to prevent.
	//
	// Only pane-is-gone boots count. A pane that is up but still `starting`
	// when the timeout expires is a slow boot, not a dead one, and must not
	// stop a recovery that is merely taking its time.
	DefaultMaxDeadBoots = 3
)
