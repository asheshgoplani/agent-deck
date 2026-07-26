package fleet

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// recorder captures the full interleaving of a sweep's side effects, so the
// tests can assert on SEQUENCING (the actual product requirement) and not just
// on counts.
type recorder struct {
	events   []string
	sleeps   []time.Duration
	restarts []string
	verified []string
	persists [][]string
}

func (r *recorder) log(format string, args ...any) {
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *recorder) sleep(d time.Duration) {
	r.sleeps = append(r.sleeps, d)
	r.log("sleep %s", d)
}

func (r *recorder) persist(instances []*session.Instance) error {
	titles := make([]string, 0, len(instances))
	for _, inst := range instances {
		titles = append(titles, inst.Title)
	}
	r.persists = append(r.persists, titles)
	r.log("persist %s", strings.Join(titles, ","))
	return nil
}

// downAssessment builds an Assessment as the Detector would, without probing.
func downAssessment(titles ...string) Assessment {
	as := Assessment{Total: len(titles), Down: len(titles), MassDeath: true}
	for _, ti := range titles {
		inst := testInstance(ti, session.StatusError)
		as.Candidates = append(as.Candidates, Candidate{
			Instance: inst,
			Health:   HealthDown,
			Status:   string(session.StatusError),
		})
	}
	return as
}

// bootedReport is the verification result of a healthy boot.
func bootedReport() VerifyReport {
	return VerifyReport{PaneAlive: true, ToolStarted: true, Status: string(session.StatusRunning)}
}

// newTestRecoverer wires a recoverer to the recorder with deterministic timing.
func newTestRecoverer(r *recorder, verify func(*session.Instance) VerifyReport) *Recoverer {
	return &Recoverer{
		Restart: func(inst *session.Instance) error {
			r.restarts = append(r.restarts, inst.Title)
			r.log("restart %s", inst.Title)
			return nil
		},
		Verify: func(inst *session.Instance) VerifyReport {
			r.verified = append(r.verified, inst.Title)
			r.log("verify %s", inst.Title)
			return verify(inst)
		},
		Persist:     r.persist,
		Sleep:       r.sleep,
		Rand:        func() float64 { return 0.5 }, // jitter-neutral
		Spacing:     5 * time.Second,
		Jitter:      0,
		MaxFailures: DefaultMaxFailures,
	}
}

// The core contract: one session at a time, spacing before every boot except
// the first, and each boot fully verified BEFORE the next restart begins.
func TestRecoverIsSequentialSpacedAndVerifiedPerSession(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })

	sum := rec.Recover(downAssessment("one", "two", "three"))

	want := []string{
		"restart one", "persist one", "verify one", "persist one",
		"sleep 5s",
		"restart two", "persist two", "verify two", "persist two",
		"sleep 5s",
		"restart three", "persist three", "verify three", "persist three",
	}
	if got := strings.Join(r.events, "|"); got != strings.Join(want, "|") {
		t.Fatalf("sweep interleaving:\n got %v\nwant %v", r.events, want)
	}
	if sum.Recovered != 3 || sum.Attempted != 3 {
		t.Errorf("summary = %+v, want 3 attempted/3 recovered", sum)
	}
	if sum.Failed != 0 || sum.Unverified != 0 || sum.Halted {
		t.Errorf("unexpected failure signals in %+v", sum)
	}
	if got := sum.Format(); got != "down=3 attempted=3 recovered=3 unverified=0 failed=0 skipped=0" {
		t.Errorf("Format() = %q", got)
	}
}

func TestRecoverFirstBootDoesNotWait(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })

	rec.Recover(downAssessment("only"))

	if len(r.sleeps) != 0 {
		t.Fatalf("first boot slept %v, want no wait", r.sleeps)
	}
}

