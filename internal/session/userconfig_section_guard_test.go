package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// S2 + S3 data-loss safeguards for config.toml (2026-06-04 incident).
//
// Incident: a partially-constructed UserConfig saved over the live config.toml
// silently dropped the whole [mcps] catalog and [groups] overrides. The atomic
// rename prevented torn writes but not this SEMANTIC clobber.
//
// S2: SaveUserConfig copies config.toml -> config.toml.bak before overwriting.
// S3: SaveUserConfig REFUSES (ErrRefusingConfigSectionDrop) a save that would
//     drop a populated [mcps] or [groups] section to empty, unless the explicit
//     SaveUserConfigWithIntent(cfg, true) path is used.
//
// CRITICAL: a NORMAL edit that legitimately removes ONE group (or one MCP) must
// still succeed — the guard fires only when a populated section goes to ZERO.

// seedConfigWithSections writes an initial config.toml (via the intent path so
// no guard interferes) carrying populated [mcps] and [groups] sections.
func seedConfigWithSections(t *testing.T) *UserConfig {
	t.Helper()
	cfg := cloneDefaultUserConfig()
	cfg.MCPs = map[string]MCPDef{
		"context7": {Command: "npx", Args: []string{"-y", "context7"}},
		"github":   {Command: "npx", Args: []string{"-y", "gh-mcp"}},
	}
	cfg.Groups = map[string]GroupSettings{
		"work":     {Claude: GroupClaudeSettings{ConfigDir: "~/.claude-work"}},
		"personal": {Claude: GroupClaudeSettings{ConfigDir: "~/.claude"}},
	}
	// Use the intent path to lay down the baseline without tripping the guard.
	if err := SaveUserConfigWithIntent(&cfg, true); err != nil {
		t.Fatalf("seed SaveUserConfigWithIntent: %v", err)
	}
	ReloadUserConfig()
	return &cfg
}

// (a) S2: config.toml.bak is created before an overwrite.
func TestSaveUserConfig_CreatesBackupBeforeOverwrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateConfigHomeXDG(t)
	seedConfigWithSections(t)

	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	bakPath := configPath + ".bak"
	if _, err := os.Stat(bakPath); err == nil {
		t.Fatalf("precondition: %s should not exist after first (intent) seed if no prior file", bakPath)
	}

	// A normal, non-shrinking edit: change theme, keep both sections.
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	cfg.Theme = "light"
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig (normal edit): %v", err)
	}

	if _, err := os.Stat(bakPath); err != nil {
		t.Fatalf("expected %s to be created before overwrite, stat err: %v", bakPath, err)
	}
}

// (c) S3: refuses to drop a populated [mcps] to empty without intent.
func TestSaveUserConfig_RefusesDroppingMCPsToEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateConfigHomeXDG(t)
	seedConfigWithSections(t)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	// Simulate the incident: a config that zeroes the MCP catalog.
	cfg.MCPs = map[string]MCPDef{}

	err = SaveUserConfig(cfg)
	if err == nil {
		t.Fatalf("expected SaveUserConfig to refuse dropping populated [mcps] to empty, got nil")
	}
	if !errors.Is(err, ErrRefusingConfigSectionDrop) {
		t.Fatalf("expected ErrRefusingConfigSectionDrop, got %v", err)
	}

	// And the on-disk MCPs must be intact (refusal happened before any write).
	reloaded, err := ReloadUserConfig()
	if err != nil {
		t.Fatalf("ReloadUserConfig: %v", err)
	}
	if len(reloaded.MCPs) != 2 {
		t.Fatalf("expected 2 MCPs preserved after refused save, got %d", len(reloaded.MCPs))
	}
}

// (c') S3: same protection for [groups].
func TestSaveUserConfig_RefusesDroppingGroupsToEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateConfigHomeXDG(t)
	seedConfigWithSections(t)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	cfg.Groups = map[string]GroupSettings{}

	err = SaveUserConfig(cfg)
	if err == nil {
		t.Fatalf("expected SaveUserConfig to refuse dropping populated [groups] to empty, got nil")
	}
	if !errors.Is(err, ErrRefusingConfigSectionDrop) {
		t.Fatalf("expected ErrRefusingConfigSectionDrop, got %v", err)
	}
}

// (d) THE KEY ANTI-FALSE-POSITIVE TEST: removing ONE group (of two) succeeds.
func TestSaveUserConfig_SingleGroupRemoval_StillSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateConfigHomeXDG(t)
	seedConfigWithSections(t)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	// Legit edit: delete just "personal", keep "work". Section still populated.
	delete(cfg.Groups, "personal")
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("expected single-group removal to succeed, got %v", err)
	}

	reloaded, err := ReloadUserConfig()
	if err != nil {
		t.Fatalf("ReloadUserConfig: %v", err)
	}
	if len(reloaded.Groups) != 1 {
		t.Fatalf("expected 1 group after single removal, got %d", len(reloaded.Groups))
	}
	if _, ok := reloaded.Groups["work"]; !ok {
		t.Fatalf("expected remaining group 'work' to survive the edit")
	}
	// Same for MCPs: removing one of two must work.
	cfg2, _ := LoadUserConfig()
	delete(cfg2.MCPs, "github")
	if err := SaveUserConfig(cfg2); err != nil {
		t.Fatalf("expected single-MCP removal to succeed, got %v", err)
	}
	reloaded2, _ := ReloadUserConfig()
	if len(reloaded2.MCPs) != 1 {
		t.Fatalf("expected 1 MCP after single removal, got %d", len(reloaded2.MCPs))
	}
}

