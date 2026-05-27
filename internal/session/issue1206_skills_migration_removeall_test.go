package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Security: skill-migration RemoveAll containment (audit M3, sec-secrets-REPORT.md).
//
// The migration branches in attachSkillCandidate / reconcile call os.RemoveAll
// on a path resolved from the per-project skills manifest JSON (existing.TargetPath).
// A tampered/corrupted manifest with an absolute path or "../../.." would delete
// outside the managed project-skills dir — the same unguarded-RemoveAll footgun
// class as #1200. The sibling removeAttachmentTarget already gates RemoveAll
// behind managedProjectSkillsDirForTarget + isContainedIn; safeRemoveManagedTarget
// is that guard, shared by every migration RemoveAll. These tests pin it.

// TestSafeRemoveManagedTarget_RefusesAbsoluteNonManagedPath proves an absolute
// path outside the managed skills dir is REFUSED and NOT removed.
func TestSafeRemoveManagedTarget_RefusesAbsoluteNonManagedPath(t *testing.T) {
	projectPath := t.TempDir()
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(sentinel, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	// Manifest TargetPath is an absolute path pointing at the victim dir.
	err := safeRemoveManagedTarget(projectPath, outside)
	if err == nil {
		t.Fatalf("expected refusal for non-managed absolute path, got nil error")
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Fatalf("sentinel was removed despite refusal (M3 regression): %v", statErr)
	}
}

// TestSafeRemoveManagedTarget_RefusesTraversalEscape proves a "../" traversal
// that escapes the project root is REFUSED and NOT removed.
func TestSafeRemoveManagedTarget_RefusesTraversalEscape(t *testing.T) {
	base := t.TempDir()
	projectPath := filepath.Join(base, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	victimDir := filepath.Join(base, "victim")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	sentinel := filepath.Join(victimDir, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	// Relative traversal that resolves to ../victim, outside the project dir
	// and outside any managed skills dir.
	err := safeRemoveManagedTarget(projectPath, filepath.Join("..", "victim"))
	if err == nil {
		t.Fatalf("expected refusal for traversal escape, got nil error")
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Fatalf("sentinel was removed despite refusal (M3 regression): %v", statErr)
	}
}

// TestSafeRemoveManagedTarget_RemovesManagedPath proves the happy path: a target
// that IS inside a managed project-skills dir is removed.
func TestSafeRemoveManagedTarget_RemovesManagedPath(t *testing.T) {
	projectPath := t.TempDir()
	skillDir, ok := GetProjectSkillsDir("claude")
	if !ok {
		t.Fatalf("expected a managed skills dir for claude")
	}
	targetRel := buildProjectSkillTargetPath(skillDir, "my-skill")
	managed := resolveTargetPath(projectPath, targetRel)
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatalf("mkdir managed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managed, "SKILL.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := safeRemoveManagedTarget(projectPath, targetRel); err != nil {
		t.Fatalf("expected managed path removal to succeed, got: %v", err)
	}
	if _, statErr := os.Stat(managed); !os.IsNotExist(statErr) {
		t.Fatalf("managed path should have been removed, stat err = %v", statErr)
	}
}
