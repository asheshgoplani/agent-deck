package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// footerFeedbackHome is a deck with a group header and sessions that the
// "does not apply" checks tell apart: a1 is a Claude session with no
// conversation yet (nothing to fork), a3 is a shell session.
func footerFeedbackHome(t *testing.T, where string) *Home {
	t.Helper()
	setXDGTestHome(t)
	home, idx := buildTwoGroupHome(t)
	home.instancesMu.Lock()
	home.instances[0].Status = session.StatusIdle
	home.instances[2].Tool = "shell"
	home.instances[2].Status = session.StatusIdle
	for _, inst := range home.instances {
		home.instanceByID[inst.ID] = inst
	}
	home.instancesMu.Unlock()
	home.rebuildFlatItems()
	if where == "group" {
		home.cursor = 0
		if home.flatItems[0].Type != session.ItemTypeGroup {
			t.Fatalf("row 0 is not a group header")
		}
	} else {
		home.cursor = idx[where]
	}
	return home
}

func footerKey(s string) tea.KeyMsg {
	switch s {
	case "ctrl+g":
		return tea.KeyMsg{Type: tea.KeyCtrlG}
	case "shift+u":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// A key that cannot apply to the row under the cursor says why in the footer,
// instead of doing nothing (the CLI already explains, e.g. `session fork`).
func TestKeyThatDoesNotApplySaysWhy(t *testing.T) {
	cases := []struct {
		row  string
		key  string
		want string // part of the one-line reason
	}{
		// a session that cannot do it
		{"a1", "f", "cannot be forked"},
		{"a1", "F", "cannot be forked"},
		{"a3", "f", "cannot be forked: no resumable session for tool shell"},
		{"a3", "F", "cannot be forked: no resumable session for tool shell"},
		{"a1", "p", "single path"},
		{"a1", "y", "YOLO"},
		{"a1", "T", "restart fresh"},
		{"a1", "E", "not sandboxed"},
		{"a3", "a", "quick approve"},
		{"a3", "L", "plugin manager"},
		{"a3", "s", "skills manager"},
		{"a1", "ctrl+g", "Gemini"},
		{"a1", "shift+u", "archived view"},
		// a group row: session keys name what they need
		{"group", "P", "select a session"},
		{"group", "p", "select a session"},
		{"group", "m", "select a session"},
		{"group", "L", "select a session"},
		{"group", "s", "select a session"},
		{"group", "f", "select a session"},
		{"group", "F", "select a session"},
		{"group", "M", "select a session"},
		{"group", "D", "select a session"},
		{"group", "A", "select a session"},
		{"group", "X", "select a session"},
		{"group", "c", "select a session"},
		{"group", "x", "select a session"},
		{"group", "o", "select a session"},
		{"group", "Q", "select a session"},
		{"group", "V", "select a session"},
		{"group", "B", "select a session"},
		{"group", "C", "select a session"},
		{"group", "Y", "select a session"},
		{"group", "y", "select a session"},
		{"group", "T", "select a session"},
		{"group", "R", "select a session"},
		{"group", "E", "select a session"},
		{"group", "a", "select a session"},
		{"group", "u", "select a session"},
		{"group", "b", "select a session"},
		{"group", "W", "select a session"},
		{"group", "ctrl+g", "select a session"},
	}
	for _, tc := range cases {
		t.Run(tc.row+"/"+tc.key, func(t *testing.T) {
			h := footerFeedbackHome(t, tc.row)
			model, cmd := h.handleMainKey(footerKey(tc.key))
			h = model.(*Home)
			if cmd != nil {
				t.Fatalf("%s on %s started a command; it should only explain", tc.key, tc.row)
			}
			if h.err == nil {
				t.Fatalf("%s on %s did nothing and said nothing; want a footer line containing %q", tc.key, tc.row, tc.want)
			}
			if !strings.Contains(h.err.Error(), tc.want) {
				t.Fatalf("%s on %s: footer says %q, want it to contain %q", tc.key, tc.row, h.err.Error(), tc.want)
			}
			if isFooterNotice(h.err) {
				t.Fatalf("%s on %s: a reason is not a success notice", tc.key, tc.row)
			}
			// The reason is one line on screen, under the help bar.
			frame := stripAnsi(h.View())
			if !strings.Contains(frame, "⚠ "+tc.want) && !strings.Contains(frame, tc.want) {
				t.Fatalf("%s on %s: reason %q is not on screen:\n%s", tc.key, tc.row, tc.want, frame)
			}
		})
	}
}

// Things that worked are shown as a notice (✓), never with the warning sign.
func TestSuccessMessagesAreNotWarnings(t *testing.T) {
	cases := []struct {
		name string
		msg  func(h *Home) tea.Msg
		want string
	}{
		{"copy 3 lines", func(*Home) tea.Msg { return copyResultMsg{sessionTitle: "a1", lineCount: 3} }, "Copied 3 lines to clipboard (a1)"},
		{"copy 1 line", func(*Home) tea.Msg { return copyResultMsg{sessionTitle: "a1", lineCount: 1} }, "Copied 1 line to clipboard (a1)"},
		{"copy pane 1 line", func(*Home) tea.Msg { return copyPaneResultMsg{sessionTitle: "a1", lineCount: 1} }, "Copied visible terminal text to clipboard (1 line, a1)"},
		{"copy pane 2 lines", func(*Home) tea.Msg { return copyPaneResultMsg{sessionTitle: "a1", lineCount: 2} }, "Copied visible terminal text to clipboard (2 lines, a1)"},
		{"send 1 line", func(*Home) tea.Msg {
			return sendOutputResultMsg{sourceTitle: "a1", targetTitle: "a2", lineCount: 1}
		}, "Sent 1 line from 'a1' to 'a2'"},
		{"closed", func(h *Home) tea.Msg { return sessionClosedMsg{sessionID: h.instances[0].ID} }, "closed 'a1'"},
		{"archived", func(h *Home) tea.Msg { return sessionArchivedMsg{sessionID: h.instances[0].ID} }, "archived 'a1'"},
		{"unarchived", func(h *Home) tea.Msg { return sessionUnarchivedMsg{sessionID: h.instances[0].ID} }, "unarchived 'a1'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := footerFeedbackHome(t, "a1")
			model, _ := h.Update(tc.msg(h))
			h = model.(*Home)
			if h.err == nil || !strings.Contains(h.err.Error(), tc.want) {
				t.Fatalf("footer = %v, want %q", h.err, tc.want)
			}
			frame := stripAnsi(h.View())
			if !strings.Contains(frame, "✓ "+tc.want) {
				t.Fatalf("success message not shown as a ✓ notice:\n%s", lastLines(frame, 4))
			}
			if strings.Contains(frame, "⚠ "+tc.want) {
				t.Fatalf("success message shown as a warning:\n%s", lastLines(frame, 4))
			}
		})
	}
}

// A real failure keeps the warning sign.
func TestFailureMessagesStayWarnings(t *testing.T) {
	h := footerFeedbackHome(t, "a1")
	model, _ := h.Update(copyPaneResultMsg{sessionTitle: "a1", empty: true})
	h = model.(*Home)
	frame := stripAnsi(h.View())
	if !strings.Contains(frame, "⚠ Nothing visible to copy (a1)") {
		t.Fatalf("a failed copy must stay a warning:\n%s", lastLines(frame, 4))
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