// Spacing is the safety property of this command: an unset field must fall back
// to the default, and only the explicit opt-out may disable it.
func TestRecoverSpacingDefaultsAndOptOut(t *testing.T) {
	t.Run("unset spacing uses the default", func(t *testing.T) {
		r := &recorder{}
		rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
		rec.Spacing = 0
		rec.Recover(downAssessment("a", "b"))
		if len(r.sleeps) != 1 || r.sleeps[0] != DefaultSpacing {
			t.Fatalf("sleeps = %v, want one %s gap", r.sleeps, DefaultSpacing)
		}
	})

	t.Run("explicit opt-out disables spacing", func(t *testing.T) {
		r := &recorder{}
		rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
		rec.NoSpacing = true
		rec.Recover(downAssessment("a", "b"))
		if len(r.sleeps) != 0 {
			t.Fatalf("sleeps = %v, want none", r.sleeps)
		}
	})
}

func TestDefaultJitterSourceStaysInRange(t *testing.T) {
	for i := 0; i < 50; i++ {
		if got := defaultJitterSource(); got < 0 || got >= 1 {
			t.Fatalf("defaultJitterSource() = %v, want [0,1)", got)
		}
	}
}

func TestRecoverJitterStaysWithinBand(t *testing.T) {
	base := 10 * time.Second
	for _, rnd := range []float64{0, 0.25, 0.5, 0.75, 0.999} {
		rec := &Recoverer{Spacing: base, Jitter: 0.2, Rand: func() float64 { return rnd }}
		got := rec.spacingFor(1)
		low, high := time.Duration(float64(base)*0.8), time.Duration(float64(base)*1.2)
		if got < low || got > high {
			t.Errorf("rand=%v gap=%s, want within [%s,%s]", rnd, got, low, high)
		}
	}

	// Jitter is clamped so an absurd flag value can never produce a negative
	// (or unbounded) gap.
	rec := &Recoverer{Spacing: base, Jitter: 5, Rand: func() float64 { return 0 }}
	if got := rec.spacingFor(1); got < 0 {
		t.Errorf("clamped jitter produced negative gap %s", got)
	}
}

// Dry run must exercise the same planning path while touching nothing.
func TestRecoverDryRunPlansWithoutSideEffects(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport {
		t.Fatal("dry run must not verify")
		return VerifyReport{}
	})
	rec.Restart = func(*session.Instance) error {
		t.Fatal("dry run must not restart")
		return nil
	}
	rec.DryRun = true

	sum := rec.Recover(downAssessment("a", "b", "c"))

	if len(r.events) != 0 {
		t.Fatalf("dry run produced side effects: %v", r.events)
	}
	if sum.Attempted != 3 || sum.Recovered != 0 {
		t.Errorf("summary = %+v, want 3 planned and 0 recovered", sum)
	}
	for i, res := range sum.Results {
		if res.Outcome != OutcomePlanned {
			t.Errorf("result %d outcome = %q, want %q", i, res.Outcome, OutcomePlanned)
		}
	}
	// The plan reports the (jitter-free) wait it would have used so the
	// operator can estimate runtime.
	if sum.Results[0].WaitedBefore != 0 || sum.Results[1].WaitedBefore != 5*time.Second {
		t.Errorf("planned waits = %s, %s", sum.Results[0].WaitedBefore, sum.Results[1].WaitedBefore)
	}
	if want := 10 * time.Second; sum.TotalWaited != want {
		t.Errorf("TotalWaited = %s, want %s", sum.TotalWaited, want)
	}
	if !strings.HasPrefix(sum.Format(), "dry-run ") {
		t.Errorf("Format() = %q, want a dry-run prefix", sum.Format())
	}
}

// A pane that exists is not a booted agent. An unverified boot must be reported
// as its own outcome — never counted as a recovery.
func TestRecoverReportsUnverifiedBootSeparately(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(inst *session.Instance) VerifyReport {
		if inst.Title == "slow" {
			return VerifyReport{PaneAlive: true, Status: string(session.StatusStarting), Elapsed: DefaultVerifyTimeout}
		}
		return bootedReport()
	})

	sum := rec.Recover(downAssessment("slow", "fine"))

	if sum.Recovered != 1 || sum.Unverified != 1 {
		t.Fatalf("summary = %+v, want 1 recovered / 1 unverified", sum)
	}
	if sum.Results[0].Outcome != OutcomeUnverified {
		t.Errorf("outcome = %q, want %q", sum.Results[0].Outcome, OutcomeUnverified)
	}
	if sum.Halted {
		t.Error("an unverified boot must not halt the sweep")
	}
	// The sweep continued to the next session.
	if len(r.restarts) != 2 {
		t.Errorf("restarts = %v, want both sessions attempted", r.restarts)
	}
}

