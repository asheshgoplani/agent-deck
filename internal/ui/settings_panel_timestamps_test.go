package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// Settings panel wiring for [display] show_session_timestamps. The toggle
// must load from the on-disk Display struct, flip on space, and round-trip
// back through GetConfig() unchanged.

func TestSettingsPanel_ShowSessionTimestamps_LoadConfig(t *testing.T) {
	panel := NewSettingsPanel()

	config := &session.UserConfig{
		Display: session.DisplaySettings{
			ShowSessionTimestamps: true,
		},
	}
	panel.LoadConfig(config)

	if !panel.showSessionTimestamps {
		t.Errorf("showSessionTimestamps should be true after loading config with Display.ShowSessionTimestamps=true")
	}

	// Default (zero-value) config should leave the panel value at false.
	panel2 := NewSettingsPanel()
	panel2.LoadConfig(&session.UserConfig{})
	if panel2.showSessionTimestamps {
		t.Errorf("showSessionTimestamps should default to false for an empty config")
	}
}

func TestSettingsPanel_ShowSessionTimestamps_ToggleAndPersist(t *testing.T) {
	// Isolate from the real ~/.agent-deck/config.toml — Show() would otherwise
	// load the developer's actual setting and the precondition below would
	// be non-deterministic.
	t.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	panel := NewSettingsPanel()
	panel.Show()
	panel.cursor = int(SettingShowSessionTimestamps)

	if panel.showSessionTimestamps {
		t.Fatal("precondition: showSessionTimestamps must start false")
	}

	_, _, changed := panel.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !changed {
		t.Error("Space on SettingShowSessionTimestamps must report changed=true")
	}
	if !panel.showSessionTimestamps {
		t.Error("Space on SettingShowSessionTimestamps must flip showSessionTimestamps to true")
	}

	// GetConfig must mirror the toggled state back into Display so SaveUserConfig persists it.
	got := panel.GetConfig()
	if !got.Display.ShowSessionTimestamps {
		t.Error("GetConfig() must propagate showSessionTimestamps into Display.ShowSessionTimestamps")
	}

	// Flip again and confirm the round trip back to false.
	_, _, _ = panel.Update(tea.KeyMsg{Type: tea.KeySpace})
	got = panel.GetConfig()
	if got.Display.ShowSessionTimestamps {
		t.Error("second toggle must flip showSessionTimestamps back to false")
	}
}

// Pins the cursorToLine[SettingShowSessionTimestamps] mapping in
// settings_panel.go: View() — if the value is off, the cursor's auto-scroll
// stops bringing the new setting into the visible window. Render the panel
// at a height short enough to force scroll mode, point the cursor at our
// setting, and assert it appears in the rendered output.
func TestSettingsPanel_ShowSessionTimestamps_ScrollMappingBringsRowIntoView(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	panel := NewSettingsPanel()
	// Width comfortable, height short enough to force scroll-windowing in View().
	panel.SetSize(100, 20)
	panel.Show()
	panel.cursor = int(SettingShowSessionTimestamps)

	view := panel.View()

	if !containsString(view, "Show session timestamps") {
		t.Fatalf("cursorToLine[SettingShowSessionTimestamps] must map to a line that scrolls the setting into view. "+
			"View did not contain the row. Got:\n%s", view)
	}
	// Sanity: the panel must have actually entered scroll mode (otherwise this
	// test passes trivially because the whole panel fits). Look for one of the
	// scroll indicators.
	if !containsString(view, "more above") && !containsString(view, "more below") {
		t.Fatal("test must run with a small enough height to force scroll mode; " +
			"neither '▲ more above' nor '▼ more below' appeared in the view")
	}
}
