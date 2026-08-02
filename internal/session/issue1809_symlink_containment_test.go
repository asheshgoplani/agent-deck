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

// --- Round-5 hardening: pinned directory descriptors ---

// TestOpenPinnedDir_RefusesSymlinkedComponentInsideProject proves the pinning
// walk refuses a managed-dir component that is a symlink even when it stays
// INSIDE the project (".claude/skills -> .."), which os.Root alone permits
// because it never leaves the root. Removal and materialization must not be
// able to reach the project root that way.
func TestOpenPinnedDir_RefusesSymlinkedComponentInsideProject(t *testing.T) {
	projectPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectPath, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.Symlink("..", filepath.Join(projectPath, ".claude", "skills")); err != nil {
		t.Fatalf("symlink skills dir: %v", err)
	}

	p, err := openProjectRoot(projectPath)
	if err != nil {
		t.Fatalf("openProjectRoot: %v", err)
	}
	defer p.Close()

	if _, _, err := p.openPinnedDir(filepath.FromSlash(projectClaudeSkillsDir), false); err == nil {
		t.Fatalf("expected pinned open to refuse a symlinked component")
	}
	if _, _, err := p.openPinnedDir(filepath.FromSlash(projectClaudeSkillsDir), true); err == nil {
		t.Fatalf("expected pinned open (create) to refuse a symlinked component")
	}
}

// TestOpenTargetParent_AllowsRealManagedDirAndPoolLink proves the pinned path
// still serves the legitimate flows: a real managed dir is pinned and created
// on demand, and a final-component pool symlink inside it is addressed by name.
func TestOpenTargetParent_AllowsRealManagedDirAndPoolLink(t *testing.T) {
	projectPath := t.TempDir()
	pool := t.TempDir()
	poolSkill := filepath.Join(pool, "my-skill")
	if err := os.MkdirAll(poolSkill, 0o755); err != nil {
		t.Fatalf("mkdir pool skill: %v", err)
	}

	p, err := openProjectRoot(projectPath)
	if err != nil {
		t.Fatalf("openProjectRoot: %v", err)
	}
	defer p.Close()

	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "my-skill")
	parent, name, _, err := p.openTargetParent(targetRel, true)
	if err != nil {
		t.Fatalf("expected managed dir to be created and pinned, got: %v", err)
	}
	if name != "my-skill" {
		t.Fatalf("entry name = %q, want my-skill", name)
	}
	if err := parent.Symlink(poolSkill, name); err != nil {
		t.Fatalf("pool symlink creation failed: %v", err)
	}
	parent.Close()

	if err := p.removeManagedTarget(targetRel); err != nil {
		t.Fatalf("removal of pool symlink failed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(projectPath, ".claude", "skills", "my-skill")); !os.IsNotExist(err) {
		t.Fatalf("expected the link to be removed, got: %v", err)
	}
	if _, err := os.Stat(poolSkill); err != nil {
		t.Fatalf("pool destination was deleted through the link: %v", err)
	}
}