func TestRecoverHaltsAfterConsecutiveRestartFailures(t *testing.T) {
	r := &recorder{}
	boom := errors.New("tmux: no server running")
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	rec.Restart = func(inst *session.Instance) error {
		r.restarts = append(r.restarts, inst.Title)
		return boom
	}
	rec.MaxFailures = 3

	sum := rec.Recover(downAssessment("a", "b", "c", "d", "e"))

	if len(r.restarts) != 3 {
		t.Fatalf("restarts = %v, want the sweep to stop after 3 failures", r.restarts)
	}
	if !sum.Halted {
		t.Fatal("Halted = false, want true")
	}
	if sum.Failed != 3 || sum.Skipped != 2 {
		t.Errorf("summary = %+v, want 3 failed / 2 skipped", sum)
	}
	if !strings.Contains(sum.HaltReason, "consecutive") {
		t.Errorf("HaltReason = %q", sum.HaltReason)
	}
	if !strings.Contains(sum.Format(), "halted=true") {
		t.Errorf("Format() = %q, want the halted marker", sum.Format())
	}
	// Skipped sessions are reported as skipped, never as failed: the operator
	// must be able to tell "we tried and it broke" from "we never tried".
	for _, res := range sum.Results[3:] {
		if res.Outcome != OutcomeSkipped {
			t.Errorf("result %q outcome = %q, want skipped", res.Title, res.Outcome)
		}
	}
}

func TestRecoverSuccessResetsTheFailureRun(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	fail := map[string]bool{"a": true, "b": true, "d": true, "e": true}
	rec.Restart = func(inst *session.Instance) error {
		r.restarts = append(r.restarts, inst.Title)
		if fail[inst.Title] {
			return errors.New("nope")
		}
		return nil
	}
	rec.MaxFailures = 3

	// a,b fail; c succeeds (resetting the run); d,e fail — never 3 in a row.
	sum := rec.Recover(downAssessment("a", "b", "c", "d", "e"))

	if sum.Halted {
		t.Fatalf("Halted = true, want false: %s", sum.HaltReason)
	}
	if len(r.restarts) != 5 {
		t.Errorf("restarts = %v, want all 5 attempted", r.restarts)
	}
	if sum.Failed != 4 || sum.Recovered != 1 {
		t.Errorf("summary = %+v, want 4 failed / 1 recovered", sum)
	}
}

// The auth cascade case: once sessions start booting into an auth failure,
// restarting the remaining 60 only re-forks the shared credential. The sweep
// must stop and say so.
func TestRecoverHaltsOnAuthCircuit(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport {
		return VerifyReport{PaneAlive: true, Substate: string(session.SubstateAuth401)}
	})
	rec.AuthGate = &SubstateAuthGate{HaltAfter: 2}

	sum := rec.Recover(downAssessment("a", "b", "c", "d"))

	if len(r.restarts) != 2 {
		t.Fatalf("restarts = %v, want the sweep to stop after 2 auth failures", r.restarts)
	}
	if !sum.Halted {
		t.Fatal("Halted = false, want true")
	}
	if !strings.Contains(sum.HaltReason, "auth circuit open") {
		t.Errorf("HaltReason = %q", sum.HaltReason)
	}
	if sum.Unverified != 2 || sum.Skipped != 2 {
		t.Errorf("summary = %+v, want 2 unverified / 2 skipped", sum)
	}
}

