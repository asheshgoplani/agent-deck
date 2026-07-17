package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMCPDialog_TypeJumpAvailable(t *testing.T) {
	dialog := NewMCPDialog()
	dialog.visible = true
	dialog.scope = MCPScopeLocal
	dialog.column = MCPColumnAvailable
	dialog.localAvailable = []MCPItem{
		{Name: "alpha"},
		{Name: "delta"},
		{Name: "docs"},
		{Name: "zeta"},
	}
	dialog.localAvailableIdx = 0

	_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if dialog.localAvailableIdx != 1 {
		t.Fatalf("expected jump to delta (index 1), got %d", dialog.localAvailableIdx)
	}

	_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if dialog.localAvailableIdx != 2 {
		t.Fatalf("expected jump to docs (index 2), got %d", dialog.localAvailableIdx)
	}
}

func TestMCPDialog_TypeJumpWrapAround(t *testing.T) {
	dialog := NewMCPDialog()
	dialog.visible = true
	dialog.scope = MCPScopeLocal
	dialog.column = MCPColumnAvailable
	dialog.localAvailable = []MCPItem{
		{Name: "alpha"},
		{Name: "delta"},
		{Name: "docs"},
	}
	dialog.localAvailableIdx = 2

	_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if dialog.localAvailableIdx != 1 {
		t.Fatalf("expected wrapped jump to delta (index 1), got %d", dialog.localAvailableIdx)
	}
}

func TestMCPDialog_TypeJumpResetOnScopeSwitch(t *testing.T) {
	dialog := NewMCPDialog()
	dialog.visible = true
	dialog.tool = "claude"
	dialog.scope = MCPScopeLocal
	dialog.column = MCPColumnAvailable
	dialog.localAvailable = []MCPItem{{Name: "docs"}}
	dialog.globalAvailable = []MCPItem{{Name: "zeta"}, {Name: "alpha"}}

	_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if dialog.typeJumpBuf != "d" {
		t.Fatalf("expected jump buffer d, got %q", dialog.typeJumpBuf)
	}

	_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.scope != MCPScopeGlobal {
		t.Fatalf("expected scope to switch to global, got %v", dialog.scope)
	}
	if dialog.typeJumpBuf != "" {
		t.Fatalf("expected jump buffer reset on scope switch, got %q", dialog.typeJumpBuf)
	}

	_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if dialog.globalAvailableIdx != 0 {
		t.Fatalf("expected jump in global list to zeta (index 0), got %d", dialog.globalAvailableIdx)
	}
}

func TestMCPDialog_CodexUsesGlobalScopeOnly(t *testing.T) {
	dialog := NewMCPDialog()
	if err := dialog.Show(t.TempDir(), "session-id", "codex"); err != nil {
		t.Fatal(err)
	}
	if dialog.scope != MCPScopeGlobal {
		t.Fatalf("scope = %v, want global", dialog.scope)
	}

	_, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
	if dialog.scope != MCPScopeGlobal {
		t.Fatalf("tab should keep Codex on global scope, got %v", dialog.scope)
	}
}

func TestMCPDialog_RemoteSessionNotApplicable(t *testing.T) {
	t.Skip("remote sessions cannot open MCPDialog directly; Home only opens it for local session items")
}
