package ui

// Follow-ups from the review of the Enter-creates change (#2913).
//
// The New Session dialog previews the name an empty Name field gets on
// submit. These tests pin that the preview is the name the create uses (the
// uniqueness suffix, an expanded ~ or ., the name length limit), that two
// quick creates never share a derived name, that a failed submit leaves the
// Name field empty, that cancelling "Directory Not Found" returns to the
// dialog, and that turning on a worktree with an empty Name fills Branch.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func mkFolder(t *testing.T, name string) string {
	t.Helper()
	folder := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	return folder
}

// namePreview renders the dialog and returns what the empty Name field shows.
func namePreview(h *Home) string {
	_ = h.newDialog.View()
	return h.newDialog.nameInput.Placeholder
}

func setInstances(h *Home, insts ...*session.Instance) {
	h.instancesMu.Lock()
	h.instances = insts
	h.instancesMu.Unlock()
}

// A folder that already has a session previews the suffixed name it creates.
func TestNewSession2913_PreviewShowsUniqueSuffix(t *testing.T) {
	folder := mkFolder(t, "opencode-idle")
	h := quickFlowHome(t, folder)
	setInstances(h, session.NewInstanceWithTool("opencode-idle", folder, "claude"))

	if got := namePreview(h); got != "opencode-idle-2" {
		t.Fatalf("name preview = %q, want opencode-idle-2 (the name the create uses)", got)
	}
	cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
	if !submitted(h, cmd) {
		t.Fatalf("Enter did not create (error %q)", h.newDialog.validationErr)
	}
	if got := h.newDialog.nameInput.Value(); got != "opencode-idle-2" {
		t.Fatalf("created name = %q, want opencode-idle-2", got)
	}
}

// `~` and `.` preview the expanded folder's name, not `~` or the placeholder.
func TestNewSession2913_PreviewExpandsTildeAndDot(t *testing.T) {
	home := mkFolder(t, "alice")
	t.Setenv("HOME", home)
	cwd := mkFolder(t, "billing-api")
	t.Chdir(cwd)

	for _, tc := range []struct{ path, want string }{
		{"~", "alice"},
		{".", "billing-api"},
		{"./", "billing-api"},
	} {
		h := quickFlowHome(t, tc.path)
		h.newDialog.pathInput.SetValue(tc.path)
		if got := namePreview(h); got != tc.want {
			t.Errorf("path %q: name preview = %q, want %q", tc.path, got, tc.want)
		}
		cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
		if !submitted(h, cmd) {
			t.Fatalf("path %q: Enter did not create (error %q)", tc.path, h.newDialog.validationErr)
		}
		if got := h.newDialog.nameInput.Value(); got != tc.want {
			t.Errorf("path %q: created name = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// A derived name respects the 50-character limit the typed name has, with or
// without a uniqueness suffix.
func TestNewSession2913_DerivedNameFitsLengthLimit(t *testing.T) {
	long := strings.Repeat("a", MaxNameLength+20)
	folder := mkFolder(t, long)
	for _, collide := range []bool{false, true} {
		h := quickFlowHome(t, folder)
		if collide {
			setInstances(h, session.NewInstanceWithTool(long[:MaxNameLength], folder, "claude"))
		}
		preview := namePreview(h)
		cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
		if !submitted(h, cmd) {
			t.Fatalf("collide=%v: Enter did not create (error %q)", collide, h.newDialog.validationErr)
		}
		got := h.newDialog.nameInput.Value()
		if len(got) > MaxNameLength {
			t.Errorf("collide=%v: derived name has %d characters, limit is %d", collide, len(got), MaxNameLength)
		}
		if got != preview {
			t.Errorf("collide=%v: created name %q differs from the preview %q", collide, got, preview)
		}
		if collide && got == long[:MaxNameLength] {
			t.Errorf("derived name %q collides with the existing session", got)
		}
	}
}

// Two quick `n` Enter in the same folder get two names, even before the first
// session has reached the list.
func TestNewSession2913_QuickRepeatCreatesGetDistinctNames(t *testing.T) {
	folder := mkFolder(t, "billing-api")
	h := quickFlowHome(t, folder)
	if cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter}); !submitted(h, cmd) {
		t.Fatalf("first Enter did not create (error %q)", h.newDialog.validationErr)
	}
	first := h.newDialog.nameInput.Value()

	h.newDialog.ShowInGroup(session.DefaultGroupPath, session.DefaultGroupName, folder, nil, "")
	if got := namePreview(h); got == first {
		t.Fatalf("second dialog previews %q, the name of the session still being created", got)
	}
	if cmd := quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter}); !submitted(h, cmd) {
		t.Fatalf("second Enter did not create (error %q)", h.newDialog.validationErr)
	}
	if second := h.newDialog.nameInput.Value(); second == first {
		t.Fatalf("two quick creates both got %q", first)
	}

	// A create that failed frees its name again.
	h.Update(sessionCreatedMsg{err: errors.New("boom"), reservedTitle: first})
	h.newDialog.ShowInGroup(session.DefaultGroupPath, session.DefaultGroupName, folder, nil, "")
	if got := namePreview(h); got != first {
		t.Fatalf("after the first create failed the preview is %q, want %q back", got, first)
	}
}