func TestRecoverAuthGateCanBeDisabled(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport {
		return VerifyReport{PaneAlive: true, Substate: string(session.SubstateAuth401)}
	})
	rec.AuthGate = nil

	sum := rec.Recover(downAssessment("a", "b", "c"))

	if sum.Halted || len(r.restarts) != 3 {
		t.Fatalf("with the gate disabled all sessions must be attempted: restarts=%v halted=%t", r.restarts, sum.Halted)
	}
}

// The 2026-07-26 shape the substate gate CANNOT see: a session whose credential
// is dead does not sit in the pane showing a 401 banner, it exits — so the
// restart "succeeds", the pane is gone by verification time, there is no
// substate, and neither the auth gate nor the failed-restart brake fires. The
// dead-boot brake is what stops the sweep from burning the whole fleet.
func TestRecoverHaltsAfterConsecutiveDeadBoots(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport {
		// Restart returned nil, but the pane is gone again: the session started
		// and immediately exited.
		return VerifyReport{PaneAlive: false, Status: string(session.StatusError)}
	})
	rec.MaxDeadBoots = 3

	sum := rec.Recover(downAssessment("a", "b", "c", "d", "e", "f"))

	if len(r.restarts) != 3 {
		t.Fatalf("restarts = %v, want the sweep to stop after 3 dead boots", r.restarts)
	}
	if !sum.Halted {
		t.Fatal("Halted = false, want true")
	}
	if !strings.Contains(sum.HaltReason, "died immediately") {
		t.Errorf("HaltReason = %q, want it to name the failure shape", sum.HaltReason)
	}
	if sum.Unverified != 3 || sum.Skipped != 3 || sum.Failed != 0 {
		t.Errorf("summary = %+v, want 3 unverified / 3 skipped / 0 failed", sum)
	}
}

// Zero is "use the default", not "off" — an under-configured Recoverer must
// keep the brake that stops a fleet-wide amplification.
func TestRecoverDeadBootBrakeDefaultsWhenUnset(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return VerifyReport{} })
	rec.MaxDeadBoots = 0

	sum := rec.Recover(downAssessment("a", "b", "c", "d", "e"))

	if len(r.restarts) != DefaultMaxDeadBoots {
		t.Fatalf("restarts = %v, want %d (the default brake)", r.restarts, DefaultMaxDeadBoots)
	}
	if !sum.Halted {
		t.Fatal("Halted = false, want the default brake to trip")
	}
}

// Turning the brake off has to be explicit and visible.
func TestRecoverDeadBootBrakeCanBeDisabled(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return VerifyReport{} })
	rec.MaxDeadBoots = -1
	rec.AuthGate = nil

	sum := rec.Recover(downAssessment("a", "b", "c", "d"))

	if sum.Halted || len(r.restarts) != 4 {
		t.Fatalf("with the brake disabled all sessions must be attempted: restarts=%v halted=%t", r.restarts, sum.Halted)
	}
}

// A slow boot is not a dead one. A pane that is up but still `starting` when the
// verify timeout expires must never stop a recovery that is merely taking its
// time — otherwise a loaded host looks like a credential outage.
func TestRecoverSlowBootsDoNotTripTheDeadBootBrake(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport {
		return VerifyReport{PaneAlive: true, Status: string(session.StatusStarting), Elapsed: DefaultVerifyTimeout}
	})
	rec.MaxDeadBoots = 2

	sum := rec.Recover(downAssessment("a", "b", "c", "d"))

	if sum.Halted {
		t.Fatalf("Halted = true (%s), want slow boots to be tolerated", sum.HaltReason)
	}
	if sum.Unverified != 4 || len(r.restarts) != 4 {
		t.Errorf("summary = %+v restarts = %v, want all four attempted and unverified", sum, r.restarts)
	}
}

