package ui

// Golden frames for the ⚡ triggers row badge: hidden at 0, the count at 1
// and 3, red when one of the session's triggers is failing, and the narrow
// list where it shrinks and then drops before the title pays. Regenerate
// with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ui/ -run 'TestTriggersBadge.*Golden'
//
// and review the diff like any other test change.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

func TestTriggersBadge_Fit(t *testing.T) {
	forceTrueColorProfile()
	if got, w := fitTriggersBadge(triggers.Badge{}, false, -1); got != "" || w != 0 {
		t.Errorf("zero triggers rendered %q (%d cells), want nothing", got, w)
	}
	got, w := fitTriggersBadge(triggers.Badge{Count: 3}, false, -1)
	if stripAnsi(got) != " ⚡3" || w != cellWidth(" ⚡3") {
		t.Errorf("3 triggers = %q (%d cells), want \" ⚡3\"", stripAnsi(got), w)
	}
	if want := DimStyle.Render(" ⚡3"); got != want {
		t.Errorf("healthy badge is not dim: %q, want %q", got, want)
	}
	bad, _ := fitTriggersBadge(triggers.Badge{Count: 3, Bad: true}, false, -1)
	if want := lipgloss.NewStyle().Foreground(ColorRed).Render(" ⚡3"); bad != want {
		t.Errorf("failing badge is not red: %q, want %q", bad, want)
	}
	// Shrink to the glyph alone, then drop.
	if got, w := fitTriggersBadge(triggers.Badge{Count: 12}, false, cellWidth(" ⚡")); stripAnsi(got) != " ⚡" || w != cellWidth(" ⚡") {
		t.Errorf("tight budget = %q (%d), want the glyph alone", stripAnsi(got), w)
	}
	if got, w := fitTriggersBadge(triggers.Badge{Count: 12}, false, 2); got != "" || w != 0 {
		t.Errorf("budget 2 = %q (%d), want dropped", got, w)
	}
}

func triggersBadgeRow(t *testing.T, h *Home, title string, b triggers.Badge, listWidth int) string {
	t.Helper()
	inst := session.NewInstanceWithTool(title, "/tmp/api", "shell")
	inst.Status = session.StatusRunning
	h.triggerBadges = map[string]triggers.Badge{}
	if b.Count > 0 {
		h.triggerBadges[inst.ID] = b
	}
	state := sessionRenderState{status: session.StatusRunning, tool: "shell", title: inst.Title}
	var sb strings.Builder
	h.renderSessionItem(&sb, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, false, map[string]sessionRenderState{inst.ID: state}, listWidth)
	row := strings.TrimSuffix(stripAnsi(sb.String()), "\n")
	if w := cellWidth(row); w > listWidth {
		t.Errorf("row is %d cells wide, list is %d:\n%s", w, listWidth, row)
	}
	return row
}

// TestTriggersBadgeRow_Golden: a session row with 0, 1 and 3 triggers and
// with a failing one (the red is asserted in TestTriggersBadge_Fit; the
// frame is stripped of color).
func TestTriggersBadgeRow_Golden(t *testing.T) {
	forceTrueColorProfile()
	for _, step := range []struct {
		name  string
		badge triggers.Badge
	}{
		{"row-0", triggers.Badge{}},
		{"row-1", triggers.Badge{Count: 1}},
		{"row-3", triggers.Badge{Count: 3}},
		{"row-failing", triggers.Badge{Count: 2, Bad: true}},
	} {
		t.Run(step.name, func(t *testing.T) {
			h := newSeamATestHome()
			h.width, h.height = 100, 30
			assertTriggersGolden(t, "badge-"+step.name, triggersBadgeRow(t, h, "API worker", step.badge, h.sessionsPaneWidth()))
		})
	}
}

// TestTriggersBadgeRowWidthBudget_Golden: on a narrow list a long title
// keeps its floor and the badge shortens to the glyph, then drops.
func TestTriggersBadgeRowWidthBudget_Golden(t *testing.T) {
	forceTrueColorProfile()
	for _, terminalWidth := range []int{100, 80, 78, 77, 70, 48} {
		name := "width-term" + strconv.Itoa(terminalWidth)
		t.Run(name, func(t *testing.T) {
			h := newSeamATestHome()
			h.width, h.height = terminalWidth, 40
			listWidth := h.sessionsPaneWidth()
			row := triggersBadgeRow(t, h, "a genuinely long session title that needs the room", triggers.Badge{Count: 12}, listWidth)
			assertTriggersGolden(t, "badge-"+name, "terminal_width="+strconv.Itoa(terminalWidth)+" list_width="+strconv.Itoa(listWidth)+"\n"+row)
		})
	}
}

// TestTriggersBadge_RemoteRowsRenderNothing: remote rows have no trigger
// data in this step, so they never show a badge, even for a remote session
// whose id collides with a local badge entry.
func TestTriggersBadge_RemoteRowsRenderNothing(t *testing.T) {
	forceTrueColorProfile()
	h := newSeamATestHome()
	h.width, h.height = 100, 30
	h.triggerBadges = map[string]triggers.Badge{"remote": {Count: 4}}
	rs := session.RemoteSessionInfo{ID: "remote", Title: "Remote worker", Tool: "claude", Status: "waiting", Path: "/srv/app", RemoteName: "lab"}
	item := session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &rs, RemoteName: "lab", Level: 1}
	var frame strings.Builder
	h.renderRemoteSessionItem(&frame, item, false)
	if strings.Contains(frame.String(), "⚡") {
		t.Errorf("remote row shows a triggers badge:\n%s", stripAnsi(frame.String()))
	}
}
