package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGridPopupHost_AttachMsg_SetsSwitchTo(t *testing.T) {
	InitTheme("dark")

	// Simulate a gridAttachMsg with nil instance (no tmux session)
	host := &GridPopupHost{
		grid: NewGridView(),
	}
	// Set grid visible so it renders
	host.grid.visible = true
	host.grid.width = 120
	host.grid.height = 40

	// gridAttachMsg with nil instance should still quit
	model, cmd := host.Update(gridAttachMsg{instance: nil})
	h := model.(*GridPopupHost)

	if h.SwitchTo() != "" {
		t.Errorf("expected empty switchTo with nil instance, got %q", h.SwitchTo())
	}

	// cmd should be tea.Quit
	if cmd == nil {
		t.Error("expected tea.Quit cmd, got nil")
	}
}

func TestGridPopupHost_EscQuitsWhenGridHidden(t *testing.T) {
	InitTheme("dark")

	host := &GridPopupHost{
		grid: NewGridView(),
	}
	host.grid.visible = true
	host.grid.width = 120
	host.grid.height = 40
	// Need at least one cell for key handling
	host.grid.cells = makeTestCells(2)

	// Press Esc — should hide grid, then host should quit
	msg := tea.KeyMsg{Type: tea.KeyEsc}
	model, cmd := host.Update(msg)
	h := model.(*GridPopupHost)

	if h.grid.IsVisible() {
		t.Error("expected grid to be hidden after Esc")
	}
	if h.SwitchTo() != "" {
		t.Error("expected empty switchTo after Esc")
	}
	if cmd == nil {
		t.Error("expected tea.Quit cmd after grid hidden")
	}
}

func TestGridView_StandaloneMode_StatusBar(t *testing.T) {
	InitTheme("dark")

	g := &GridView{
		width:          120,
		height:         40,
		cells:          makeTestCells(2),
		visible:        true,
		standaloneMode: true,
		mode:           GridModeNavigate,
	}

	bar := g.renderStatusBar()

	if !containsStr(bar, "switch") {
		t.Error("standalone status bar should show 'switch', not 'attach'")
	}
	if !containsStr(bar, "close") {
		t.Error("standalone status bar should show 'close', not 'back'")
	}
}

func TestGridView_NonStandaloneMode_StatusBar(t *testing.T) {
	InitTheme("dark")

	g := &GridView{
		width:          120,
		height:         40,
		cells:          makeTestCells(2),
		visible:        true,
		standaloneMode: false,
		mode:           GridModeNavigate,
	}

	bar := g.renderStatusBar()

	if !containsStr(bar, "attach") {
		t.Error("non-standalone status bar should show 'attach'")
	}
	if !containsStr(bar, "back") {
		t.Error("non-standalone status bar should show 'back'")
	}
}

// containsStr checks if rendered text contains a substring (ANSI-aware check is too complex, use raw).
func containsStr(rendered, sub string) bool {
	return len(rendered) > 0 && contains(rendered, sub)
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
