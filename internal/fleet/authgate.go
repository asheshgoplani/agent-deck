package fleet

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// AuthGate is the circuit-breaker seam a recovery sweep consults before every
// boot and reports every boot to.
//
// INTEGRATION POINT (deliberate, and mechanical to complete). The auth-cascade
// half of the 2026-07-26 fleet deaths is fixed on a separate branch: N sessions
// share one rotating OAuth refresh token, so a burst of restarts forks the token
// and 401s the whole fleet (bug_oauth_multisession_rotation_race_rootcause).
// That work adds a per-session auth HOLD plus a paced bulk-boot breaker in
// internal/session (Instance.IsAuthHeld / RecordAuthBootFailure and
// session.BootSweep). It is not on main yet, so this package cannot reference it
// without breaking the build; when it lands, the adapter is:
//
//	type heldAuthGate struct{ sweep *session.BootSweep; consecutive, limit int }
//	// Allow():   consecutive < limit
//	// Observe(): if inst.IsAuthHeld() (or rep.AuthFailed()) → consecutive++,
//	//            inst.RecordAuthBootFailure(); else consecutive = 0
//
// plus one line in Detector.Classify to report an auth-held session as
// HealthSkipped ("auth hold: <remedy>") so a sweep never restarts a session the
// hold has already parked. Nothing in the sequencing logic changes: that is what
// the AuthGate seam is for.
//
// Until then Recoverer defaults to SubstateAuthGate below, which reads the same
// signal from the pane the TUI already classifies (Honest-Status v2 substate
// "auth-401") and halts the sweep rather than grinding through 60 doomed boots.
// The complementary case — a session that does not sit in a 401 banner but
// EXITS on the failed refresh, leaving no pane and no substate — is covered by
// the recoverer's dead-boot brake (Recoverer.MaxDeadBoots), not here.
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

	// GroupByCredential opts this gate into credential-identity grouping
	// (#1816): the halt message names WHICH credential is dead and reports one
	// line per credential instead of one number for the whole sweep, and
	// AuthFailuresByCredential becomes populated.
	//
	// OFF BY DEFAULT, and that is a product boundary, not an oversight (the
	// maintainer ruling on #1816: local-only, opt-in, per-session view stays the
	// default surface). With it false this gate behaves EXACTLY as it did
	// before #1816, down to the byte-for-byte halt string above — a sweep that
	// did not opt in must not have its operator message change under it.
	GroupByCredential bool

	// Grouper resolves credential identity when GroupByCredential is set. nil
	// uses the real config-dir chain. Injectable for the same reason every side
	// effect in this package is.
	Grouper *AuthCredentialGrouper

	seen int
	// creds accumulates the credentials behind the auth-failed boots. Built
	// only when GroupByCredential is set, so the default path allocates nothing.
	creds *credAccumulator
	// recovered names credentials whose LAST observation in this sweep
	// authenticated successfully.
	recovered map[string]bool
}

// Allow reports whether another boot may proceed.
func (g *SubstateAuthGate) Allow() (bool, string) {
	limit := g.limit()
	if g.seen < limit {
		return true, ""
	}
	if g.GroupByCredential {
		return false, g.groupedHaltReason()
	}
	return false, fmt.Sprintf(
		"auth circuit open: %d session(s) booted into an auth failure (substate %q) — recovering the rest would keep re-forking the shared credential; re-authenticate, then re-run",
		g.seen, session.SubstateAuth401)
}

