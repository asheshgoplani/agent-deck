package session

import "testing"

// TestReviver_SkipsAuthHeldSession asserts the reviver leaves an auth-held
// session alone.
//
// Why this matters beyond futility: for a session whose agent has exited,
// defaultReviveAction's only effect is flipping StatusError → StatusRunning. On
// an auth-held session that is an actively HARMFUL lie — it erases the auth-401
// substate, which is the one signal telling the user (and the conductor) that a
// credential needs fixing, and hands the fleet a green light it has not earned.
func TestReviver_SkipsAuthHeldSession(t *testing.T) {
	inst := &Instance{ID: "reviver-auth-held", Title: "held", Status: StatusError}
	t.Cleanup(func() { clearAuthHoldRecord(inst.ID) })
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401 · Please run /login")

	spyCalls := 0
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return false },
		ReviveAction: func(*Instance) error { spyCalls++; return nil },
		Stagger:      0,
		AuthHeld:     defaultBootAuthHeld,
	}

	outcomes := r.ReviveAll([]*Instance{inst})

	if spyCalls != 0 {
		t.Fatalf("an auth-held session must not be revived; action called %d×", spyCalls)
	}
	if len(outcomes) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(outcomes))
	}
	if !outcomes[0].AuthHeld {
		t.Fatal("the outcome must report the auth hold so the CLI can surface it")
	}
	if outcomes[0].Revived {
		t.Fatal("a skipped session must not be reported as revived")
	}
	if inst.Status != StatusError {
		t.Fatalf("the honest error status must survive the sweep, got %q", inst.Status)
	}
}

// TestReviver_AuthHeldNilDisablesCheck preserves legacy behavior for the unit
// tests (and any caller) that construct Reviver{} without the seam.
func TestReviver_AuthHeldNilDisablesCheck(t *testing.T) {
	inst := &Instance{ID: "reviver-auth-nil-seam", Title: "held", Status: StatusError}
	t.Cleanup(func() { clearAuthHoldRecord(inst.ID) })
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")

	spyCalls := 0
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return false },
		ReviveAction: func(*Instance) error { spyCalls++; return nil },
		Stagger:      0,
		// AuthHeld deliberately nil.
	}

	outcomes := r.ReviveAll([]*Instance{inst})

	if spyCalls != 1 {
		t.Fatalf("a nil AuthHeld seam must not gate the revive; action called %d×", spyCalls)
	}
	if outcomes[0].AuthHeld {
		t.Fatal("no hold may be reported when the seam is disabled")
	}
}

// TestReviver_HealthySessionUnaffectedByAuthSeam asserts the new gate does not
// change the outcome for a session with no hold.
func TestReviver_HealthySessionUnaffectedByAuthSeam(t *testing.T) {
	inst := &Instance{ID: "reviver-auth-no-hold", Title: "errored", Status: StatusError}
	t.Cleanup(func() { clearAuthHoldRecord(inst.ID) })

	spyCalls := 0
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return false },
		ReviveAction: func(*Instance) error { spyCalls++; return nil },
		Stagger:      0,
		AuthHeld:     defaultBootAuthHeld,
	}

	outcomes := r.ReviveAll([]*Instance{inst})

	if spyCalls != 1 {
		t.Fatalf("an errored session with no auth hold must still be revived; got %d calls", spyCalls)
	}
	if outcomes[0].AuthHeld {
		t.Fatal("no hold may be reported for a session without one")
	}
}
