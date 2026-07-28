package session

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// bootTestClock drives BootSweep deterministically: Sleep advances the clock instead
// of blocking, so the whole verification-scheduling policy is testable in
// microseconds.
type bootTestClock struct {
	now    time.Time
	slept  []time.Duration
	totalS time.Duration
}

func newBootTestClock() *bootTestClock {
	return &bootTestClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *bootTestClock) Now() time.Time { return c.now }

func (c *bootTestClock) Sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.totalS += d
	c.now = c.now.Add(d)
}

// testSweep builds a BootSweep with every slow/random seam replaced. authHeld and
// authDeath are keyed by instance title so cases read declaratively.
func testSweep(clock *bootTestClock, held map[string]bool, authDeath map[string]bool) *BootSweep {
	return &BootSweep{
		MaxInFlight:       2,
		BaseStagger:       100 * time.Millisecond,
		Jitter:            0,
		AuthTripThreshold: 3,
		VerifyDelay:       1 * time.Second,
		Sleep:             clock.Sleep,
		Now:               clock.Now,
		JitterFor:         func(time.Duration) time.Duration { return 0 },
		AuthHeld: func(inst *Instance) (bool, string) {
			if held[inst.Title] {
				return true, "held: run /login"
			}
			return false, ""
		},
		AuthDeathAfterBoot: func(inst *Instance) bool { return authDeath[inst.Title] },
	}
}

func fakeInstances(titles ...string) []*Instance {
	out := make([]*Instance, 0, len(titles))
	for _, t := range titles {
		out = append(out, &Instance{ID: "id-" + t, Title: t})
	}
	return out
}

// TestBootSweep_TripsAfterConsecutiveAuthDeaths is the fleet-death regression:
// during a credential outage the sweep must STOP, not walk the whole fleet.
func TestBootSweep_TripsAfterConsecutiveAuthDeaths(t *testing.T) {
	clock := newBootTestClock()
	instances := fakeInstances("a", "b", "c", "d", "e", "f", "g", "h")
	// Every boot dies on auth — the exact shape of a fleet-wide 401.
	authDeath := map[string]bool{}
	for _, inst := range instances {
		authDeath[inst.Title] = true
	}
	sweep := testSweep(clock, nil, authDeath)

	var booted []string
	result := sweep.Run(instances, func(inst *Instance) error {
		booted = append(booted, inst.Title)
		return nil
	})

	if !result.Tripped {
		t.Fatal("expected the circuit to trip on a fleet-wide auth failure")
	}
	if len(booted) >= len(instances) {
		t.Fatalf("the whole fleet was burned: booted %d of %d", len(booted), len(instances))
	}
	if result.Abandoned == 0 {
		t.Fatal("expected sessions to be deliberately left untouched")
	}
	if result.Booted+result.Abandoned+result.SkippedHeld+result.Failed != len(instances) {
		t.Fatalf("every instance must be accounted for: %+v", result)
	}
	// One loud, actionable message — not a wall of per-session errors.
	if !strings.Contains(result.TripMessage, "/login") {
		t.Fatalf("trip message must name the remedy, got %q", result.TripMessage)
	}
	if !strings.Contains(result.TripMessage, "left untouched") {
		t.Fatalf("trip message must report the blast radius avoided, got %q", result.TripMessage)
	}
}

// TestBootSweep_ConsecutiveCounterResetsOnHealthyBoot asserts the breaker tracks
// CONSECUTIVE failures. A host with a couple of individually-broken sessions must
// not have its whole sweep aborted.
func TestBootSweep_ConsecutiveCounterResetsOnHealthyBoot(t *testing.T) {
	clock := newBootTestClock()
	instances := fakeInstances("bad1", "ok1", "bad2", "ok2", "bad3", "ok3")
	authDeath := map[string]bool{"bad1": true, "bad2": true, "bad3": true}
	sweep := testSweep(clock, nil, authDeath)
	sweep.MaxInFlight = 1 // verify each boot before the next, so the interleave is exact

	result := sweep.Run(instances, func(*Instance) error { return nil })

	if result.Tripped {
		t.Fatalf("interleaved failures must not trip the breaker: %+v", result)
	}
	if result.Booted != len(instances) {
		t.Fatalf("expected all %d sessions booted, got %d", len(instances), result.Booted)
	}
	if result.AuthDeaths != 3 {
		t.Fatalf("expected 3 auth deaths recorded, got %d", result.AuthDeaths)
	}
}

// TestBootSweep_SkipsAlreadyHeldSessions asserts a held session is never booted:
// it cannot succeed and would only add contention on the shared token.
func TestBootSweep_SkipsAlreadyHeldSessions(t *testing.T) {
	clock := newBootTestClock()
	instances := fakeInstances("held", "fine")
	sweep := testSweep(clock, map[string]bool{"held": true}, nil)

	var booted []string
	result := sweep.Run(instances, func(inst *Instance) error {
		booted = append(booted, inst.Title)
		return nil
	})

	if len(booted) != 1 || booted[0] != "fine" {
		t.Fatalf("expected only the un-held session to boot, got %v", booted)
	}
	if result.SkippedHeld != 1 {
		t.Fatalf("expected 1 held skip, got %d", result.SkippedHeld)
	}
	var skipReason string
	for _, a := range result.Attempts {
		if a.Title == "held" {
			skipReason = a.SkipReason
		}
	}
	if !strings.Contains(skipReason, "/login") {
		t.Fatalf("a skip must carry the remedy, got %q", skipReason)
	}
}

