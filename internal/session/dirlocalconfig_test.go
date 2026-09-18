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

	settings, sources, _, err := ResolveWorktreeSettingsForDir(target)
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

	settings, sources, _, err = ResolveWorktreeSettingsForDir(target)
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

	settings, sources, _, err := ResolveWorktreeSettingsForDir(target)
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

	_, _, _, err := ResolveWorktreeSettingsForDir(target)
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

	_, _, _, err := ResolveWorktreeSettingsForDir(target)
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

	settings, sources, _, err := ResolveWorktreeSettingsForDir(other)
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

// --- P1 follow-up: dir-local default_location/path_template validation ---
// (#2093 review; a dir-local .agent-deck/config.toml may come from an
// untrusted checkout, so these two keys are bound to the workspace directory
// they were discovered from before being trusted; see
// validateDirLocalWorktreeValue in dirlocalconfig.go.)

// setupDirLocalWorkspace creates the layout these tests share inside an
// isolated HOME: a workspace directory with one checkout ("main") in it.
func setupDirLocalWorkspace(t *testing.T) (home, workspace, target string) {
	t.Helper()
	home = setupDirLocalHome(t)
	workspace = filepath.Join(home, "projects", "example")
	target = filepath.Join(workspace, "main")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, workspace, target
}

// requireOneRejection asserts that exactly one rejection was recorded for key
// and that its reason mentions want.
func requireOneRejection(t *testing.T, rejections map[string][]string, key, want string) {
	t.Helper()
	got := rejections[key]
	if len(got) != 1 {
		t.Fatalf("rejections[%s] = %v, want exactly one rejection", key, got)
	}
	if !strings.Contains(got[0], want) {
		t.Errorf("rejection reason %q does not mention %q", got[0], want)
	}
}

func TestResolveWorktreeSettingsForDir_RejectsHomeRelativeTemplate(t *testing.T) {
	_, _, target := setupDirLocalWorkspace(t)
	writeDirLocalConfig(t, target, "[worktree]\npath_template = \"~/Library/LaunchAgents/{branch}\"\n")

	settings, sources, rejections, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.Template() != "" {
		t.Errorf("Template() = %q, want empty (rejected value must not be assigned)", settings.Template())
	}
	if sources[WorktreeKeyPathTemplate] != sourceDefault {
		t.Errorf("sources[path_template] = %q, want fallback to %q", sources[WorktreeKeyPathTemplate], sourceDefault)
	}
	requireOneRejection(t, rejections, WorktreeKeyPathTemplate, "home-relative")
}

func TestResolveWorktreeSettingsForDir_RejectsHomeRelativeDefaultLocation(t *testing.T) {
	_, _, target := setupDirLocalWorkspace(t)
	writeDirLocalConfig(t, target, "[worktree]\ndefault_location = \"~/.ssh\"\n")

	settings, sources, rejections, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.DefaultLocation != "subdirectory" {
		t.Errorf("DefaultLocation = %q, want built-in default (rejected value must not be assigned)", settings.DefaultLocation)
	}
	if sources[WorktreeKeyDefaultLocation] != sourceDefault {
		t.Errorf("sources[default_location] = %q, want fallback to %q", sources[WorktreeKeyDefaultLocation], sourceDefault)
	}
	requireOneRejection(t, rejections, WorktreeKeyDefaultLocation, "home-relative")
}

func TestResolveWorktreeSettingsForDir_RejectsDotDotSegmentInDefaultLocation(t *testing.T) {
	_, _, target := setupDirLocalWorkspace(t)
	writeDirLocalConfig(t, target, "[worktree]\ndefault_location = \"../../x\"\n")

	settings, _, rejections, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.DefaultLocation != "subdirectory" {
		t.Errorf("DefaultLocation = %q, want built-in default", settings.DefaultLocation)
	}
	requireOneRejection(t, rejections, WorktreeKeyDefaultLocation, "'..' path segment")
}

