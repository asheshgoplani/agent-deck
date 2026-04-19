package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectSkillsDirMapping(t *testing.T) {
	cases := []struct {
		tool        string
		wantDir     string
		wantOK      bool
		wantRestart bool
	}{
		{tool: "claude", wantDir: ".claude/skills", wantOK: true, wantRestart: true},
		{tool: "gemini", wantDir: ".agents/skills", wantOK: true, wantRestart: true},
		{tool: "codex", wantDir: ".agents/skills", wantOK: true, wantRestart: true},
		{tool: "pi", wantDir: ".agents/skills", wantOK: true, wantRestart: false},
		{tool: "shell", wantDir: "", wantOK: false, wantRestart: false},
	}

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			got, ok := GetProjectSkillsDir(tc.tool)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantDir, got)
			require.Equal(t, tc.wantOK, SupportsProjectSkills(tc.tool))
			require.Equal(t, tc.wantRestart, ShouldRestartProjectSkills(tc.tool))
		})
	}
}

// TestSkillRuntime_AttachedSkillIsReadable verifies that after AttachSkillToProject,
// the materialized path contains a readable SKILL.md with the expected content.
func TestSkillRuntime_AttachedSkillIsReadable(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	writeSkillDir(t, sourcePath, "my-skill", "my-skill", "A test skill")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	projectPath := t.TempDir()

	attachment, err := AttachSkillToProject(projectPath, "claude", "my-skill", "local")
	require.NoError(t, err, "AttachSkillToProject should succeed")
	require.NotNil(t, attachment)
	require.Equal(t, ".claude/skills/my-skill", attachment.TargetPath)

	targetDir := resolveTargetPath(projectPath, attachment.TargetPath)
	skillMDPath := filepath.Join(targetDir, "SKILL.md")

	content, err := os.ReadFile(skillMDPath)
	require.NoError(t, err, "SKILL.md should be readable at materialized path")
	assert.Contains(t, string(content), "my-skill", "SKILL.md should contain the skill name")
}

func TestSkillRuntime_AttachUsesAgentSkillsDirForGemini(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	writeSkillDir(t, sourcePath, "my-skill", "my-skill", "A test skill")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	projectPath := t.TempDir()

	attachment, err := AttachSkillToProject(projectPath, "gemini", "my-skill", "local")
	require.NoError(t, err)
	require.Equal(t, ".agents/skills/my-skill", attachment.TargetPath)

	_, err = os.Stat(filepath.Join(projectPath, ".agents", "skills", "my-skill", "SKILL.md"))
	require.NoError(t, err)
}

// TestSkillRuntime_ApplyCreatesReadableSkills verifies that ApplyProjectSkills
// creates a runtime-specific project skills directory with readable SKILL.md for each skill.
func TestSkillRuntime_ApplyCreatesReadableSkills(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	writeSkillDir(t, sourcePath, "alpha", "alpha", "Alpha skill")
	writeSkillDir(t, sourcePath, "beta", "beta", "Beta skill")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	alphaCandidate, err := ResolveSkillCandidate("alpha", "local")
	require.NoError(t, err)
	betaCandidate, err := ResolveSkillCandidate("beta", "local")
	require.NoError(t, err)

	projectPath := t.TempDir()

	err = ApplyProjectSkills(projectPath, "gemini", []SkillCandidate{*alphaCandidate, *betaCandidate})
	require.NoError(t, err, "ApplyProjectSkills should succeed")

	skillsDir := GetProjectSkillsPath(projectPath, "gemini")
	entries, err := os.ReadDir(skillsDir)
	require.NoError(t, err, ".agents/skills directory should exist")
	assert.Len(t, entries, 2, "should have 2 skill directories")

	for _, entry := range entries {
		skillMDPath := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		content, err := os.ReadFile(skillMDPath)
		assert.NoError(t, err, "SKILL.md should be readable for %s", entry.Name())
		assert.NotEmpty(t, content, "SKILL.md should have content for %s", entry.Name())
	}
}

func TestSkillRuntime_ApplyMigratesBetweenManagedRoots(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	writeSkillDir(t, sourcePath, "alpha", "alpha", "Alpha skill")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	projectPath := t.TempDir()
	_, err := AttachSkillToProject(projectPath, "claude", "alpha", "local")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(projectPath, ".claude", "skills", "alpha", "SKILL.md"))
	require.NoError(t, err)

	alphaCandidate, err := ResolveSkillCandidate("alpha", "local")
	require.NoError(t, err)

	err = ApplyProjectSkills(projectPath, "gemini", []SkillCandidate{*alphaCandidate})
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(projectPath, ".agents", "skills", "alpha", "SKILL.md"))
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(projectPath, ".claude", "skills", "alpha"))
	require.True(t, os.IsNotExist(err), "old Claude-managed target should be removed")

	attached, err := GetAttachedProjectSkills(projectPath)
	require.NoError(t, err)
	require.Len(t, attached, 1)
	assert.Equal(t, ".agents/skills/alpha", attached[0].TargetPath)
}

