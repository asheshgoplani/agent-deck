package fleet

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// AuthGate is the circuit-breaker seam a recovery sweep consults before every
// boot and reports every boot to.
//
// INTEGRATION POINT. The auth-cascade half of the 2026-07-26 fleet deaths is
// being fixed separately: N sessions share one rotating OAuth refresh token, so
// a burst of restarts can fork the token and 401 the whole fleet (see
// bug_oauth_multisession_rotation_race_rootcause). When that breaker lands it
// should be adapted to this interface and passed as Recoverer.AuthGate — no
// change to the sequencing logic is needed. Until then Recoverer defaults to
// SubstateAuthGate below, which reads the same signal from the pane the TUI
// already classifies (Honest-Status v2 substate "auth-401") and halts the sweep
// rather than grinding through 60 doomed boots.
type AuthGate interface {
	// Allow is consulted BEFORE each boot. Returning false halts the sweep;
	// the string is the operator-facing reason. It must be cheap and must not
	// block.
	Allow() (bool, string)

	// Observe is called after each boot with what verification saw. err is the
	// restart error (nil when the restart itself succeeded).
	Observe(inst *session.Instance, rep VerifyReport, err error)
}

// SubstateAuthGate is the built-in auth breaker: it counts boots whose pane
// came up showing an auth-failure banner and opens after HaltAfter of them.
//
// It deliberately does NOT count SubstateModelUnavailable (a dead model is not
// a credential problem and recovers on its own when the model returns) and does
// not count restart errors (those are handled by the consecutive-failure brake,
// which is about tmux/spawn health rather than credentials).
type SubstateAuthGate struct {
	// HaltAfter is the number of auth-failed boots that opens the circuit.
	// <=0 uses DefaultAuthHaltAfter.
	HaltAfter int

	seen int
}

// Allow reports whether another boot may proceed.
func (g *SubstateAuthGate) Allow() (bool, string) {
	limit := g.limit()
	if g.seen < limit {
		return true, ""
	}
	return false, fmt.Sprintf(
		"auth circuit open: %d session(s) booted into an auth failure (substate %q) — recovering the rest would keep re-forking the shared credential; re-authenticate, then re-run",
		g.seen, session.SubstateAuth401)
}

// Observe records one boot's verification result.
func (g *SubstateAuthGate) Observe(_ *session.Instance, rep VerifyReport, _ error) {
	if rep.Substate == string(session.SubstateAuth401) {
		g.seen++
	}
}

// AuthFailures returns how many auth-failed boots have been observed. Exposed
// for the CLI summary.
func (g *SubstateAuthGate) AuthFailures() int { return g.seen }

func (g *SubstateAuthGate) limit() int {
	if g.HaltAfter <= 0 {
		return DefaultAuthHaltAfter
	}
	return g.HaltAfter
}
