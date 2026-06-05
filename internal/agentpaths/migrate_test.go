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

func TestMigrateLegacyLayout_ForceOverwritesConflict(t *testing.T) {
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
			if len(result.Conflicts) != 0 {
				t.Fatalf("force migration should not leave conflicts: %#v", result.Conflicts)
			}
			assertMigrationFile(t, xdgConfig, "legacy\n")
			assertMigrationFile(t, legacyConfig, "legacy\n")
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
