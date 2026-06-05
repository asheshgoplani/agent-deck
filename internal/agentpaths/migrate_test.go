package agentpaths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func setupMigrationHome(t *testing.T) (home, legacy string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	legacy = filepath.Join(home, ".agent-deck")
	return home, legacy
}

func assertMigrationFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, string(data), want)
	}
}

func TestMigrateLegacyLayout_CopiesSplitCategories(t *testing.T) {
	home, legacy := setupMigrationHome(t)
	if err := os.MkdirAll(filepath.Join(legacy, "profiles", "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), []byte("theme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "profiles", "default", "state.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "update-cache.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := MigrateLegacyLayout(MigrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) == 0 {
		t.Fatal("expected copied items")
	}

	assertMigrationFile(t, filepath.Join(home, ".config", "agent-deck", "config.toml"), "theme = \"dark\"\n")
	assertMigrationFile(t, filepath.Join(home, ".local", "share", "agent-deck", "profiles", "default", "state.db"), "db")
	assertMigrationFile(t, filepath.Join(home, ".cache", "agent-deck", "update-cache.json"), "{}")
	assertMigrationFile(t, filepath.Join(legacy, "config.toml"), "theme = \"dark\"\n")
}

func TestMigrateLegacyLayout_ConflictRequiresForceAndLeavesFilesUntouched(t *testing.T) {
	home, legacy := setupMigrationHome(t)
	legacyConfig := filepath.Join(legacy, "config.toml")
	xdgConfig := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(legacyConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(xdgConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyConfig, []byte("legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdgConfig, []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := MigrateLegacyLayout(MigrationOptions{})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !errors.Is(err, ErrMigrationConflict) {
		t.Fatalf("error = %v, want ErrMigrationConflict", err)
	}
	if result == nil || len(result.Conflicts) != 1 {
		t.Fatalf("expected one conflict in result, got %#v", result)
	}
	assertMigrationFile(t, legacyConfig, "legacy\n")
	assertMigrationFile(t, xdgConfig, "existing\n")
}

// TestMigrateLegacyLayout_ForcePreservesExistingLeafOnConflict verifies the
// hardened (data-safe) force semantics: a force migration MERGES legacy into
// the destination but PRESERVES an existing (newer) XDG leaf on a per-file
// conflict, reporting the conflict instead of clobbering it.
//
// This supersedes the old "force overwrites conflict" behavior, which was the
// root of the 2026-06-04 data-loss incident family (Blocker 2).
func TestMigrateLegacyLayout_ForcePreservesExistingLeafOnConflict(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, path string)
	}{
		{
			name: "file",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("existing\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			seed: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "existing-target")
				if err := os.WriteFile(target, []byte("existing\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, legacy := setupMigrationHome(t)
			legacyConfig := filepath.Join(legacy, "config.toml")
			xdgConfig := filepath.Join(home, ".config", "agent-deck", "config.toml")
			if err := os.MkdirAll(filepath.Dir(legacyConfig), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(xdgConfig), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacyConfig, []byte("legacy\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.seed(t, xdgConfig)

			result, err := MigrateLegacyLayout(MigrationOptions{Force: true})
			if err != nil {
				t.Fatal(err)
			}
			// The existing XDG leaf must be reported as a conflict and PRESERVED.
			if len(result.Conflicts) == 0 {
				t.Fatalf("force migration over an existing leaf should report a conflict, got none")
			}
			// Legacy source is never mutated.
			assertMigrationFile(t, legacyConfig, "legacy\n")
			// Existing XDG leaf is preserved (not overwritten with legacy).
			info, err := os.Lstat(xdgConfig)
			if err != nil {
				t.Fatalf("xdg leaf should still exist: %v", err)
			}
			switch tc.name {
			case "file":
				assertMigrationFile(t, xdgConfig, "existing\n")
			case "directory":
				if !info.IsDir() {
					t.Fatalf("xdg directory should be preserved, got mode %v", info.Mode())
				}
			case "symlink":
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("xdg symlink should be preserved, got mode %v", info.Mode())
				}
			}
		})
	}
}

func TestMigrateLegacyLayout_DryRunDoesNotCopy(t *testing.T) {
	home, legacy := setupMigrationHome(t)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), []byte("legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := MigrateLegacyLayout(MigrationOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun {
		t.Fatal("result should record dry-run mode")
	}
	if len(result.Copied) != 1 {
		t.Fatalf("dry-run should plan one copy, got %#v", result.Copied)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "agent-deck", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create config destination, stat err=%v", err)
	}
}

// TestMigrateLegacyLayout_ForcePreservesNewerXDGOnlyData is the data-safety
// regression test for Blocker 2 (2026-06-04 data-loss incident family).
//
// With --force, the old removeExistingDestination did os.RemoveAll on the
// whole category DIRECTORY (e.g. profiles/) before copying legacy in. That
// destroyed newer XDG-only data that had no legacy counterpart. The hardened
// migration must MERGE per-file: copy legacy files in without deleting the
// whole destination tree, so XDG-only files survive.
func TestMigrateLegacyLayout_ForcePreservesNewerXDGOnlyData(t *testing.T) {
	home, legacy := setupMigrationHome(t)

	// Legacy has an OLD profile directory with a config profile.
	legacyOld := filepath.Join(legacy, "profiles", "old", "state.db")
	if err := os.MkdirAll(filepath.Dir(legacyOld), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyOld, []byte("legacy-old-db"), 0o600); err != nil {
		t.Fatal(err)
	}

	// XDG data dir already has a NEWER profile that exists ONLY in XDG.
	xdgProfiles := filepath.Join(home, ".local", "share", "agent-deck", "profiles")
	xdgOnly := filepath.Join(xdgProfiles, "newer", "state.db")
	if err := os.MkdirAll(filepath.Dir(xdgOnly), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdgOnly, []byte("irreplaceable-xdg-only"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := MigrateLegacyLayout(MigrationOptions{Force: true})
	if err != nil {
		t.Fatalf("force migrate returned error: %v", err)
	}
	_ = result

	// CRITICAL: the XDG-only profile MUST survive the forced migration.
	if data, err := os.ReadFile(xdgOnly); err != nil {
		t.Fatalf("XDG-only data was DELETED by force migration (data loss!): %v", err)
	} else if string(data) != "irreplaceable-xdg-only" {
		t.Fatalf("XDG-only data corrupted: got %q", string(data))
	}

	// And the legacy profile should have been merged in alongside it.
	mergedOld := filepath.Join(xdgProfiles, "old", "state.db")
	if data, err := os.ReadFile(mergedOld); err != nil {
		t.Fatalf("legacy profile was not merged into XDG: %v", err)
	} else if string(data) != "legacy-old-db" {
		t.Fatalf("merged legacy profile wrong content: %q", string(data))
	}
}

// TestMigrateLegacyLayout_ForcePrefersNewerXDGOnPerFileConflict asserts that on
// a per-file conflict inside a merged directory, force does NOT clobber the
// existing (newer) XDG file — it is reported as a conflict and left intact.
func TestMigrateLegacyLayout_ForcePrefersNewerXDGOnPerFileConflict(t *testing.T) {
	home, legacy := setupMigrationHome(t)

	legacyFile := filepath.Join(legacy, "profiles", "default", "state.db")
	if err := os.MkdirAll(filepath.Dir(legacyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyFile, []byte("legacy-db"), 0o600); err != nil {
		t.Fatal(err)
	}

	xdgFile := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "default", "state.db")
	if err := os.MkdirAll(filepath.Dir(xdgFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdgFile, []byte("newer-xdg-db"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := MigrateLegacyLayout(MigrationOptions{Force: true})
	if err != nil {
		t.Fatalf("force migrate returned error: %v", err)
	}

	// The existing newer XDG file must be preserved, not overwritten by legacy.
	if data, err := os.ReadFile(xdgFile); err != nil {
		t.Fatalf("read xdg file: %v", err)
	} else if string(data) != "newer-xdg-db" {
		t.Fatalf("per-file conflict clobbered newer XDG data: got %q, want %q", string(data), "newer-xdg-db")
	}

	// The skipped/conflicting file should be reported.
	if len(result.Conflicts) == 0 {
		t.Fatalf("expected per-file conflict to be reported, got none")
	}
}
