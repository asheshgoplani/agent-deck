package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #1659: scrolling in the settings panel is cursor-anchored, and the
// content below the last navigable setting (MCP config-path hint + help bar)
// was permanently unreachable — "▼ more below" never cleared even with the
// cursor on the last setting. With the fix, parking the cursor on the last
// setting scrolls the tail into view.
func TestSettingsPanel_LastSetting_ScrollsTailIntoView(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	panel := NewSettingsPanel()
	// Height short enough to force scroll-windowing in View().
	panel.SetSize(100, 20)
	panel.Show()
	panel.cursor = settingsCount - 1

	view := panel.View()

	// Sanity: the panel must actually be in scroll mode, and scrolled down.
	if !containsString(view, "more above") {
		t.Fatal("test must run with a small enough height to force scroll mode; " +
			"'▲ more above' did not appear in the view")
	}
	// The tail must be fully visible: no "more below" indicator...
	if containsString(view, "more below") {
		t.Errorf("'▼ more below' must clear when the cursor is on the last setting. Got:\n%s", view)
	}
	// ...and the trailing info lines must be on screen.
	if !containsString(view, "MCP SERVERS & CUSTOM TOOLS") {
		t.Errorf("trailing MCP section must scroll into view at the last setting. Got:\n%s", view)
	}
	// (token chosen to survive lipgloss word-wrapping of the help bar)
	if !containsString(view, "j/k Navigate") {
		t.Errorf("help bar must scroll into view at the last setting. Got:\n%s", view)
	}
	// The cursor's own row must still be visible.
	if !containsString(view, "Visible tools") {
		t.Errorf("last setting row must remain visible after scrolling to the tail. Got:\n%s", view)
	}
}