// TestSaveManifest_UsesExclusiveRandomTempFile proves the manifest save no
// longer truncates a predictable temp path (which could be pre-created as a
// hard link to a victim file) and leaves no temp file behind.
func TestSaveManifest_UsesExclusiveRandomTempFile(t *testing.T) {
	projectPath := t.TempDir()
	dir := filepath.Join(projectPath, projectSkillsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	squatted := filepath.Join(dir, projectSkillsManifest+".tmp")
	if err := os.WriteFile(squatted, []byte("victim"), 0o600); err != nil {
		t.Fatalf("seed squatted temp file: %v", err)
	}

	if err := SaveProjectSkillsManifest(projectPath, &ProjectSkillsManifest{}); err != nil {
		t.Fatalf("SaveProjectSkillsManifest failed: %v", err)
	}

	content, err := os.ReadFile(squatted)
	if err != nil {
		t.Fatalf("squatted temp file disappeared: %v", err)
	}
	if string(content) != "victim" {
		t.Fatalf("squatted temp file was overwritten: %q", string(content))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read manifest dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != projectSkillsManifest && e.Name() != projectSkillsManifest+".tmp" {
			t.Fatalf("save left a stray temp file behind: %s", e.Name())
		}
	}
}

// TestTargetExists_DanglingPoolLinkCountsAbsent proves a leftover link into a
// pool entry that no longer exists is treated as ABSENT (so attach
// rematerializes) rather than as an attached skill, while a live pool link
// still counts as present. A FOREIGN but resolvable replacement also stays
// "present" on purpose: the loadout layer reports those instead of silently
// overwriting them (TestLoadout_RefusesForeignReplacementOfManagedSymlink).
func TestTargetExists_DanglingPoolLinkCountsAbsent(t *testing.T) {
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
	skillsDir := filepath.Join(projectPath, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(poolRoot, "gone"), filepath.Join(skillsDir, "lint")); err != nil {
		t.Fatalf("symlink dangling pool entry: %v", err)
	}

	p, err := openProjectRoot(projectPath)
	if err != nil {
		t.Fatalf("openProjectRoot: %v", err)
	}
	defer p.Close()

	targetRel := buildProjectSkillTargetPath(projectClaudeSkillsDir, "lint")
	exists, err := p.targetExists(targetRel, filepath.Join(poolRoot, "gone"))
	if err != nil {
		t.Fatalf("targetExists failed: %v", err)
	}
	if exists {
		t.Fatalf("dangling link to this attachment's own source should count as absent")
	}

	// A dangling link into an UNRELATED pool entry is a foreign replacement:
	// present, so the loadout layer reports it instead of overwriting it.
	exists, err = p.targetExists(targetRel, filepath.Join(poolRoot, "lint"))
	if err != nil {
		t.Fatalf("targetExists failed: %v", err)
	}
	if !exists {
		t.Fatalf("dangling link to an unrelated pool entry must count as present")
	}

	// A live pool link counts as present.
	if err := os.Remove(filepath.Join(skillsDir, "lint")); err != nil {
		t.Fatalf("remove dangling link: %v", err)
	}
	if err := os.Symlink(filepath.Join(poolRoot, "lint"), filepath.Join(skillsDir, "lint")); err != nil {
		t.Fatalf("symlink live pool entry: %v", err)
	}
	exists, err = p.targetExists(targetRel, filepath.Join(poolRoot, "lint"))
	if err != nil {
		t.Fatalf("targetExists failed: %v", err)
	}
	if !exists {
		t.Fatalf("live pool link should count as present")
	}
}

// TestTargetExists_ForeignResolvableLinkCountsPresent pins the policy the
// loadout layer depends on: a managed entry replaced by a link to an unrelated
// but existing directory is PRESENT (reported as already attached, then
// refused as unhealthy upstream), never silently rematerialized over.
func TestTargetExists_ForeignResolvableLinkCountsPresent(t *testing.T) {
	projectPath := t.TempDir()
	foreign := t.TempDir()
	skillsDir := filepath.Join(projectPath, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(foreign, filepath.Join(skillsDir, "lint")); err != nil {
		t.Fatalf("symlink foreign dir: %v", err)
	}

	p, err := openProjectRoot(projectPath)
	if err != nil {
		t.Fatalf("openProjectRoot: %v", err)
	}
	defer p.Close()

	exists, err := p.targetExists(buildProjectSkillTargetPath(projectClaudeSkillsDir, "lint"), "")
	if err != nil {
		t.Fatalf("targetExists failed: %v", err)
	}
	if !exists {
		t.Fatalf("foreign but resolvable link must count as present")
	}
}

// TestMaterialize_RefusesPoolLinkPointingOutsideRegisteredRoots proves the
// other direction of the source-symlink hop: a managed entry whose link
// escapes every registered source root is refused as a materialization source
// (no filesystem call touches the destination), while the legitimate pool link
// keeps migrating (TestMaterialize_MigratesFromPoolSymlinkWhenSourceGone).
func TestMaterialize_RefusesPoolLinkPointingOutsideRegisteredRoots(t *testing.T) {
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
	writeSkillDir(t, outside, "lint", "lint", "Planted outside every registered root")

	skillsDir := filepath.Join(projectPath, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "lint"), filepath.Join(skillsDir, "lint")); err != nil {
		t.Fatalf("symlink outside dir: %v", err)
	}

	targetRel := buildProjectSkillTargetPath(projectAgentsSkillsDir, "lint")
	if _, err := materializeSkillCopyOnly(projectPath, filepath.Join(skillsDir, "lint"), targetRel); err == nil {
		t.Fatalf("expected refusal for a managed link escaping every registered source root")
	}
	if _, err := os.Stat(filepath.Join(projectPath, ".agents", "skills", "lint")); !os.IsNotExist(err) {
		t.Fatalf("nothing should have been materialized, stat err = %v", err)
	}
}

// TestCopyIntoRoot_RefusesToTruncateSquattedDestination proves the copy path
// creates its leaves exclusively: a name squatted at the destination (in the
// real attack, a hard link to a victim file, which os.Root cannot constrain)
// is refused rather than truncated.
func TestCopyIntoRoot_RefusesToTruncateSquattedDestination(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	victim := filepath.Join(dstDir, "SKILL.md")
	if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	srcRoot, err := os.OpenRoot(srcDir)
	if err != nil {
		t.Fatalf("open source root: %v", err)
	}
	defer srcRoot.Close()
	dstRoot, err := os.OpenRoot(dstDir)
	if err != nil {
		t.Fatalf("open dest root: %v", err)
	}
	defer dstRoot.Close()

	if err := copyFileIntoRoot(dstRoot, srcRoot, "SKILL.md", "SKILL.md"); err == nil {
		t.Fatalf("expected refusal to write over an existing destination entry")
	}
	content, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("victim disappeared: %v", err)
	}
	if string(content) != "victim" {
		t.Fatalf("victim was truncated/overwritten: %q", string(content))
	}
}