func TestSkillRuntime_AttachMigratesFromExistingTargetWhenSourceUnavailable(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	skillSourcePath := writeSkillDir(t, sourcePath, "alpha", "alpha", "Alpha skill")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	projectPath := t.TempDir()
	attachment, err := AttachSkillToProject(projectPath, "claude", "alpha", "local")
	require.NoError(t, err)

	currentTargetPath := resolveTargetPath(projectPath, attachment.TargetPath)
	if attachment.Mode == "symlink" {
		require.NoError(t, os.RemoveAll(currentTargetPath))
		require.NoError(t, copyDir(skillSourcePath, currentTargetPath))
	}

	require.NoError(t, os.RemoveAll(skillSourcePath))

	attachment, err = attachSkillCandidate(projectPath, "gemini", SkillCandidate{
		ID:         "local/alpha",
		Name:       "alpha",
		Source:     "local",
		SourcePath: skillSourcePath,
		EntryName:  "alpha",
		Kind:       "dir",
	})
	require.NoError(t, err)
	assert.Equal(t, ".agents/skills/alpha", attachment.TargetPath)

	content, err := os.ReadFile(filepath.Join(projectPath, ".agents", "skills", "alpha", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Alpha skill")
	_, err = os.Lstat(filepath.Join(projectPath, ".claude", "skills", "alpha"))
	require.True(t, os.IsNotExist(err), "old Claude-managed target should be removed after migration")
}

func TestSkillRuntime_ApplyMigratesFromExistingTargetWhenSourceUnavailable(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	skillSourcePath := writeSkillDir(t, sourcePath, "alpha", "alpha", "Alpha skill")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	projectPath := t.TempDir()
	attachment, err := AttachSkillToProject(projectPath, "claude", "alpha", "local")
	require.NoError(t, err)

	currentTargetPath := resolveTargetPath(projectPath, attachment.TargetPath)
	if attachment.Mode == "symlink" {
		require.NoError(t, os.RemoveAll(currentTargetPath))
		require.NoError(t, copyDir(skillSourcePath, currentTargetPath))
	}

	require.NoError(t, os.RemoveAll(skillSourcePath))

	err = ApplyProjectSkills(projectPath, "gemini", []SkillCandidate{{
		ID:         "local/alpha",
		Name:       "alpha",
		Source:     "local",
		SourcePath: skillSourcePath,
		EntryName:  "alpha",
		Kind:       "dir",
	}})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(projectPath, ".agents", "skills", "alpha", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Alpha skill")
	_, err = os.Lstat(filepath.Join(projectPath, ".claude", "skills", "alpha"))
	require.True(t, os.IsNotExist(err), "old Claude-managed target should be removed after migration")
}

func TestSkillRuntime_DetachRemovesAgentSkillsTarget(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	writeSkillDir(t, sourcePath, "alpha", "alpha", "Alpha skill")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	projectPath := t.TempDir()
	_, err := AttachSkillToProject(projectPath, "pi", "alpha", "local")
	require.NoError(t, err)

	removed, err := DetachSkillFromProject(projectPath, "alpha", "local")
	require.NoError(t, err)
	assert.Equal(t, ".agents/skills/alpha", removed.TargetPath)

	_, err = os.Lstat(filepath.Join(projectPath, ".agents", "skills", "alpha"))
	require.True(t, os.IsNotExist(err), "detached target should be removed from .agents/skills")
}

// TestSkillRuntime_DiscoveryFindsRegisteredSkills verifies that ListAvailableSkills
// discovers all skills from a registered source.
func TestSkillRuntime_DiscoveryFindsRegisteredSkills(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	writeSkillDir(t, sourcePath, "skill-one", "skill-one", "First skill")
	writeSkillDir(t, sourcePath, "skill-two", "skill-two", "Second skill")
	writeSkillDir(t, sourcePath, "skill-three", "skill-three", "Third skill")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"test-source": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	skills, err := ListAvailableSkills()
	require.NoError(t, err, "ListAvailableSkills should succeed")

	names := make(map[string]bool, len(skills))
	for _, s := range skills {
		names[s.Name] = true
	}

	assert.True(t, names["skill-one"], "should discover skill-one")
	assert.True(t, names["skill-two"], "should discover skill-two")
	assert.True(t, names["skill-three"], "should discover skill-three")
}

// TestSkillRuntime_ResolveByName verifies that ResolveSkillCandidate correctly
// finds skills by name when the source is specified.
func TestSkillRuntime_ResolveByName(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourceA := t.TempDir()
	sourceB := t.TempDir()
	writeSkillDir(t, sourceA, "alpha", "alpha", "Alpha from source A")
	writeSkillDir(t, sourceB, "beta", "beta", "Beta from source B")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"src1": {Path: sourceA, Enabled: boolPtr(true)},
		"src2": {Path: sourceB, Enabled: boolPtr(true)},
	}))

	resolved, err := ResolveSkillCandidate("alpha", "src1")
	require.NoError(t, err, "should resolve alpha from src1")
	assert.Equal(t, "alpha", resolved.Name)
	assert.Equal(t, "src1", resolved.Source)

	resolved, err = ResolveSkillCandidate("beta", "src2")
	require.NoError(t, err, "should resolve beta from src2")
	assert.Equal(t, "beta", resolved.Name)
	assert.Equal(t, "src2", resolved.Source)
}

