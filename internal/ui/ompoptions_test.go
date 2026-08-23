package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOMPOptionsPanelExposesAndCapturesAllHarnessControls(t *testing.T) {
	p := NewOMPOptionsPanel()
	view := p.View()
	for _, label := range []string{"Session mode", "No session", "Models", "Smol model", "Slow model", "Plan model", "Print thoughts", "Approval mode", "Auto approve", "Max time", "Profile", "Import from Claude", "Import from Codex"} {
		if !strings.Contains(view, label) {
			t.Errorf("OMP panel missing %q", label)
		}
	}
	p.Focus()
	p.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := p.GetOptions("primary"); got.SessionMode != "new" || got.Model != "primary" {
		t.Fatalf("panel options = %+v", got)
	}
}

func TestNewDialogSelectsOMPStructuredOptionsPanel(t *testing.T) {
	d := NewNewDialog()
	for idx, command := range d.presetCommands {
		if command == "omp" {
			d.commandCursor = idx
			break
		}
	}
	d.updateToolOptions()
	if d.toolOptions != d.ompOptions {
		t.Fatal("OMP selection did not expose structured options panel")
	}
	if d.GetOMPOptions() == nil {
		t.Fatal("OMP selection did not produce structured options")
	}
}