// One session dying on boot is a crash, not a cascade: a verified boot in
// between proves the host and the credential still work.
func TestRecoverVerifiedBootResetsTheDeadBootRun(t *testing.T) {
	r := &recorder{}
	dead := map[string]bool{"a": true, "b": true, "d": true, "e": true}
	rec := newTestRecoverer(r, func(inst *session.Instance) VerifyReport {
		if dead[inst.Title] {
			return VerifyReport{}
		}
		return bootedReport()
	})
	rec.MaxDeadBoots = 3

	sum := rec.Recover(downAssessment("a", "b", "c", "d", "e"))

	if sum.Halted {
		t.Fatalf("Halted = true (%s), want the run broken by the boot that worked", sum.HaltReason)
	}
	if len(r.restarts) != 5 || sum.Recovered != 1 || sum.Unverified != 4 {
		t.Errorf("summary = %+v restarts = %v, want all 5 attempted", sum, r.restarts)
	}
}

// A model outage is not a credential problem: it clears on its own and must not
// stop a recovery that is otherwise working.
func TestSubstateAuthGateIgnoresModelUnavailable(t *testing.T) {
	g := &SubstateAuthGate{HaltAfter: 1}
	g.Observe(nil, VerifyReport{PaneAlive: true, Substate: string(session.SubstateModelUnavailable)}, nil)
	g.Observe(nil, VerifyReport{PaneAlive: true, Substate: string(session.SubstateModelUnavailable)}, nil)
	if ok, reason := g.Allow(); !ok {
		t.Fatalf("gate opened on model-unavailable: %s", reason)
	}
	if g.AuthFailures() != 0 {
		t.Errorf("AuthFailures = %d, want 0", g.AuthFailures())
	}
}

func TestSubstateAuthGateDefaultsHaltAfter(t *testing.T) {
	g := &SubstateAuthGate{}
	for i := 0; i < DefaultAuthHaltAfter; i++ {
		if ok, _ := g.Allow(); !ok {
			t.Fatalf("gate closed after %d observations, want %d", i, DefaultAuthHaltAfter)
		}
		g.Observe(nil, VerifyReport{PaneAlive: true, Substate: string(session.SubstateAuth401)}, nil)
	}
	if ok, _ := g.Allow(); ok {
		t.Fatalf("gate still open after %d auth failures", DefaultAuthHaltAfter)
	}
}

// THE destructive direction. A 65-session sweep runs for minutes; if a session
// comes back on its own (or the operator restarts it) in that window, restarting
// it again would kill a live agent mid-work. The pre-restart re-probe is the
// guard, and it must not consume a spacing slot for a session it skipped.
func TestRecoverSkipsSessionsThatCameBackOnTheirOwn(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	rec.StillDown = func(inst *session.Instance) bool { return inst.Title != "self-healed" }

	sum := rec.Recover(downAssessment("self-healed", "still-dead"))

	if len(r.restarts) != 1 || r.restarts[0] != "still-dead" {
		t.Fatalf("restarts = %v, want only [still-dead]", r.restarts)
	}
	if sum.Skipped != 1 || sum.Recovered != 1 {
		t.Errorf("summary = %+v, want 1 skipped / 1 recovered", sum)
	}
	if !strings.Contains(sum.Results[0].Reason, "came back on its own") {
		t.Errorf("skip reason = %q", sum.Results[0].Reason)
	}
	// The skipped session did not consume a boot slot, so the first real boot
	// still starts immediately.
	if len(r.sleeps) != 0 {
		t.Errorf("sleeps = %v, want none (no boot preceded the only restart)", r.sleeps)
	}
	if len(r.persists) != 2 {
		t.Errorf("persist batches = %v, want only the restarted session (pre+post verify)", r.persists)
	}
}

func TestRecoverReProbesAfterTheSpacingWait(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	rec.StillDown = func(inst *session.Instance) bool {
		r.log("probe %s", inst.Title)
		return true
	}

	rec.Recover(downAssessment("a", "b"))

	// The freshness reading must be the LAST thing before the restart, i.e.
	// after the wait — otherwise it is as stale as the assessment was.
	want := "sleep 5s|probe b|restart b"
	if got := strings.Join(r.events, "|"); !strings.Contains(got, want) {
		t.Fatalf("events = %v, want the subsequence %q", r.events, want)
	}
}

