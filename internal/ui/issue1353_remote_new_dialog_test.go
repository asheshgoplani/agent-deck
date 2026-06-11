package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #1353: pressing `n` while the cursor is on a remote group/session used
// to silently quick-create a SHELL session on the remote (`add --quick --json`,
// no -c flag) with no tool-selection dialog. The user got a shell regardless of
// the tool they wanted (claude, codex, pi, ...).
//
// New contract: `n` on a remote item opens the same new-session dialog as for
// local items, with the remote target remembered (Home.pendingRemoteName).
// Submitting routes the create to the remote via SSH with the chosen tool
// (`add --json -c <tool> -t <title> [path]`) — the local-only paths (worktree
// resolution, directory-exists check, local create) are all skipped, which
// preserves the #743 invariant that the session must NOT be created on
// localhost.
//
// `N` (quick create) on a remote is intentionally unchanged: it still
// quick-creates on the remote without a dialog.

func remoteGroupItem(name string) session.Item {
	return session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: name, Path: "remotes/" + name}
}

func remoteSessionItem(name string) session.Item {
	rs := session.RemoteSessionInfo{ID: "remote-123", Title: "remote-session", RemoteName: name}
	return session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &rs, RemoteName: name}
}

func pressN(t *testing.T, h *Home) *Home {
	t.Helper()
	model, _ := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	home, ok := model.(*Home)
	if !ok {
		t.Fatal("handleMainKey should return *Home")
	}
	return home
}

// TestIssue1353_NOnRemoteGroup_OpensDialog: `n` on a remote group header must
// open the new-session dialog (tool selection included) instead of silently
// quick-creating a shell on the remote.
func TestIssue1353_NOnRemoteGroup_OpensDialog(t *testing.T) {
	setXDGTestHome(t)
	home := NewHome()
	home.width = 100
	home.height = 30
	home.flatItems = []session.Item{remoteGroupItem("myserver")}
	home.cursor = 0

	h := pressN(t, home)
	if !h.newDialog.IsVisible() {
		t.Fatal("pressing n on a remote group must open the new-session dialog (#1353)")
	}
	if h.pendingRemoteName != "myserver" {
		t.Fatalf("pendingRemoteName = %q, want %q", h.pendingRemoteName, "myserver")
	}
}

// TestIssue1353_NOnRemoteSession_OpensDialog: same contract when the cursor is
// on a remote session row.
func TestIssue1353_NOnRemoteSession_OpensDialog(t *testing.T) {
	setXDGTestHome(t)
	home := NewHome()
	home.width = 100
	home.height = 30
	home.flatItems = []session.Item{remoteSessionItem("myserver")}
	home.cursor = 0

	h := pressN(t, home)
	if !h.newDialog.IsVisible() {
		t.Fatal("pressing n on a remote session must open the new-session dialog (#1353)")
	}
	if h.pendingRemoteName != "myserver" {
		t.Fatalf("pendingRemoteName = %q, want %q", h.pendingRemoteName, "myserver")
	}
}

// TestIssue1353_RemoteDialogDefaults: for a remote target the path field
// defaults to "." (remote CWD) so a local filesystem path is never sent to the
// remote. The dialog must NOT be seeded with the synthetic "remotes/<host>"
// label: that path is a local UI bucket, not a user-defined remote group, and
// handleNewDialogKey forwards the selected group unchanged to
// CreateSessionWithOptions — so seeding it would create a bogus "remotes/<host>"
// group on the remote. It defaults to the standard group instead.
func TestIssue1353_RemoteDialogDefaults(t *testing.T) {
	setXDGTestHome(t)
	home := NewHome()
	home.width = 100
	home.height = 30
	home.flatItems = []session.Item{remoteGroupItem("myserver")}
	home.cursor = 0

	h := pressN(t, home)
	_, path, _ := h.newDialog.GetValues()
	if path != "." {
		t.Fatalf("remote dialog path default = %q, want %q (remote CWD)", path, ".")
	}
	if got := h.newDialog.GetSelectedGroup(); got != session.DefaultGroupPath {
		t.Fatalf("remote dialog group = %q, want %q (no synthetic remotes/<host> group)", got, session.DefaultGroupPath)
	}
}

// TestIssue1353_EscClearsPendingRemote: cancelling the dialog must clear the
// remote target so a later local `n` does not accidentally create remotely.
func TestIssue1353_EscClearsPendingRemote(t *testing.T) {
	setXDGTestHome(t)
	home := NewHome()
	home.width = 100
	home.height = 30
	home.flatItems = []session.Item{remoteGroupItem("myserver")}
	home.cursor = 0

	h := pressN(t, home)
	if h.pendingRemoteName == "" {
		t.Fatal("precondition: pendingRemoteName must be set after n on remote")
	}
	h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEsc})
	if h.newDialog.IsVisible() {
		t.Fatal("Esc must close the dialog")
	}
	if h.pendingRemoteName != "" {
		t.Fatalf("Esc must clear pendingRemoteName, got %q", h.pendingRemoteName)
	}
}

// TestIssue1353_SubmitRoutesToRemote: submitting the dialog for a remote
// target must route to the remote-create path (never the local create). The
// sandboxed test config has no [remotes.myserver], so the returned command
// resolves to a remote-lookup error — proof the submit took the remote branch
// instead of creating a local session.
func TestIssue1353_SubmitRoutesToRemote(t *testing.T) {
	setXDGTestHome(t)
	home := NewHome()
	home.width = 100
	home.height = 30
	home.flatItems = []session.Item{remoteGroupItem("myserver")}
	home.cursor = 0

	h := pressN(t, home)
	// Type a session name (focus starts on the Name field).
	for _, r := range "my-remote-task" {
		h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	model, cmd := h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	h = model.(*Home)

	if h.newDialog.IsVisible() {
		t.Fatal("submit must close the dialog")
	}
	if h.pendingRemoteName != "" {
		t.Fatalf("submit must consume pendingRemoteName, got %q", h.pendingRemoteName)
	}
	if cmd == nil {
		t.Fatal("submit must return a command (remote create)")
	}
	msg := cmd()
	created, ok := msg.(sessionCreatedMsg)
	if !ok {
		t.Fatalf("expected sessionCreatedMsg from remote-create path, got %T", msg)
	}
	if created.err == nil || !strings.Contains(created.err.Error(), "remote") {
		t.Fatalf("expected remote-lookup error (proves remote routing), got %v", created.err)
	}
	// The local create path must not have run: no instances created.
	if len(h.instances) != 0 {
		t.Fatalf("local create must not run for remote targets; got %d instances", len(h.instances))
	}
}

// TestIssue1353_LocalNUnaffected: `n` on a local group keeps the existing
// behavior and must not leave any stale remote target around.
func TestIssue1353_LocalNUnaffected(t *testing.T) {
	setXDGTestHome(t)
	home := NewHome()
	home.width = 100
	home.height = 30
	home.flatItems = []session.Item{{
		Type:  session.ItemTypeGroup,
		Group: &session.Group{Name: "proj", Path: "proj"},
		Path:  "proj",
	}}
	home.cursor = 0
	// Simulate a previous remote flow that was abandoned without Esc.
	home.pendingRemoteName = "stale-remote"

	h := pressN(t, home)
	if !h.newDialog.IsVisible() {
		t.Fatal("n on a local group must open the dialog")
	}
	if h.pendingRemoteName != "" {
		t.Fatalf("n on a local group must clear pendingRemoteName, got %q", h.pendingRemoteName)
	}
}
