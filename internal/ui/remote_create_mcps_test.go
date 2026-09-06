package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The remote new-session dialog offers the target remote's MCPs (its
// `mcp list --json`, fetched when the dialog opens) as a row of checkboxes
// and forwards the picks as RemoteAddOptions.MCPs (one --mcp each). The row
// never lists this machine's MCPs: the server resolves every name against
// its own config.toml and would reject a local-only one.

const localOnlyMCPConfig = "[mcps.local-only]\ncommand = \"true\"\n"

// pickRemoteMCP moves focus to the MCP row, walks right to the named entry
// and presses Space, the way a user would.
func pickRemoteMCP(t *testing.T, h *Home, name string) {
	t.Helper()
	idx := h.newDialog.indexOf(focusRemoteMCPs)
	if idx < 0 {
		t.Fatalf("MCP row is not focusable; offered = %v", h.newDialog.remoteMCPs)
	}
	h.newDialog.focusIndex = idx
	h.newDialog.updateFocus()
	for i := 0; i < len(h.newDialog.remoteMCPs); i++ {
		if h.newDialog.remoteMCPs[h.newDialog.remoteMCPCursor] == name {
			h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeySpace})
			return
		}
		h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRight})
	}
	t.Fatalf("MCP %q is not offered; offered = %v", name, h.newDialog.remoteMCPs)
}