// TestBootSweep_StaggersWithJitter asserts boots are paced AND de-correlated.
// A fixed stagger only moves a synchronized burst; jitter is what actually stops
// N agents from refreshing the shared token at the same instant.
func TestBootSweep_StaggersWithJitter(t *testing.T) {
	clock := newBootTestClock()
	instances := fakeInstances("a", "b", "c")
	sweep := testSweep(clock, nil, nil)
	sweep.BaseStagger = 200 * time.Millisecond
	sweep.Jitter = 500 * time.Millisecond
	jitterCalls := 0
	sweep.JitterFor = func(width time.Duration) time.Duration {
		jitterCalls++
		if width != 500*time.Millisecond {
			t.Fatalf("jitter width must be the configured Jitter, got %s", width)
		}
		return 50 * time.Millisecond
	}

	boots := 0
	sweep.Run(instances, func(*Instance) error {
		boots++
		return nil
	})

	// One jittered gap per boot AFTER the first: the first boot is immediate, and
	// every later one waits BaseStagger + jitter.
	if jitterCalls != len(instances)-1 {
		t.Fatalf("expected %d jittered gaps, got %d", len(instances)-1, jitterCalls)
	}
	if boots != len(instances) {
		t.Fatalf("expected %d boots, got %d", len(instances), boots)
	}
	// The stagger sleep itself must be BaseStagger + jitter, never bare
	// BaseStagger — a fixed gap merely offsets a synchronized burst.
	var sawStaggerPlusJitter bool
	for _, d := range clock.slept {
		if d == 250*time.Millisecond {
			sawStaggerPlusJitter = true
		}
		if d == 200*time.Millisecond {
			t.Fatalf("saw a bare BaseStagger sleep with no jitter added: %v", clock.slept)
		}
	}
	if !sawStaggerPlusJitter {
		t.Fatalf("expected BaseStagger+jitter sleeps, saw %v", clock.slept)
	}
}

// TestBootSweep_CapsUnverifiedBootsInFlight asserts the token-contention cap:
// at no point may more than MaxInFlight booted-but-unverified agents exist,
// because that count is exactly how many are racing the shared refresh token.
func TestBootSweep_CapsUnverifiedBootsInFlight(t *testing.T) {
	clock := newBootTestClock()
	instances := fakeInstances("a", "b", "c", "d", "e")
	sweep := testSweep(clock, nil, nil)
	sweep.MaxInFlight = 2

	inFlight := 0
	maxSeen := 0
	sweep.AuthDeathAfterBoot = func(*Instance) bool {
		inFlight--
		return false
	}
	sweep.Run(instances, func(*Instance) error {
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		return nil
	})

	if maxSeen > 2 {
		t.Fatalf("unverified boots exceeded the cap: saw %d, cap 2", maxSeen)
	}
	if inFlight != 0 {
		t.Fatalf("every boot must be verified by the end of the sweep, %d left", inFlight)
	}
}

// TestBootSweep_BootErrorIsNotAnAuthDeath asserts a plain boot failure (bad
// command, tmux refused) is reported as a failure and does NOT count toward the
// auth circuit. Conflating the two would abort sweeps for unrelated reasons.
func TestBootSweep_BootErrorIsNotAnAuthDeath(t *testing.T) {
	clock := newBootTestClock()
	instances := fakeInstances("a", "b", "c", "d")
	sweep := testSweep(clock, nil, nil)

	result := sweep.Run(instances, func(*Instance) error { return errors.New("tmux refused") })

	if result.Tripped {
		t.Fatal("boot errors must not trip the AUTH circuit")
	}
	if result.Failed != len(instances) {
		t.Fatalf("expected %d failures, got %d", len(instances), result.Failed)
	}
	if result.AuthDeaths != 0 {
		t.Fatalf("expected no auth deaths, got %d", result.AuthDeaths)
	}
}