// groupedHaltReason is the #1816 halt message: one line per dead credential
// rather than one count for the sweep.
//
// It falls back to the ungrouped wording when nothing was attributable, because
// "0 credentials" would read as "no credential problem" — the exact opposite of
// why the circuit just opened.
func (g *SubstateAuthGate) groupedHaltReason() string {
	sum := g.credentialSummary()
	// Nothing was collected at all (the circuit opened without this gate ever
	// resolving a credential). Fall back rather than print a grouped message
	// with no groups in it.
	if len(sum.Groups) == 0 {
		return fmt.Sprintf(
			"auth circuit open: %d session(s) booted into an auth failure (substate %q) — recovering the rest would keep re-forking the shared credential; re-authenticate, then re-run",
			g.seen, session.SubstateAuth401)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "auth circuit open: %d credential(s) dead behind %d booted session(s) (substate %q) — recovering the rest would keep re-forking them; re-authenticate, then re-run",
		sum.Credentials, g.seen, session.SubstateAuth401)
	// GroupByCredential can be turned on after this gate has already observed
	// boots (it is an exported field on an exported type). Those boots were
	// never attributed, so the per-credential lines below account for fewer
	// sessions than the headline count. Saying so is the whole difference
	// between an incomplete report and a wrong one: silently listing 1 session
	// under a headline of 3 reads as "the other 2 are fine".
	if ungrouped := g.seen - sum.Held; ungrouped > 0 {
		fmt.Fprintf(&b, " — NOTE: %d earlier boot(s) were observed before credential grouping was enabled and are not attributed below",
			ungrouped)
	}
	for _, grp := range sum.Groups {
		fmt.Fprintf(&b, "\n  - %s", grp.Escalation())
	}
	return b.String()
}

// Observe records one boot's verification result.
func (g *SubstateAuthGate) Observe(inst *session.Instance, rep VerifyReport, _ error) {
	authFailed := rep.Substate == string(session.SubstateAuth401)
	if authFailed {
		g.seen++
	}
	if !g.GroupByCredential {
		return
	}

	// A boot that authenticated is evidence about a CREDENTIAL, not just about
	// one session — it is how "the token was fixed mid-sweep" actually presents,
	// and without it the halt would keep escalating a credential that has since
	// proved it works. Not tracked at all when the grouping is off: the
	// ungrouped gate has no notion of which credential anything ran under.
	if !authFailed && !rep.Booted() {
		return
	}

	key := g.grouper().Identify(inst).Key
	if !authFailed {
		if g.recovered == nil {
			g.recovered = make(map[string]bool)
		}
		g.recovered[key] = true
		return
	}

	// Last observation wins: a credential that booted cleanly and then 401'd is
	// dead now, whatever it did earlier in the sweep.
	delete(g.recovered, key)
	if g.creds == nil {
		g.creds = newCredAccumulator(g.grouper().Identify)
	}
	// The sidecar is not guaranteed to exist yet for a boot that just came up on
	// the banner, so Reason is left empty rather than guessed: credential
	// identity does not depend on the hold record.
	g.creds.add(inst, HeldSession{
		ID:      instanceID(inst),
		Title:   instanceTitle(inst),
		Account: instanceAccount(inst),
	})
}

// AuthFailures returns how many auth-failed boots have been observed. Exposed
// for the CLI summary.
func (g *SubstateAuthGate) AuthFailures() int { return g.seen }

// AuthFailuresByCredential returns the auth-failed boots grouped by which
// credential they were running under (#1816).
//
// Empty unless GroupByCredential was set: the grouping is opt-in, so a caller
// that did not ask for it gets nothing rather than a silently-collected view.
func (g *SubstateAuthGate) AuthFailuresByCredential() AuthCredentialSummary {
	return g.credentialSummary()
}

func (g *SubstateAuthGate) credentialSummary() AuthCredentialSummary {
	if g.creds == nil {
		return AuthCredentialSummary{}
	}
	return g.creds.summaryWithRecovered(g.recovered)
}

func (g *SubstateAuthGate) grouper() *AuthCredentialGrouper {
	if g.Grouper != nil {
		return g.Grouper
	}
	return NewAuthCredentialGrouper()
}

func (g *SubstateAuthGate) limit() int {
	if g.HaltAfter <= 0 {
		return DefaultAuthHaltAfter
	}
	return g.HaltAfter
}
