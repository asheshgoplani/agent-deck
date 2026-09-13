package ui

// Golden frames for the New Session dialog flow. Each fixture under
// testdata/newdialog_flow/ is the plain-text render of the dialog at one step
// of the walk the owner reported (model row, after Enter on it, the Create
// button, the Claude Options account row). Regenerate with
// UPDATE_GOLDEN=1 go test ./internal/ui/ -run TestNewSessionFlow_Golden
// and review the diff like any other test change.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func goldenDialog(t *testing.T) *NewDialog {
	t.Helper()
	// Pill rows wrap differently under the Ascii profile a bare `go test`
	// gets; pin the profile the TUI and the rest of the package render with.
	forceTrueColorProfile()
	d := NewNewDialog()
	d.SetDefaultTool("claude")
	d.SetSize(100, 50)
	d.Show()
	d.nameInput.SetValue("demo")
	d.pathInput.SetValue("/home/user/project")
	d.modelInput.SetValue("")
	// Config-derived toggles (dangerous_mode etc.) are pinned so the frame
	// does not depend on whatever config the test host has.
	d.claudeOptions.SetFromOptions(&session.ClaudeOptions{})
	d.claudeOptions.SetAccounts([]string{"personal", "work"})
	d.rebuildFocusTargets()
	return d
}

func TestNewSessionFlow_Golden(t *testing.T) {
	steps := []struct {
		name  string
		setup func(t *testing.T, d *NewDialog)
	}{
		{"01-model-row", func(t *testing.T, d *NewDialog) { flowFocus(t, d, focusModel) }},
		{"02-enter-on-model-advances", func(t *testing.T, d *NewDialog) {
			flowFocus(t, d, focusModel)
			d.Update(tea.KeyMsg{Type: tea.KeyEnter})
		}},
		{"03-space-opens-model-list", func(t *testing.T, d *NewDialog) {
			flowFocus(t, d, focusModel)
			d.Update(tea.KeyMsg{Type: tea.KeySpace})
		}},
		{"04-options-account-row", func(t *testing.T, d *NewDialog) {
			flowFocus(t, d, focusOptions)
			d.claudeOptions.FocusLast()
		}},
		{"05-create-button", func(t *testing.T, d *NewDialog) { flowFocus(t, d, focusCreate) }},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			d := goldenDialog(t)
			step.setup(t, d)
			got := strings.TrimRight(stripAnsi(d.View()), "\n") + "\n"
			path := filepath.Join("testdata", "newdialog_flow", step.name+".txt")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (UPDATE_GOLDEN=1 to create)", path, err)
			}
			if string(want) != got {
				t.Fatalf("golden %s differs from the rendered dialog.\n--- want\n%s\n--- got\n%s", path, want, got)
			}
		})
	}
}
