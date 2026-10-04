package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

func TestTelemetryInstallTickSettings(t *testing.T) {
	telemetryDialogHarness(t)
	p, err := telemetry.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(p), "telemetry-tick.json"), []byte(`{"day":"2026-10-04","tick_id":"01234567-89ab-cdef-0123-456789abcdef","v":"1.16.26","sent":true,"last_sent_day":"2026-10-04"}`), 0600); err != nil {
		t.Fatal(err)
	}
	panel := NewSettingsPanel()
	panel.Show()
	label := panel.privacyTickLabel
	if !strings.Contains(label, "install.tick: sent; last sent 2026-10-04") {
		t.Fatalf("no Settings parity: %s", label)
	}
}

func TestTelemetryInstallTickBackgroundCommand(t *testing.T) {
	telemetryDialogHarness(t)
	telemetry.SetConfigDisabled(true)
	t.Cleanup(func() { telemetry.SetConfigDisabled(false) })
	msg, ok := telemetryUploadCmd()().(telemetryUploadMsg)
	if !ok {
		t.Fatal("no completion")
	}
	if msg.tickResult.Reason == "" || msg.tickResult.Attempted {
		t.Fatalf("tick not called with opt-out: %+v", msg.tickResult)
	}
}

func TestTelemetryInstallTickSettingsFrames(t *testing.T) {
	telemetryDialogHarness(t)
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
		panel := NewSettingsPanel()
		panel.SetSize(size[0], size[1])
		panel.Show()
		panel.privacyLabel = "on (full)"
		panel.privacyTickLabel = "install.tick: sent; last sent 2026-10-04"
		panel.cursor = int(SettingPrivacy)
		frame := panel.View()
		assertDialogFits(t, frame, size[0], size[1], "install.tick", "2026-10-04", "Esc Close")
		t.Logf("Settings frame %dx%d:\n%s", size[0], size[1], frame)
	}
}
