package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestQuickMessageQueueRecipientContinuity ensures native queueing never sends
// terminal keys, even when the session no longer has a live tmux recipient.
func TestQuickMessageQueueRecipientContinuity(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "terminal-keys")
	t.Setenv("QUEUE_KEYS_MARKER", marker)
	for name, script := range map[string]string{
		"tmux":      "#!/bin/sh\nprintf keys >> \"$QUEUE_KEYS_MARKER\"\nexit 1\n",
		"opencode2": "#!/bin/sh\nprintf '%s\\n' '{\"data\":{\"id\":\"msg_queued\",\"sessionID\":\"ses_target\",\"delivery\":\"queue\"}}'\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	inst := session.NewInstanceWithGroupAndTool("queue", dir, "", "opencode2")
	inst.OpenCodeSessionID = "ses_target"
	for _, id := range []string{"ses_target", ""} {
		inst.OpenCodeSessionID = id
		msg := quickMessageCmd("work", inst, promptSubmitMsg{instanceID: inst.ID, text: "first\nsecond", queue: true})().(promptDeliveryMsg)
		if (msg.err != nil) != (id == "") {
			t.Fatalf("session %q: %v", id, msg.err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native queue attempted terminal delivery or fallback")
	}
}

// TestQuickMessageRemoteSession documents the intentionally local-only scope:
// remote queueing needs a versioned SSH/API transport, not local tmux delivery.
func TestQuickMessageRemoteSession(t *testing.T) {
	t.Skip("Send/Queue is local-only: RemoteSession has no versioned native-queue transport; attaching or remote session send remains available")
}

// TestQuickMessageRemoteDoesNotTargetLocal ensures remote rows cannot accidentally
// open a composer for an unrelated local session while remote delivery is unsupported.
func TestQuickMessageRemoteDoesNotTargetLocal(t *testing.T) {
	for _, key := range []string{"s", "Q"} {
		h, _ := armHomeWithRunningClaudeSession(t, "claude")
		h.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession}}
		h.cursor = 0
		h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if h.promptInputDialog.IsVisible() {
			t.Fatal("remote hotkey opened a local message composer")
		}
	}
}

func TestQuickMessageHotkeys(t *testing.T) {
	for _, tool := range []string{"claude", "opencode", "codex", "pi", "gemini", "copilot", "crush", "muse", "cursor", "hermes", "omp", "shell"} {
		for _, key := range []string{"s", "Q"} {
			t.Run(tool+"/"+key, func(t *testing.T) {
				h, inst := armHomeWithRunningClaudeSession(t, tool)
				h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
				if !h.promptInputDialog.IsVisible() || h.promptInputDialog.instanceID != inst.ID {
					t.Fatal("message composer did not open for selected session")
				}
				if h.promptInputDialog.queue != (key == "Q") {
					t.Fatal("incorrect message mode")
				}
			})
		}
	}
}

func TestQuickMessageQueueAndMultiline(t *testing.T) {
	d := NewPromptInputDialog()
	d.Show("target", "session")
	d.queue = true
	typeInto(d, "first")
	d.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	typeInto(d, "second")
	_, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd().(promptSubmitMsg)
	if !msg.queue || msg.text != "first\nsecond" || msg.instanceID != "target" {
		t.Fatalf("submission = %+v", msg)
	}
	d.Show("other", "other")
	if d.queue {
		t.Fatal("reopening retained queue mode")
	}
}

func TestQuickMessageEditorReturn(t *testing.T) {
	h, _ := armHomeWithRunningClaudeSession(t, "claude")
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Q")})
	h.updateInner(promptEditorMsg{text: "edited\nmessage"})
	if h.promptInputDialog.input.Value() != "edited\nmessage" || !h.promptInputDialog.queue {
		t.Fatal("editor return lost draft or mode")
	}
	d := h.promptInputDialog
	d.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if !d.editorChord {
		t.Fatal("Ctrl+X did not arm editor chord")
	}
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	if d.editorChord || !strings.Contains(d.input.Value(), "z") {
		t.Fatal("unmatched chord swallowed text")
	}
}

// TestQuickMessageCtrlE opens the external editor without submitting the draft.
func TestQuickMessageCtrlE(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	d := NewPromptInputDialog()
	d.Show("target", "session")
	d.input.SetValue("draft")
	_, cmd := d.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	if cmd == nil || !d.IsVisible() || d.input.Value() != "draft" {
		t.Fatal("Ctrl+E did not open the editor while preserving the draft")
	}
}

func TestQuickMessageLayoutReservesRows(t *testing.T) {
	h, _ := armHomeWithRunningClaudeSession(t, "claude")
	h.width = 180
	before, _ := h.sidebarLineBudget()
	h.openPromptInput(h.instances[0])
	after, _ := h.sidebarLineBudget()
	if after >= before {
		t.Fatalf("composer does not reserve sidebar space: before %d, after %d", before, after)
	}
	view := h.View()
	if lipgloss.Height(view) != h.height {
		t.Fatalf("height = %d", lipgloss.Height(view))
	}
	if !strings.Contains(view, "Ctrl+X E Editor") {
		t.Fatal("editor hint not visible")
	}
}

func TestQuickMessageArgs(t *testing.T) {
	for _, queue := range []bool{false, true} {
		args := strings.Join(quickMessageArgs("work", promptSubmitMsg{instanceID: "target", queue: queue}), " ")
		if !strings.Contains(args, "--profile work session send target --message-file -") {
			t.Fatal(args)
		}
		if strings.Contains(args, "--defer-if-busy") != queue || strings.Contains(args, "--no-wait") == queue {
			t.Fatal(args)
		}
	}
}
