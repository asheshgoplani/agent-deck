package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// selectPresetCommand points the dialog's command cursor at the given
// preset so claudeOptions (and its Account row) becomes the active tool
// options panel.
func selectPresetCommand(d *NewDialog, cmd string) {
	for i, c := range d.presetCommands {
		if c == cmd {
			d.commandCursor = i
			break
		}
	}
	d.updateToolOptions()
}

// TestNewDialogAccountRowNeverDisappearsAt80Cols: at 80 columns the Account:
// selector used to render with no values and no marker at all, because its
// pills were joined with no literal space, giving word-wrap no break point.
func TestNewDialogAccountRowNeverDisappearsAt80Cols(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 60)
	d.Show()
	selectPresetCommand(d, "claude")
	d.claudeOptions.SetAccounts([]string{"buddii", "personal", "seminno", "work"})

	view := tmux.StripANSI(d.View())
	if !strings.Contains(view, "Account:") {
		t.Fatalf("dialog should show the Account: row at 80 cols, got:\n%s", view)
	}
	for _, want := range []string{"inherit", "buddii", "personal", "seminno", "work"} {
		if !strings.Contains(view, want) {
			t.Errorf("Account row missing value %q at 80 cols:\n%s", want, view)
		}
	}
}

// TestNewDialogCommandRowNeverWrapsMidWordAt80Cols: at 80 columns the
// Command: tool row used to wrap mid-word ("copi" / "lot" for "copilot")
// because its pills were joined with no literal space, leaving word-wrap no
// break point except inside a tool name.
func TestNewDialogCommandRowNeverWrapsMidWordAt80Cols(t *testing.T) {
	d := NewNewDialog()
	d.SetSize(80, 60)
	d.Show()

	view := tmux.StripANSI(d.View())
	if !strings.Contains(view, "copilot") {
		t.Errorf("expected the whole word 'copilot' to appear at 80 cols, got:\n%s", view)
	}

	// Every preset tool name must appear intact somewhere in the view.
	for _, cmd := range d.presetCommands {
		name := "shell"
		if cmd != "" {
			name = displayCommandPreset(cmd)
		}
		if !strings.Contains(view, name) {
			t.Errorf("tool name %q not found intact in the 80-col Command row:\n%s", name, view)
		}
	}
}
