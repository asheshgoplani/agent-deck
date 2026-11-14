package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestKimiYoloShortcutFromToolRow(t *testing.T) {
	setXDGTestHome(t)
	d := NewNewDialog()
	d.SetDefaultTool("kimi")
	d.Show()
	d.focusIndex = d.indexOf(focusCommand)
	d.updateFocus()

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	if d.GetKimiOptions().PermissionMode != session.KimiPermissionAskWhenNeeded {
		t.Fatal("PermissionMode = false, want ask-when-needed")
	}
}

func TestKimiYoloShortcutOnExistingSession(t *testing.T) {
	setXDGTestHome(t)
	h := NewHome()
	inst := session.NewInstanceWithTool("kimi-test", t.TempDir(), "kimi")
	h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	h.cursor = 0

	h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	opts := inst.GetKimiOptions()
	if opts == nil || opts.PermissionMode != session.KimiPermissionAskWhenNeeded {
		t.Fatalf("Kimi options = %+v, want ask-when-needed", opts)
	}
}

func TestKimiYoloSettingPersistsAfterEscape(t *testing.T) {
	home := setXDGTestHome(t)
	writeXDGTestConfig(t, home, "[kimi]\npermission_mode = \"always-ask\"\n")
	h := NewHome()
	h.settingsPanel.Show()
	h.settingsPanel.cursor = int(SettingKimiPermissionMode)

	h.Update(tea.KeyMsg{Type: tea.KeySpace})
	h.Update(tea.KeyMsg{Type: tea.KeyEsc})

	config, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if config.Kimi.PermissionMode != session.KimiPermissionAskWhenNeeded {
		t.Fatal("PermissionMode != ask-when-needed, want true after closing Settings")
	}
}

func TestHermesYoloSettingPersistsAfterEscape(t *testing.T) {
	home := setXDGTestHome(t)
	writeXDGTestConfig(t, home, "[hermes]\nyolo_mode = false\n")
	h := NewHome()
	h.settingsPanel.Show()
	h.settingsPanel.cursor = int(SettingHermesYoloMode)

	h.Update(tea.KeyMsg{Type: tea.KeySpace})
	h.Update(tea.KeyMsg{Type: tea.KeyEsc})

	config, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if !config.Hermes.YoloMode {
		t.Fatal("Hermes YOLO mode was not saved after toggling and closing Settings")
	}
}
