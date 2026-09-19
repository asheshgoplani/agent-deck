package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ui"
)

// v1.16.11 rollout: the startup reviver of the re-exec'd TUI sampled every
// pipe as dead before the new image had attached any and respawned live
// sessions. A process that was restarted in place pauses that sweep; a
// fresh start does not wait.
func TestStartupReviveDelay(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if d := startupReviveDelay(getenv); d != 0 {
		t.Fatalf("fresh start: delay = %v, want 0", d)
	}
	env[ui.RestartedFromEnv] = "1.16.11-rc.6"
	if d := startupReviveDelay(getenv); d != reviverRestartGrace || d <= 0 {
		t.Fatalf("after an in-place restart: delay = %v, want %v", d, reviverRestartGrace)
	}
	env[ui.RestartedFromEnv] = "  "
	if d := startupReviveDelay(getenv); d != 0 {
		t.Fatalf("blank hand-off: delay = %v, want 0", d)
	}
}