// TestSkillRuntime_PoolSkillWithoutScripts verifies that a pool skill with only
// SKILL.md (no scripts/ subdirectory) can be attached and read without errors.
// Pool skills like gsd-conductor often have only references/, no scripts/.
func TestSkillRuntime_PoolSkillWithoutScripts(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()

	poolSkillDir := filepath.Join(sourcePath, "gsd-conductor")
	require.NoError(t, os.MkdirAll(poolSkillDir, 0o755))
	skillContent := "---\nname: gsd-conductor\ndescription: GSD orchestration conductor\n---\n\n# GSD Conductor\n\nOrchestrates multi-agent workflows.\n"
	require.NoError(t, os.WriteFile(filepath.Join(poolSkillDir, "SKILL.md"), []byte(skillContent), 0o644))

	refsDir := filepath.Join(poolSkillDir, "references")
	require.NoError(t, os.MkdirAll(refsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(refsDir, "checkpoints.md"), []byte("# Checkpoints\n"), 0o644))

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"pool": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	projectPath := t.TempDir()

	attachment, err := AttachSkillToProject(projectPath, "claude", "gsd-conductor", "pool")
	require.NoError(t, err, "pool skill without scripts/ should attach successfully")
	require.NotNil(t, attachment)

	targetDir := resolveTargetPath(projectPath, attachment.TargetPath)
	content, err := os.ReadFile(filepath.Join(targetDir, "SKILL.md"))
	require.NoError(t, err, "SKILL.md should be readable from materialized pool skill")
	assert.Contains(t, string(content), "gsd-conductor")

	refContent, err := os.ReadFile(filepath.Join(targetDir, "references", "checkpoints.md"))
	require.NoError(t, err, "references/ should be materialized alongside SKILL.md")
	assert.Contains(t, string(refContent), "Checkpoints")
}

// TestSkillRuntime_ResolveSkillContent verifies that after attaching a skill,
// the SKILL.md content contains expected frontmatter (name, description).
func TestSkillRuntime_ResolveSkillContent(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	writeSkillDir(t, sourcePath, "code-review", "code-review", "Automated code review rules")

	require.NoError(t, SaveSkillSources(map[string]SkillSourceDef{
		"local": {Path: sourcePath, Enabled: boolPtr(true)},
	}))

	projectPath := t.TempDir()

	attachment, err := AttachSkillToProject(projectPath, "claude", "code-review", "local")
	require.NoError(t, err)

	targetDir := resolveTargetPath(projectPath, attachment.TargetPath)
	content, err := os.ReadFile(filepath.Join(targetDir, "SKILL.md"))
	require.NoError(t, err, "SKILL.md should be readable")

	text := string(content)

	require.True(t, strings.HasPrefix(text, "---\n"), "SKILL.md should start with YAML frontmatter")
	rest := text[4:]
	endIdx := strings.Index(rest, "\n---")
	require.True(t, endIdx >= 0, "SKILL.md should have closing frontmatter delimiter")

	frontmatter := rest[:endIdx]
	assert.Contains(t, frontmatter, "name: code-review", "frontmatter should contain skill name")
	assert.Contains(t, frontmatter, "description: Automated code review rules", "frontmatter should contain description")
}