// TestRemovePinned_RemovesTreeWithoutFollowingLinks proves the replacement for
// Root.RemoveAll deletes a real managed subtree, unlinks symlinks instead of
// following them, and leaves link destinations untouched.
func TestRemovePinned_RemovesTreeWithoutFollowingLinks(t *testing.T) {
	projectPath := t.TempDir()
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	entry := filepath.Join(projectPath, ".claude", "skills", "lint")
	if err := os.MkdirAll(filepath.Join(entry, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir entry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(entry, "nested", "SKILL.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed skill file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(entry, "linked")); err != nil {
		t.Fatalf("symlink into entry: %v", err)
	}

	p, err := openProjectRoot(projectPath)
	if err != nil {
		t.Fatalf("openProjectRoot: %v", err)
	}
	defer p.Close()

	if err := p.removeManagedTarget(buildProjectSkillTargetPath(projectClaudeSkillsDir, "lint")); err != nil {
		t.Fatalf("removeManagedTarget failed: %v", err)
	}
	if _, err := os.Lstat(entry); !os.IsNotExist(err) {
		t.Fatalf("managed entry should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("removal followed a symlink out of the project: %v", err)
	}
}

// TestAliasRegisteredSourceRoot_AttachMigrateAndHeal is the regression test for
// sources registered through a symlink alias (/alias/pool -> /real/pool), which
// is a normal local setup. Physical link destinations never match a lexical
// registered root, so attach worked but migration from the managed link and
// healing of a dangling attachment silently failed. All three paths are pinned
// here.
func TestAliasRegisteredSourceRoot_AttachMigrateAndHeal(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	root := t.TempDir()
	realPool := filepath.Join(root, "real-pool")
	writeSkillDir(t, realPool, "lint", "lint", "Linting best practices")
	aliasPool := filepath.Join(root, "alias-pool")
	if err := os.Symlink(realPool, aliasPool); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	// Registered through the ALIAS, while the filesystem answers physically.
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"pool": {Path: aliasPool, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath := t.TempDir()

	// 1. Attach.
	if _, err := AttachSkillToProject(projectPath, "claude", "lint", "pool"); err != nil {
		t.Fatalf("attach from alias-registered source failed: %v", err)
	}
	claudeEntry := filepath.Join(projectPath, ".claude", "skills", "lint")
	if _, err := os.Stat(filepath.Join(claudeEntry, "SKILL.md")); err != nil {
		t.Fatalf("expected attached skill content: %v", err)
	}

	// 2. Migrate from the managed link (recorded source unavailable).
	stale := SkillCandidate{
		ID:         buildSkillID("pool", "lint"),
		Name:       "lint",
		Source:     "pool",
		SourcePath: filepath.Join(aliasPool, "gone-away"),
		EntryName:  "lint",
		Kind:       "dir",
	}
	if err := ApplyProjectSkills(projectPath, "codex", []SkillCandidate{stale}); err != nil {
		t.Fatalf("migration from alias-registered managed link failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectPath, ".agents", "skills", "lint", "SKILL.md")); err != nil {
		t.Fatalf("expected migrated skill content: %v", err)
	}

	// 3. Heal a dangling attachment whose link points at the alias-registered
	//    source: it must count as ABSENT so attach rematerializes it.
	p, err := openProjectRoot(projectPath)
	if err != nil {
		t.Fatalf("openProjectRoot: %v", err)
	}
	defer p.Close()

	agentsEntry := filepath.Join(projectPath, ".agents", "skills", "lint")
	if err := os.RemoveAll(agentsEntry); err != nil {
		t.Fatalf("clear migrated entry: %v", err)
	}
	if err := os.Symlink(filepath.Join(aliasPool, "lint"), agentsEntry); err != nil {
		t.Fatalf("install alias pool link: %v", err)
	}
	exists, err := p.targetExists(buildProjectSkillTargetPath(projectAgentsSkillsDir, "lint"), filepath.Join(aliasPool, "lint"))
	if err != nil {
		t.Fatalf("targetExists failed: %v", err)
	}
	if !exists {
		t.Fatalf("live alias-registered pool link must count as present")
	}

	if err := os.Remove(agentsEntry); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if err := os.Symlink(filepath.Join(aliasPool, "vanished"), agentsEntry); err != nil {
		t.Fatalf("install dangling alias link: %v", err)
	}
	exists, err = p.targetExists(buildProjectSkillTargetPath(projectAgentsSkillsDir, "lint"), filepath.Join(aliasPool, "vanished"))
	if err != nil {
		t.Fatalf("targetExists failed: %v", err)
	}
	if exists {
		t.Fatalf("dangling alias-registered link must count as absent so it can heal")
	}
}

// --- Final round: source-side pinning and dangling-link classification ---

// TestMaterialize_LinkTargetComesFromPinnedSourceRoot pins the invariant behind
// the source-side no-reopen-after-pinning rule: the created managed link names
// the entry INSIDE the registered source root (root physical path + validated
// remainder), never a path re-resolved from the source after validation. That
// is what stops a source entry swapped for an external symlink between
// validation and link creation from becoming a managed attachment pointing
// outside the source root.
func TestMaterialize_LinkTargetComesFromPinnedSourceRoot(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	root := t.TempDir()
	realPool := filepath.Join(root, "real-pool")
	writeSkillDir(t, realPool, "lint", "lint", "Linting best practices")
	aliasPool := filepath.Join(root, "alias-pool")
	if err := os.Symlink(realPool, aliasPool); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"pool": {Path: aliasPool, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath := t.TempDir()
	attached, err := AttachSkillToProject(projectPath, "claude", "lint", "pool")
	if err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	entry := filepath.Join(projectPath, ".claude", "skills", "lint")
	info, err := os.Lstat(entry)
	if err != nil {
		t.Fatalf("lstat attached entry: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Skipf("attach fell back to copy mode (%s)", attached.Mode)
	}

	linkText, err := os.Readlink(entry)
	if err != nil {
		t.Fatalf("readlink attached entry: %v", err)
	}
	if !filepath.IsAbs(linkText) {
		linkText = filepath.Join(filepath.Dir(entry), linkText)
	}
	poolPhysical := physicalPath(aliasPool)
	if _, inside := relUnderBase(poolPhysical, filepath.Clean(linkText)); !inside {
		t.Fatalf("managed link %q must name an entry inside the registered source root %q", linkText, poolPhysical)
	}
	if _, err := os.Stat(filepath.Join(entry, "SKILL.md")); err != nil {
		t.Fatalf("expected the link to resolve to the skill: %v", err)
	}
}

// TestTargetExists_DanglingLinkOutsideManagedDirsCountsPresent covers the
// classification-ordering fix: a Stat failure is no longer treated as proof of
// absence. A RELATIVE dangling link that stays inside the managed os.Root but
// resolves outside the managed skills dirs is a foreign entry and must be
// reported (present), not deleted and replaced.
func TestTargetExists_DanglingLinkOutsideManagedDirsCountsPresent(t *testing.T) {
	projectPath := t.TempDir()
	skillsDir := filepath.Join(projectPath, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	// Resolves to <project>/.claude/evil-missing: inside the project, outside
	// every managed skills dir, and dangling.
	if err := os.Symlink(filepath.Join("..", "evil-missing"), filepath.Join(skillsDir, "lint")); err != nil {
		t.Fatalf("install dangling foreign link: %v", err)
	}

	p, err := openProjectRoot(projectPath)
	if err != nil {
		t.Fatalf("openProjectRoot: %v", err)
	}
	defer p.Close()

	exists, err := p.targetExists(buildProjectSkillTargetPath(projectClaudeSkillsDir, "lint"),
		filepath.Join(t.TempDir(), "lint"))
	if err != nil {
		t.Fatalf("targetExists failed: %v", err)
	}
	if !exists {
		t.Fatalf("a dangling foreign link must count as present, not be overwritten")
	}
}
