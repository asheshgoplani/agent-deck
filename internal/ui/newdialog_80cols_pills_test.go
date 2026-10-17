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

// The v1.16.11 pill-wrap fix inserted plain separator cells after each pill's
// SGR reset. On terminals such as Kitty, whose native background is black,
// those cells appeared as a row of black squares through the dialog surface.
func TestNewDialogCommandPillSeparatorsKeepSurfaceBackground(t *testing.T) {
	forceTrueColorProfile()
	useThemeForTest(t, "dark")

	d := NewNewDialog()
	d.presetCommands = []string{"", "claude", "gemini"}
	d.SetSize(100, 30)
	d.Show()

	sawCommandRow := false
	for i, row := range strings.Split(d.View(), "\n") {
		plain := stripAnsi(row)
		if !strings.Contains(plain, "shell") || !strings.Contains(plain, "claude") {
			continue
		}
		sawCommandRow = true
		// Check through the final pill, excluding the row's trailing padding:
		// that older gap is independent of the inter-option regression.
		end := strings.Index(row, "gemini")
		if end < 0 {
			t.Fatal("final command pill was not rendered")
		}
		pillRow := row[:end+len("gemini")] + "│"
		if holes := backgroundHoles(pillRow); len(holes) > 0 {
			t.Errorf("command row %d exposes the terminal background at %q\nplain=%q\nraw=%q", i, holes, plain, row)
		}
	}
	if !sawCommandRow {
		t.Fatal("command pill row was not rendered")
	}
}

func TestSharedPillSeparatorsKeepSurfaceBackground(t *testing.T) {
	forceTrueColorProfile()
	useThemeForTest(t, "dark")

	for name, pills := range map[string]string{
		"labels": renderLabelPills([]string{"inherit", "work"}, 0),
		"tools":  renderToolPills([]string{"", "claude"}, 0),
	} {
		t.Run(name, func(t *testing.T) {
			row := "│" + pills + "│"
			if holes := backgroundHoles(row); len(holes) > 0 {
				t.Fatalf("pill row exposes the terminal background at %q: %q", holes, row)
			}
		})
	}
}
