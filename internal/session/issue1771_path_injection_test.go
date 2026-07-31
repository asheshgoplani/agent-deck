package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Security: skills-catalog read/write path containment (CodeQL go/path-injection,
// issue #1771). safeRemoveManagedTarget already gated os.RemoveAll behind
// managedProjectSkillsDirForTarget + isContainedIn (Audit M3); that guard was
// missing on the os.Stat/os.Lstat/materializeSkill sinks in attachSkillCandidate
// and ApplyProjectSkills, which resolved a manifest- or candidate-derived
// TargetPath via the unchecked resolveTargetPath and used the result directly.
// resolveContainedTargetPath closes that gap by applying the same containment
// check to every sink, not just removal.

func TestResolveContainedTargetPath_RefusesTraversalEscape(t *testing.T) {
	base := t.TempDir()
	projectPath := filepath.Join(base, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	if _, err := resolveContainedTargetPath(projectPath, filepath.Join("..", "evil")); err == nil {
		t.Fatalf("expected refusal for traversal escape, got nil error")
	}
}

func TestResolveContainedTargetPath_RefusesAbsoluteNonManagedPath(t *testing.T) {
	projectPath := t.TempDir()
	outside := t.TempDir()

	if _, err := resolveContainedTargetPath(projectPath, outside); err == nil {
		t.Fatalf("expected refusal for non-managed absolute path, got nil error")
	}
}

func TestResolveContainedTargetPath_AllowsManagedPath(t *testing.T) {
	projectPath := t.TempDir()
	skillDir, ok := GetProjectSkillsDir("claude")
	if !ok {
		t.Fatalf("expected a managed skills dir for claude")
	}
	targetRel := buildProjectSkillTargetPath(skillDir, "my-skill")

	got, err := resolveContainedTargetPath(projectPath, targetRel)
	if err != nil {
		t.Fatalf("expected managed path to be allowed, got: %v", err)
	}
	want := resolveTargetPath(projectPath, targetRel)
	if got != want {
		t.Fatalf("resolveContainedTargetPath = %q, want %q", got, want)
	}
}

// TestAttachSkillCandidate_RefusesTamperedManifestTargetOnMigration proves the
// migration codepath in attachSkillCandidate (an existing manifest entry whose
// TargetPath no longer matches the tool's skill dir) refuses to Stat/Lstat/
// materialize against a manifest TargetPath that escapes the project root,
// instead of resolving it unchecked the way it did before #1771.
func TestAttachSkillCandidate_RefusesTamperedManifestTargetOnMigration(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	sourcePath, err := os.MkdirTemp("", "agentdeck-1771-source-*")
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

	projectPath, err := os.MkdirTemp("", "agentdeck-1771-project-*")
	if err != nil {
		t.Fatalf("failed to create project path: %v", err)
	}
	defer os.RemoveAll(projectPath)

	// Seed a manifest as if it had been tampered with (or corrupted) to point
	// its TargetPath outside any managed project-skills dir.
	tampered := ProjectSkillAttachment{
		ID:         buildSkillID("local", "lint"),
		Name:       "lint",
		Source:     "local",
		SourcePath: filepath.Join(sourcePath, "lint"),
		EntryName:  "lint",
		TargetPath: filepath.Join("..", "evil"),
		Mode:       "copy",
	}
	if err := SaveProjectSkillsManifest(projectPath, &ProjectSkillsManifest{
		Skills: []ProjectSkillAttachment{tampered},
	}); err != nil {
		t.Fatalf("SaveProjectSkillsManifest failed: %v", err)
	}

	if _, err := AttachSkillToProject(projectPath, "claude", "lint", "local"); err == nil {
		t.Fatalf("expected AttachSkillToProject to refuse a tampered out-of-tree TargetPath")
	}
}
