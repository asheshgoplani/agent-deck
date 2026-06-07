package main

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// TestUninstallDataCandidates_IncludesXDGDirs is the regression test for the
// Codex round-2 P2 finding: the not-installed dry-run "Checked locations"
// preview must be built from the SAME data-location source as a real uninstall,
// so it includes XDG config/data/cache (not just the legacy dir + ~/.tmux.conf)
// for an XDG-only install.
//
// uninstallDataCandidates() resolves candidates regardless of on-disk
// existence, which is exactly what the not-installed branch needs (the dirs do
// not exist when nothing is installed).
func TestUninstallDataCandidates_IncludesXDGDirs(t *testing.T) {
	root := t.TempDir()
	xdgConfig := filepath.Join(root, "config")
	xdgData := filepath.Join(root, "data")
	xdgCache := filepath.Join(root, "cache")

	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("XDG_CACHE_HOME", xdgCache)

	wantConfig, err := agentpaths.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	wantData, err := agentpaths.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	wantCache, err := agentpaths.CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	wantLegacy, err := agentpaths.LegacyDir()
	if err != nil {
		t.Fatalf("LegacyDir: %v", err)
	}

	candidates := uninstallDataCandidates()

	byType := make(map[string]string, len(candidates))
	for _, c := range candidates {
		byType[c.itemType] = filepath.Clean(c.path)
	}

	cases := []struct {
		itemType string
		want     string
	}{
		{"config", filepath.Clean(wantConfig)},
		{"data", filepath.Clean(wantData)},
		{"cache", filepath.Clean(wantCache)},
		{"legacy", filepath.Clean(wantLegacy)},
	}
	for _, tc := range cases {
		got, ok := byType[tc.itemType]
		if !ok {
			t.Errorf("uninstallDataCandidates() missing %q candidate; a real uninstall would remove it but the preview would omit it", tc.itemType)
			continue
		}
		if got != tc.want {
			t.Errorf("%q candidate path = %q, want %q", tc.itemType, got, tc.want)
		}
	}

	// The XDG dirs must NOT collapse onto the legacy dir — that was the whole
	// point of the fix (XDG-only installs were invisible in the preview).
	if byType["config"] == byType["legacy"] {
		t.Errorf("config candidate (%q) unexpectedly equals legacy candidate (%q)", byType["config"], byType["legacy"])
	}
}
