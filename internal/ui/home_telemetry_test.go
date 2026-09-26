package ui

import (
	"os"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	tea "github.com/charmbracelet/bubbletea"
)

// TestSettingsPrivacyOffNeverBlocksTheTUI: Disable may wait for an upload's
// state lock, so the Settings toggle hands it to a command instead of
// calling it on the TUI goroutine.
func TestSettingsPrivacyOffNeverBlocksTheTUI(t *testing.T) {
	h := telemetryDialogHarness(t)
	os.Unsetenv(telemetry.EnvTelemetry) // set-but-empty is a hard off
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
	returned := make(chan tea.Cmd, 1)
	go func() { returned <- home.togglePrivacyFromSettings() }()
	var cmd tea.Cmd
	select {
	case cmd = <-returned:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("turning telemetry off blocked the TUI goroutine")
	}
	close(release)
	if cmd == nil {
		t.Fatal("no command: telemetry was not on")
	}
	msg, ok := cmd().(telemetryDisabledMsg)
	if !ok || msg.err != nil {
		t.Fatalf("msg = %#v", msg)
	}
	if on, _ := telemetry.Enabled(telemetry.LoadState()); on {
		t.Fatal("telemetry still on after the command ran")
	}
}
