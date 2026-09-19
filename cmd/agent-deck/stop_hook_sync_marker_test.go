package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Messaging audit P2-1 / review round 2 P1-B / review round 3 finding 3: the
// Stop-hook inbox drain answers with {decision:"block"}, which Claude Code
// only reads from a synchronous hook. The sync install exports the marker,
// but every install made BEFORE the marker existed is synchronous as well,
// so an absent marker must keep draining (the first cut silently disabled
// the drain on every existing machine). An explicit non-"1" marker disables
// it, and so does an agent-deck Stop entry that is installed ASYNC in the
// config dir's settings.json: the handler checks the installed form itself
// instead of relying on the heal to have flipped it.
func TestStopHookDrain_MarkerCompat(t *testing.T) {
	noSettings := t.TempDir()
	t.Run("pre-marker sync install still drains", func(t *testing.T) {
		t.Setenv(session.StopHookSyncMarkerEnv, "")
		if !stopHookDrainEnabled(noSettings) {
			t.Fatal("a Stop entry installed before the marker existed must still drain")
		}
	})
	t.Run("sync marker drains", func(t *testing.T) {
		t.Setenv(session.StopHookSyncMarkerEnv, "1")
		if !stopHookDrainEnabled(noSettings) {
			t.Fatal("the sync install's marker must enable the drain")
		}
	})
	t.Run("explicit async marker skips the drain", func(t *testing.T) {
		t.Setenv(session.StopHookSyncMarkerEnv, "0")
		if stopHookDrainEnabled(noSettings) {
			t.Fatal("an explicit async marker must disable the drain")
		}
	})
}

func writeStopEntry(t *testing.T, async string) string {
	t.Helper()
	dir := t.TempDir()
	settings := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/mine.sh","async":true},{"type":"command","command":"agent-deck hook-handler"` + async + `}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStopHookDrain_AsyncInstallNeverDrains(t *testing.T) {
	t.Setenv(session.StopHookSyncMarkerEnv, "")
	t.Run("async-installed entry never drains, marker or not", func(t *testing.T) {
		dir := writeStopEntry(t, `,"async":true`)
		if got := session.StopHookInstallForm(dir); got != session.StopHookFormAsync {
			t.Fatalf("form = %v, want async", got)
		}
		if stopHookDrainEnabled(dir) {
			t.Fatal("an async-installed Stop entry must never drain")
		}
		t.Setenv(session.StopHookSyncMarkerEnv, "1")
		if stopHookDrainEnabled(dir) {
			t.Fatal("a stale marker must not override the installed async form")
		}
	})
	t.Run("sync-installed entry drains", func(t *testing.T) {
		dir := writeStopEntry(t, "")
		if got := session.StopHookInstallForm(dir); got != session.StopHookFormSync {
			t.Fatalf("form = %v, want sync", got)
		}
		if !stopHookDrainEnabled(dir) {
			t.Fatal("a sync-installed Stop entry must drain")
		}
	})
	t.Run("no agent-deck entry: marker decides", func(t *testing.T) {
		dir := t.TempDir()
		settings := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/mine.sh","async":true}]}]}}`
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := session.StopHookInstallForm(dir); got != session.StopHookFormUnknown {
			t.Fatalf("form = %v, want unknown", got)
		}
		if !stopHookDrainEnabled(dir) {
			t.Fatal("unknown form with no marker keeps draining")
		}
		t.Setenv(session.StopHookSyncMarkerEnv, "0")
		if stopHookDrainEnabled(dir) {
			t.Fatal("unknown form with an async marker must not drain")
		}
	})
}