func TestRecoverLimit(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	rec.Limit = 2

	sum := rec.Recover(downAssessment("a", "b", "c", "d"))

	if len(r.restarts) != 2 {
		t.Fatalf("restarts = %v, want 2", r.restarts)
	}
	if sum.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", sum.Skipped)
	}
	if !strings.Contains(sum.Results[2].Reason, "--limit") {
		t.Errorf("skip reason = %q, want it to name --limit", sum.Results[2].Reason)
	}
}

// DATA-SAFETY REGRESSION GUARD (2026-06-04). The persist seam must only ever
// receive the single session a boot just mutated. A sweep that hands storage its
// whole stale snapshot is how the profile index got wiped.
func TestRecoverPersistsOnlyTheSessionItJustRestarted(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })

	rec.Recover(downAssessment("a", "b"))

	if len(r.persists) == 0 {
		t.Fatal("nothing was persisted")
	}
	for i, batch := range r.persists {
		if len(batch) != 1 {
			t.Fatalf("persist batch %d = %v, want exactly one session", i, batch)
		}
	}
}

// A failed restart did not mutate the session, so it must not be persisted:
// writing a row we did not change is pure clobber risk.
func TestRecoverDoesNotPersistFailedRestarts(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	rec.Restart = func(*session.Instance) error { return errors.New("nope") }
	rec.MaxFailures = 99

	rec.Recover(downAssessment("a", "b"))

	if len(r.persists) != 0 {
		t.Fatalf("persisted %v after failed restarts, want nothing", r.persists)
	}
}

// Storage trouble on one session must not strand the other 60.
func TestRecoverContinuesWhenPersistFails(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	rec.Persist = func([]*session.Instance) error { return errors.New("database is locked") }

	sum := rec.Recover(downAssessment("a", "b"))

	if sum.Recovered != 2 {
		t.Fatalf("summary = %+v, want both recovered despite persist errors", sum)
	}
}

func TestRecoverWithoutRestartActionFailsLoudly(t *testing.T) {
	rec := &Recoverer{Sleep: func(time.Duration) {}, MaxFailures: 99}
	sum := rec.Recover(downAssessment("a"))
	if sum.Failed != 1 {
		t.Fatalf("summary = %+v, want the missing action reported as a failure", sum)
	}
	if sum.Results[0].Err == nil {
		t.Fatal("expected an error on the result")
	}
}

func TestRecoverSkipsNilInstances(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	as := downAssessment("a")
	as.Candidates = append(as.Candidates, Candidate{Health: HealthDown})
	as.Down = len(as.Candidates)

	sum := rec.Recover(as)

	if sum.Recovered != 1 || sum.Skipped != 1 {
		t.Fatalf("summary = %+v, want 1 recovered / 1 skipped", sum)
	}
}

func TestRecoverEmptyAssessment(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	sum := rec.Recover(Assessment{})
	if sum.Attempted != 0 || len(sum.Results) != 0 || sum.Halted {
		t.Fatalf("summary = %+v, want an empty no-op", sum)
	}
}

func TestRecoverProgressReportsEveryAttempt(t *testing.T) {
	r := &recorder{}
	rec := newTestRecoverer(r, func(*session.Instance) VerifyReport { return bootedReport() })
	var lines []string
	rec.Progress = func(index, total int, c Candidate) {
		lines = append(lines, fmt.Sprintf("%d/%d %s", index, total, c.Title()))
	}

	rec.Recover(downAssessment("a", "b"))

	want := []string{"1/2 a", "2/2 b"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Fatalf("progress = %v, want %v", lines, want)
	}
}

func TestHealthString(t *testing.T) {
	cases := map[Health]string{
		HealthAlive:   "alive",
		HealthDown:    "down",
		HealthSkipped: "skipped",
		Health(99):    "unknown",
	}
	for h, want := range cases {
		if got := h.String(); got != want {
			t.Errorf("Health(%d).String() = %q, want %q", h, got, want)
		}
	}
}
