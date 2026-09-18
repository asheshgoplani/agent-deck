package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Messaging audit P2-1 / review round 2 P1-B: the Stop-hook inbox drain answers
// with {decision:"block"}, which Claude Code only reads from a synchronous
// hook. The sync install exports the marker, but every install made BEFORE
// the marker existed is synchronous as well, so an absent marker must keep
// draining (the first cut silently disabled the drain on every existing
// machine). Only an explicit non-"1" marker, written for an async entry,
// disables it.
func TestStopHookDrain_MarkerCompat(t *testing.T) {
	t.Run("pre-marker sync install still drains", func(t *testing.T) {
		t.Setenv(session.StopHookSyncMarkerEnv, "")
		if !stopHookDrainEnabled() {
			t.Fatal("a Stop entry installed before the marker existed must still drain")
		}
	})
	t.Run("sync marker drains", func(t *testing.T) {
		t.Setenv(session.StopHookSyncMarkerEnv, "1")
		if !stopHookDrainEnabled() {
			t.Fatal("the sync install's marker must enable the drain")
		}
	})
	t.Run("explicit async marker skips the drain", func(t *testing.T) {
		t.Setenv(session.StopHookSyncMarkerEnv, "0")
		if stopHookDrainEnabled() {
			t.Fatal("an explicit async marker must disable the drain")
		}
	})
}
