package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupDirLocalHome creates an isolated temp HOME (with XDG pointed at the
// same tree, so global-config resolution is testable too) and returns it.
func setupDirLocalHome(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	t.Setenv("HOME", tempDir)
	t.Cleanup(func() { os.Setenv("HOME", originalHome) })
	isolateConfigHomeXDG(t)
	return tempDir
}

func writeDirLocalConfig(t *testing.T, dir, contents string) string {
	t.Helper()
	agentDeckDir := filepath.Join(dir, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", agentDeckDir, err)
	}
	path := filepath.Join(agentDeckDir, "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestDiscoverDirLocalConfigPaths_NoneFound(t *testing.T) {
	home := setupDirLocalHome(t)
	target := filepath.Join(home, "projects", "example", "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	paths, err := DiscoverDirLocalConfigPaths(target)
	if err != nil {
		t.Fatalf("DiscoverDirLocalConfigPaths: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want none", paths)
	}
}

func TestDiscoverDirLocalConfigPaths_TargetDirOnly(t *testing.T) {
	home := setupDirLocalHome(t)
	target := filepath.Join(home, "projects", "example", "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	want := writeDirLocalConfig(t, target, "[worktree]\ndefault_location = \"sibling\"\n")

	paths, err := DiscoverDirLocalConfigPaths(target)
	if err != nil {
		t.Fatalf("DiscoverDirLocalConfigPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("paths = %v, want [%s]", paths, want)
	}
}

func TestDiscoverDirLocalConfigPaths_OutermostFirst(t *testing.T) {
	home := setupDirLocalHome(t)
	workspace := filepath.Join(home, "projects", "example")
	target := filepath.Join(workspace, "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	outer := writeDirLocalConfig(t, workspace, "[worktree]\ndefault_location = \"sibling\"\n")
	inner := writeDirLocalConfig(t, target, "[worktree]\ndefault_location = \"subdirectory\"\n")

	paths, err := DiscoverDirLocalConfigPaths(target)
	if err != nil {
		t.Fatalf("DiscoverDirLocalConfigPaths: %v", err)
	}
	if len(paths) != 2 || paths[0] != outer || paths[1] != inner {
		t.Fatalf("paths = %v, want [%s, %s] (outermost first)", paths, outer, inner)
	}
}

func TestDiscoverDirLocalConfigPaths_SiblingWorkspaceParent(t *testing.T) {
	// The issue's motivating layout: a workspace-parent .agent-deck/config.toml
	// applies to sibling checkouts even though it is not inside either git root.
	home := setupDirLocalHome(t)
	workspace := filepath.Join(home, "projects", "example")
	main := filepath.Join(workspace, "main")
	featureOne := filepath.Join(workspace, "feature-one")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(featureOne, 0o755); err != nil {
		t.Fatal(err)
	}
	want := writeDirLocalConfig(t, workspace, "[worktree]\ndefault_location = \"sibling\"\n")

	for _, target := range []string{main, featureOne} {
		paths, err := DiscoverDirLocalConfigPaths(target)
		if err != nil {
			t.Fatalf("DiscoverDirLocalConfigPaths(%s): %v", target, err)
		}
		if len(paths) != 1 || paths[0] != want {
			t.Fatalf("DiscoverDirLocalConfigPaths(%s) = %v, want [%s]", target, paths, want)
		}
	}
}

func TestDiscoverDirLocalConfigPaths_StopsAtHomeAndExcludesGlobal(t *testing.T) {
	home := setupDirLocalHome(t)

	// The legacy global config lives at $HOME/.agent-deck/config.toml; it must
	// never be double-counted as dir-local.
	globalPath := filepath.Join(home, ".agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalPath, []byte("[worktree]\ndefault_location = \"sibling\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(home, "projects", "example", "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	paths, err := DiscoverDirLocalConfigPaths(target)
	if err != nil {
		t.Fatalf("DiscoverDirLocalConfigPaths: %v", err)
	}
	for _, p := range paths {
		if p == globalPath {
			t.Fatalf("paths %v should not include the global config path %s", paths, globalPath)
		}
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want none (only the excluded global file exists)", paths)
	}
}

func TestResolveWorktreeSettingsForDir_Precedence(t *testing.T) {
	home := setupDirLocalHome(t)

	// Global config sets default_location=sibling.
	autoCleanupTrue := true
	globalCfg := &UserConfig{Worktree: WorktreeSettings{DefaultLocation: "sibling", AutoCleanup: &autoCleanupTrue}}
	if err := SaveUserConfig(globalCfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	workspace := filepath.Join(home, "projects", "example")
	target := filepath.Join(workspace, "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	// Outer dir-local file overrides default_location and sets a template.
	outerPath := writeDirLocalConfig(t, workspace,
		"[worktree]\ndefault_location = \"subdirectory\"\npath_template = \"{repo-root}/../wt-{branch}\"\n")

	settings, sources, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.DefaultLocation != "subdirectory" {
		t.Errorf("DefaultLocation = %q, want subdirectory (outer dir-local should win over global)", settings.DefaultLocation)
	}
	if settings.Template() != "{repo-root}/../wt-{branch}" {
		t.Errorf("Template() = %q, want the outer dir-local template", settings.Template())
	}
	if sources["default_location"] != outerPath {
		t.Errorf("sources[default_location] = %q, want %q", sources["default_location"], outerPath)
	}
	if !settings.GetAutoCleanup() {
		t.Error("GetAutoCleanup() should remain true from global config (auto_cleanup is not a dir-local key)")
	}

	// Inner dir-local file overrides default_location again; outer's template
	// still applies (inner does not set path_template).
	innerPath := writeDirLocalConfig(t, target, "[worktree]\ndefault_location = \"sibling\"\n")

	settings, sources, err = ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.DefaultLocation != "sibling" {
		t.Errorf("DefaultLocation = %q, want sibling (inner dir-local should win)", settings.DefaultLocation)
	}
	if sources["default_location"] != innerPath {
		t.Errorf("sources[default_location] = %q, want %q", sources["default_location"], innerPath)
	}
	if settings.Template() != "{repo-root}/../wt-{branch}" {
		t.Errorf("Template() = %q, want outer template to survive (inner doesn't set path_template)", settings.Template())
	}
	if sources["path_template"] != outerPath {
		t.Errorf("sources[path_template] = %q, want %q (outer file, unchanged)", sources["path_template"], outerPath)
	}
}

func TestResolveWorktreeSettingsForDir_EmptyTemplateClearsInherited(t *testing.T) {
	home := setupDirLocalHome(t)
	workspace := filepath.Join(home, "projects", "example")
	target := filepath.Join(workspace, "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	writeDirLocalConfig(t, workspace, "[worktree]\npath_template = \"{repo-root}/../wt-{branch}\"\n")
	innerPath := writeDirLocalConfig(t, target, "[worktree]\npath_template = \"\"\n")

	settings, sources, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.PathTemplate == nil {
		t.Fatal("PathTemplate is nil, want an explicit empty-string override (not \"not set\")")
	}
	if settings.Template() != "" {
		t.Errorf("Template() = %q, want empty (inner file explicitly clears the inherited template)", settings.Template())
	}
	if sources["path_template"] != innerPath {
		t.Errorf("sources[path_template] = %q, want %q", sources["path_template"], innerPath)
	}
}

func TestResolveWorktreeSettingsForDir_UnknownTopLevelSection(t *testing.T) {
	home := setupDirLocalHome(t)
	target := filepath.Join(home, "projects", "example", "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDirLocalConfig(t, target, "[tool]\nfoo = \"bar\"\n")

	_, _, err := ResolveWorktreeSettingsForDir(target)
	if err == nil {
		t.Fatal("expected an error for an unknown top-level section, got nil")
	}
	if !strings.Contains(err.Error(), "config.toml") || !strings.Contains(err.Error(), "tool") {
		t.Errorf("error %q does not name the file and the offending key", err.Error())
	}
}

func TestResolveWorktreeSettingsForDir_UnknownWorktreeKey(t *testing.T) {
	home := setupDirLocalHome(t)
	target := filepath.Join(home, "projects", "example", "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeDirLocalConfig(t, target, "[worktree]\nauto_cleanup = false\n")

	_, _, err := ResolveWorktreeSettingsForDir(target)
	if err == nil {
		t.Fatal("expected an error for auto_cleanup (excluded key), got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the offending file %q", err.Error(), path)
	}
	if !strings.Contains(err.Error(), "auto_cleanup") {
		t.Errorf("error %q does not name the offending key", err.Error())
	}
}

func TestResolveWorktreeSettingsForDir_OutsideAnyDirLocalConfig(t *testing.T) {
	home := setupDirLocalHome(t)
	workspace := filepath.Join(home, "projects", "example")
	target := filepath.Join(workspace, "main")
	other := filepath.Join(home, "other-project")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDirLocalConfig(t, workspace, "[worktree]\ndefault_location = \"sibling\"\n")

	settings, sources, err := ResolveWorktreeSettingsForDir(other)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.DefaultLocation != "subdirectory" {
		t.Errorf("DefaultLocation = %q, want built-in default subdirectory (outside the dir-local tree)", settings.DefaultLocation)
	}
	if sources["default_location"] != sourceDefault {
		t.Errorf("sources[default_location] = %q, want %q", sources["default_location"], sourceDefault)
	}
}

func TestGetWorktreeSettingsForDir_PropagatesError(t *testing.T) {
	home := setupDirLocalHome(t)
	target := filepath.Join(home, "projects", "example", "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDirLocalConfig(t, target, "[worktree]\nrun_repo_scripts = \"always\"\n")

	if _, err := GetWorktreeSettingsForDir(target); err == nil {
		t.Fatal("expected an error for run_repo_scripts (excluded key), got nil")
	}
}
