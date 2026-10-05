package ui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type fakeNativeQueuePane struct {
	text     string
	keys     []string
	failText bool
	failKey  bool
}

func (p *fakeNativeQueuePane) SendKeysChunkedToPrimaryWindow(text string) error {
	if p.failText {
		return errors.New("paste failed")
	}
	p.text = text
	return nil
}

func (p *fakeNativeQueuePane) SendNamedKeyToPrimaryWindow(key string) error {
	if p.failKey {
		return errors.New("key failed")
	}
	p.keys = append(p.keys, key)
	return nil
}

func TestQuickMessageNativeQueue(t *testing.T) {
	p := &fakeNativeQueuePane{}
	if err := queueOpenCodePrompt(p, "first\nsecond"); err != nil {
		t.Fatal(err)
	}
	if p.text != "first\nsecond" || !reflect.DeepEqual(p.keys, []string{"C-x", "Enter"}) {
		t.Fatalf("wrong native queue sequence: %+v", p)
	}
	p = &fakeNativeQueuePane{failText: true}
	if err := queueOpenCodePrompt(p, "message"); err == nil || len(p.keys) != 0 {
		t.Fatal("paste failure still submitted")
	}
	p = &fakeNativeQueuePane{failKey: true}
	if err := queueOpenCodePrompt(p, "message"); err == nil {
		t.Fatal("submit failure not reported")
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
