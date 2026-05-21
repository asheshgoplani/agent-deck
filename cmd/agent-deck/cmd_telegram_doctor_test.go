// Tests for `agent-deck telegram-doctor` (issue #1138). Three cases:
//
//	happy path  — settings.json has telegram=true, doctor reports OK
//	settings drift — settings.json has telegram=false or absent, reported as DRIFT
//	non-channel session — ignored entirely (no false alarms for non-conductors)
//
// `pollersForInstance` is exercised via `scanBunTelegramProcesses`'s
// pgrep dependency, which is not present in CI for all platforms.
// The unit tests below cover the pure-function path
// (VerifyTelegramChannelEnabled) which is the actual diagnostic logic.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// hasTelegramChannel is the local mirror; pin it.
func TestHasTelegramChannel(t *testing.T) {
	if !hasTelegramChannel([]string{"plugin:telegram@claude-plugins-official"}) {
		t.Errorf("official telegram channel must be detected")
	}
	if !hasTelegramChannel([]string{"plugin:telegram@acme/fork"}) {
		t.Errorf("forked telegram channel must be detected (prefix match)")
	}
	if hasTelegramChannel([]string{"plugin:discord@claude-plugins-official"}) {
		t.Errorf("non-telegram channel must not match")
	}
	if hasTelegramChannel(nil) {
		t.Errorf("empty channels must not match")
	}
}

// TestDoctor_VerifyHealthy mirrors how the doctor evaluates a healthy
// session: an effective config dir whose settings.json enables the
// channel plugin must yield OK=true.
func TestDoctor_VerifyHealthy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"enabledPlugins":{"telegram@claude-plugins-official":true}}`),
		0o600); err != nil {
		t.Fatal(err)
	}
	r := session.VerifyTelegramChannelEnabled(dir, []string{"plugin:telegram@claude-plugins-official"})
	if !r.OK {
		t.Fatalf("healthy session must report OK; got %+v", r)
	}
	if r.EffectiveValue != "true" {
		t.Errorf("EffectiveValue: got %q, want %q", r.EffectiveValue, "true")
	}
}

// TestDoctor_VerifyDriftFalse — telegram=false is the drop variant
// where claude can't open the MCP transport. Doctor must report DRIFT.
func TestDoctor_VerifyDriftFalse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"enabledPlugins":{"telegram@claude-plugins-official":false}}`),
		0o600); err != nil {
		t.Fatal(err)
	}
	r := session.VerifyTelegramChannelEnabled(dir, []string{"plugin:telegram@claude-plugins-official"})
	if r.OK {
		t.Fatalf("drift session (telegram=false) must report not-OK")
	}
	if r.EffectiveValue != "false" {
		t.Errorf("EffectiveValue: got %q, want %q", r.EffectiveValue, "false")
	}
}

// TestDoctor_VerifyDriftAbsent — the other observed variant: entry
// completely missing from enabledPlugins.
func TestDoctor_VerifyDriftAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"enabledPlugins":{"superpowers@claude-plugins-official":true}}`),
		0o600); err != nil {
		t.Fatal(err)
	}
	r := session.VerifyTelegramChannelEnabled(dir, []string{"plugin:telegram@claude-plugins-official"})
	if r.OK {
		t.Fatalf("drift session (telegram absent) must report not-OK")
	}
	if r.EffectiveValue != "absent" {
		t.Errorf("EffectiveValue: got %q, want %q", r.EffectiveValue, "absent")
	}
}

// TestDoctor_NonChannelSession_Ignored — sessions without a telegram
// channel must yield OK=true so the doctor never false-alarms on plain
// workers.
func TestDoctor_NonChannelSession_Ignored(t *testing.T) {
	r := session.VerifyTelegramChannelEnabled(t.TempDir(), nil)
	if !r.OK {
		t.Errorf("non-channel session must be vacuously OK; got %+v", r)
	}
}

// TestDoctor_MissingSettingsFile — a config dir whose settings.json
// doesn't exist for a channel-owning session is reported as drift.
func TestDoctor_MissingSettingsFile(t *testing.T) {
	r := session.VerifyTelegramChannelEnabled(t.TempDir(),
		[]string{"plugin:telegram@claude-plugins-official"})
	if r.OK {
		t.Errorf("missing settings.json for channel owner must be DRIFT; got %+v", r)
	}
	if r.EffectiveValue != "missing-file" {
		t.Errorf("EffectiveValue: got %q, want missing-file", r.EffectiveValue)
	}
}
