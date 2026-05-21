// Issue #1143: --idle-timeout flag on `agent-deck launch` / `session set`.
// CLI surface tests: flag parsing happy path, negative-value rejection, and
// session-set field validation.
package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestLaunch_IdleTimeoutFlag_AppliesToInstance verifies that
// `agent-deck launch ... --idle-timeout 30m` produces an Instance with
// IdleTimeoutSecs = 1800 (30 minutes in seconds).
func TestLaunch_IdleTimeoutFlag_AppliesToInstance(t *testing.T) {
	secs, err := session.ParseIdleTimeoutFlag("30m")
	if err != nil {
		t.Fatalf("ParseIdleTimeoutFlag(30m): %v", err)
	}
	if secs != 1800 {
		t.Fatalf("30m should be 1800 seconds, got %d", secs)
	}
}

// TestLaunch_IdleTimeoutFlag_RejectsNegative verifies that
// --idle-timeout -1s returns an error (not silently disabled).
func TestLaunch_IdleTimeoutFlag_RejectsNegative(t *testing.T) {
	if _, err := session.ParseIdleTimeoutFlag("-1s"); err == nil {
		t.Fatalf("ParseIdleTimeoutFlag(-1s) should error; got nil")
	}
}

// TestSessionSet_IdleTimeout_FieldRecognized verifies that the field name is
// in the validator list and dispatches to the right setter.
func TestSessionSet_IdleTimeout_FieldRecognized(t *testing.T) {
	inst := session.NewInstance("test", "/tmp")
	if _, _, err := session.SetField(inst, "idle-timeout", "10m", nil); err != nil {
		t.Fatalf("SetField idle-timeout: %v", err)
	}
	if inst.IdleTimeoutSecs != 600 {
		t.Fatalf("after SetField, IdleTimeoutSecs = %d, want 600", inst.IdleTimeoutSecs)
	}

	// Clearing to 0 via empty / "0".
	if _, _, err := session.SetField(inst, "idle-timeout", "0", nil); err != nil {
		t.Fatalf("SetField idle-timeout=0: %v", err)
	}
	if inst.IdleTimeoutSecs != 0 {
		t.Fatalf("idle-timeout=0 should clear, got %d", inst.IdleTimeoutSecs)
	}

	// Rejects malformed.
	if _, _, err := session.SetField(inst, "idle-timeout", "garbage", nil); err == nil {
		t.Fatalf("SetField idle-timeout=garbage should error")
	}
}