// (e) Normal non-shrinking save (no sections touched, unrelated field changed)
// is unaffected.
func TestSaveUserConfig_NormalSave_Unaffected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateConfigHomeXDG(t)
	seedConfigWithSections(t)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	cfg.DefaultTool = "codex"
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("expected normal save to succeed, got %v", err)
	}
	reloaded, _ := ReloadUserConfig()
	if reloaded.DefaultTool != "codex" {
		t.Fatalf("expected default_tool=codex persisted, got %q", reloaded.DefaultTool)
	}
	if len(reloaded.MCPs) != 2 || len(reloaded.Groups) != 2 {
		t.Fatalf("expected both sections intact, got mcps=%d groups=%d", len(reloaded.MCPs), len(reloaded.Groups))
	}
}

// (f) The explicit-intent escape hatch DOES allow clearing a populated section.
func TestSaveUserConfigWithIntent_AllowsSectionClear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateConfigHomeXDG(t)
	seedConfigWithSections(t)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	cfg.MCPs = map[string]MCPDef{}
	cfg.Groups = map[string]GroupSettings{}
	if err := SaveUserConfigWithIntent(cfg, true); err != nil {
		t.Fatalf("expected intent path to allow clearing sections, got %v", err)
	}
	reloaded, _ := ReloadUserConfig()
	if len(reloaded.MCPs) != 0 || len(reloaded.Groups) != 0 {
		t.Fatalf("expected both sections cleared via intent path, got mcps=%d groups=%d", len(reloaded.MCPs), len(reloaded.Groups))
	}
}

// (b for config) S2: the .bak holds the PRE-save (populated) content even when
// the new save is the intent-cleared one — so the catalog is recoverable.
func TestSaveUserConfig_BackupHoldsPreSaveSections(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateConfigHomeXDG(t)
	seedConfigWithSections(t)

	configPath, _ := GetUserConfigPath()
	bakPath := configPath + ".bak"

	cfg, _ := LoadUserConfig()
	cfg.MCPs = map[string]MCPDef{}
	cfg.Groups = map[string]GroupSettings{}
	if err := SaveUserConfigWithIntent(cfg, true); err != nil {
		t.Fatalf("SaveUserConfigWithIntent: %v", err)
	}

	// The .bak should still contain the two MCPs + two groups from before.
	data, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	var fromBak UserConfig
	if _, err := toml.Decode(string(data), &fromBak); err != nil {
		t.Fatalf("decode .bak: %v", err)
	}
	if len(fromBak.MCPs) != 2 || len(fromBak.Groups) != 2 {
		t.Fatalf("expected .bak to preserve pre-save sections, got mcps=%d groups=%d", len(fromBak.MCPs), len(fromBak.Groups))
	}
}

// (g) S3 edge case: a user hand-edits config.toml to add a bare [mcps.stub]
// header (all-zero MCPDef). On the next save, stripEmptyTOMLSections removes it
// (no key=value content), but the guard must NOT refuse — the lost entry was
// non-functional (all fields zero).
func TestSaveUserConfig_AllZeroMCPEntry_DoesNotBlockSaves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateConfigHomeXDG(t)

	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Simulate hand-editing: write a config with a bare [mcps.stub] section.
	handEdited := []byte("# Agent Deck Configuration\n\ntheme = \"dark\"\n\n[mcps.stub]\n")
	if err := os.WriteFile(configPath, handEdited, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	ReloadUserConfig()

	// A normal save (changing theme) must succeed — the guard should not
	// refuse because the "lost" entry was non-functional (all-zero).
	loaded, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	loaded.Theme = "light"
	if err := SaveUserConfig(loaded); err != nil {
		t.Fatalf("expected save to succeed when sole MCP entry is all-zero, got: %v", err)
	}
}

// Same scenario for groups.
func TestSaveUserConfig_AllZeroGroupEntry_DoesNotBlockSaves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateConfigHomeXDG(t)

	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	handEdited := []byte("# Agent Deck Configuration\n\ntheme = \"dark\"\n\n[groups.placeholder]\n")
	if err := os.WriteFile(configPath, handEdited, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	ReloadUserConfig()

	loaded, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	loaded.Theme = "light"
	if err := SaveUserConfig(loaded); err != nil {
		t.Fatalf("expected save to succeed when sole group entry is all-zero, got: %v", err)
	}
}
