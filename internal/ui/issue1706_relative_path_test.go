package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Issue #1706 — "Wrong path generated session": the New Session dialog accepted
// a relative project path and passed it through verbatim. The TUI then resolved
// it against its own process cwd (directory-exists check + os.MkdirAll), while
// tmux resolved `new-session -c <relative>` against the tmux SERVER's cwd. The
// session therefore did not land in the directory the user named, and a folder
// could be created in a third, unrelated place.
//
// Contract: by the time a submitted local path reaches the create flow it is
// absolute. The CLI already did this (cmd/agent-deck/cli_utils.go); these tests
// pin it for the TUI.
//
// The submit is driven through the real key handler (handleNewDialogKey) and
// stops at the "Directory Not Found" confirmation, so no session and no
// directory are created — the assertion reads the pending path the confirmation
// captured.

func TestIssue1706_SubmitAnchorsRelativePathToAbsolute(t *testing.T) {
	setXDGTestHome(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	// Relative on purpose (that is the bug), but unique so the test can never
	// collide with a directory that happens to exist in the package dir.
	relPath := fmt.Sprintf("issue1706-relative-target-%d", time.Now().UnixNano())
	if _, err := os.Stat(relPath); !os.IsNotExist(err) {
		t.Fatalf("test precondition: %q must not exist (stat err = %v)", relPath, err)
	}
	want := filepath.Join(cwd, relPath)

	h := NewHome()
	h.width, h.height = 120, 40
	h.newDialog.SetSize(120, 50)
	h.newDialog.Show()
	h.newDialog.nameInput.SetValue("issue1706")
	h.newDialog.pathInput.SetValue(relPath)

	model, _ := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	home, ok := model.(*Home)
	if !ok {
		t.Fatal("handleNewDialogKey should return *Home")
	}

	if !home.confirmDialog.IsVisible() {
		t.Fatalf("submitting a non-existent path must open the create-directory confirmation (validation error: %q)", home.newDialog.validationErr)
	}
	if got := home.confirmDialog.confirmType; got != ConfirmCreateDirectory {
		t.Fatalf("confirmType = %v, want ConfirmCreateDirectory", got)
	}

	_, pendingPath, _, _, _, _, _, _, _, _ := home.confirmDialog.GetPendingSession()
	if !filepath.IsAbs(pendingPath) {
		t.Fatalf("pending session path = %q, want an absolute path (#1706: a relative path is created next to the TUI's cwd but tmux resolves it against the tmux server's cwd)", pendingPath)
	}
	if pendingPath != want {
		t.Fatalf("pending session path = %q, want %q (relative entry anchored to the process cwd)", pendingPath, want)
	}
	// The confirmation also displays the path; it must show the resolved one so
	// the user can see where the directory would actually be created.
	if got := home.confirmDialog.targetName; got != want {
		t.Fatalf("confirmation displays %q, want %q", got, want)
	}

	// Guard: the submit path must not have created anything on its own.
	if _, err := os.Stat(relPath); !os.IsNotExist(err) {
		t.Fatalf("submit must not create the directory before confirmation (stat err = %v)", err)
	}
}

func TestIssue1706_AbsLocalProjectPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "absolute path is unchanged", input: "/Users/dev/work/app", want: "/Users/dev/work/app"},
		{name: "bare relative name anchors to cwd", input: "app", want: filepath.Join(cwd, "app")},
		{name: "nested relative path anchors to cwd", input: "dev/app", want: filepath.Join(cwd, "dev", "app")},
		{name: "dot resolves to cwd", input: ".", want: cwd},
		{name: "dot-slash prefix is cleaned", input: "./app", want: filepath.Join(cwd, "app")},
		{name: "parent traversal is cleaned", input: "/Users/dev/work/../app", want: "/Users/dev/app"},
		{name: "empty stays empty", input: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := absLocalProjectPath(tt.input)
			if err != nil {
				t.Fatalf("absLocalProjectPath(%q) returned error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("absLocalProjectPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if tt.want != "" && !filepath.IsAbs(got) {
				t.Fatalf("absLocalProjectPath(%q) = %q, which is not absolute", tt.input, got)
			}
		})
	}
}

// The submit resolves every declared multi-repo path to an absolute one, so
// Validate's duplicate check has to compare resolved paths — otherwise "repo"
// and "./repo" pass validation and then collapse into the same project path
// plus a duplicate additional_paths entry.
func TestIssue1706_ValidateRejectsRelativeDuplicateSpellings(t *testing.T) {
	d := NewNewDialog()
	d.nameInput.SetValue("issue1706")
	d.multiRepoEnabled = true
	d.multiRepoPaths = []string{"repo-alpha", "./repo-alpha"}

	if got := d.Validate(); got != "Duplicate paths in multi-repo mode" {
		t.Fatalf("Validate() = %q, want the duplicate-path rejection (both spellings resolve to the same directory)", got)
	}
}
