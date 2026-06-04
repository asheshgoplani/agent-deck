package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupMigratePathsCommandHome(t *testing.T) (home, legacy string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	legacy = filepath.Join(home, ".agent-deck")
	return home, legacy
}

func TestMigratePathsCommand_DryRunReportsPlannedCopiesAndDoesNotCopy(t *testing.T) {
	home, legacy := setupMigratePathsCommandHome(t)
	if err := os.MkdirAll(filepath.Join(legacy, "profiles", "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), []byte("theme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "profiles", "default", "state.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runMigratePaths([]string{"--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runMigratePaths exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Migrating legacy ~/.agent-deck paths to XDG layout",
		"would copy config config.toml",
		"would copy data profiles",
		"legacy directory left untouched: " + legacy,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "agent-deck", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create config destination, stat err=%v", err)
	}
}

func TestMigratePathsCommand_ConflictReportsForceHint(t *testing.T) {
	home, legacy := setupMigratePathsCommandHome(t)
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

	var stdout, stderr bytes.Buffer
	code := runMigratePaths(nil, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected conflict exit\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "conflict config config.toml already exists at "+xdgConfig) {
		t.Fatalf("conflict output missing destination:\n%s", combined)
	}
	if !strings.Contains(combined, "rerun with --force to overwrite destination files") {
		t.Fatalf("conflict output missing force hint:\n%s", combined)
	}
}
