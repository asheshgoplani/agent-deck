package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConductorHome creates a conductor home dir under base populated with the
// given files (relative names → contents).
func writeConductorHome(t *testing.T, base, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	for fname, content := range files {
		p := filepath.Join(dir, fname)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", p, err)
		}
	}
	return dir
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("file %q = %q, want it to contain %q", path, string(data), want)
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to not exist, stat err = %v", path, err)
	}
}

func TestMigrateConductorDir_MovesHomesAndPreservesUserState(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	meta := `{"name":"alpha","agent":"claude","profile":"default","heartbeat_enabled":true,` +
		`"description":"keep me","created_at":"2020-01-01T00:00:00Z","env":{"K":"V"},` +
		`"env_file":"my.env","heartbeat_idle_minutes":9}`
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json":    meta,
		"CLAUDE.md":    "edited claude",
		"LEARNINGS.md": "my learnings",
		"state.json":   `{"x":1}`,
		"heartbeat.sh": "OLD_ROOT=/old/path/conductor",
	})
	// A base-level user-state file must move too.
	if err := os.WriteFile(filepath.Join(defaultBase, "LEARNINGS.md"), []byte("shared learnings"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "vault-conductors")

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	if !res.ConfigWritten {
		t.Fatal("expected ConfigWritten=true")
	}

	// Source home gone; target home present with user-state preserved.
	assertNotExist(t, filepath.Join(defaultBase, "alpha"))
	td := filepath.Join(target, "alpha")
	assertFileContains(t, filepath.Join(td, "LEARNINGS.md"), "my learnings")
	assertFileContains(t, filepath.Join(td, "state.json"), `"x":1`)
	assertFileContains(t, filepath.Join(target, "LEARNINGS.md"), "shared learnings")

	// meta.json preserved verbatim (no field clobbered by the move).
	m, err := LoadConductorMeta("alpha")
	if err != nil {
		t.Fatalf("LoadConductorMeta: %v", err)
	}
	if m.Description != "keep me" {
		t.Fatalf("Description = %q, want %q", m.Description, "keep me")
	}
	if m.CreatedAt != "2020-01-01T00:00:00Z" {
		t.Fatalf("CreatedAt = %q, want preserved", m.CreatedAt)
	}
	if m.Env["K"] != "V" || m.EnvFile != "my.env" || m.HeartbeatIdleMinutes != 9 {
		t.Fatalf("meta user-state lost: %+v", m)
	}

	// heartbeat.sh re-rendered with the NEW conductor root.
	assertFileContains(t, filepath.Join(td, "heartbeat.sh"), target)

	// Resolver now points at target.
	cd, err := ConductorDir()
	if err != nil {
		t.Fatalf("ConductorDir: %v", err)
	}
	if cd != target {
		t.Fatalf("ConductorDir() = %q, want %q", cd, target)
	}

	// The reconcile set is reported for daemon reload.
	if len(res.Conductors) != 1 || res.Conductors[0] != "alpha" {
		t.Fatalf("res.Conductors = %v, want [alpha]", res.Conductors)
	}
}

func TestMigrateConductorDir_DryRunChangesNothing(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md": "x",
	})
	target := filepath.Join(t.TempDir(), "vault-conductors")

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: false})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	if !res.DryRun || res.ConfigWritten {
		t.Fatalf("dry-run wrote state: DryRun=%v ConfigWritten=%v", res.DryRun, res.ConfigWritten)
	}
	if len(res.Actions) == 0 {
		t.Fatal("dry-run should report a plan")
	}
	// Nothing moved, nothing created.
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "meta.json"), "alpha")
	assertNotExist(t, target)
	// Resolver still the default (no override written).
	cd, _ := ConductorDir()
	if cd != defaultBase {
		t.Fatalf("ConductorDir() = %q, want unchanged default %q", cd, defaultBase)
	}
}

func TestMigrateConductorDir_SkipsExistingWithoutForce(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md": "source version",
	})
	target := filepath.Join(t.TempDir(), "vault-conductors")
	writeConductorHome(t, target, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md": "dest version",
	})

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	// Destination preserved; source NOT removed (no --force).
	assertFileContains(t, filepath.Join(target, "alpha", "CLAUDE.md"), "dest version")
	assertFileContains(t, filepath.Join(defaultBase, "alpha", "CLAUDE.md"), "source version")
	var found bool
	for _, a := range res.Actions {
		if a.Name == "alpha" {
			found = true
			if a.Action != "skip-exists" {
				t.Fatalf("alpha action = %q, want skip-exists", a.Action)
			}
		}
	}
	if !found {
		t.Fatal("no action recorded for alpha")
	}
}

func TestMigrateConductorDir_ForceMergesPreservingDest(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json":  `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md":  "source version",
		"state.json": "source state",
	})
	target := filepath.Join(t.TempDir(), "vault-conductors")
	writeConductorHome(t, target, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
		"CLAUDE.md": "dest version",
	})

	res, err := MigrateConductorDir(ConductorDirMigrateOptions{Target: target, Apply: true, Force: true})
	if err != nil {
		t.Fatalf("MigrateConductorDir: %v", err)
	}
	td := filepath.Join(target, "alpha")
	// Existing dest file preserved; source-only file merged in.
	assertFileContains(t, filepath.Join(td, "CLAUDE.md"), "dest version")
	assertFileContains(t, filepath.Join(td, "state.json"), "source state")
	// Source removed after merge.
	assertNotExist(t, filepath.Join(defaultBase, "alpha"))

	var merged bool
	for _, a := range res.Actions {
		if a.Name == "alpha" && a.Action == "merge" {
			merged = true
			if !a.Conflict {
				t.Fatal("expected merge to report a conflict (CLAUDE.md existed)")
			}
		}
	}
	if !merged {
		t.Fatal("expected a merge action for alpha")
	}
}

func TestDetectConductorDirSplitBrain(t *testing.T) {
	_, xdgConfigHome, xdgDataHome := setupSessionXDGPathEnv(t)
	defaultBase := filepath.Join(xdgDataHome, "agent-deck", "conductor")
	writeConductorHome(t, defaultBase, "alpha", map[string]string{
		"meta.json": `{"name":"alpha","profile":"default"}`,
	})

	// No override yet → resolved == default (populated) → no split-brain.
	if _, ok := DetectConductorDirSplitBrain(); ok {
		t.Fatal("no override should not report split-brain")
	}

	// Override set to an empty dir while default stays populated → split-brain.
	override := filepath.Join(t.TempDir(), "empty-override")
	writeConductorDirConfig(t, xdgConfigHome, override)
	msg, ok := DetectConductorDirSplitBrain()
	if !ok {
		t.Fatal("expected split-brain when override empty and default populated")
	}
	if !strings.Contains(msg, "migrate-dir") {
		t.Fatalf("warning should point at migrate-dir, got %q", msg)
	}

	// Once the override is itself populated → no split-brain.
	writeConductorHome(t, override, "beta", map[string]string{
		"meta.json": `{"name":"beta","profile":"default"}`,
	})
	if _, ok := DetectConductorDirSplitBrain(); ok {
		t.Fatal("populated override should not report split-brain")
	}
}
