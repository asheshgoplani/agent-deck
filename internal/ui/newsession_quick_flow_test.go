package ui

// New Session in one or two keys (ASK-110).
//
// Owner report (2026-10-22): "keep in mind the new session thing, it is hard;
// we have to go to the end to do it." Enter walked the whole form to a
// trailing Create button and the name was required, so the common case (this
// folder, default tool, default group) took `n`, a typed name and one Enter
// per row: 13 keys for a 4-letter name with Claude selected.
//
// These tests pin the new contract: Enter creates from any row with the
// defaults that are already filled in, an empty name is derived from the
// folder, an invalid required field takes focus and says why, and Tab/↓ still
// walk the optional rows below.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// quickFlowHome opens the New Session dialog the way `n` does on the home
// screen, with Claude as the remembered tool and the given folder as the
// group's default path.
func quickFlowHome(t *testing.T, folder string) *Home {
	t.Helper()
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetSize(120, 60)
	h.newDialog.SetDefaultTool("claude")
	h.newDialog.ShowInGroup(session.DefaultGroupPath, session.DefaultGroupName, folder, nil, "")
	return h
}

// submitted reports whether the last key created a session: the dialog closed
// on the create path (not on a confirmation) and a create command came back.
func submitted(h *Home, cmd tea.Cmd) bool {
	return !h.newDialog.IsVisible() && !h.confirmDialog.IsVisible() && cmd != nil
}

func quickFlowKey(h *Home, msg tea.KeyMsg) tea.Cmd {
	_, cmd := h.handleNewDialogKey(msg)
	return cmd
}

// The common case from the home screen: `n` opens the dialog with this
// folder, the remembered tool and the default group filled in, and Enter
// creates. Two keys, no typing.
func TestNewSessionQuickFlow_CommonCaseIsTwoKeys(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "billing-api")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(folder)
	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetSize(120, 60)

	keys := 0
	model, _ := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	keys++
	h = model.(*Home)
	if !h.newDialog.IsVisible() {
		t.Fatal("n did not open the New Session dialog")
	}
	cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
	keys++
	if !submitted(h, cmd) {
		t.Fatalf("Enter on the freshly opened dialog did not create the session (error %q, focus %v)",
			h.newDialog.validationErr, h.newDialog.currentTarget())
	}
	if keys != 2 {
		t.Fatalf("common case took %d keys, want 2", keys)
	}
	if got := h.newDialog.nameInput.Value(); got != "billing-api" {
		t.Fatalf("derived session name = %q, want the folder name %q", got, "billing-api")
	}
}

// The empty name field shows the name it will get, so Enter is not a guess.
func TestNewSessionQuickFlow_NamePlaceholderShowsFolderName(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "billing-api")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	h := quickFlowHome(t, folder)
	view := stripAnsi(h.newDialog.View())
	if !strings.Contains(view, "billing-api") || strings.Contains(view, "session-name") {
		t.Fatalf("empty name field should preview the folder name:\n%s", view)
	}
}

// A derived name never collides with an existing session title.
func TestNewSessionQuickFlow_DerivedNameIsUnique(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "billing-api")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	h := quickFlowHome(t, folder)
	existing := session.NewInstanceWithTool("billing-api", folder, "claude")
	h.instancesMu.Lock()
	h.instances = []*session.Instance{existing}
	h.instancesMu.Unlock()

	cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
	if !submitted(h, cmd) {
		t.Fatalf("Enter did not create (error %q)", h.newDialog.validationErr)
	}
	if got := h.newDialog.nameInput.Value(); got != "billing-api-2" {
		t.Fatalf("derived name = %q, want billing-api-2", got)
	}
}

// Enter creates from every row of the form, not only from the Create button
// at the end; optional rows never block it.
func TestNewSessionQuickFlow_EnterCreatesFromEveryField(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "shell"} {
		probe := quickFlowHome(t, t.TempDir())
		probe.newDialog.SetDefaultTool(tool)
		probe.newDialog.ShowInGroup(session.DefaultGroupPath, session.DefaultGroupName, t.TempDir(), nil, "")
		targets := append([]focusTarget(nil), probe.newDialog.focusTargets...)
		if len(targets) < 5 {
			t.Fatalf("%s: form has only %d rows, want the full form", tool, len(targets))
		}
		for _, target := range targets {
			for _, named := range []bool{false, true} {
				h := quickFlowHome(t, t.TempDir())
				h.newDialog.SetDefaultTool(tool)
				h.newDialog.ShowInGroup(session.DefaultGroupPath, session.DefaultGroupName, t.TempDir(), nil, "")
				if named {
					h.newDialog.nameInput.SetValue("demo")
				}
				flowFocus(t, h.newDialog, target)
				cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
				if !submitted(h, cmd) {
					t.Fatalf("%s, named=%v: Enter on %v did not create (visible=%v error=%q focus=%v)",
						tool, named, target, h.newDialog.IsVisible(), h.newDialog.validationErr, h.newDialog.currentTarget())
				}
			}
		}
	}
}