func TestResolveWorktreeSettingsForDir_RejectsAbsolutePaths(t *testing.T) {
	_, _, target := setupDirLocalWorkspace(t)
	writeDirLocalConfig(t, target, "[worktree]\ndefault_location = \"/tmp/x\"\npath_template = \"/tmp/x\"\n")

	settings, _, rejections, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.DefaultLocation != "subdirectory" {
		t.Errorf("DefaultLocation = %q, want built-in default", settings.DefaultLocation)
	}
	if settings.Template() != "" {
		t.Errorf("Template() = %q, want empty", settings.Template())
	}
	for _, key := range []string{WorktreeKeyDefaultLocation, WorktreeKeyPathTemplate} {
		requireOneRejection(t, rejections, key, "absolute path")
	}
}

// TestResolveWorktreeSettingsForDir_TemplateEscapeVsSiblingCase proves the
// resolved-path bound check does real containment work, not blanket ".."
// string matching: a *small* relative ".." template that stays within the
// workspace boundary (the legitimate sibling-worktree case from #2093's own
// example) must be ACCEPTED, while a template with enough "../.." to escape
// the boundary must be REJECTED.
func TestResolveWorktreeSettingsForDir_TemplateEscapeVsSiblingCase(t *testing.T) {
	_, workspace, target := setupDirLocalWorkspace(t)

	t.Run("accepted sibling case", func(t *testing.T) {
		writeDirLocalConfig(t, workspace, "[worktree]\npath_template = \"{repo-root}/../wt-{branch}\"\n")
		settings, sources, rejections, err := ResolveWorktreeSettingsForDir(target)
		if err != nil {
			t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
		}
		if settings.Template() != "{repo-root}/../wt-{branch}" {
			t.Errorf("Template() = %q, want the sibling template (must stay accepted)", settings.Template())
		}
		if len(rejections[WorktreeKeyPathTemplate]) != 0 {
			t.Errorf("rejections[path_template] = %v, want none for the in-boundary sibling case", rejections[WorktreeKeyPathTemplate])
		}
		if sources[WorktreeKeyPathTemplate] == sourceDefault {
			t.Errorf("sources[path_template] = %q, want the dir-local file", sources[WorktreeKeyPathTemplate])
		}
	})

	t.Run("rejected escape case", func(t *testing.T) {
		// Escapes past the workspace boundary (projects/example's parent and
		// beyond), unlike the sibling case above which stays within it.
		writeDirLocalConfig(t, workspace, "[worktree]\npath_template = \"{repo-root}/../../../../../escaped-{branch}\"\n")
		settings, sources, rejections, err := ResolveWorktreeSettingsForDir(target)
		if err != nil {
			t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
		}
		if settings.Template() != "" {
			t.Errorf("Template() = %q, want empty (escape must be rejected)", settings.Template())
		}
		if sources[WorktreeKeyPathTemplate] != sourceDefault {
			t.Errorf("sources[path_template] = %q, want fallback to %q", sources[WorktreeKeyPathTemplate], sourceDefault)
		}
		requireOneRejection(t, rejections, WorktreeKeyPathTemplate, "resolves outside workspace boundary")
	})
}

func TestResolveWorktreeSettingsForDir_RejectsSymlinkEscape(t *testing.T) {
	home, workspace, target := setupDirLocalWorkspace(t)
	outsideDir := filepath.Join(home, "outside-workspace")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the workspace pointing outside it. The raw candidate
	// path (before symlink resolution) looks like it stays within the
	// workspace; only resolving the symlink reveals the escape.
	escapeLink := filepath.Join(workspace, "escape-link")
	if err := os.Symlink(outsideDir, escapeLink); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	writeDirLocalConfig(t, workspace, "[worktree]\npath_template = \"{repo-root}/../escape-link/pwned-{branch}\"\n")

	settings, sources, rejections, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.Template() != "" {
		t.Errorf("Template() = %q, want empty (symlink escape must be rejected)", settings.Template())
	}
	if sources[WorktreeKeyPathTemplate] != sourceDefault {
		t.Errorf("sources[path_template] = %q, want fallback to %q", sources[WorktreeKeyPathTemplate], sourceDefault)
	}
	requireOneRejection(t, rejections, WorktreeKeyPathTemplate, "resolves outside workspace boundary")
}

