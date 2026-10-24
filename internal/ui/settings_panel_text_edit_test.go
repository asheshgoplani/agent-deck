package ui

import (
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// startClaudeConfigDirEdit opens the text editor on the Claude config dir
// field without Show(), so no user config is loaded from HOME.
func startClaudeConfigDirEdit(t *testing.T, value string) *SettingsPanel {
	t.Helper()
	panel := NewSettingsPanel()
	panel.visible = true
	panel.cursor = int(SettingClaudeConfigDir)
	panel.claudeConfigDir = value
	panel.startTextEdit()
	if !panel.editingText {
		t.Fatal("text edit did not start on SettingClaudeConfigDir")
	}
	return panel
}

func TestSettingsPanel_TextEdit_BackspaceRemovesWholeRune(t *testing.T) {
	panel := startClaudeConfigDirEdit(t, "/home/u/Документы")
	panel.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	panel.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !utf8.ValidString(panel.claudeConfigDir) {
		t.Fatalf("invalid UTF-8 after backspace: %q", panel.claudeConfigDir)
	}
	if want := "/home/u/Документ"; panel.claudeConfigDir != want {
		t.Fatalf("got %q, want %q", panel.claudeConfigDir, want)
	}
}

func TestSettingsPanel_TextEdit_TypesNonASCIIRunes(t *testing.T) {
	panel := startClaudeConfigDirEdit(t, "/home/u/")
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("é")})
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	panel.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Док")})
	panel.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if want := "/home/u/éa Док"; panel.claudeConfigDir != want {
		t.Fatalf("got %q, want %q", panel.claudeConfigDir, want)
	}
}
