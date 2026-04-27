//go:build eval_smoke

package ui

// Behavioral eval for the TUI EditSessionDialog (CLAUDE.md:82-108 mandate
// for interactive prompts). Lives in internal/ui/ because Go's internal-
// package rule blocks tests/eval/... from importing it; still runs under
// `-tags eval_smoke`. See tests/eval/README.md.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// Empty-form regression: a refactor that reset textinputs after Show
// would leave fields blank and the next save would zero the instance.
func TestEval_EditSessionDialog_ShowRendersCurrentInstanceValues(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	inst := &session.Instance{
		ID:        "sess-eval-1",
		Title:     "my-eval-session",
		Tool:      "claude",
		ExtraArgs: []string{"--model", "haiku"},
		GroupPath: "personal/scratch",
	}
	d.Show(inst)

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyShiftTab})

	view := d.View()
	want := []string{
		"Edit Session",    // dialog title (parity with "New Session")
		"in group:",       // group header parity with newdialog
		"scratch",         // last segment of GroupPath
		"my-eval-session", // Title prepopulated
		"--model haiku",
		"Skip permissions",
		"Auto mode",
	}
	for _, tok := range want {
		if !strings.Contains(view, tok) {
			t.Errorf("View() missing expected token %q.\nFull view:\n%s", tok, view)
		}
	}
}

// Shell sessions have no claude-specific extra args — surfacing them would
// only lead to a SetField "claude only" rejection on submit.
func TestEval_EditSessionDialog_ShellToolHidesExtraArgs(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	d.Show(&session.Instance{
		ID:    "sess-eval-shell",
		Title: "shell-session",
		Tool:  "shell",
	})

	view := d.View()
	if strings.Contains(view, "Extra args") {
		t.Errorf("shell-tool session should not render Extra args; rendered view:\n%s", view)
	}
	for _, required := range []string{"Title", "Tool"} {
		if !strings.Contains(view, required) {
			t.Errorf("shell-tool session should still render %q; got:\n%s", required, view)
		}
	}
}

// "Why isn't my rename applying?" — a misplaced return in the navigation
// switch could swallow runes before they reach the textinput, leaving the
// dialog looking edited but GetChanges empty.
func TestEval_EditSessionDialog_TypingAndEnterProducesChange(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	inst := &session.Instance{
		ID:    "sess-eval-type",
		Title: "original",
		Tool:  "claude",
	}
	d.Show(inst)

	// Title is the first field — focus is already there after Show.
	for range "original" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "renamed" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})

	changes := d.GetChanges(inst)
	if len(changes) != 1 || changes[0].Field != session.FieldTitle || changes[0].Value != "renamed" {
		t.Fatalf("expected single title change to 'renamed'; got %+v", changes)
	}
	// The rendered frame must show the typed value — a broken View() would
	// still show "original" and a user screenshot would look broken to them.
	if !strings.Contains(d.View(), "renamed") {
		t.Errorf("View() after typing should contain 'renamed'; got:\n%s", d.View())
	}
}

// home.go reads HasRestartRequiredChanges to decide on the "press R to
// restart" hint. False negative → user wonders why edits don't apply.
// ExtraArgs is the canonical restart-required text field in the slim set.
func TestEval_EditSessionDialog_RestartHintSurfacesForRestartFields(t *testing.T) {
	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	inst := &session.Instance{
		ID:    "sess-eval-restart",
		Title: "restart-test",
		Tool:  "claude",
	}
	d.Show(inst)

	idx := -1
	for i, f := range d.fields {
		if f.key == session.FieldExtraArgs {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("ExtraArgs field not found in dialog")
	}
	d.focusIndex = idx
	d.updateFocus()

	for _, r := range "--model haiku" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if !d.HasRestartRequiredChanges(inst) {
		t.Error("editing ExtraArgs must flag HasRestartRequiredChanges=true; home.go relies on this to prompt the user")
	}
	changes := d.GetChanges(inst)
	var extraChange *Change
	for i := range changes {
		if changes[i].Field == session.FieldExtraArgs {
			extraChange = &changes[i]
			break
		}
	}
	if extraChange == nil {
		t.Fatalf("GetChanges did not include ExtraArgs edit; got %+v", changes)
	}
	if extraChange.IsLive {
		t.Error("ExtraArgs change must carry IsLive=false so home.go labels it restart-required")
	}
	if extraChange.Value != "--model haiku" {
		t.Errorf("ExtraArgs change value = %q, want %q", extraChange.Value, "--model haiku")
	}
}

// TestEval_PluginToggleSurfacesAsRestartHint locks the RFC PLUGIN_ATTACH.md
// §4.8 invariant: editing the Plugins field in the EditSessionDialog must
// surface as a restart-required change so home.go's auto-restart pipeline
// fires (claude reads enabledPlugins only at process start).
//
// CLAUDE.md:82-108 mandate eval coverage for any interactive prompt that
// pure Go tests cannot structurally express — the live read of
// HasRestartRequiredChanges + GetChanges is the contract that home.go
// relies on, and a refactor that broke it would not be caught by the
// pure-unit tests in edit_session_dialog_test.go (which exercise the
// dialog state machinery in isolation).
func TestEval_PluginToggleSurfacesAsRestartHint(t *testing.T) {
	// Set up a HOME with a non-empty plugin catalog so the dialog renders
	// the Plugins field. The dialog reads via session.GetAvailablePluginNames.
	temp := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", temp)
	t.Cleanup(func() { os.Setenv("HOME", originalHome) })

	configDir := filepath.Join(temp, ".agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir agent-deck: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	d := NewEditSessionDialog()
	d.SetSize(100, 40)
	inst := &session.Instance{
		ID:    "sess-eval-plugin",
		Title: "plugin-test",
		Tool:  "claude",
	}
	d.Show(inst)

	// Find the Plugins field — must be present for claude with non-empty
	// catalog. Absence here is itself a regression worth catching.
	idx := -1
	for i, f := range d.fields {
		if f.key == session.FieldPlugins {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("Plugins field not registered in dialog despite catalog containing octopus and tool=claude")
	}

	// Type the catalog name into the field. The dialog stores it as a CSV
	// string; the mutator parses on save.
	d.focusIndex = idx
	d.updateFocus()
	for _, r := range "octopus" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if !d.HasRestartRequiredChanges(inst) {
		t.Error("editing Plugins must flag HasRestartRequiredChanges=true; home.go relies on this for auto-restart")
	}
	changes := d.GetChanges(inst)
	var pluginChange *Change
	for i := range changes {
		if changes[i].Field == session.FieldPlugins {
			pluginChange = &changes[i]
			break
		}
	}
	if pluginChange == nil {
		t.Fatalf("GetChanges did not include Plugins edit; got %+v", changes)
	}
	if pluginChange.IsLive {
		t.Error("Plugins change must carry IsLive=false (restart-required); claude reads enabledPlugins only at process start")
	}
	if pluginChange.Value != "octopus" {
		t.Errorf("Plugins change value = %q, want %q", pluginChange.Value, "octopus")
	}

	// View() must render the typed value so a user screenshot looks right.
	if !strings.Contains(d.View(), "octopus") {
		t.Errorf("View() after typing must contain 'octopus'; rendered:\n%s", d.View())
	}
}
