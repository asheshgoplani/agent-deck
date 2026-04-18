package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// CLI-level telegram topology tests (fix v1.7.22, Closes #658).
//
// These exercise the CLI glue: reading enabledPlugins from Claude Code's
// settings.json, feeding the validator, and formatting warnings on stderr.

func TestReadTelegramGloballyEnabled_TrueWhenSettingsHasIt(t *testing.T) {
	dir := t.TempDir()
	settings := `{
      "enabledPlugins": {
        "telegram@claude-plugins-official": true,
        "watcher@claude-plugins-official": true
      }
    }`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	got, err := readTelegramGloballyEnabled(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected true, got false")
	}
}

func TestReadTelegramGloballyEnabled_FalseWhenMissing(t *testing.T) {
	dir := t.TempDir()
	// no settings.json at all — must not error.
	got, err := readTelegramGloballyEnabled(dir)
	if err != nil {
		t.Fatalf("missing settings.json must not error, got: %v", err)
	}
	if got {
		t.Fatalf("missing settings.json must be treated as disabled")
	}
}

func TestReadTelegramGloballyEnabled_FalseWhenExplicitlyDisabled(t *testing.T) {
	dir := t.TempDir()
	settings := `{
      "enabledPlugins": {
        "telegram@claude-plugins-official": false
      }
    }`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	got, err := readTelegramGloballyEnabled(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatalf("expected false when explicitly disabled")
	}
}

func TestConductorSetup_WarnsOnGlobalTelegramEnabled(t *testing.T) {
	var buf bytes.Buffer
	in := session.TelegramValidatorInput{
		GlobalEnabled:   true,
		SessionChannels: nil,
		SessionWrapper:  "",
	}
	emitTelegramWarnings(&buf, in)

	out := buf.String()
	if !strings.Contains(out, "⚠") {
		t.Errorf("warning output should be prefixed with ⚠, got: %s", out)
	}
	if !strings.Contains(out, "GLOBAL_ANTIPATTERN") && !strings.Contains(strings.ToLower(out), "anti-pattern") {
		t.Errorf("output should surface the GLOBAL_ANTIPATTERN code or anti-pattern label, got: %s", out)
	}
	if !strings.Contains(out, "enabledPlugins") {
		t.Errorf("output should reference enabledPlugins so users can find the setting, got: %s", out)
	}
}

func TestConductorSetup_RecommendsEnvFileOverWrapper(t *testing.T) {
	var buf bytes.Buffer
	in := session.TelegramValidatorInput{
		GlobalEnabled: false,
		SessionChannels: []string{
			"plugin:telegram@claude-plugins-official",
		},
		SessionWrapper: "TELEGRAM_STATE_DIR=/home/me/.claude/channels/telegram {command}",
	}
	emitTelegramWarnings(&buf, in)

	out := buf.String()
	if !strings.Contains(out, "env_file") {
		t.Errorf("recommendation must reference env_file, got: %s", out)
	}
	if !strings.Contains(out, "WRAPPER_DEPRECATED") && !strings.Contains(strings.ToLower(out), "wrapper") {
		t.Errorf("output should surface wrapper-deprecated concern, got: %s", out)
	}
}

// Silent path: nothing to warn about → emitter writes nothing (clean logs).
func TestEmitTelegramWarnings_CleanConfig_Silent(t *testing.T) {
	var buf bytes.Buffer
	in := session.TelegramValidatorInput{
		GlobalEnabled:   false,
		SessionChannels: []string{"plugin:telegram@claude-plugins-official"},
		SessionWrapper:  "",
	}
	emitTelegramWarnings(&buf, in)
	if buf.Len() != 0 {
		t.Errorf("clean config must produce no output, got: %q", buf.String())
	}
}
