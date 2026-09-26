package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// TestSettingsPrivacyOffNeverBlocksTheTUI: Disable may wait for an upload's
// state lock, so the Settings toggle hands it to a command instead of
// calling it on the TUI goroutine.
func TestSettingsPrivacyOffNeverBlocksTheTUI(t *testing.T) {
	h := telemetryDialogHarness(t)
	if err := telemetry.Grant(h.st, "9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.SaveState(h.st); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	prev := telemetryDisable
	telemetryDisable = func(v string, now time.Time) error {
		<-release // an upload holds the state lock
		return telemetry.Disable(v, now)
	}
	t.Cleanup(func() { telemetryDisable = prev })

	home := &Home{}
	returned := make(chan func() any, 1)
	go func() {
		cmd := home.togglePrivacyFromSettings()
		returned <- func() any { return cmd() }
	}()
	var run func() any
	select {
	case run = <-returned:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("turning telemetry off blocked the TUI goroutine")
	}
	close(release)
	msg, ok := run().(telemetryDisabledMsg)
	if !ok || msg.err != nil {
		t.Fatalf("msg = %#v", msg)
	}
	if on, _ := telemetry.Enabled(telemetry.LoadState()); on {
		t.Fatal("telemetry still on after the command ran")
	}
}