// An open dropdown still owns Enter: it picks, it does not create.
func TestNewSessionQuickFlow_OpenListsStillOwnEnter(t *testing.T) {
	h := quickFlowHome(t, t.TempDir())
	flowFocus(t, h.newDialog, focusModel)
	space(h) // open the model list
	if !h.newDialog.IsModelSuggestionsActive() {
		t.Fatal("precondition: model list open")
	}
	enter(h)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter in the open model list created the session; it must only pick")
	}

	flowFocus(t, h.newDialog, focusPath)
	h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRight}) // open browse
	if !h.newDialog.IsSuggestionsActive() {
		t.Skip("path browse list did not open in this environment")
	}
	enter(h)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter in the open path list created the session; it must only pick")
	}
}

// Validation keeps working: Enter on an invalid form moves focus to the
// field at fault and shows why, wherever the cursor was.
func TestNewSessionQuickFlow_ValidationJumpsToField(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(d *NewDialog)
		want   focusTarget
		reason string
	}{
		{"empty path", func(d *NewDialog) { d.pathInput.SetValue("") }, focusPath, "Project path cannot be empty"},
		{"multi-repo with one path", func(d *NewDialog) {
			d.multiRepoEnabled = true
			d.multiRepoPaths = []string{d.pathInput.Value()}
			d.rebuildFocusTargets()
		}, focusMultiRepo, "at least 2 paths"},
		{"invalid branch", func(d *NewDialog) {
			d.worktreeEnabled = true
			d.branchInput.SetValue("bad..branch")
			d.rebuildFocusTargets()
		}, focusBranch, ""},
		{"worktree without branch", func(d *NewDialog) {
			d.worktreeEnabled = true
			d.branchInput.SetValue("")
			d.rebuildFocusTargets()
		}, focusBranch, "Branch name required"},
	}
	for _, tc := range cases {
		h := quickFlowHome(t, t.TempDir())
		tc.setup(h.newDialog)
		flowFocus(t, h.newDialog, focusCommand)
		cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
		if submitted(h, cmd) || !h.newDialog.IsVisible() {
			t.Fatalf("%s: Enter created an invalid session", tc.name)
		}
		if h.newDialog.validationErr == "" || !strings.Contains(h.newDialog.validationErr, tc.reason) {
			t.Fatalf("%s: error = %q, want it to contain %q", tc.name, h.newDialog.validationErr, tc.reason)
		}
		if got := h.newDialog.currentTarget(); got != tc.want {
			t.Fatalf("%s: focus after the failed Enter = %v, want %v", tc.name, got, tc.want)
		}
		if view := stripAnsi(h.newDialog.View()); !strings.Contains(view, tc.reason) {
			t.Fatalf("%s: the reason is not shown:\n%s", tc.name, view)
		}
	}
}

// Every row's footer says Enter creates, and Tab still walks.
func TestNewSessionQuickFlow_FooterSaysEnterCreates(t *testing.T) {
	h := quickFlowHome(t, t.TempDir())
	for _, target := range h.newDialog.focusTargets {
		flowFocus(t, h.newDialog, target)
		view := stripAnsi(h.newDialog.View())
		if !strings.Contains(view, "Enter create") {
			t.Fatalf("%v: footer does not say Enter creates:\n%s", target, view)
		}
		if strings.Contains(view, "Enter next") {
			t.Fatalf("%v: footer still says Enter next:\n%s", target, view)
		}
	}
}

// #2152 / #924: a one-off account pick made in the dialog belongs to the
// session it created; Enter-creates must not carry it to the next opening.
func TestNewSessionQuickFlow_AccountPickDoesNotLeakAfterEnterCreate(t *testing.T) {
	withAccountsConfig(t, "work", "personal")
	h := quickFlowHome(t, t.TempDir())
	h.newDialog.claudeOptions.SetAccount("work")
	flowFocus(t, h.newDialog, focusName)
	cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
	if !submitted(h, cmd) {
		t.Fatalf("Enter did not create (error %q)", h.newDialog.validationErr)
	}
	h.newDialog.ShowInGroup(session.DefaultGroupPath, session.DefaultGroupName, t.TempDir(), nil, "")
	if got := h.newDialog.GetClaudeAccount(); got != "" {
		t.Fatalf("next opening carries account %q; each opening must start at inherit", got)
	}
}

// The walk is still there for people who opted in with
// [ui] new_session_enter_advances = true.
func TestNewSessionQuickFlow_OptInWalkStillAdvances(t *testing.T) {
	h := quickFlowHome(t, t.TempDir())
	h.newDialog.enterAdvances = true
	h.newDialog.nameInput.SetValue("demo")
	flowFocus(t, h.newDialog, focusName)
	enter(h)
	if !h.newDialog.IsVisible() || h.newDialog.currentTarget() == focusName {
		t.Fatal("opt-in walk: Enter on Name should advance, not create")
	}
}

// The one-key path from the home screen already exists: `N` creates a session
// in the selected group's folder with its defaults, with no dialog.
func TestNewSessionQuickFlow_ShiftNIsOneKey(t *testing.T) {
	t.Chdir(t.TempDir())
	h := NewHome()
	h.width, h.height = 120, 40
	_, cmd := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if cmd == nil || h.newDialog.IsVisible() {
		t.Fatal("N should quick-create without opening the dialog")
	}
}
