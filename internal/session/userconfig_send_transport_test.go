package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGetSendTransport_DefaultAndOverrides covers the in-memory normalization
// GetSendTransport performs, independent of how the struct got populated.
func TestGetSendTransport_DefaultAndOverrides(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"absent (zero value)", "", "auto"},
		{"explicit tmux", "tmux", "tmux"},
		{"wrong case TMUX normalizes to auto", "TMUX", "auto"},
		{"garbage normalizes to auto", "garbage", "auto"},
		{"explicit auto stays auto", "auto", "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &UserConfig{SendTransport: tc.raw}
			if got := c.GetSendTransport(); got != tc.want {
				t.Errorf("GetSendTransport() with raw %q = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestGetSendTransport_RoundTripsThroughWrittenConfig writes a real
// config.toml under a temp HOME and confirms LoadUserConfig picks up
// send_transport, matching the shape of
// claude_title_reconcile_test.go's TestReconcileTitleFromClaude_NoopWhenSyncDisabled
// (HOME/.agent-deck/config.toml, not XDG_CONFIG_HOME — the plan's own
// pointer describes the latter, but the precedent it names uses the former;
// following the actual precedent).
func TestGetSendTransport_RoundTripsThroughWrittenConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("send_transport = \"tmux\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// LoadUserConfig's cache key is the file's mtime alone, not its path
	// (internal/session/userconfig.go): two temp config.toml files from two
	// different tests can share an mtime, in which case a stale cache would
	// hand back a DIFFERENT test's config. Clear explicitly rather than
	// relying on mtimes to differ.
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got := cfg.GetSendTransport(); got != "tmux" {
		t.Errorf("GetSendTransport() after round-trip = %q, want %q", got, "tmux")
	}
}

// TestGetSendTransport_AbsentFromWrittenConfig_DefaultsAuto confirms a
// config.toml with no send_transport key at all resolves to "auto", the
// same default-on-absence guarantee existing installs get for group_sort.
func TestGetSendTransport_AbsentFromWrittenConfig_DefaultsAuto(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("group_sort = \"actionable\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// See the ClearUserConfigCache comment in
	// TestGetSendTransport_RoundTripsThroughWrittenConfig: the cache keys on
	// mtime alone, so a stale hit from a same-mtime temp file elsewhere would
	// silently return the wrong test's config.
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got := cfg.GetSendTransport(); got != "auto" {
		t.Errorf("GetSendTransport() with no send_transport key = %q, want %q", got, "auto")
	}
}
