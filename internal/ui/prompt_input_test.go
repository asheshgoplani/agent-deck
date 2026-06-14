// Issue #1410: prompt the highlighted session directly from the main TUI list
// without attaching. These tests pin (a) the PromptInputDialog input→submit
// routing and (b) the `o` hotkey gating at the handleMainKey boundary. No real
// tmux — submission emits a promptSubmitMsg the Home handler routes through the
// existing prompt-state-aware send path.
package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// runPromptKeys feeds a sequence of key messages to the dialog and returns the
// last emitted command (if any).
func typeInto(d *PromptInputDialog, runes string) {
	for _, r := range runes {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// TestPromptInputDialog_SubmitEmitsMsg: Show, type, Enter → a promptSubmitMsg
// carrying the bound instance id and trimmed text; the dialog hides.
func TestPromptInputDialog_SubmitEmitsMsg(t *testing.T) {
	d := NewPromptInputDialog()
	d.SetSize(120, 40)
	d.Show("sess-123", "my-session")
	if !d.IsVisible() {
		t.Fatal("dialog should be visible after Show")
	}

	typeInto(d, "run the tests")

	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter with non-empty text must emit a command")
	}
	if d.IsVisible() {
		t.Error("dialog should hide after submit")
	}

	msg := cmd()
	sub, ok := msg.(promptSubmitMsg)
	if !ok {
		t.Fatalf("emitted msg type = %T, want promptSubmitMsg", msg)
	}
	if sub.instanceID != "sess-123" {
		t.Errorf("instanceID = %q, want sess-123", sub.instanceID)
	}
	if sub.text != "run the tests" {
		t.Errorf("text = %q, want %q", sub.text, "run the tests")
	}
}

// TestPromptInputDialog_EscCancels: Esc closes the dialog and emits no submit.
func TestPromptInputDialog_EscCancels(t *testing.T) {
	d := NewPromptInputDialog()
	d.Show("sess-1", "s")
	typeInto(d, "abc")

	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if d.IsVisible() {
		t.Error("dialog should hide after Esc")
	}
	if cmd != nil {
		if _, ok := cmd().(promptSubmitMsg); ok {
			t.Error("Esc must not emit a promptSubmitMsg")
		}
	}
}

// TestPromptInputDialog_EmptySubmitIsNoOp: Enter with blank/whitespace text
// cancels without emitting a submit (no empty prompt sent).
func TestPromptInputDialog_EmptySubmitIsNoOp(t *testing.T) {
	d := NewPromptInputDialog()
	d.Show("sess-1", "s")
	typeInto(d, "   ")

	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if d.IsVisible() {
		t.Error("dialog should hide after empty submit")
	}
	if cmd != nil {
		if _, ok := cmd().(promptSubmitMsg); ok {
			t.Error("empty submit must not emit a promptSubmitMsg")
		}
	}
}

// TestPromptInputDialog_ReopenRebinds: Show on a second session rebinds the
// target so a stale id from a prior open can't leak into the next submit.
func TestPromptInputDialog_ReopenRebinds(t *testing.T) {
	d := NewPromptInputDialog()
	d.Show("sess-A", "a")
	d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	d.Show("sess-B", "b")
	typeInto(d, "hi")
	_, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a submit command")
	}
	sub := cmd().(promptSubmitMsg)
	if sub.instanceID != "sess-B" {
		t.Errorf("instanceID = %q, want sess-B (reopen must rebind target)", sub.instanceID)
	}
}

// armHomeWithRunningClaudeSession builds a Home whose cursor sits on a running
// claude session row (non-nil tmux session) so the `o` hotkey can open the
// prompt input.
func armHomeWithRunningClaudeSession(t *testing.T, tool string) (*Home, *session.Instance) {
	t.Helper()
	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	inst := session.NewInstanceWithTool("prompt-session", "/tmp/prompt", tool)
	tmuxSess := tmux.ReconnectSessionLazy("agentdeck_prompt_test", inst.ID, "/tmp/prompt", tool, "idle")
	inst.SetTmuxSessionForTest(tmuxSess)

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID = map[string]*session.Instance{inst.ID: inst}
	home.instancesMu.Unlock()

	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: inst},
	}
	home.cursor = 0
	return home, inst
}

// TestPromptHotkey_OpensInputForClaudeSession: pressing the prompt-session
// hotkey on a running claude session row opens the inline prompt input bound to
// that session.
func TestPromptHotkey_OpensInputForClaudeSession(t *testing.T) {
	home, inst := armHomeWithRunningClaudeSession(t, "claude")

	key := defaultHotkeyBindings[hotkeyPromptSession]
	home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})

	if !home.promptInputDialog.IsVisible() {
		t.Fatalf("prompt input should be visible after pressing %q on a claude session", key)
	}
	if home.promptInputDialog.instanceID != inst.ID {
		t.Errorf("prompt input bound to %q, want %q", home.promptInputDialog.instanceID, inst.ID)
	}
}

// TestPromptHotkey_NonClaudeSessionNoOp: the hotkey is inert on a non-claude
// session (the composer-draft guard + delivery verify are claude-shaped).
func TestPromptHotkey_NonClaudeSessionNoOp(t *testing.T) {
	home, _ := armHomeWithRunningClaudeSession(t, "shell")

	key := defaultHotkeyBindings[hotkeyPromptSession]
	home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})

	if home.promptInputDialog.IsVisible() {
		t.Error("prompt input must not open for a non-claude session")
	}
}

// TestPromptSubmitMsg_RoutesToTargetSession: the Home update loop resolves the
// promptSubmitMsg target by id and does not error for a known running session.
// (Actual tmux delivery is exercised by the deliverToConductorPane guard tests;
// here we assert the routing/lookup half does not surface an error.)
func TestPromptSubmitMsg_RoutesToTargetSession(t *testing.T) {
	home, inst := armHomeWithRunningClaudeSession(t, "claude")
	home.err = nil

	model, _ := home.updateInner(promptSubmitMsg{instanceID: inst.ID, text: "hello"})
	h := model.(*Home)
	if h.err != nil {
		t.Errorf("routing a prompt to a running session surfaced an error: %v", h.err)
	}
}

// TestPromptSubmitMsg_MissingSessionErrors: a prompt for an unknown session id
// surfaces a clear error rather than silently dropping.
func TestPromptSubmitMsg_MissingSessionErrors(t *testing.T) {
	home, _ := armHomeWithRunningClaudeSession(t, "claude")
	home.err = nil

	model, _ := home.updateInner(promptSubmitMsg{instanceID: "does-not-exist", text: "hello"})
	h := model.(*Home)
	if h.err == nil {
		t.Error("prompt to a missing session should surface an error")
	}
}
