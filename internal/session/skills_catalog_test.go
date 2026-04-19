package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func setupSkillTestEnv(t *testing.T) (string, func()) {
	t.Helper()

	homeDir, err := os.MkdirTemp("", "agentdeck-skills-home-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}

	claudeDir := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("failed to create claude dir: %v", err)
	}

	oldHome := os.Getenv("HOME")
	oldClaude := os.Getenv("CLAUDE_CONFIG_DIR")
	_ = os.Setenv("HOME", homeDir)
	_ = os.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	ClearUserConfigCache()

	cleanup := func() {
		_ = os.Setenv("HOME", oldHome)
		_ = os.Setenv("CLAUDE_CONFIG_DIR", oldClaude)
		ClearUserConfigCache()
		_ = os.RemoveAll(homeDir)
	}

	return homeDir, cleanup
}

func writeSkillDir(t *testing.T, root, folder, name, description string) string {
	t.Helper()
	skillPath := filepath.Join(root, folder)
	if err := os.MkdirAll(skillPath, 0o755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# %s\n", name, description, name)
	if err := os.WriteFile(filepath.Join(skillPath, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write SKILL.md: %v", err)
	}
	return skillPath
}

func TestLoadSkillSources_DefaultsWhenMissing(t *testing.T) {
	homeDir, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sources, err := LoadSkillSources()
	if err != nil {
		t.Fatalf("LoadSkillSources failed: %v", err)
	}

	pool, ok := sources[defaultSkillSourcePool]
	if !ok {
		t.Fatalf("expected default pool source")
	}
	if pool.Path == "" {
		t.Fatalf("pool path should not be empty")
	}

	claude, ok := sources[defaultSkillSourceClaude]
	if !ok {
		t.Fatalf("expected default claude-global source")
	}
	wantClaude := filepath.Join(homeDir, ".claude", "skills")
	if claude.Path != wantClaude {
		t.Fatalf("claude source path = %q, want %q", claude.Path, wantClaude)
	}
}

func TestExpandSkillPath_DollarHome(t *testing.T) {
	homeDir, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	got := expandSkillPath("$HOME/.agent-deck/skills/pool")
	want := filepath.Join(homeDir, ".agent-deck", "skills", "pool")
	if got != want {
		t.Fatalf("expandSkillPath($HOME/...) = %q, want %q", got, want)
	}
}

func TestExpandSkillPath_BracedDollarHome(t *testing.T) {
	homeDir, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	got := expandSkillPath("${HOME}/foo")
	want := filepath.Join(homeDir, "foo")
	if got != want {
		t.Fatalf("expandSkillPath(${HOME}/foo) = %q, want %q", got, want)
	}
}

func TestExpandSkillPath_BareDollarHome(t *testing.T) {
	homeDir, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	if got := expandSkillPath("$HOME"); got != homeDir {
		t.Fatalf("expandSkillPath($HOME) = %q, want %q", got, homeDir)
	}
	if got := expandSkillPath("${HOME}"); got != homeDir {
		t.Fatalf("expandSkillPath(${HOME}) = %q, want %q", got, homeDir)
	}
}

func TestExpandSkillPath_DollarHomeSubstringPreserved(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	// $HOMEBREW must NOT match $HOME — env var name is HOMEBREW, not HOME.
	got := expandSkillPath("/etc/$HOMEBREW/foo")
	want := filepath.Clean("/etc/$HOMEBREW/foo")
	if got != want {
		t.Fatalf("expandSkillPath(/etc/$HOMEBREW/foo) = %q, want %q", got, want)
	}
}

func TestExpandSkillPath_UnknownEnvVarPreserved(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	// Only $HOME is expanded. Other env refs pass through verbatim so sources.toml
	// does not silently inherit arbitrary process environment into config paths.
	got := expandSkillPath("$USER/foo")
	want := filepath.Clean("$USER/foo")
	if got != want {
		t.Fatalf("expandSkillPath($USER/foo) = %q, want %q", got, want)
	}
}

func TestLoadSkillSources_ExpandsDollarHomeOnLoad(t *testing.T) {
	homeDir, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcesPath, err := GetSkillSourcesPath()
	if err != nil {
		t.Fatalf("GetSkillSourcesPath failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sourcesPath), 0o700); err != nil {
		t.Fatalf("failed to create sources dir: %v", err)
	}

	toml := "[sources.pool]\n" +
		`path = "$HOME/.agent-deck/skills/pool"` + "\n" +
		`description = "sync-from-mac"` + "\n" +
		"enabled = true\n"
	if err := os.WriteFile(sourcesPath, []byte(toml), 0o600); err != nil {
		t.Fatalf("failed to write sources.toml: %v", err)
	}

	sources, err := LoadSkillSources()
	if err != nil {
		t.Fatalf("LoadSkillSources failed: %v", err)
	}

	pool, ok := sources["pool"]
	if !ok {
		t.Fatalf("expected pool source in sources map")
	}
	want := filepath.Join(homeDir, ".agent-deck", "skills", "pool")
	if pool.Path != want {
		t.Fatalf("pool.Path = %q, want %q (was $HOME expanded on load?)", pool.Path, want)
	}
}

func TestLoadSkillSources_DiscoversSkillsViaDollarHome(t *testing.T) {
	homeDir, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	poolRoot := filepath.Join(homeDir, ".agent-deck", "skills", "pool")
	writeSkillDir(t, poolRoot, "demo", "demo", "Demo skill for #617 repro")

	sourcesPath, err := GetSkillSourcesPath()
	if err != nil {
		t.Fatalf("GetSkillSourcesPath failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sourcesPath), 0o700); err != nil {
		t.Fatalf("failed to create sources dir: %v", err)
	}
	toml := "[sources.pool]\n" +
		`path = "$HOME/.agent-deck/skills/pool"` + "\n" +
		"enabled = true\n"
	if err := os.WriteFile(sourcesPath, []byte(toml), 0o600); err != nil {
		t.Fatalf("failed to write sources.toml: %v", err)
	}

	candidates, err := ListAvailableSkills()
	if err != nil {
		t.Fatalf("ListAvailableSkills failed: %v", err)
	}

	found := false
	for _, c := range candidates {
		if c.Source == "pool" && c.Name == "demo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected to discover pool/demo via $HOME expansion; got %d candidates: %+v", len(candidates), candidates)
	}
}

func TestAddAndRemoveSkillSource(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath, err := os.MkdirTemp("", "agentdeck-team-skills-*")
	if err != nil {
		t.Fatalf("failed to create source path: %v", err)
	}
	defer os.RemoveAll(sourcePath)

	if err := AddSkillSource("team", sourcePath, "Team skills"); err != nil {
		t.Fatalf("AddSkillSource failed: %v", err)
	}

	sources, err := ListSkillSources()
	if err != nil {
		t.Fatalf("ListSkillSources failed: %v", err)
	}

	found := false
	for _, source := range sources {
		if source.Name == "team" {
			found = true
			if source.Path != sourcePath {
				t.Fatalf("source path = %q, want %q", source.Path, sourcePath)
			}
		}
	}
	if !found {
		t.Fatalf("expected to find source 'team'")
	}

	if err := RemoveSkillSource("team"); err != nil {
		t.Fatalf("RemoveSkillSource failed: %v", err)
	}

	sources, err = ListSkillSources()
	if err != nil {
		t.Fatalf("ListSkillSources failed after remove: %v", err)
	}
	for _, source := range sources {
		if source.Name == "team" {
			t.Fatalf("source 'team' should be removed")
		}
	}
}

func TestListAvailableSkills_DiscoversDirectoryAndSkillFile(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath, err := os.MkdirTemp("", "agentdeck-discovery-source-*")
	if err != nil {
		t.Fatalf("failed to create source path: %v", err)
	}
	defer os.RemoveAll(sourcePath)

	writeSkillDir(t, sourcePath, "lint", "lint", "Linting best practices")
	if err := os.WriteFile(filepath.Join(sourcePath, "quick.skill"), []byte("dummy"), 0o644); err != nil {
		t.Fatalf("failed to write .skill file: %v", err)
	}

	if err := SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	skills, err := ListAvailableSkills()
	if err != nil {
		t.Fatalf("ListAvailableSkills failed: %v", err)
	}

	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	seen := map[string]bool{}
	for _, skill := range skills {
		seen[skill.Name] = true
	}
	if !seen["lint"] {
		t.Fatalf("expected to discover 'lint' skill")
	}
	if !seen["quick"] {
		t.Fatalf("expected to discover 'quick' skill")
	}
}

func TestResolveSkillCandidate_AmbiguousWithoutSource(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourceA, _ := os.MkdirTemp("", "agentdeck-source-a-*")
	sourceB, _ := os.MkdirTemp("", "agentdeck-source-b-*")
	defer os.RemoveAll(sourceA)
	defer os.RemoveAll(sourceB)

	writeSkillDir(t, sourceA, "build", "build", "Build from source A")
	writeSkillDir(t, sourceB, "build", "build", "Build from source B")

	if err := SaveSkillSources(map[string]SkillSourceDef{
		"a": {Path: sourceA, Enabled: boolPtr(true)},
		"b": {Path: sourceB, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	_, err := ResolveSkillCandidate("build", "")
	if err == nil {
		t.Fatalf("expected ambiguous error")
	}
	if !errors.Is(err, ErrSkillAmbiguous) {
		t.Fatalf("expected ErrSkillAmbiguous, got %v", err)
	}

	resolved, err := ResolveSkillCandidate("build", "a")
	if err != nil {
		t.Fatalf("ResolveSkillCandidate with source failed: %v", err)
	}
	if resolved.Source != "a" {
		t.Fatalf("resolved source = %q, want %q", resolved.Source, "a")
	}
}

func TestAttachDetachSkillProject(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath, err := os.MkdirTemp("", "agentdeck-attach-source-*")
	if err != nil {
		t.Fatalf("failed to create source path: %v", err)
	}
	defer os.RemoveAll(sourcePath)

	writeSkillDir(t, sourcePath, "lint", "lint", "Linting best practices")
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath, err := os.MkdirTemp("", "agentdeck-project-*")
	if err != nil {
		t.Fatalf("failed to create project path: %v", err)
	}
	defer os.RemoveAll(projectPath)

	attached, err := AttachSkillToProject(projectPath, "claude", "lint", "local")
	if err != nil {
		t.Fatalf("AttachSkillToProject failed: %v", err)
	}
	if attached.Name != "lint" {
		t.Fatalf("attached skill name = %q, want %q", attached.Name, "lint")
	}

	targetPath := filepath.Join(projectPath, ".claude", "skills", "lint")
	if _, err := os.Lstat(targetPath); err != nil {
		t.Fatalf("expected materialized skill at %s: %v", targetPath, err)
	}

	manifest, err := LoadProjectSkillsManifest(projectPath)
	if err != nil {
		t.Fatalf("LoadProjectSkillsManifest failed: %v", err)
	}
	if len(manifest.Skills) != 1 {
		t.Fatalf("expected 1 manifest skill, got %d", len(manifest.Skills))
	}

	if _, err := AttachSkillToProject(projectPath, "claude", "lint", "local"); !errors.Is(err, ErrSkillAlreadyAttached) {
		t.Fatalf("expected ErrSkillAlreadyAttached, got %v", err)
	}

	removed, err := DetachSkillFromProject(projectPath, "lint", "local")
	if err != nil {
		t.Fatalf("DetachSkillFromProject failed: %v", err)
	}
	if removed.Name != "lint" {
		t.Fatalf("removed skill name = %q, want %q", removed.Name, "lint")
	}

	if _, err := os.Lstat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("expected target removed, got err=%v", err)
	}

	manifest, err = LoadProjectSkillsManifest(projectPath)
	if err != nil {
		t.Fatalf("LoadProjectSkillsManifest failed after detach: %v", err)
	}
	if len(manifest.Skills) != 0 {
		t.Fatalf("expected empty manifest after detach, got %d", len(manifest.Skills))
	}
}

func TestAttachSkillToProject_RejectsLegacyFileSkill(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath, err := os.MkdirTemp("", "agentdeck-legacy-file-source-*")
	if err != nil {
		t.Fatalf("failed to create source path: %v", err)
	}
	defer os.RemoveAll(sourcePath)

	if err := os.WriteFile(filepath.Join(sourcePath, "legacy.skill"), []byte("legacy"), 0o644); err != nil {
		t.Fatalf("failed to write legacy .skill file: %v", err)
	}

	if err := SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath, err := os.MkdirTemp("", "agentdeck-legacy-project-*")
	if err != nil {
		t.Fatalf("failed to create project path: %v", err)
	}
	defer os.RemoveAll(projectPath)

	_, err = AttachSkillToProject(projectPath, "claude", "legacy", "local")
	if !errors.Is(err, ErrSkillUnsupportedKind) {
		t.Fatalf("expected ErrSkillUnsupportedKind, got %v", err)
	}
}

func TestApplyProjectSkills_RejectsLegacyFileSkill(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath, err := os.MkdirTemp("", "agentdeck-legacy-file-source-*")
	if err != nil {
		t.Fatalf("failed to create source path: %v", err)
	}
	defer os.RemoveAll(sourcePath)

	legacyPath := filepath.Join(sourcePath, "legacy.skill")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o644); err != nil {
		t.Fatalf("failed to write legacy .skill file: %v", err)
	}

	projectPath, err := os.MkdirTemp("", "agentdeck-legacy-project-*")
	if err != nil {
		t.Fatalf("failed to create project path: %v", err)
	}
	defer os.RemoveAll(projectPath)

	err = ApplyProjectSkills(projectPath, "claude", []SkillCandidate{{
		ID:         "local/legacy",
		Name:       "legacy",
		Source:     "local",
		SourcePath: legacyPath,
		EntryName:  "legacy",
		Kind:       "file",
	}})
	if !errors.Is(err, ErrSkillUnsupportedKind) {
		t.Fatalf("expected ErrSkillUnsupportedKind, got %v", err)
	}
}

func TestAttachSkillToProject_RematerializesBrokenSymlink(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath, err := os.MkdirTemp("", "agentdeck-broken-link-source-*")
	if err != nil {
		t.Fatalf("failed to create source path: %v", err)
	}
	defer os.RemoveAll(sourcePath)

	writeSkillDir(t, sourcePath, "lint", "lint", "Linting best practices")
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath, err := os.MkdirTemp("", "agentdeck-broken-link-project-*")
	if err != nil {
		t.Fatalf("failed to create project path: %v", err)
	}
	defer os.RemoveAll(projectPath)

	if _, err := AttachSkillToProject(projectPath, "claude", "lint", "local"); err != nil {
		t.Fatalf("initial attach failed: %v", err)
	}

	targetPath := filepath.Join(projectPath, ".claude", "skills", "lint")
	if err := os.RemoveAll(targetPath); err != nil {
		t.Fatalf("failed to remove target: %v", err)
	}
	if err := os.Symlink("missing-target", targetPath); err != nil {
		t.Fatalf("failed to create broken symlink: %v", err)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("expected broken symlink, stat err=%v", err)
	}

	if _, err := AttachSkillToProject(projectPath, "claude", "lint", "local"); err != nil {
		t.Fatalf("reattach should rematerialize broken link, got %v", err)
	}

	if _, err := os.Stat(filepath.Join(targetPath, "SKILL.md")); err != nil {
		t.Fatalf("expected rematerialized skill content, got %v", err)
	}
}

func TestMaterializeSkill_SymlinkedTargetPathCreatesReadableTarget(t *testing.T) {
	root := t.TempDir()

	sourceRoot := filepath.Join(root, "source")
	sourcePath := writeSkillDir(t, sourceRoot, "lint", "lint", "Linting best practices")

	realBase := filepath.Join(root, "real", "nested", "path")
	if err := os.MkdirAll(realBase, 0o755); err != nil {
		t.Fatalf("failed to create real base path: %v", err)
	}

	aliasBase := filepath.Join(root, "alias")
	if err := os.Symlink(realBase, aliasBase); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	targetPath := filepath.Join(aliasBase, "project", ".claude", "skills", "lint")
	mode, err := materializeSkill(sourcePath, targetPath)
	if err != nil {
		t.Fatalf("materializeSkill failed: %v", err)
	}
	if mode != "symlink" && mode != "copy" {
		t.Fatalf("unexpected materialize mode: %s", mode)
	}

	if _, err := os.Stat(filepath.Join(targetPath, "SKILL.md")); err != nil {
		t.Fatalf("expected readable materialized target, got %v", err)
	}
}

func TestApplyProjectSkills_SyncsAttachments(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath, err := os.MkdirTemp("", "agentdeck-apply-source-*")
	if err != nil {
		t.Fatalf("failed to create source path: %v", err)
	}
	defer os.RemoveAll(sourcePath)

	writeSkillDir(t, sourcePath, "one", "one", "Skill one")
	writeSkillDir(t, sourcePath, "two", "two", "Skill two")

	if err := SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath, err := os.MkdirTemp("", "agentdeck-apply-project-*")
	if err != nil {
		t.Fatalf("failed to create project path: %v", err)
	}
	defer os.RemoveAll(projectPath)

	one, err := ResolveSkillCandidate("one", "local")
	if err != nil {
		t.Fatalf("ResolveSkillCandidate(one) failed: %v", err)
	}
	two, err := ResolveSkillCandidate("two", "local")
	if err != nil {
		t.Fatalf("ResolveSkillCandidate(two) failed: %v", err)
	}

	if err := ApplyProjectSkills(projectPath, "claude", []SkillCandidate{*one}); err != nil {
		t.Fatalf("ApplyProjectSkills(one) failed: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(projectPath, ".claude", "skills", "one")); err != nil {
		t.Fatalf("expected skill one materialized: %v", err)
	}

	if err := ApplyProjectSkills(projectPath, "claude", []SkillCandidate{*two}); err != nil {
		t.Fatalf("ApplyProjectSkills(two) failed: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(projectPath, ".claude", "skills", "one")); !os.IsNotExist(err) {
		t.Fatalf("expected skill one removed, err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(projectPath, ".claude", "skills", "two")); err != nil {
		t.Fatalf("expected skill two materialized: %v", err)
	}

	manifest, err := LoadProjectSkillsManifest(projectPath)
	if err != nil {
		t.Fatalf("LoadProjectSkillsManifest failed: %v", err)
	}
	if len(manifest.Skills) != 1 || manifest.Skills[0].Name != "two" {
		t.Fatalf("expected manifest to contain only 'two', got %+v", manifest.Skills)
	}
}
