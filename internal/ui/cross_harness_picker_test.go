package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Synthetic dialog fixture: the selected tool determines target account slots;
// no lifecycle, process, credentials, or real transcript is involved.
func TestEditSessionDialog_TargetHarnessFiltersAccountPicker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	cfg := &session.UserConfig{Profiles: map[string]session.ProfileSettings{
		"claude-only": {Claude: session.ProfileClaudeSettings{ConfigDir: home + "/claude"}},
		"codex-only":  {Codex: session.ProfileCodexSettings{ConfigDir: home + "/codex"}},
	}}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	dialog := NewEditSessionDialog()
	dialog.Show(&session.Instance{ID: "picker", Tool: "claude", Account: "claude-only"})
	dialog.refreshTargetAccountPills("codex")
	found := false
	for _, field := range dialog.fields {
		if field.key != session.FieldAccount {
			continue
		}
		found = true
		if len(field.pillOptions) != 2 || field.pillOptions[1] != "codex-only" {
			t.Fatalf("Codex target account picker = %#v, want only codex-only", field.pillOptions)
		}
		break
	}
	if !found {
		t.Fatal("target account picker was not shown")
	}

	// Pi has no source-account field, but it can choose a configured Claude or
	// Codex destination after the target harness changes.
	dialog.Show(&session.Instance{ID: "pi-picker", Tool: "pi"})
	found = false
	for _, field := range dialog.fields {
		if field.key == session.FieldAccount {
			found = true
			if len(field.pillOptions) != 1 || field.pillOptions[0] != "" {
				t.Fatalf("Pi initial target account picker = %#v, want default only", field.pillOptions)
			}
		}
	}
	if !found {
		t.Fatal("Pi source did not create target account picker")
	}
	for _, tc := range []struct{ harness, want string }{{"claude", "claude-only"}, {"codex", "codex-only"}} {
		dialog.refreshTargetAccountPills(tc.harness)
		for _, field := range dialog.fields {
			if field.key == session.FieldAccount {
				if len(field.pillOptions) != 2 || field.pillOptions[1] != tc.want {
					t.Fatalf("Pi-to-%s account picker = %#v, want %q", tc.harness, field.pillOptions, tc.want)
				}
				break
			}
		}
	}
}

// Shift+P keeps the source session intact while the user navigates destination
// harness then destination-specific account slots. The concise summary makes
// clear that Pi → Claude creates a fresh target rather than restarting Pi.
func TestEditSessionDialog_PiToClaudeRendersDestinationAccountAndNarrowly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	cfg := &session.UserConfig{Profiles: map[string]session.ProfileSettings{
		"claude-target": {Claude: session.ProfileClaudeSettings{ConfigDir: filepath.Join(home, "claude")}},
	}}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	d := NewEditSessionDialog()
	d.SetSize(100, 30)
	d.Show(&session.Instance{ID: "pi-source", Title: "Pi source", Tool: "pi"})

	tool := -1
	for i, field := range d.fields {
		if field.key == session.FieldTool {
			tool = i
			break
		}
	}
	if tool < 0 {
		t.Fatal("missing harness field")
	}
	d.focusIndex = tool
	for tries := 0; tries < len(d.fields[tool].pillOptions); tries++ {
		if d.fields[tool].pillOptions[d.fields[tool].pillCursor] == "claude" {
			break
		}
		d.Update(tea.KeyMsg{Type: tea.KeyRight})
	}
	if got := d.fields[tool].pillOptions[d.fields[tool].pillCursor]; got != "claude" {
		t.Fatalf("selected harness = %q, want claude", got)
	}
	account := accountFieldIndex(t, d)
	d.focusIndex = account
	d.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := d.fields[account].pillOptions[d.fields[account].pillCursor]; got != "claude-target" {
		t.Fatalf("selected Claude account = %q, want claude-target", got)
	}
	view := d.View()
	for _, want := range []string{"Harness (choose destination first)", "Account for selected harness", "NEW claude/claude-target", "source kept"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Pi→Claude view missing %q:\n%s", want, view)
		}
	}
	d.SetSize(28, 18)
	if got := lipgloss.Width(d.View()); got > 28 {
		t.Fatalf("narrow dialog width = %d, want <= 28", got)
	}
}

// Esc remains a no-write exit from Shift+P; the source and the selected
// account must remain unchanged until the separate confirmation/switch path.
func TestEditSessionDialog_EscCancelsSameHarnessAccountChangeWithoutMutation(t *testing.T) {
	withAccountsConfig(t, "personal", "work")
	inst := sampleInstance()
	inst.Account = "personal"
	d := NewEditSessionDialog()
	d.Show(inst)
	d.focusIndex = accountFieldIndex(t, d)
	d.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := d.selectedPill(session.FieldAccount); got != "work" {
		t.Fatalf("selected account = %q, want work", got)
	}
	if view := d.View(); !strings.Contains(view, "claude/personal → claude/work") || !strings.Contains(view, "same-harness resume") {
		t.Fatalf("same-harness account summary missing:\n%s", view)
	}
	d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if inst.Tool != "claude" || inst.Account != "personal" {
		t.Fatalf("Esc mutated source: tool=%q account=%q", inst.Tool, inst.Account)
	}
	if d.GetChanges(inst) == nil {
		t.Fatal("selection should remain local dialog state until Home cancels it")
	}
}
