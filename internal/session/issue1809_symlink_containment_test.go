package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Security: symlink-aware containment for skills-catalog targets (Codex P1 on
// PR #1809). resolveContainedTargetPath used filepath.Clean + string prefix
// only, so a repo shipping .claude/skills (or any ancestor component) as a
// symlink pointing outside the project string-passed containment while the
// target physically lived at the symlink destination — letting
// safeRemoveManagedTarget's os.RemoveAll delete files there and letting
// materialization create files there. Containment now compares
// symlink-RESOLVED ancestors; the final target component is deliberately left
// unresolved so pool-attached skills (symlinks inside the managed dir) keep
// working.

// TestResolveContainedTargetPath_RefusesSymlinkedSkillsDirEscape proves a
// managed skills dir that is itself a symlink out of the project is refused.
func TestResolveContainedTargetPath_RefusesSymlinkedSkillsDirEscape(t *testing.T) {
	projectPath := t.TempDir()
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(projectPath, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(projectPath, ".claude", "skills")); err != nil {
		t.Fatalf("symlink skills dir: %v", err)
	}

	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "victim")
	if _, err := resolveContainedTargetPath(projectPath, targetRel); err == nil {
		t.Fatalf("expected refusal for symlinked skills dir escaping the project, got nil error")
	}
}

// TestResolveContainedTargetPath_RefusesSymlinkedAncestorEscape proves an
// intermediate ancestor symlink (.claude -> outside) is refused too.
func TestResolveContainedTargetPath_RefusesSymlinkedAncestorEscape(t *testing.T) {
	projectPath := t.TempDir()
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(outside, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir outside skills: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(projectPath, ".claude")); err != nil {
		t.Fatalf("symlink .claude: %v", err)
	}

	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "victim")
	if _, err := resolveContainedTargetPath(projectPath, targetRel); err == nil {
		t.Fatalf("expected refusal for symlinked ancestor escaping the project, got nil error")
	}
}

// TestSafeRemoveManagedTarget_DoesNotFollowSymlinkedSkillsDir proves the
// physical outcome: with .claude/skills symlinked outside the project, remove
// is refused and the file at the symlink destination survives.
func TestSafeRemoveManagedTarget_DoesNotFollowSymlinkedSkillsDir(t *testing.T) {
	projectPath := t.TempDir()
	outside := t.TempDir()

	victimDir := filepath.Join(outside, "victim")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	victimFile := filepath.Join(victimDir, "precious.txt")
	if err := os.WriteFile(victimFile, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write victim file: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(projectPath, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(projectPath, ".claude", "skills")); err != nil {
		t.Fatalf("symlink skills dir: %v", err)
	}

	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "victim")
	if err := safeRemoveManagedTarget(projectPath, targetRel); err == nil {
		t.Fatalf("expected safeRemoveManagedTarget to refuse a symlinked skills dir")
	}
	if _, err := os.Stat(victimFile); err != nil {
		t.Fatalf("victim file outside the project was touched: %v", err)
	}
}

// TestAttachSkillCandidate_RefusesSymlinkedSkillsDirOnMaterialize proves the
// materialization path refuses to create anything through a skills dir that
// symlinks out of the project.
func TestAttachSkillCandidate_RefusesSymlinkedSkillsDirOnMaterialize(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath, err := os.MkdirTemp("", "agentdeck-1809-source-*")
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

	projectPath, err := os.MkdirTemp("", "agentdeck-1809-project-*")
	if err != nil {
		t.Fatalf("failed to create project path: %v", err)
	}
	defer os.RemoveAll(projectPath)

	outside, err := os.MkdirTemp("", "agentdeck-1809-outside-*")
	if err != nil {
		t.Fatalf("failed to create outside path: %v", err)
	}
	defer os.RemoveAll(outside)

	if err := os.MkdirAll(filepath.Join(projectPath, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(projectPath, ".claude", "skills")); err != nil {
		t.Fatalf("symlink skills dir: %v", err)
	}

	if _, err := AttachSkillToProject(projectPath, "claude", "lint", "local"); err == nil {
		t.Fatalf("expected AttachSkillToProject to refuse a symlinked skills dir")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("materialization escaped into %s: %v", outside, entries)
	}
}

// TestResolveContainedTargetPath_AllowsFinalComponentSymlink proves the
// pool-attach pattern keeps working: the target ITSELF being a symlink inside
// a real managed dir is fine (RemoveAll removes the link, not the
// destination).
func TestResolveContainedTargetPath_AllowsFinalComponentSymlink(t *testing.T) {
	projectPath := t.TempDir()
	pool := t.TempDir()

	poolSkill := filepath.Join(pool, "my-skill")
	if err := os.MkdirAll(poolSkill, 0o755); err != nil {
		t.Fatalf("mkdir pool skill: %v", err)
	}
	keepFile := filepath.Join(poolSkill, "SKILL.md")
	if err := os.WriteFile(keepFile, []byte("# my-skill"), 0o600); err != nil {
		t.Fatalf("write pool SKILL.md: %v", err)
	}

	skillsDir := filepath.Join(projectPath, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(poolSkill, filepath.Join(skillsDir, "my-skill")); err != nil {
		t.Fatalf("symlink pool skill: %v", err)
	}

	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "my-skill")
	got, err := resolveContainedTargetPath(projectPath, targetRel)
	if err != nil {
		t.Fatalf("expected final-component symlink to be allowed, got: %v", err)
	}
	want := resolveTargetPath(projectPath, targetRel)
	if got != want {
		t.Fatalf("resolveContainedTargetPath = %q, want %q", got, want)
	}

	// Detach-style removal removes the LINK, never the pool destination.
	if err := safeRemoveManagedTarget(projectPath, targetRel); err != nil {
		t.Fatalf("safeRemoveManagedTarget on pool symlink failed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(skillsDir, "my-skill")); !os.IsNotExist(err) {
		t.Fatalf("expected symlink to be removed, got: %v", err)
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("pool skill content was deleted through the symlink: %v", err)
	}
}

// TestResolveContainedTargetPath_AllowsNonExistingTargetUnderRealDir proves
// the creation path still works when neither the target nor the managed dir
// exists yet (first attach creates them).
func TestResolveContainedTargetPath_AllowsNonExistingTargetUnderRealDir(t *testing.T) {
	projectPath := t.TempDir()

	// Managed dir does not exist at all yet.
	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "brand-new")
	got, err := resolveContainedTargetPath(projectPath, targetRel)
	if err != nil {
		t.Fatalf("expected non-existing target under non-existing managed dir to be allowed, got: %v", err)
	}
	want := resolveTargetPath(projectPath, targetRel)
	if got != want {
		t.Fatalf("resolveContainedTargetPath = %q, want %q", got, want)
	}

	// Managed dir exists, target does not.
	if err := os.MkdirAll(filepath.Join(projectPath, ".claude", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if _, err := resolveContainedTargetPath(projectPath, targetRel); err != nil {
		t.Fatalf("expected non-existing target under existing managed dir to be allowed, got: %v", err)
	}
}
