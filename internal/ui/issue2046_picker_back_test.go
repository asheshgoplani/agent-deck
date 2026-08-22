package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agents"
	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// Issue #2046 pins the navigation contract shared by the picker-like session
// creation surfaces: Esc leaves exactly the level currently on screen, and the
// visible footer says what that Esc will do.
func TestIssue2046_ModelPickerEscPerLevelAndVisibleHints(t *testing.T) {
	d := focusModelForCodex(t)

	// The model list is initially visible but not yet in keyboard-navigation
	// mode. This was the missing affordance reported in #2046.
	view := stripAnsi(d.View())
	if !strings.Contains(view, "Esc back") {
		t.Fatalf("passive model list does not advertise Esc back:\n%s", view)
	}

	// Enter descends into the navigable model list; Esc returns exactly one
	// level to the model field without closing the session form.
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !d.IsModelSuggestionsActive() {
		t.Fatal("Enter did not enter the nested model list")
	}
	if view = stripAnsi(d.View()); !strings.Contains(view, "Esc back") {
		t.Fatalf("nested model list does not advertise Esc back:\n%s", view)
	}
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !d.IsVisible() || d.IsModelSuggestionsActive() || !d.IsModelPickerOpen() {
		t.Fatal("first Esc must return from nested navigation to the passive model list")
	}

	// The next Esc dismisses the passive list, still leaving the form alive.
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !d.IsVisible() || d.IsModelPickerOpen() {
		t.Fatal("second Esc must return from the passive model list to the form")
	}
	if view = stripAnsi(d.View()); !strings.Contains(view, "Esc cancel") {
		t.Fatalf("top-level model field does not advertise Esc cancel:\n%s", view)
	}

	// At the form level, Esc returns to the previous screen.
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if d.IsVisible() {
		t.Fatal("third Esc must close the top-level session form")
	}
}

func TestIssue2046_SessionOptionsTopLevelEscAndHint(t *testing.T) {
	d := NewNewDialog()
	d.SetDefaultTool("claude")
	d.SetSize(100, 50)
	d.Show()
	d.focusIndex = d.indexOf(focusOptions)
	d.updateFocus()

	if view := stripAnsi(d.View()); !strings.Contains(view, "Esc cancel") {
		t.Fatalf("session options do not advertise the top-level Esc path:\n%s", view)
	}
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if d.IsVisible() {
		t.Fatal("Esc from session options must return to the previous screen")
	}
}

func TestIssue2046_RemotePickerUsesSharedNewDialogEscContract(t *testing.T) {
	setXDGTestHome(t)
	h := NewHome()
	h.width, h.height = 100, 30
	h.flatItems = []session.Item{remoteGroupItem("myserver")}
	h = pressN(t, h)

	if view := stripAnsi(h.newDialog.View()); !strings.Contains(view, "Esc cancel") {
		t.Fatalf("remote new-session picker does not advertise Esc cancel:\n%s", view)
	}
	h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEsc})
	if h.newDialog.IsVisible() || h.pendingRemoteName != "" {
		t.Fatal("Esc from remote picker must close one level and clear its remote target")
	}
}

func TestIssue2046_AgentsPickerEscPerLevelAndVisibleHints(t *testing.T) {
	ap := NewAgentsPanel()
	ap.SetSize(100, 30)
	ap.Show()
	ap.SetView(agents.View{TotalAgents: 1, Machines: []agents.Machine{{
		Name: "local", Link: agents.LinkLocal,
		Agents: []agents.AgentRow{{Name: "builder", Class: agents.ClassAgent, State: agents.RunIdle}},
	}}}, time.Now())

	if view := stripAnsi(ap.View()); !strings.Contains(view, "[Esc] Close") {
		t.Fatalf("agents top list does not advertise Esc close:\n%s", view)
	}
	ap.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if view := stripAnsi(ap.View()); !strings.Contains(view, "[h/Esc] Back") {
		t.Fatalf("agents detail does not advertise Esc back:\n%s", view)
	}
	ap.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !ap.IsVisible() || ap.detailMode {
		t.Fatal("first Esc must return from agents detail to agents list")
	}
	ap.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if ap.IsVisible() {
		t.Fatal("second Esc must return from agents list to the previous screen")
	}
}

func TestIssue2046_GeminiModelPickerTopLevelEscAndHint(t *testing.T) {
	d := NewGeminiModelDialog()
	d.SetSize(100, 30)
	d.Show("session-id", "")
	if view := stripAnsi(d.View()); !strings.Contains(view, "Esc Cancel") {
		t.Fatalf("Gemini model picker does not advertise Esc cancel:\n%s", view)
	}
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if d.IsVisible() {
		t.Fatal("Esc from Gemini model picker must return to the previous screen")
	}
}
