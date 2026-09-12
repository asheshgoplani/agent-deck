package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// stubHeadlessSuppression swaps the headless predicate for one test.
func stubHeadlessSuppression(t *testing.T, reason string) {
	t.Helper()
	prev := headlessAutoUpdateSuppressed
	headlessAutoUpdateSuppressed = func() string { return reason }
	t.Cleanup(func() { headlessAutoUpdateSuppressed = prev })
}

// TestHeadlessAutoRestart_HonoursSkipMarkers pins issue #2251 for the
// headless paths (`web --no-tui`, remote-agent): every process-wide
// reason (go test, skip env, CI, test marker) disables the idle-point
// restart, an unsuppressed process keeps it, and the config opt-out still
// applies on top. The remote agent's watcher is built through the same
// gate, so it is nil when suppressed.
func TestHeadlessAutoRestart_HonoursSkipMarkers(t *testing.T) {
	// The package TestMain already isolates HOME; nothing here touches
	// config, GetUpdateSettings just sees the defaults.
	for _, reason := range []string{
		"running under go test",
		update.SkipUpdateCheckEnv + " set",
		"CI=true",
		"AGENTDECK_TEST_ARGV_LOG set",
	} {
		t.Run(reason, func(t *testing.T) {
			stubHeadlessSuppression(t, reason)
			if headlessAutoRestartEnabled() {
				t.Fatalf("%s: headless auto restart must be off", reason)
			}
			if w := newRemoteAgentBinaryWatch("/bin/agent-deck"); w != nil {
				t.Fatalf("%s: remote agent must not build a binary watch", reason)
			}
		})
	}

	stubHeadlessSuppression(t, "")
	if !headlessAutoRestartEnabled() {
		t.Fatal("an unsuppressed daemon with default config must keep its idle-point restart")
	}
	if w := newRemoteAgentBinaryWatch("/bin/agent-deck"); w == nil || w.Exe != "/bin/agent-deck" {
		t.Fatalf("remote agent watcher = %+v, want one for the binary", w)
	}

	// The real predicate is what production uses: under go test it must
	// refuse without any environment help.
	headlessAutoUpdateSuppressed = update.AutoUpdateSuppressed
	if headlessAutoRestartEnabled() {
		t.Fatal("the real predicate must suppress under go test")
	}
}
