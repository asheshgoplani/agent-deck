package ui

import (
	"encoding/json"
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestKimiRecentSessionPreviewResetsPermissionMode(t *testing.T) {
	home := setXDGTestHome(t)
	writeXDGTestConfig(t, home, "[kimi]\npermission_mode = \"ask-when-needed\"\n")

	savedNeverAsk, err := session.MarshalToolOptions(&session.KimiOptions{
		PermissionMode: session.KimiPermissionNeverAsk,
	})
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}

	tests := []struct {
		name        string
		toolOptions json.RawMessage
	}{
		{name: "empty"},
		{name: "empty object", toolOptions: json.RawMessage(`{}`)},
		{name: "undecodable", toolOptions: json.RawMessage(`{"tool":"kimi","options":{"permission_mode":123}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewNewDialog()
			d.Show()

			d.previewRecentSession(&statedb.RecentSessionRow{
				Tool:        "kimi",
				ToolOptions: savedNeverAsk,
			})
			if got := d.GetKimiOptions().PermissionMode; got != session.KimiPermissionNeverAsk {
				t.Fatalf("saved permission mode = %q, want never-ask", got)
			}
			if !slices.Contains(d.GetKimiOptions().ToArgs(), "--auto") {
				t.Fatal("saved never-ask options should produce --auto")
			}

			d.previewRecentSession(&statedb.RecentSessionRow{
				Tool:        "kimi",
				ToolOptions: tt.toolOptions,
			})

			opts := d.GetKimiOptions()
			if got := opts.PermissionMode; got != session.KimiPermissionAskWhenNeeded {
				t.Fatalf("permission mode = %q, want configured default ask-when-needed", got)
			}
			if slices.Contains(opts.ToArgs(), "--auto") {
				t.Fatalf("submission args = %v, want no --auto", opts.ToArgs())
			}
		})
	}
}

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

func TestKimiYoloShortcutRestartsIdleSession(t *testing.T) {
	setXDGTestHome(t)
	h := NewHome()
	inst := session.NewInstanceWithTool("kimi-test", t.TempDir(), "kimi")
	inst.Status = session.StatusIdle
	if err := inst.SetKimiOptions(&session.KimiOptions{PermissionMode: session.KimiPermissionNeverAsk}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}
	h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	h.cursor = 0

	_, cmd := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	if cmd == nil {
		t.Fatal("idle Kimi session was not restarted after permission change")
	}
	if got := inst.GetKimiOptions().PermissionMode; got != session.KimiPermissionAlwaysAsk {
		t.Fatalf("PermissionMode = %q, want always-ask", got)
	}
}

func TestKimiYHotkeyTogglesYoloNotAuto(t *testing.T) {
	setXDGTestHome(t)
	h := NewHome()
	inst := session.NewInstanceWithTool("kimi-test", t.TempDir(), "kimi")
	h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	h.cursor = 0

	// always-ask -> ask-when-needed
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if got := inst.GetKimiOptions().PermissionMode; got != session.KimiPermissionAskWhenNeeded {
		t.Fatalf("y from always-ask = %q, want ask-when-needed", got)
	}
	// ask-when-needed -> always-ask
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if got := inst.GetKimiOptions().PermissionMode; got != session.KimiPermissionAlwaysAsk {
		t.Fatalf("y from ask-when-needed = %q, want always-ask", got)
	}
	// never-ask -> always-ask (y never toggles TO never-ask)
	_ = inst.SetKimiOptions(&session.KimiOptions{PermissionMode: session.KimiPermissionNeverAsk})
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if got := inst.GetKimiOptions().PermissionMode; got != session.KimiPermissionAlwaysAsk {
		t.Fatalf("y from never-ask = %q, want always-ask", got)
	}
}

func TestKimiYoloShortcutOnRemoteSession(t *testing.T) {
	t.Skip("remote Kimi permission changes are unsupported: the remote session path forwards KimiPermissionMode only during creation")
}

func TestKimiYoloSettingPersistsAfterEscape(t *testing.T) {
	home := setXDGTestHome(t)
	writeXDGTestConfig(t, home, "[kimi]\npermission_mode = \"always-ask\"\n")
	h := NewHome()
	h.settingsPanel.Show()
	h.settingsPanel.cursor = int(SettingKimiPermissionMode)

	h.Update(tea.KeyMsg{Type: tea.KeyRight})
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