// TestBootSweep_ZeroValueGetsEveryBrake is the anti-footgun test. An
// under-configured BootSweep{} must NOT degrade into the unbraked sweep that
// caused the outage: a zero VerifyDelay in particular would judge every boot
// before the agent could fail authentication, so no auth-death could ever be
// observed and the breaker could never trip.
func TestBootSweep_ZeroValueGetsEveryBrake(t *testing.T) {
	sweep := &BootSweep{}
	result := sweep.Run(nil, func(*Instance) error { return nil })
	if result.Tripped || result.Booted != 0 {
		t.Fatalf("empty sweep must be a clean no-op, got %+v", result)
	}
	if sweep.MaxInFlight < 1 {
		t.Fatal("MaxInFlight must be normalised to at least 1")
	}
	if sweep.BaseStagger != DefaultBootBaseStagger {
		t.Fatalf("BaseStagger = %s, want the default %s", sweep.BaseStagger, DefaultBootBaseStagger)
	}
	if sweep.Jitter != DefaultBootJitter {
		t.Fatalf("Jitter = %s, want the default %s", sweep.Jitter, DefaultBootJitter)
	}
	if sweep.AuthTripThreshold != DefaultAuthTripThreshold {
		t.Fatalf("AuthTripThreshold = %d, want the default %d", sweep.AuthTripThreshold, DefaultAuthTripThreshold)
	}
	if sweep.VerifyDelay != DefaultBootVerifyDelay {
		t.Fatalf("VerifyDelay = %s, want the default %s", sweep.VerifyDelay, DefaultBootVerifyDelay)
	}
}

// TestBootSweep_ZeroValueStillTrips is the behavioral half of the above: a
// BootSweep{} carrying only test seams must still stop on a fleet-wide failure.
func TestBootSweep_ZeroValueStillTrips(t *testing.T) {
	clock := newBootTestClock()
	instances := fakeInstances("a", "b", "c", "d", "e", "f")
	sweep := &BootSweep{
		Sleep:              clock.Sleep,
		Now:                clock.Now,
		JitterFor:          func(time.Duration) time.Duration { return 0 },
		AuthHeld:           func(*Instance) (bool, string) { return false, "" },
		AuthDeathAfterBoot: func(*Instance) bool { return true },
	}

	result := sweep.Run(instances, func(*Instance) error { return nil })

	if !result.Tripped {
		t.Fatal("a zero-value sweep must still trip on a fleet-wide auth failure")
	}
	if result.Booted >= len(instances) {
		t.Fatalf("the whole fleet was burned: booted %d of %d", result.Booted, len(instances))
	}
}

// TestBootSweep_NegativeThresholdDisablesBreaker preserves an escape hatch for a
// caller that explicitly wants no breaker. Disabling must be NEGATIVE, never
// zero, so an unset field can never silently mean "off".
func TestBootSweep_NegativeThresholdDisablesBreaker(t *testing.T) {
	clock := newBootTestClock()
	instances := fakeInstances("a", "b", "c", "d", "e")
	authDeath := map[string]bool{"a": true, "b": true, "c": true, "d": true, "e": true}
	sweep := testSweep(clock, nil, authDeath)
	sweep.AuthTripThreshold = -1

	result := sweep.Run(instances, func(*Instance) error { return nil })

	if result.Tripped {
		t.Fatal("a negative threshold must disable the circuit breaker")
	}
	if result.Booted != len(instances) {
		t.Fatalf("expected all %d booted, got %d", len(instances), result.Booted)
	}
}

// TestBootSweep_NegativeStaggerSkipsPacing asserts the negative-means-disabled
// convention holds for pacing too, without ever calling Sleep with a nonsense
// duration.
func TestBootSweep_NegativeStaggerSkipsPacing(t *testing.T) {
	clock := newBootTestClock()
	instances := fakeInstances("a", "b", "c")
	sweep := testSweep(clock, nil, nil)
	sweep.BaseStagger = -1
	sweep.JitterFor = func(time.Duration) time.Duration { return 0 }

	sweep.Run(instances, func(*Instance) error { return nil })

	for _, d := range clock.slept {
		if d <= 0 {
			t.Fatalf("Sleep must never be called with a non-positive duration, got %v", clock.slept)
		}
	}
}

// TestBootSweep_SkipsNilInstances keeps the sweep total over a sparse slice
// (storage loads can contain nils).
func TestBootSweep_SkipsNilInstances(t *testing.T) {
	clock := newBootTestClock()
	instances := []*Instance{nil, {ID: "id-a", Title: "a"}, nil}
	sweep := testSweep(clock, nil, nil)

	result := sweep.Run(instances, func(*Instance) error { return nil })

	if result.Booted != 1 {
		t.Fatalf("expected 1 boot, got %d", result.Booted)
	}
}

// TestBootSweepDefaults_ArePaced guards the tuning constants: a default sweep
// must actually pace and actually trip, since a zero default would silently
// restore the unbraked behavior that caused the outage.
func TestBootSweepDefaults_ArePaced(t *testing.T) {
	sweep := NewBootSweep()
	if sweep.BaseStagger <= 0 {
		t.Fatal("default sweep must stagger boots")
	}
	if sweep.Jitter <= 0 {
		t.Fatal("default sweep must jitter boots to de-correlate token refreshes")
	}
	if sweep.AuthTripThreshold <= 0 {
		t.Fatal("default sweep must have an armed circuit breaker")
	}
	if sweep.MaxInFlight < 1 {
		t.Fatal("default sweep must cap unverified boots")
	}
	if sweep.VerifyDelay <= 0 {
		t.Fatal("default sweep must wait before judging a boot")
	}
}
