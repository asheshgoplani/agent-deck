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
//
// PATH is process-wide, so the fake tmux also answers goroutines other tests
// left running (issue 2712). It records only calls naming this session; any
// delivery to the recipient has to name it. The session-send fallback is
// caught at its seam rather than by re-running the test binary.
func TestQuickMessageQueueRecipientContinuity(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "terminal-keys")
	t.Setenv("QUEUE_KEYS_MARKER", marker)
	for name, script := range map[string]string{
		"tmux":     "#!/bin/sh\ncase \"$*\" in *\"$QUEUE_TMUX_TARGET\"*) printf keys >> \"$QUEUE_KEYS_MARKER\" ;; esac\nexit 1\n",
		"opencode": "#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'opencode v2.0.26\\n'; exit 0; fi\nprintf '%s\\n' '{\"data\":{\"id\":\"msg_queued\",\"sessionID\":\"ses_target\",\"delivery\":\"queue\"}}'\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	configPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[opencode]\ncommand = '"+filepath.Join(dir, "opencode")+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	inst := session.NewInstanceWithGroupAndTool("queue", dir, "", "opencode")
	t.Setenv("QUEUE_TMUX_TARGET", inst.GetTmuxSession().Name)
	origSend := quickMessageRunSend
	fallback := false
	quickMessageRunSend = func([]string, string) error { fallback = true; return nil }
	t.Cleanup(func() { quickMessageRunSend = origSend })
	inst.OpenCodeSessionID = "ses_target"
	for _, id := range []string{"ses_target", ""} {
		inst.OpenCodeSessionID = id
		msg := quickMessageCmd("work", inst, promptSubmitMsg{instanceID: inst.ID, text: "first\nsecond", queue: true})().(promptDeliveryMsg)
		if (msg.err != nil) != (id == "") {
			t.Fatalf("session %q: %v", id, msg.err)
		}
	}
	if fallback {
		t.Fatal("native queue fell back to session send")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native queue attempted terminal delivery")
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
	for _, key := range []string{"o", "Q"} {
		h, _ := armHomeWithRunningClaudeSession(t, "claude")
		h.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession}}
		h.cursor = 0
		h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if h.promptInputDialog.IsVisible() {
			t.Fatal("remote hotkey opened a local message composer")
		}
	}
}

// TestQuickMessageDefaultBindings preserves the existing Send and Skills keys.
func TestQuickMessageDefaultBindings(t *testing.T) {
	for action, want := range map[string]string{
		hotkeyPromptSession: "o",
		hotkeySkillsManager: "s",
		hotkeyQueueMessage:  "Q",
	} {
		if got := defaultHotkeyBindings[action]; got != want {
			t.Errorf("%s = %q, want %q", action, got, want)
		}
	}
}

func TestQuickMessageHotkeys(t *testing.T) {
	for _, tool := range []string{"claude", "opencode", "codex", "pi", "gemini", "copilot", "crush", "muse", "cursor", "hermes", "omp", "shell"} {
		for _, key := range []string{"o", "Q"} {
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

// TestQuickMessageCtrlE keeps the textarea's end-of-line shortcut intact.
func TestQuickMessageCtrlE(t *testing.T) {
	d := NewPromptInputDialog()
	d.Show("target", "session")
	d.input.SetValue("draft")
	d.input.CursorStart()
	d.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	if !d.IsVisible() || d.input.Value() != "draft" {
		t.Fatal("Ctrl+E changed the draft")
	}
	typeInto(d, "!")
	if d.input.Value() != "draft!" {
		t.Fatal("Ctrl+E did not move to end of line")
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

// TestQuickMessageSendDoesNotInterruptBusyClaude pins that `o` on a busy Claude
// session queues into its composer, as on main (#1410, #2652): no Escape (or any
// other key) is sent from the UI, and delivery goes through session send, which
// holds the per-session send lock and the draft guard.
func TestQuickMessageSendDoesNotInterruptBusyClaude(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "tmux-calls")
	t.Setenv("QUICK_MSG_TMUX_MARKER", marker)
	// A fake tmux that records every call and shows an empty Claude composer,
	// so a draft guard would pass and an Escape would be observable.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$QUICK_MSG_TMUX_MARKER\"\nif [ \"$1\" = capture-pane ]; then printf '\\342\\235\\257 \\n'; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var gotArgs []string
	var gotText string
	calls := 0
	orig := quickMessageRunSend
	quickMessageRunSend = func(args []string, text string) error {
		calls++
		gotArgs, gotText = args, text
		return nil
	}
	t.Cleanup(func() { quickMessageRunSend = orig })

	_, inst := armHomeWithRunningClaudeSession(t, "claude")
	inst.SetStatusThreadSafe(session.StatusRunning)
	out := quickMessageCmd("work", inst, promptSubmitMsg{instanceID: inst.ID, text: "follow up"})().(promptDeliveryMsg)
	if out.err != nil {
		t.Fatalf("send failed: %v", out.err)
	}
	if calls != 1 || gotText != "follow up" {
		t.Fatalf("session send calls = %d, text = %q", calls, gotText)
	}
	if args := strings.Join(gotArgs, " "); !strings.Contains(args, "session send "+inst.ID+" --message-file - --no-wait") {
		t.Fatalf("send args = %q", args)
	}
	if data, err := os.ReadFile(marker); err == nil && strings.Contains(string(data), "Escape") {
		t.Fatalf("busy Claude send interrupted the turn; tmux calls:\n%s", data)
	}
}
