package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The footer error line is drawn below the help bar, after the main area
// has taken its rows. This pins that it is actually on screen: at a full
// height the main area used to take every row and the final clamp cut the
// error line off, so a close notice ("R to restart") or a create's setup
// warning was set and logged but never visible. Checked with the onboarding
// hint bar on and off; the line must survive both.
func TestFooterErrorVisible(t *testing.T) {
	const notice = "closed 'demo'. R to restart"
	for _, hints := range []bool{false, true} {
		h := NewHome()
		h.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		h.Update(loadSessionsMsg{instances: []*session.Instance{
			session.NewInstanceWithTool("old", t.TempDir(), "shell"),
		}})
		h.hints.armed = hints
		if hints {
			h.hints.step = hintStepOpen
		}
		h.Update(sessionCreatedMsg{instance: session.NewInstanceWithTool("demo", t.TempDir(), "shell")})
		h.setError(errors.New(notice))
		view := stripAnsi(h.View())
		if !strings.Contains(view, notice) {
			t.Fatalf("hints=%v: notice not on screen:\n%s", hints, view)
		}
	}
}

// Remote parity (#3120 review): the reserved row is item-type agnostic;
// pin it with a remote session row on screen too, the way the real list
// renders one from a remote snapshot.
func TestFooterErrorVisibleWithRemoteRow(t *testing.T) {
	const notice = "remote update failed"
	h := NewHome()
	h.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	h.Update(loadSessionsMsg{instances: []*session.Instance{
		session.NewInstanceWithTool("old", t.TempDir(), "shell"),
	}})
	h.remoteSessionsMu.Lock()
	h.remoteSessions = map[string][]session.RemoteSessionInfo{"lab": {{
		ID: "r1", Title: "remote-one", Tool: "shell", Status: "idle", RemoteName: "lab",
	}}}
	h.remoteSessionsMu.Unlock()
	h.rebuildFlatItems()
	base := stripAnsi(h.View())
	if !strings.Contains(base, "remote-one") {
		t.Fatalf("precondition: the remote row is not on screen:\n%s", base)
	}
	h.setError(errors.New(notice))
	if view := stripAnsi(h.View()); !strings.Contains(view, notice) {
		t.Fatalf("notice not on screen over a remote row:\n%s", view)
	}
}
