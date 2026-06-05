package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackupUninstallDataLocations_IncludesXDGData is the data-safety
// regression test for Blocker 1 (2026-06-04 data-loss incident family).
//
// The old uninstall path tar'd ONLY legacy ~/.agent-deck before removing
// EVERY data location (XDG config + data + cache + legacy). An XDG-only
// install (no legacy dir) therefore got its real data deleted with NO
// backup. This test simulates an XDG-only install and asserts the backup
// archive CONTAINS the XDG data before any deletion happens.
func TestBackupUninstallDataLocations_IncludesXDGData(t *testing.T) {
	homeDir := t.TempDir()

	// Simulate an XDG-only install: data lives only in XDG dirs, NO legacy.
	xdgConfig := filepath.Join(homeDir, ".config", "agent-deck")
	xdgData := filepath.Join(homeDir, ".local", "share", "agent-deck")
	xdgCache := filepath.Join(homeDir, ".cache", "agent-deck")

	configFile := filepath.Join(xdgConfig, "config.toml")
	dataFile := filepath.Join(xdgData, "profiles", "default", "state.db")
	cacheFile := filepath.Join(xdgCache, "update", "latest.json")

	mustWrite(t, configFile, "config-payload")
	mustWrite(t, dataFile, "irreplaceable-session-state")
	mustWrite(t, cacheFile, "cache-payload")

	items := []uninstallFoundItem{
		{itemType: "config", path: xdgConfig, description: ""},
		{itemType: "data", path: xdgData, description: ""},
		{itemType: "cache", path: xdgCache, description: ""},
		// Note: NO legacy item — this is the XDG-only case.
	}

	backupFile, err := backupUninstallDataLocations(items, homeDir)
	if err != nil {
		t.Fatalf("backupUninstallDataLocations returned error: %v", err)
	}
	if backupFile == "" {
		t.Fatal("backupUninstallDataLocations returned empty backup path")
	}

	members := tarGzMembers(t, backupFile)

	// The critical XDG data file MUST be in the archive.
	if !tarContains(members, dataFile) {
		t.Fatalf("backup archive MUST contain XDG data file %q before deletion; archive members:\n%s",
			dataFile, strings.Join(members, "\n"))
	}
	if !tarContains(members, configFile) {
		t.Errorf("backup archive should contain XDG config file %q; members:\n%s",
			configFile, strings.Join(members, "\n"))
	}
	if !tarContains(members, cacheFile) {
		t.Errorf("backup archive should contain XDG cache file %q; members:\n%s",
			cacheFile, strings.Join(members, "\n"))
	}

	// Backup must exist on disk and be non-empty.
	if info, err := os.Stat(backupFile); err != nil || info.Size() == 0 {
		t.Fatalf("backup file missing or empty: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tarGzMembers(t *testing.T, archive string) []string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var members []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		members = append(members, hdr.Name)
	}
	return members
}

// tarContains reports whether any archive member path ends with the basename
// chain of want (tar stores relative paths, so we match on suffix).
func tarContains(members []string, want string) bool {
	// Match on the deepest unique components to be robust to the archive's
	// relative-path rooting (e.g. ".config/agent-deck/...").
	wantSuffix := want
	if idx := strings.Index(want, string(filepath.Separator)+".config"); idx >= 0 {
		wantSuffix = want[idx+1:]
	} else if idx := strings.Index(want, string(filepath.Separator)+".local"); idx >= 0 {
		wantSuffix = want[idx+1:]
	} else if idx := strings.Index(want, string(filepath.Separator)+".cache"); idx >= 0 {
		wantSuffix = want[idx+1:]
	}
	for _, m := range members {
		clean := filepath.Clean(m)
		if strings.HasSuffix(clean, filepath.Clean(wantSuffix)) || strings.HasSuffix(clean, filepath.Base(want)) {
			return true
		}
	}
	return false
}
