package session

import (
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// Regression tests for issue #1361: the [conductor].enabled config flag is
// removed and the conductor system's active state is derived from filesystem
// presence (a conductor dir with a valid meta.json) instead.

// TestConductorSystemActive_DerivesFromPresence verifies that a conductor with
// no `enabled` flag anywhere is correctly reported active once it exists on
// disk, and inactive when none exist.
func TestConductorSystemActive_DerivesFromPresence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))

	// No conductors yet -> system is not active. There is no `enabled = true`
	// flag anywhere; presence alone is the source of truth.
	if ConductorSystemActive() {
		t.Fatalf("expected ConductorSystemActive()=false with zero conductors on disk")
	}

	// Create a conductor on disk without ever touching any enabled flag.
	if err := SaveConductorMeta(&ConductorMeta{
		Name:             "alpha",
		Profile:          "default",
		HeartbeatEnabled: true,
		CreatedAt:        "2026-06-14T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveConductorMeta: %v", err)
	}

	if !ConductorSystemActive() {
		t.Fatalf("expected ConductorSystemActive()=true once a conductor exists on disk (no enabled flag required)")
	}
}

// TestConductorSettings_LegacyEnabledFlagStillLoads verifies graceful
// migration: a pre-#1361 config that still carries `enabled = true` (or
// `enabled = false`) decodes without error. The flag is simply ignored.
func TestConductorSettings_LegacyEnabledFlagStillLoads(t *testing.T) {
	for _, legacy := range []string{
		"[conductor]\nenabled = true\nheartbeat_interval = 15\n",
		"[conductor]\nenabled = false\nheartbeat_interval = 15\n",
	} {
		var cfg struct {
			Conductor ConductorSettings `toml:"conductor"`
		}
		if _, err := toml.Decode(legacy, &cfg); err != nil {
			t.Fatalf("legacy config with enabled flag must still decode, got error: %v\nconfig:\n%s", err, legacy)
		}
		if cfg.Conductor.HeartbeatInterval == nil || *cfg.Conductor.HeartbeatInterval != 15 {
			t.Fatalf("expected heartbeat_interval=15 to survive decode of legacy config:\n%s", legacy)
		}
	}
}

// TestConductorSystemActive_LegacyEnabledFalseIgnored is the key safety case:
// a config that still says `enabled = false` must NOT disable a conductor that
// actually exists on disk. The flag is dead; presence wins.
func TestConductorSystemActive_LegacyEnabledFalseIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))

	if err := SaveConductorMeta(&ConductorMeta{
		Name:      "legacy",
		Profile:   "default",
		CreatedAt: "2026-06-14T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveConductorMeta: %v", err)
	}

	// Even with a stale enabled=false flag decoded into settings, the derived
	// active-state ignores it entirely.
	var cfg struct {
		Conductor ConductorSettings `toml:"conductor"`
	}
	if _, err := toml.Decode("[conductor]\nenabled = false\n", &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ConductorSystemActive() {
		t.Fatalf("a live conductor on disk must be active regardless of a stale enabled=false flag")
	}
}
