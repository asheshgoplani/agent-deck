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

// TestCopyMigrationFile_NeverTruncatesExistingDestination is the unit-level
// Blocker 1 (TOCTOU) regression: the copy primitive must NEVER truncate an
// existing destination. It opens with O_EXCL, so a pre-existing destination
// (e.g. one created concurrently by a running Agent Deck after the caller's
// existence check) is left byte-for-byte intact and surfaces a conflict, not a
// clobbered/empty file.
func TestCopyMigrationFile_NeverTruncatesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	if err := os.WriteFile(source, []byte("legacy-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Destination already present (simulating a concurrent create that won the
	// race after the higher-level existence check passed).
	if err := os.WriteFile(dest, []byte("concurrently-written-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := copyMigrationFile(source, dest, 0o600)
	if !errors.Is(err, errDestinationExists) {
		t.Fatalf("copyMigrationFile over existing dest = %v, want errDestinationExists", err)
	}
	// The pre-existing destination must be intact, NOT truncated.
	if data, readErr := os.ReadFile(dest); readErr != nil {
		t.Fatalf("read dest: %v", readErr)
	} else if string(data) != "concurrently-written-data" {
		t.Fatalf("destination was clobbered: got %q, want %q", string(data), "concurrently-written-data")
	}
}

// TestMergeMigrationPath_ConcurrentDestinationFileIsPreserved exercises the
// Blocker 1 TOCTOU window at the merge layer: a destination file that does NOT
// exist when mergeMigrationPath performs its Lstat fast-path, but DOES exist by
// the time the copy runs (created concurrently by a running Agent Deck), must be
// preserved and reported as a conflict — never truncated. We reproduce the race
// deterministically by pre-seeding the destination and verifying that the copy
// (which goes through the O_EXCL primitive) refuses to overwrite it.
func TestMergeMigrationPath_ConcurrentDestinationFileIsPreserved(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy", "file")
	dest := filepath.Join(dir, "xdg", "file")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("live-agent-deck-write"), 0o600); err != nil {
		t.Fatal(err)
	}

	conflicted, err := mergeMigrationPath(source, dest)
	if err != nil {
		t.Fatalf("mergeMigrationPath returned error: %v", err)
	}
	if !conflicted {
		t.Fatalf("expected concurrent destination to be reported as a conflict")
	}
	if data, readErr := os.ReadFile(dest); readErr != nil {
		t.Fatalf("read dest: %v", readErr)
	} else if string(data) != "live-agent-deck-write" {
		t.Fatalf("concurrent destination clobbered: got %q, want %q", string(data), "live-agent-deck-write")
	}
}

// TestCopyMigrationFile_AtomicLeavesNoTempArtifacts verifies the atomic
// copy-via-temp path: after a successful copy the destination has the expected
// contents/mode and NO stray temp file is left behind in the destination dir.
func TestCopyMigrationFile_AtomicLeavesNoTempArtifacts(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "sub", "dest")
	if err := os.WriteFile(source, []byte("legacy-content"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := copyMigrationFile(source, dest, 0o640); err != nil {
		t.Fatalf("copyMigrationFile: %v", err)
	}
	assertMigrationFile(t, dest, "legacy-content")

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("dest mode = %o, want 0640", info.Mode().Perm())
	}

	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatalf("read dest dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "dest" {
			t.Fatalf("stray artifact left in destination dir: %q", e.Name())
		}
	}
}

// TestCopyMigrationFile_FailedCopyLeavesNoPartialDestination is the core
// data-safety guard for the atomic copy: if the copy fails mid-stream, no
// partial file may exist at the FINAL destination name (which a later run would
// otherwise mistake for an existing XDG conflict), and no temp artifact may be
// left behind. We force a copy failure by making the source a directory passed
// straight to copyMigrationFile (io.Copy from a dir fd errors on read).
func TestCopyMigrationFile_FailedCopyLeavesNoPartialDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "srcdir")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "sub", "dest")

	err := copyMigrationFile(source, dest, 0o600)
	if err == nil {
		t.Fatal("expected copyMigrationFile to fail when source is a directory")
	}
	if errors.Is(err, errDestinationExists) {
		t.Fatalf("unexpected conflict sentinel: %v", err)
	}

	// The final destination name must NOT exist — no partial file.
	if _, statErr := os.Lstat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("partial destination left behind: stat err = %v", statErr)
	}
	// No temp artifact may remain in the destination directory.
	if entries, readErr := os.ReadDir(filepath.Dir(dest)); readErr == nil {
		for _, e := range entries {
			t.Fatalf("stray temp artifact left behind: %q", e.Name())
		}
	}
}