// A submit refused after the name is known (here: an explicit worktree on a
// folder that is not a repo) leaves the Name field empty, so changing the path
// still changes the name.
func TestNewSession2913_RefusedSubmitLeavesNameEmpty(t *testing.T) {
	folder := mkFolder(t, "billing-api")
	h := quickFlowHome(t, folder)
	h.newDialog.ToggleWorktree()
	h.newDialog.branchInput.SetValue("feature/x")

	quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
	if !h.newDialog.IsVisible() || h.newDialog.validationErr == "" {
		t.Fatalf("worktree on a non-repo should be refused in the dialog (visible %v, error %q)",
			h.newDialog.IsVisible(), h.newDialog.validationErr)
	}
	if got := h.newDialog.nameInput.Value(); got != "" {
		t.Fatalf("refused submit wrote %q into the Name field; it goes stale when the path changes", got)
	}
}

// Cancel on "Directory Not Found" (a mistyped path) returns to the New
// Session dialog with what was entered, focus on Path, instead of closing it.
func TestNewSession2913_CancelMissingDirectoryReturnsToDialog(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter}, // Cancel is the default button
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'n'}},
	} {
		parent := t.TempDir()
		typo := filepath.Join(parent, "biling-api")
		h := quickFlowHome(t, parent)
		h.newDialog.pathInput.SetValue(typo)

		quickFlowKey(h, tea.KeyMsg{Type: tea.KeyEnter})
		if !h.confirmDialog.IsVisible() || h.confirmDialog.GetConfirmType() != ConfirmCreateDirectory {
			t.Fatalf("Enter on a missing path should ask to create it")
		}
		h.handleConfirmDialogKey(key)
		if h.confirmDialog.IsVisible() {
			t.Fatalf("%q: confirmation still open", key.String())
		}
		if !h.newDialog.IsVisible() {
			t.Fatalf("%q: cancelling Directory Not Found closed the New Session dialog", key.String())
		}
		if got := h.newDialog.pathInput.Value(); got != typo {
			t.Errorf("%q: path = %q, want the entered %q kept", key.String(), got, typo)
		}
		if got := h.newDialog.currentTarget(); got != focusPath {
			t.Errorf("%q: focus = %v, want the Path row", key.String(), got)
		}
		if got := h.newDialog.nameInput.Value(); got != "" {
			t.Errorf("%q: Name field = %q, want it left empty (derived names follow the path)", key.String(), got)
		}
	}
}

// With Name empty, turning on a worktree fills Branch from the name the
// session will get, so Enter does not stop at "Branch name required".
func TestNewSession2913_WorktreeWithEmptyNameFillsBranch(t *testing.T) {
	folder := mkFolder(t, "billing-api")
	h := quickFlowHome(t, folder)
	d := h.newDialog
	d.ToggleWorktree()
	if got, want := d.branchInput.Value(), d.branchPrefix+"billing-api"; got != want {
		t.Fatalf("branch after turning on worktree with an empty name = %q, want %q", got, want)
	}
	if msg, _ := d.validate(); msg != "" {
		t.Fatalf("form should be valid, got %q", msg)
	}

	// The branch follows the path while it is still derived.
	other := mkFolder(t, "payments")
	flowFocus(t, d, focusPath)
	d.pathInput.SetValue("")
	for _, r := range other {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got, want := d.branchInput.Value(), d.branchPrefix+"payments"; got != want {
		t.Fatalf("branch after changing the path = %q, want %q", got, want)
	}
}

// The same with [worktree] default_enabled = true: the dialog opens with
// Branch filled from the derived name, so the first Enter does not stop at
// "Branch name required".
func TestNewSession2913_WorktreeByDefaultWithEmptyNameFillsBranch(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, ".config"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	if err := os.MkdirAll(filepath.Join(tempDir, ".agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &session.UserConfig{}
	cfg.Worktree.DefaultEnabled = true
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()

	folder := mkFolder(t, "billing-api")
	h := quickFlowHome(t, folder)
	d := h.newDialog
	if !d.IsWorktreeEnabled() {
		t.Fatal("config default_enabled did not turn on the worktree")
	}
	if got, want := d.branchInput.Value(), d.branchPrefix+"billing-api"; got != want {
		t.Fatalf("branch on open with an empty name = %q, want %q", got, want)
	}
	if msg, _ := d.validate(); msg != "" {
		t.Fatalf("form should be valid on open, got %q", msg)
	}
}
