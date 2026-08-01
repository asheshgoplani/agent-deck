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

// TestResolveContainedTargetPath_RefusesSkillsDirSymlinkedInsideProject
// proves the adversarial variant where the symlink points back INSIDE the
// project: .claude/skills -> .. makes ".claude/skills/.git" physically the
// project's .git dir while still resolving inside the project root, so a
// resolved-prefix compare against the project alone would pass. The
// no-symlinked-ancestor-components rule refuses it and the .git content
// survives.
func TestResolveContainedTargetPath_RefusesSkillsDirSymlinkedInsideProject(t *testing.T) {
	projectPath := t.TempDir()

	gitDir := filepath.Join(projectPath, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	gitFile := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(gitFile, []byte("ref: refs/heads/main"), 0o600); err != nil {
		t.Fatalf("write .git/HEAD: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectPath, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.Symlink("..", filepath.Join(projectPath, ".claude", "skills")); err != nil {
		t.Fatalf("symlink skills dir: %v", err)
	}

	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, ".git")
	if _, err := resolveContainedTargetPath(projectPath, targetRel); err == nil {
		t.Fatalf("expected refusal for skills dir symlinked back into the project, got nil error")
	}
	if err := safeRemoveManagedTarget(projectPath, targetRel); err == nil {
		t.Fatalf("expected safeRemoveManagedTarget to refuse, got nil error")
	}
	if _, err := os.Stat(gitFile); err != nil {
		t.Fatalf(".git content was deleted through the symlinked skills dir: %v", err)
	}
}

// TestResolveContainedTargetPath_RefusesDanglingSymlinkedAncestor proves a
// DANGLING skills-dir symlink is refused rather than treated as an absent
// component: .claude/skills -> /outside/not-yet-created ENOENTs under
// EvalSymlinks, which an existence-based walk would misread as "nothing
// there yet" and pass lexically — but materializing through it would create
// the outside tree once the destination becomes creatable.
func TestResolveContainedTargetPath_RefusesDanglingSymlinkedAncestor(t *testing.T) {
	projectPath := t.TempDir()
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(projectPath, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	dangling := filepath.Join(outside, "not-yet-created")
	if err := os.Symlink(dangling, filepath.Join(projectPath, ".claude", "skills")); err != nil {
		t.Fatalf("symlink skills dir: %v", err)
	}

	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "victim")
	if _, err := resolveContainedTargetPath(projectPath, targetRel); err == nil {
		t.Fatalf("expected refusal for dangling symlinked skills dir, got nil error")
	}
}

// TestResolveContainedTargetPath_RefusesManagedDirItself proves the target
// must be a STRICT descendant: a tampered ".claude/skills/." cleans to the
// managed dir itself, and RemoveAll there would wipe the whole catalog.
func TestResolveContainedTargetPath_RefusesManagedDirItself(t *testing.T) {
	projectPath := t.TempDir()

	skillsDir := filepath.Join(projectPath, ".claude", "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "my-skill"), 0o755); err != nil {
		t.Fatalf("mkdir skills content: %v", err)
	}
	keepFile := filepath.Join(skillsDir, "my-skill", "SKILL.md")
	if err := os.WriteFile(keepFile, []byte("# my-skill"), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	for _, rel := range []string{
		projectClaudeSkillsDir,
		projectClaudeSkillsDir + "/.",
	} {
		if _, err := resolveContainedTargetPath(projectPath, rel); err == nil {
			t.Fatalf("expected refusal for managed-dir-itself target %q, got nil error", rel)
		}
		if err := safeRemoveManagedTarget(projectPath, rel); err == nil {
			t.Fatalf("expected safeRemoveManagedTarget to refuse %q, got nil error", rel)
		}
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("skills catalog content was wiped: %v", err)
	}
}

// TestMaterializeSkill_RefusesUnregisteredSource proves the SOURCE side of
// materialization is gated too (CodeQL go/path-injection alert 237): the
// destination is Root-confined, but the source path arrives from manifest or
// candidate data and is opened by path. A source outside every registered
// skill source root and outside the project's managed skills dirs is refused
// before any read.
func TestMaterializeSkill_RefusesUnregisteredSource(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	projectPath, err := os.MkdirTemp("", "agentdeck-1809-src-project-*")
	if err != nil {
		t.Fatalf("failed to create project path: %v", err)
	}
	defer os.RemoveAll(projectPath)

	outside, err := os.MkdirTemp("", "agentdeck-1809-src-outside-*")
	if err != nil {
		t.Fatalf("failed to create outside path: %v", err)
	}
	defer os.RemoveAll(outside)
	writeSkillDir(t, outside, "evil", "evil", "Not from a registered source")

	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "evil")
	if _, err := materializeSkill(projectPath, filepath.Join(outside, "evil"), targetRel); err == nil {
		t.Fatalf("expected materializeSkill to refuse a source outside registered skill sources")
	}
	if _, err := materializeSkillCopyOnly(projectPath, filepath.Join(outside, "evil"), targetRel); err == nil {
		t.Fatalf("expected materializeSkillCopyOnly to refuse a source outside registered skill sources")
	}
}

// TestIsContainedIn_RootBase proves containment works when the base is the
// filesystem root: the old base+PathSeparator string-prefix compare produced
// "//" as the required prefix, which no cleaned path carries, so every
// legitimate target under a root-based project was rejected. The
// filepath.Rel-based check accepts root-based targets and still refuses
// escapes.
func TestIsContainedIn_RootBase(t *testing.T) {
	root := string(os.PathSeparator)
	cases := []struct {
		base, target string
		want         bool
	}{
		{root, filepath.Join(root, ".claude", "skills"), true},
		{root, root, true},
		{filepath.Join(root, ".claude", "skills"), filepath.Join(root, ".claude", "skills", "my-skill"), true},
		{filepath.Join(root, ".claude", "skills"), filepath.Join(root, ".claude"), false},
		{filepath.Join(root, ".claude", "skills"), filepath.Join(root, "elsewhere"), false},
		{filepath.Join(root, "a"), filepath.Join(root, "ab"), false},
	}
	for _, c := range cases {
		if got := isContainedIn(c.base, c.target); got != c.want {
			t.Errorf("isContainedIn(%q, %q) = %v, want %v", c.base, c.target, got, c.want)
		}
	}
}

// TestResolveContainedTargetPath_RootProjectPath proves the full containment
// pipeline accepts a managed target for a project rooted at "/" (read-only:
// nothing is created; resolution walks up to the deepest existing ancestor).
func TestResolveContainedTargetPath_RootProjectPath(t *testing.T) {
	projectPath := string(os.PathSeparator)
	if _, err := os.Lstat(filepath.Join(projectPath, ".claude")); err == nil {
		t.Skip("/.claude exists on this host; skipping literal-root check")
	}
	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "my-skill")
	got, err := resolveContainedTargetPath(projectPath, targetRel)
	if err != nil {
		t.Fatalf("expected root-based managed target to be allowed, got: %v", err)
	}
	want := resolveTargetPath(projectPath, targetRel)
	if got != want {
		t.Fatalf("resolveContainedTargetPath = %q, want %q", got, want)
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

// --- Round-4 hardening: one pinned project descriptor, no reopen-by-name ---

// TestMaterialize_MigratesFromPoolSymlinkWhenSourceGone is the regression test
// for the migration path: the source being copied is the CURRENT managed
// entry, which for a pool-attached skill is a symlink whose destination lives
// outside the project. Reading it strictly inside the project skills root
// refused that escape and broke migration. The source resolver now follows a
// final-component symlink one hop and re-validates the DESTINATION as a
// registered source, so migration works while non-source destinations stay
// refused.
func TestMaterialize_MigratesFromPoolSymlinkWhenSourceGone(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	poolRoot := t.TempDir()
	writeSkillDir(t, poolRoot, "lint", "lint", "Linting best practices")
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"pool": {Path: poolRoot, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath := t.TempDir()
	attached, err := AttachSkillToProject(projectPath, "claude", "lint", "pool")
	if err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	claudeEntry := filepath.Join(projectPath, ".claude", "skills", "lint")
	info, err := os.Lstat(claudeEntry)
	if err != nil {
		t.Fatalf("lstat attached entry: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Skipf("attach fell back to copy mode (%s); symlink migration path not exercised", attached.Mode)
	}

	// Migrate to the .agents/skills dir by copying FROM the pool symlink,
	// exactly as the migration branch does when the recorded source is gone.
	targetRel := buildProjectSkillTargetPath(projectAgentsSkillsDir, "lint")
	if _, err := materializeSkillCopyOnly(projectPath, claudeEntry, targetRel); err != nil {
		t.Fatalf("migration from pool symlink failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectPath, ".agents", "skills", "lint", "SKILL.md")); err != nil {
		t.Fatalf("expected migrated skill content, got: %v", err)
	}
}

// TestApplyProjectSkills_MigratesPoolSymlinkWithStaleSourcePath drives the same
// regression end to end: a desired candidate whose recorded SourcePath no
// longer exists, migrating a pool-symlinked entry from .claude/skills to
// .agents/skills.
func TestApplyProjectSkills_MigratesPoolSymlinkWithStaleSourcePath(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	poolRoot := t.TempDir()
	writeSkillDir(t, poolRoot, "lint", "lint", "Linting best practices")
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"pool": {Path: poolRoot, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath := t.TempDir()
	if _, err := AttachSkillToProject(projectPath, "claude", "lint", "pool"); err != nil {
		t.Fatalf("attach failed: %v", err)
	}

	stale := SkillCandidate{
		ID:         buildSkillID("pool", "lint"),
		Name:       "lint",
		Source:     "pool",
		SourcePath: filepath.Join(poolRoot, "gone-away"),
		EntryName:  "lint",
		Kind:       "dir",
	}
	if err := ApplyProjectSkills(projectPath, "codex", []SkillCandidate{stale}); err != nil {
		t.Fatalf("apply with stale source path failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectPath, ".agents", "skills", "lint", "SKILL.md")); err != nil {
		t.Fatalf("expected migrated skill content, got: %v", err)
	}
}

// TestManifestWrites_RefuseSymlinkedAgentDeckDir proves manifest I/O goes
// through the pinned project descriptor: a repo shipping ".agent-deck" as a
// symlink out of the project must not redirect the manifest write.
func TestManifestWrites_RefuseSymlinkedAgentDeckDir(t *testing.T) {
	projectPath := t.TempDir()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(projectPath, projectSkillsDirName)); err != nil {
		t.Fatalf("symlink .agent-deck: %v", err)
	}

	manifest := &ProjectSkillsManifest{Skills: []ProjectSkillAttachment{{
		ID: "pool/lint", Name: "lint", Source: "pool", EntryName: "lint",
		TargetPath: buildProjectSkillTargetPath(projectClaudeSkillsDir, "lint"),
	}}}
	if err := SaveProjectSkillsManifest(projectPath, manifest); err == nil {
		t.Fatalf("expected manifest save to refuse a symlinked .agent-deck dir")
	}
	if _, err := LoadProjectSkillsManifest(projectPath); err == nil {
		t.Fatalf("expected manifest load to refuse a symlinked .agent-deck dir")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("manifest write escaped into %s: %v", outside, entries)
	}
}

// TestMaterialize_RefusesSymlinkedProjectSourceDir proves project-local source
// dirs are validated BEFORE being opened: ".agents/skills -> /outside" must be
// refused as a materialization source, not anchored and read through.
func TestMaterialize_RefusesSymlinkedProjectSourceDir(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	poolRoot := t.TempDir()
	writeSkillDir(t, poolRoot, "lint", "lint", "Linting best practices")
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"pool": {Path: poolRoot, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath := t.TempDir()
	outside := t.TempDir()
	writeSkillDir(t, outside, "evil", "evil", "Planted outside the project")

	if err := os.MkdirAll(filepath.Join(projectPath, ".agents"), 0o755); err != nil {
		t.Fatalf("mkdir .agents: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(projectPath, ".agents", "skills")); err != nil {
		t.Fatalf("symlink .agents/skills: %v", err)
	}

	source := filepath.Join(projectPath, ".agents", "skills", "evil")
	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "evil")
	if _, err := materializeSkillCopyOnly(projectPath, source, targetRel); err == nil {
		t.Fatalf("expected refusal for source under a symlinked project skills dir")
	}
}
