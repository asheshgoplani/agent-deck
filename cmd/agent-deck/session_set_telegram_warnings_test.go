package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression-locks v1.7.22 / #658: the SetField extraction nearly dropped
// the inline `if field == "wrapper"|"channels"` block on the CLI side, and
// validator-level tests in conductor_cmd_telegram_test.go didn't catch it
// because they assert on emitTelegramWarnings directly, not on the gate.
// These tests pin maybeEmitSessionSetTelegramWarnings.

func withGlobalTelegramSettings(t *testing.T, enabled bool) string {
	t.Helper()
	dir := t.TempDir()
	settingsBody := `{
  "enabledPlugins": {
    "telegram@claude-plugins-official": true
  }
}`
	if !enabled {
		settingsBody = `{
  "enabledPlugins": {
    "telegram@claude-plugins-official": false
  }
}`
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settingsBody), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	return dir
}

func TestMaybeEmitTelegramWarnings_ChannelsField(t *testing.T) {
	cfgDir := withGlobalTelegramSettings(t, true)

	inst := &session.Instance{
		Tool:      "claude",
		GroupPath: "my-sessions",
		Channels:  []string{"plugin:telegram@claude-plugins-official"},
	}
	var buf bytes.Buffer
	maybeEmitSessionSetTelegramWarnings(&buf, cfgDir, inst, "channels")

	out := buf.String()
	if out == "" {
		t.Fatal("expected warning on channels edit with global telegram enabled, got nothing")
	}
	if !strings.Contains(out, "⚠") {
		t.Errorf("warning should be prefixed with ⚠ marker, got: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "telegram") {
		t.Errorf("warning should mention telegram, got: %q", out)
	}
}

func TestMaybeEmitTelegramWarnings_WrapperField(t *testing.T) {
	cfgDir := withGlobalTelegramSettings(t, false)

	inst := &session.Instance{
		Tool:      "claude",
		GroupPath: "my-sessions",
		Channels:  []string{"plugin:telegram@claude-plugins-official"},
		Wrapper:   "TELEGRAM_STATE_DIR=/home/me/state {command}",
	}
	var buf bytes.Buffer
	maybeEmitSessionSetTelegramWarnings(&buf, cfgDir, inst, "wrapper")

	out := buf.String()
	if out == "" {
		t.Fatal("expected wrapper-deprecated warning when wrapper carries env vars and channel is subscribed, got nothing")
	}
	if !strings.Contains(strings.ToLower(out), "wrapper") &&
		!strings.Contains(out, "env_file") {
		t.Errorf("warning should reference wrapper-deprecated guidance or env_file recommendation, got: %q", out)
	}
}

// Widening the gate to all fields would spam users on every rename.
func TestMaybeEmitTelegramWarnings_NonGatedFieldStaysSilent(t *testing.T) {
	cfgDir := withGlobalTelegramSettings(t, true)

	inst := &session.Instance{
		Tool:      "claude",
		GroupPath: "my-sessions",
		Channels:  []string{"plugin:telegram@claude-plugins-official"},
	}
	for _, field := range []string{"title", "color", "notes", "command", "tool", "extra-args"} {
		t.Run(field, func(t *testing.T) {
			var buf bytes.Buffer
			maybeEmitSessionSetTelegramWarnings(&buf, cfgDir, inst, field)
			if buf.Len() != 0 {
				t.Errorf("field %q must not emit any output; got: %q", field, buf.String())
			}
		})
	}
}

// Clean config on a gated field must produce nothing — guards against a
// validator default flip that would emit a noise warning.
func TestMaybeEmitTelegramWarnings_CleanConfigSilent(t *testing.T) {
	cfgDir := withGlobalTelegramSettings(t, false)

	inst := &session.Instance{
		Tool:      "claude",
		GroupPath: "my-sessions",
		Channels:  []string{"plugin:telegram@claude-plugins-official"},
		// Wrapper empty — no env-var leakage.
	}
	var buf bytes.Buffer
	maybeEmitSessionSetTelegramWarnings(&buf, cfgDir, inst, "channels")
	if buf.Len() != 0 {
		t.Errorf("clean config on channels field must stay silent, got: %q", buf.String())
	}
}
