package main

// Issue #666 — `agent-deck conductor setup` must auto-remediate the
// enabledPlugins."telegram@claude-plugins-official" = true anti-pattern,
// not merely warn. The v1.7.22 validator warned at setup time; users
// missed the warning in scrollback and the flag kept silently crashing
// generic child claude sessions that auto-loaded the plugin and raced
// the conductor's poller (409 Conflict → claude exits → session flips
// to error).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDisableTelegramGlobally_FixesTrueToFalse writes a settings.json that
// has the anti-pattern set, runs the remediator, and asserts:
//   - file still exists
//   - telegram plugin key is now false (not removed, not still true)
//   - other unrelated keys survive unchanged
//   - the function reports it mutated the file (changed=true)
func TestDisableTelegramGlobally_FixesTrueToFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	initial := `{
  "enabledPlugins": {
    "telegram@claude-plugins-official": true,
    "watcher@claude-plugins-official": true
  },
  "theme": "dark",
  "customKey": 42
}`
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	changed, err := disableTelegramGlobally(dir)
	if err != nil {
		t.Fatalf("disableTelegramGlobally: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true for a file with enabledPlugins.telegram=true")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse remediated file: %v (raw: %s)", err, raw)
	}

	enabled, _ := parsed["enabledPlugins"].(map[string]any)
	if enabled == nil {
		t.Fatalf("enabledPlugins block missing after remediation: %s", raw)
	}
	tele, exists := enabled["telegram@claude-plugins-official"]
	if !exists {
		t.Fatalf("telegram key must be kept (as false) for idempotence tracking, got removed: %s", raw)
	}
	if teleBool, ok := tele.(bool); !ok || teleBool {
		t.Fatalf("telegram key must be false after remediation, got %v: %s", tele, raw)
	}

	// Unrelated keys must survive.
	if _, ok := enabled["watcher@claude-plugins-official"]; !ok {
		t.Fatalf("watcher plugin key was clobbered by remediation: %s", raw)
	}
	if theme, _ := parsed["theme"].(string); theme != "dark" {
		t.Fatalf("top-level theme key was clobbered: got %v, want \"dark\"", parsed["theme"])
	}
	if v, _ := parsed["customKey"].(float64); v != 42 {
		t.Fatalf("top-level customKey was clobbered: got %v, want 42", parsed["customKey"])
	}
}

// Idempotence: running twice on an already-false file is a no-op and
// reports changed=false. This is what `conductor setup` relies on — it
// may be run many times over a profile's lifetime.
func TestDisableTelegramGlobally_NoOpWhenAlreadyFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	initial := `{
  "enabledPlugins": {
    "telegram@claude-plugins-official": false
  }
}`
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	origStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	changed, err := disableTelegramGlobally(dir)
	if err != nil {
		t.Fatalf("disableTelegramGlobally: %v", err)
	}
	if changed {
		t.Fatalf("expected changed=false when already disabled (idempotence)")
	}

	// File must be untouched (mtime preserved) — important for avoiding
	// spurious watch-triggered reloads in Claude Code.
	newStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("re-stat: %v", err)
	}
	if !newStat.ModTime().Equal(origStat.ModTime()) {
		t.Fatalf("no-op remediation rewrote the file (mtime %v → %v)",
			origStat.ModTime(), newStat.ModTime())
	}
}

// Missing file is a safe baseline — the anti-pattern cannot exist in a
// file that isn't there. Remediator must not create the file.
func TestDisableTelegramGlobally_NoOpWhenMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	changed, err := disableTelegramGlobally(dir)
	if err != nil {
		t.Fatalf("missing settings.json must not error, got: %v", err)
	}
	if changed {
		t.Fatalf("missing settings.json must not report changed=true")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("remediator must not create settings.json when absent; stat err=%v", err)
	}
}

// Key absent (enabledPlugins block exists but telegram key missing) is a
// no-op — the anti-pattern is "explicitly true", not "default".
func TestDisableTelegramGlobally_NoOpWhenKeyAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	initial := `{
  "enabledPlugins": {
    "watcher@claude-plugins-official": true
  }
}`
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	changed, err := disableTelegramGlobally(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Fatalf("changed=false expected when telegram key is absent, got true")
	}
}