func TestResolveWorktreeSettingsForDir_GlobalConfigTrustedUnchanged(t *testing.T) {
	_, _, target := setupDirLocalWorkspace(t)

	template := "~/Library/LaunchAgents/{branch}"
	globalCfg := &UserConfig{Worktree: WorktreeSettings{
		DefaultLocation: "~/.ssh",
		PathTemplate:    &template,
	}}
	if err := SaveUserConfig(globalCfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	settings, sources, rejections, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.DefaultLocation != "~/.ssh" {
		t.Errorf("DefaultLocation = %q, want the global value unchanged (global config stays fully trusted)", settings.DefaultLocation)
	}
	if settings.Template() != template {
		t.Errorf("Template() = %q, want the global value unchanged", settings.Template())
	}
	if sources[WorktreeKeyDefaultLocation] != sourceGlobal || sources[WorktreeKeyPathTemplate] != sourceGlobal {
		t.Errorf("sources = %+v, want both keys attributed to global", sources)
	}
	if len(rejections[WorktreeKeyDefaultLocation]) != 0 || len(rejections[WorktreeKeyPathTemplate]) != 0 {
		t.Errorf("rejections = %+v, want none (global config is not validated)", rejections)
	}
}

// --- Round 3 follow-up: per-file boundary (#2093 review round 2, finding 1) ---
// (a dir-local file may only bound a value to its OWN directory tree; only
// the outermost file gets the workspace-wide bound, for values it itself
// contributes.)

// mkdirs creates each directory (and its parents), failing the test if it
// cannot.
func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// requireTemplateBoundaryRejection asserts that path_template was refused by
// the resolved-path bound check, that nothing was applied in its place, and
// that the recorded rejection names offendingFile (the dir-local file the
// value came from). explain describes what the rejected value was trying to
// reach, for the failure message.
func requireTemplateBoundaryRejection(
	t *testing.T,
	settings WorktreeSettings,
	sources map[string]string,
	rejections map[string][]string,
	offendingFile, explain string,
) {
	t.Helper()
	if settings.Template() != "" {
		t.Errorf("Template() = %q, want empty (%s)", settings.Template(), explain)
	}
	if sources[WorktreeKeyPathTemplate] != sourceDefault {
		t.Errorf("sources[path_template] = %q, want fallback to %q", sources[WorktreeKeyPathTemplate], sourceDefault)
	}
	requireOneRejection(t, rejections, WorktreeKeyPathTemplate, "resolves outside workspace boundary")
	if got := rejections[WorktreeKeyPathTemplate][0]; !strings.Contains(got, offendingFile) {
		t.Errorf("rejection %q does not name the offending file %q", got, offendingFile)
	}
}

// TestResolveWorktreeSettingsForDir_RejectsInnerFileEscapeViaOuterBoundary
// reproduces review round 2 finding 1 live: a legitimate outer workspace-
// parent file (default_location = "sibling" only) plus an untrusted inner
// dir-local file (as would ship inside a cloned third-party repo) that tries
// to redirect worktree creation into a SIBLING repo it does not own, via
// "{repo-root}/../repo1-important/{branch}". Before the fix this was accepted
// because the boundary was computed once from the outer file and reused for
// the inner file's value; it must now be rejected, bounded to the inner
// file's own directory.
func TestResolveWorktreeSettingsForDir_RejectsInnerFileEscapeViaOuterBoundary(t *testing.T) {
	_, workspace, _ := setupDirLocalWorkspace(t)

	repo1 := filepath.Join(workspace, "repo1-important")
	repo2Evil := filepath.Join(workspace, "repo2-evil")
	mkdirs(t, repo1, repo2Evil)

	writeDirLocalConfig(t, workspace, "[worktree]\ndefault_location = \"sibling\"\n")
	evilPath := writeDirLocalConfig(t, repo2Evil,
		"[worktree]\npath_template = \"{repo-root}/../repo1-important/{branch}\"\n")

	settings, sources, rejections, err := ResolveWorktreeSettingsForDir(repo2Evil)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	requireTemplateBoundaryRejection(t, settings, sources, rejections, evilPath,
		"inner file's escape into a sibling repo must be rejected")
}

// TestResolveWorktreeSettingsForDir_OuterOnlySiblingStillWorks confirms the
// legitimate workspace-parent pattern (an outer-only file, no inner file at
// all) is unaffected by the per-file boundary: the outer file's own
// directory IS the workspace boundary, so a sibling-worktree template it
// contributes itself must still be accepted.
func TestResolveWorktreeSettingsForDir_OuterOnlySiblingStillWorks(t *testing.T) {
	_, workspace, target := setupDirLocalWorkspace(t)

	outerPath := writeDirLocalConfig(t, workspace, "[worktree]\npath_template = \"{repo-root}/../wt-{branch}\"\n")

	settings, sources, rejections, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.Template() != "{repo-root}/../wt-{branch}" {
		t.Errorf("Template() = %q, want the outer file's sibling template", settings.Template())
	}
	if sources[WorktreeKeyPathTemplate] != outerPath {
		t.Errorf("sources[path_template] = %q, want %q", sources[WorktreeKeyPathTemplate], outerPath)
	}
	if len(rejections[WorktreeKeyPathTemplate]) != 0 {
		t.Errorf("rejections[path_template] = %v, want none", rejections[WorktreeKeyPathTemplate])
	}
}

// TestResolveWorktreeSettingsForDir_ThreeLevelNestingBoundsToOwnFile checks a
// three-file chain: outermost workspace file, a middle directory with its own
// file, and an innermost target directory with an untrusted file that tries
// to reach a directory that sits within the MIDDLE file's tree (and inside
// the overall workspace boundary) but outside the innermost file's own
// directory. It must still be rejected: each file is bounded to its own
// subtree regardless of how many outer files exist above it.
func TestResolveWorktreeSettingsForDir_ThreeLevelNestingBoundsToOwnFile(t *testing.T) {
	_, workspace, _ := setupDirLocalWorkspace(t)

	mid := filepath.Join(workspace, "mid")
	midSibling := filepath.Join(workspace, "mid-sibling")
	inner := filepath.Join(mid, "inner")
	mkdirs(t, mid, midSibling, inner)

	writeDirLocalConfig(t, workspace, "[worktree]\ndefault_location = \"sibling\"\n")
	writeDirLocalConfig(t, mid, "[worktree]\ndefault_location = \"sibling\"\n")
	innerPath := writeDirLocalConfig(t, inner,
		"[worktree]\npath_template = \"{repo-root}/../../mid-sibling/{branch}\"\n")

	settings, sources, rejections, err := ResolveWorktreeSettingsForDir(inner)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	requireTemplateBoundaryRejection(t, settings, sources, rejections, innerPath,
		"innermost file cannot reach the middle file's sibling")
}

// --- Round 3 follow-up: empty path_template is inherently safe (#2093
// review round 2, finding 2) ---

// TestResolveWorktreeSettingsForDir_EmptyTemplateSingleFileNoOuter
// reproduces review round 2 finding 2 live: a single dir-local file with
// path_template = "" and NO outer file above it (so the boundary would
// otherwise collapse to targetDir itself). This must restore the built-in
// default (sibling) behavior, not be rejected as "outside the workspace
// boundary" — an empty template is inherently safe, same as
// default_location's empty/sibling/subdirectory values.
func TestResolveWorktreeSettingsForDir_EmptyTemplateSingleFileNoOuter(t *testing.T) {
	_, _, target := setupDirLocalWorkspace(t)

	path := writeDirLocalConfig(t, target, "[worktree]\npath_template = \"\"\n")

	settings, sources, rejections, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.PathTemplate == nil || *settings.PathTemplate != "" {
		t.Errorf("PathTemplate = %v, want an explicit empty-string override", settings.PathTemplate)
	}
	if len(rejections[WorktreeKeyPathTemplate]) != 0 {
		t.Errorf("rejections[path_template] = %v, want none (empty template is inherently safe)", rejections[WorktreeKeyPathTemplate])
	}
	if sources[WorktreeKeyPathTemplate] != path {
		t.Errorf("sources[path_template] = %q, want %q", sources[WorktreeKeyPathTemplate], path)
	}
}
