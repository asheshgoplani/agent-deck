package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedSkillSource registers a source directory containing one skill and points
// config.toml's Codex group at an isolated home. Returns the home path.
func seedSkillSource(t *testing.T, skillName string, groupSkills []string) string {
	t.Helper()
	root := t.TempDir()

	sourceDir := filepath.Join(root, "skills-src")
	skillDir := filepath.Join(sourceDir, skillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+skillName+"\ndescription: test skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	codexHome := filepath.Join(root, "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}

	// Skill sources live in ~/.agent-deck/skills/sources.toml, separate from
	// config.toml. HOME is already isolated by this package's TestMain.
	sourcesDir := filepath.Join(os.Getenv("HOME"), ".agent-deck", "skills")
	if err := os.MkdirAll(sourcesDir, 0o700); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	sources := "[sources.testsrc]\npath = \"" + sourceDir + "\"\n"
	if err := os.WriteFile(filepath.Join(sourcesDir, "sources.toml"), []byte(sources), 0o600); err != nil {
		t.Fatalf("write sources.toml: %v", err)
	}

	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("resolve config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	var quoted []string
	for _, s := range groupSkills {
		quoted = append(quoted, `"`+s+`"`)
	}
	cfg := `
[groups."team".codex]
config_dir = "` + codexHome + `"
skills = [` + strings.Join(quoted, ", ") + `]
`
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return codexHome
}

func TestSyncGroupCodexHomeSkillsAttachesDeclaredSkill(t *testing.T) {
	home := seedSkillSource(t, "port-registry", []string{"testsrc/port-registry"})

	problems, err := SyncGroupCodexHomeSkills("team")
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if _, statErr := os.Lstat(filepath.Join(home, "skills", "port-registry")); statErr != nil {
		t.Fatalf("skill was not materialized into the home: %v", statErr)
	}
}

// Re-running must be a silent no-op, not a duplicate attach or an error: the
// sync is expected to run repeatedly alongside session starts.
func TestSyncGroupCodexHomeSkillsIsIdempotent(t *testing.T) {
	home := seedSkillSource(t, "port-registry", []string{"testsrc/port-registry"})

	if _, err := SyncGroupCodexHomeSkills("team"); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}
	problems, err := SyncGroupCodexHomeSkills("team")
	if err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("re-sync must be a no-op, got: %v", problems)
	}
	if _, statErr := os.Lstat(filepath.Join(home, "skills", "port-registry")); statErr != nil {
		t.Fatalf("skill lost on re-sync: %v", statErr)
	}
}

// An unresolvable entry is reported but must not abort the sync, so one bad
// entry cannot stop the rest of the home from converging.
func TestSyncGroupCodexHomeSkillsReportsUnknownWithoutFailing(t *testing.T) {
	home := seedSkillSource(t, "port-registry", []string{"testsrc/port-registry", "testsrc/does-not-exist"})

	problems, err := SyncGroupCodexHomeSkills("team")
	if err != nil {
		t.Fatalf("an unresolvable entry must not fail the sync: %v", err)
	}
	if len(problems) != 1 {
		t.Fatalf("expected exactly 1 problem, got %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "does-not-exist") {
		t.Fatalf("problem should name the bad entry: %q", problems[0])
	}
	// The good entry still converged.
	if _, statErr := os.Lstat(filepath.Join(home, "skills", "port-registry")); statErr != nil {
		t.Fatalf("a bad sibling entry blocked a good one: %v", statErr)
	}
}

// A human-placed directory at the target must never be clobbered.
func TestSyncGroupCodexHomeSkillsRefusesToClobberForeignTarget(t *testing.T) {
	home := seedSkillSource(t, "port-registry", []string{"testsrc/port-registry"})

	foreign := filepath.Join(home, "skills", "port-registry")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatalf("mkdir foreign target: %v", err)
	}
	marker := filepath.Join(foreign, "MINE.md")
	if err := os.WriteFile(marker, []byte("hand written"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	problems, err := SyncGroupCodexHomeSkills("team")
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if len(problems) != 1 {
		t.Fatalf("expected the foreign target to be reported, got: %v", problems)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("hand-placed content was clobbered: %v", statErr)
	}
}

func TestSyncGroupCodexHomeSkillsNoopWithoutDeclaredSkills(t *testing.T) {
	seedSkillSource(t, "port-registry", nil)

	problems, err := SyncGroupCodexHomeSkills("team")
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("expected no problems, got %v", problems)
	}
}
