package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// #2895: the Claude section's "Default account" radio row is built from the
// configured slots (none first), loads [claude].default_account and saves the
// pick back through the same panel-merge path the TUI uses.
func TestSettingsPanel_DefaultAccountSaveLoad(t *testing.T) {
	withDefaultAccountConfig(t, "ai", nil, "ai", "it", "work")

	panel := NewSettingsPanel()
	panel.SetSize(120, 200)
	panel.Show()
	panel.cursor = int(SettingClaudeDefaultAccount)

	view := stripAnsi(panel.View())
	for _, want := range []string{"Default account", "None", "ai", "it", "work"} {
		if !strings.Contains(view, want) {
			t.Fatalf("settings view lacks %q:\n%s", want, view)
		}
	}
	if got := panel.GetConfig().Claude.DefaultAccount; got != "ai" {
		t.Fatalf("loaded default account = %q, want ai", got)
	}

	// → twice: ai -> it -> work.
	panel.Update(tea.KeyMsg{Type: tea.KeyRight})
	_, _, changed := panel.Update(tea.KeyMsg{Type: tea.KeyRight})
	if !changed {
		t.Fatal("moving the default account row must report a change")
	}
	merged, err := session.MergePanelConfigOntoDisk(panel.GetConfig())
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk: %v", err)
	}
	if err := session.SaveUserConfig(merged); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.Claude.DefaultAccount != "work" {
		t.Fatalf("saved default account = %q, want work", cfg.Claude.DefaultAccount)
	}
	if got := strings.Join(session.ConfiguredAccountNames(cfg), ","); got != "ai,it,work" {
		t.Fatalf("saving must keep the account slots, got %q", got)
	}

	// Back to none clears the key.
	panel.Show()
	panel.cursor = int(SettingClaudeDefaultAccount)
	for i := 0; i < 5; i++ {
		panel.Update(tea.KeyMsg{Type: tea.KeyLeft})
	}
	if got := panel.GetConfig().Claude.DefaultAccount; got != "" {
		t.Fatalf("none must clear the default, got %q", got)
	}
}

// A default naming a removed slot survives an unrelated settings save: the
// panel only rewrites the key when the user moves its row.
func TestSettingsPanel_DefaultAccountKeepsUnknownSlotUntilChanged(t *testing.T) {
	withDefaultAccountConfig(t, "gone", nil, "ai")

	panel := NewSettingsPanel()
	panel.SetSize(120, 200)
	panel.Show()
	panel.cursor = int(SettingDangerousMode)
	panel.Update(tea.KeyMsg{Type: tea.KeySpace})
	if got := panel.GetConfig().Claude.DefaultAccount; got != "gone" {
		t.Fatalf("unrelated save rewrote the default account to %q, want gone", got)
	}
}
