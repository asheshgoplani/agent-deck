package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func setupSkillDialogEnv(t *testing.T) func() {
	t.Helper()

	homeDir := t.TempDir()
	claudeDir := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("failed to create claude dir: %v", err)
	}

	oldHome := os.Getenv("HOME")
	oldClaude := os.Getenv("CLAUDE_CONFIG_DIR")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatalf("failed setting HOME: %v", err)
	}
	if err := os.Setenv("CLAUDE_CONFIG_DIR", claudeDir); err != nil {
		t.Fatalf("failed setting CLAUDE_CONFIG_DIR: %v", err)
	}
	session.ClearUserConfigCache()

	return func() {
		_ = os.Setenv("HOME", oldHome)
		_ = os.Setenv("CLAUDE_CONFIG_DIR", oldClaude)
		session.ClearUserConfigCache()
	}
}

func writeDialogSkillDir(t *testing.T, root, folder, name, description string) string {
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

func boolPtrDialog(v bool) *bool {
	b := v
	return &b
}

func TestSkillDialog_Show_NonClaudeSession(t *testing.T) {
	cleanup := setupSkillDialogEnv(t)
	defer cleanup()

	dialog := NewSkillDialog()
	if err := dialog.Show(t.TempDir(), "sess-1", "gemini"); err != nil {
		t.Fatalf("Show failed: %v", err)
	}

	if !dialog.IsVisible() {
		t.Fatal("dialog should be visible")
	}
	if dialog.emptyHelpText == "" {
		t.Fatal("expected non-claude help text")
	}
	if len(dialog.attached) != 0 || len(dialog.available) != 0 {
		t.Fatal("non-claude dialog should not populate skill lists")
	}
}

func TestSkillDialog_MoveAndApply(t *testing.T) {
	cleanup := setupSkillDialogEnv(t)
	defer cleanup()

	sourcePath := t.TempDir()
	writeDialogSkillDir(t, sourcePath, "lint", "lint", "Linting best practices")

	if err := session.SaveSkillSources(map[string]session.SkillSourceDef{
		"pool": {Path: sourcePath, Enabled: boolPtrDialog(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	projectPath := t.TempDir()

	dialog := NewSkillDialog()
	if err := dialog.Show(projectPath, "sess-1", "claude"); err != nil {
		t.Fatalf("Show failed: %v", err)
	}

	if len(dialog.available) != 1 {
		t.Fatalf("expected 1 available skill, got %d", len(dialog.available))
	}
	if len(dialog.attached) != 0 {
		t.Fatalf("expected no attached skills, got %d", len(dialog.attached))
	}

	dialog.column = SkillColumnAvailable
	dialog.availableIdx = 0
	dialog.Move()

	if !dialog.HasChanged() {
		t.Fatal("expected dialog changes after move")
	}
	if len(dialog.attached) != 1 {
		t.Fatalf("expected 1 attached skill after move, got %d", len(dialog.attached))
	}
	if len(dialog.available) != 0 {
		t.Fatalf("expected no available skills after move, got %d", len(dialog.available))
	}

	if err := dialog.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	attached, err := session.GetAttachedProjectSkills(projectPath)
	if err != nil {
		t.Fatalf("GetAttachedProjectSkills failed: %v", err)
	}
	if len(attached) != 1 {
		t.Fatalf("expected 1 persisted attachment, got %d", len(attached))
	}
	if attached[0].Name != "lint" {
		t.Fatalf("persisted skill = %q, want %q", attached[0].Name, "lint")
	}

	targetPath := filepath.Join(projectPath, ".claude", "skills", "lint")
	if _, err := os.Lstat(targetPath); err != nil {
		t.Fatalf("expected materialized skill at %s: %v", targetPath, err)
	}
}

func TestSkillDialog_Show_AvailableOnlyPoolSource(t *testing.T) {
	cleanup := setupSkillDialogEnv(t)
	defer cleanup()

	poolPath := t.TempDir()
	claudeGlobalPath := t.TempDir()
	writeDialogSkillDir(t, poolPath, "pool-one", "pool-one", "Pool managed skill")
	writeDialogSkillDir(t, claudeGlobalPath, "global-one", "global-one", "Global claude skill")

	if err := session.SaveSkillSources(map[string]session.SkillSourceDef{
		"pool":          {Path: poolPath, Enabled: boolPtrDialog(true)},
		"claude-global": {Path: claudeGlobalPath, Enabled: boolPtrDialog(true)},
	}); err != nil {
		t.Fatalf("SaveSkillSources failed: %v", err)
	}

	dialog := NewSkillDialog()
	if err := dialog.Show(t.TempDir(), "sess-1", "claude"); err != nil {
		t.Fatalf("Show failed: %v", err)
	}

	if len(dialog.available) != 1 {
		t.Fatalf("expected 1 pool available skill, got %d", len(dialog.available))
	}
	if dialog.available[0].Candidate.Name != "pool-one" {
		t.Fatalf("expected pool-one in available, got %q", dialog.available[0].Candidate.Name)
	}
	if dialog.available[0].Candidate.Source != "pool" {
		t.Fatalf("expected pool source in available, got %q", dialog.available[0].Candidate.Source)
	}
}

func TestSkillDialog_TypeJump(t *testing.T) {
	dialog := NewSkillDialog()
	dialog.visible = true
	dialog.tool = "claude"
	dialog.column = SkillColumnAvailable
	dialog.available = []SkillDialogItem{
		{Candidate: session.SkillCandidate{Name: "alpha", ID: "pool/alpha"}},
		{Candidate: session.SkillCandidate{Name: "delta", ID: "pool/delta"}},
		{Candidate: session.SkillCandidate{Name: "docs", ID: "pool/docs"}},
		{Candidate: session.SkillCandidate{Name: "zeta", ID: "pool/zeta"}},
	}
	dialog.availableIdx = 0

	_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if dialog.availableIdx != 1 {
		t.Fatalf("expected jump to delta (index 1), got %d", dialog.availableIdx)
	}

	_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if dialog.availableIdx != 2 {
		t.Fatalf("expected jump to docs (index 2), got %d", dialog.availableIdx)
	}
}

func TestSkillDialog_ScrollWindowFollowsSelection(t *testing.T) {
	dialog := NewSkillDialog()
	dialog.visible = true
	dialog.tool = "claude"
	dialog.height = 22 // maxRowsPerColumn => 6
	dialog.column = SkillColumnAvailable
	dialog.available = make([]SkillDialogItem, 0, 12)
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		dialog.available = append(dialog.available, SkillDialogItem{
			Candidate: session.SkillCandidate{Name: name, ID: "pool/" + name},
		})
	}
	dialog.availableIdx = 0
	dialog.availableOff = 0

	for i := 0; i < 8; i++ {
		_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	if dialog.availableIdx != 8 {
		t.Fatalf("expected index 8 after scrolling, got %d", dialog.availableIdx)
	}
	if dialog.availableOff == 0 {
		t.Fatalf("expected non-zero scroll offset once selection exceeds visible window")
	}
}

func TestSkillDialog_ViewShowsCounts(t *testing.T) {
	dialog := NewSkillDialog()
	dialog.visible = true
	dialog.tool = "claude"
	dialog.width = 120
	dialog.height = 40
	dialog.attached = []SkillDialogItem{
		{Candidate: session.SkillCandidate{Name: "a", ID: "pool/a"}},
	}
	dialog.available = []SkillDialogItem{
		{Candidate: session.SkillCandidate{Name: "b", ID: "pool/b"}},
		{Candidate: session.SkillCandidate{Name: "c", ID: "pool/c"}},
	}

	view := dialog.View()
	if !strings.Contains(view, "Attached (1)") {
		t.Fatalf("expected attached count in view")
	}
	if !strings.Contains(view, "Available (2)") {
		t.Fatalf("expected available count in view")
	}
}