func TestRemoteDialog_MCPs_ComeFromRemote(t *testing.T) {
	t.Run("local-only MCP is not offered and the remote is asked", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), localOnlyMCPConfig, "claude", "mcp-task")
		if got := strings.Join(session.GetAvailableMCPNames(), ","); got != "local-only" {
			t.Fatalf("precondition: this machine's config must define the local-only MCP, got %q", got)
		}
		if h.newDialog.hasRemoteMCPRow() {
			t.Fatalf("remote dialog offers %v before the remote answered, want no row", h.newDialog.remoteMCPs)
		}
		if strings.Join(capture.mcpsFetchedFor, ",") != "myserver" {
			t.Fatalf("MCP names fetched for %v, want myserver once", capture.mcpsFetchedFor)
		}
		submitRemoteDialog(t, h)
		if len(capture.opts.MCPs) != 0 {
			t.Fatalf("MCPs = %v, want none: an untouched dialog forwards no --mcp", capture.opts.MCPs)
		}
	})

	t.Run("remote-only MCPs are offered and the picks are forwarded", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), localOnlyMCPConfig, "claude", "mcp-task")
		model, _ := h.Update(remoteMCPsFetchedMsg{remoteName: "myserver", mcps: []string{"github", "memory", "sequential-thinking"}, gen: h.remoteAccountsGen})
		h = model.(*Home)
		h.newDialog.SetSize(100, 50)
		if got := strings.Join(h.newDialog.remoteMCPs, ","); got != "github,memory,sequential-thinking" {
			t.Fatalf("offered MCPs = %q, want exactly the remote's", got)
		}
		if strings.Contains(h.newDialog.View(), "local-only") {
			t.Fatal("the dialog must not render this machine's MCP names for a remote target")
		}
		pickRemoteMCP(t, h, "sequential-thinking")
		pickRemoteMCP(t, h, "github")
		if !strings.Contains(h.newDialog.View(), "MCPs (remote):") {
			t.Fatal("the MCP row must be rendered once the remote answered")
		}

		submitRemoteDialog(t, h)

		if got := strings.Join(capture.opts.MCPs, ","); got != "github,sequential-thinking" {
			t.Fatalf("forwarded MCPs = %q, want github,sequential-thinking (remote order, unpicked memory left out)", got)
		}
	})

	t.Run("space toggles a pick off again", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), "", "claude", "mcp-task")
		model, _ := h.Update(remoteMCPsFetchedMsg{remoteName: "myserver", mcps: []string{"memory"}, gen: h.remoteAccountsGen})
		h = model.(*Home)
		pickRemoteMCP(t, h, "memory")
		pickRemoteMCP(t, h, "memory")
		submitRemoteDialog(t, h)
		if len(capture.opts.MCPs) != 0 {
			t.Fatalf("MCPs = %v after toggling off, want none", capture.opts.MCPs)
		}
	})

	t.Run("answers for another remote, a failed fetch or a closed dialog are dropped", func(t *testing.T) {
		h, _ := openRemoteDialogOn(t, remoteGroupItem("myserver"), "", "claude", "mcp-task")
		for _, msg := range []remoteMCPsFetchedMsg{
			{remoteName: "otherserver", mcps: []string{"stale"}, gen: h.remoteAccountsGen},
			{remoteName: "myserver", mcps: []string{"stale"}, err: errUnavailable, gen: h.remoteAccountsGen},
		} {
			model, _ := h.Update(msg)
			h = model.(*Home)
			if h.newDialog.hasRemoteMCPRow() {
				t.Fatalf("msg %+v must not populate the MCP row", msg)
			}
		}
		h.newDialog.Hide()
		model, _ := h.Update(remoteMCPsFetchedMsg{remoteName: "myserver", mcps: []string{"late"}, gen: h.remoteAccountsGen})
		h = model.(*Home)
		if h.newDialog.hasRemoteMCPRow() {
			t.Fatal("a late answer for a closed dialog must be dropped")
		}
	})

	t.Run("a late answer for an earlier opening never replaces the current list", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), "", "claude", "mcp-task")
		firstGen := h.remoteAccountsGen
		if firstGen == 0 {
			t.Fatal("precondition: opening the remote dialog must number the fetch")
		}
		// Close and reopen on the same remote while fetch A is still pending.
		h.newDialog.Hide()
		h.pendingRemoteName = ""
		model, cmd := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		h = model.(*Home)
		if cmd == nil || h.remoteAccountsGen != firstGen+1 {
			t.Fatalf("reopening must request a new numbered fetch (gen %d -> %d)", firstGen, h.remoteAccountsGen)
		}
		if strings.Join(capture.mcpsFetchedFor, ",") != "myserver,myserver" {
			t.Fatalf("MCP fetches requested for %v, want myserver twice", capture.mcpsFetchedFor)
		}
		h.newDialog.SetDefaultTool("claude")
		for _, r := range "mcp-task" {
			h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		// Fetch B (this opening) answers first and the user picks an MCP.
		model, _ = h.Update(remoteMCPsFetchedMsg{remoteName: "myserver", mcps: []string{"github", "memory"}, gen: h.remoteAccountsGen})
		h = model.(*Home)
		pickRemoteMCP(t, h, "memory")
		// Fetch A (the earlier opening) answers late with a different list in
		// which the same cursor position would name another MCP.
		model, _ = h.Update(remoteMCPsFetchedMsg{remoteName: "myserver", mcps: []string{"exa", "github", "memory"}, gen: firstGen})
		h = model.(*Home)
		if got := strings.Join(h.newDialog.remoteMCPs, ","); got != "github,memory" {
			t.Fatalf("offered MCPs = %q after the late answer, want the current opening's list unchanged", got)
		}
		submitRemoteDialog(t, h)
		if got := strings.Join(capture.opts.MCPs, ","); got != "memory" {
			t.Fatalf("forwarded MCPs = %q, want memory", got)
		}
	})

	t.Run("a new list clears picks made against the old one", func(t *testing.T) {
		h, capture := openRemoteDialogOn(t, remoteGroupItem("myserver"), "", "claude", "mcp-task")
		model, _ := h.Update(remoteMCPsFetchedMsg{remoteName: "myserver", mcps: []string{"github", "memory"}, gen: h.remoteAccountsGen})
		h = model.(*Home)
		pickRemoteMCP(t, h, "github")
		// The dialog is closed and reopened; the reopened dialog must not
		// carry the earlier pick, nor the earlier list.
		h.newDialog.Hide()
		h.pendingRemoteName = ""
		model, _ = h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		h = model.(*Home)
		if h.newDialog.hasRemoteMCPRow() {
			t.Fatalf("reopened dialog still offers %v before the remote answered", h.newDialog.remoteMCPs)
		}
		h.newDialog.SetDefaultTool("claude")
		for _, r := range "mcp-task" {
			h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		model, _ = h.Update(remoteMCPsFetchedMsg{remoteName: "myserver", mcps: []string{"github", "memory"}, gen: h.remoteAccountsGen})
		h = model.(*Home)
		submitRemoteDialog(t, h)
		if len(capture.opts.MCPs) != 0 {
			t.Fatalf("MCPs = %v, want none: a pick from an earlier opening must not travel", capture.opts.MCPs)
		}
	})
}

// A local opening never shows the row, even with MCPs configured on this
// machine: attaching MCPs to a local session stays where it was (the MCP
// dialog after creation), so nothing changes for users without remotes.
func TestNewDialog_LocalOpening_HasNoRemoteMCPRow(t *testing.T) {
	home := setXDGTestHome(t)
	writeXDGTestConfig(t, home, localOnlyMCPConfig)
	d := NewNewDialog()
	d.SetSize(100, 50)
	d.ShowInGroup("default", "default", "", nil, "")
	if d.hasRemoteMCPRow() || d.indexOf(focusRemoteMCPs) >= 0 {
		t.Fatalf("local dialog offers %v, want no MCP row", d.remoteMCPs)
	}
	if strings.Contains(d.View(), "MCPs (remote)") {
		t.Fatal("local dialog must not render the remote MCP row")
	}
	if got := d.GetRemoteMCPs(); len(got) != 0 {
		t.Fatalf("GetRemoteMCPs = %v for a local opening, want none", got)
	}
}
