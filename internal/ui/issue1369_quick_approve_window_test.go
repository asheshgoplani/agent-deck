// Issue #1369: quick-approve (`a`) must act on the window the cursor is on,
// gating on that window's detected tool (WindowTool) rather than the session's
// stored Tool, and delivering to that specific window index. These tests pin
// the dispatch at the handleMainKey boundary with an injected sink, mirroring
// the insert-mode tests in issue1069_type_through_test.go — no real tmux.
package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type quickApproveCall struct {
	sessionID   string
	windowIndex int
}

// quickApproveCapture records every quick-approve dispatch so assertions can
// observe the targeted (instance, windowIndex).
type quickApproveCapture struct {
	calls []quickApproveCall
}

func (c *quickApproveCapture) sink(inst *session.Instance, windowIndex int) error {
	c.calls = append(c.calls, quickApproveCall{sessionID: inst.ID, windowIndex: windowIndex})
	return nil
}

// armHomeWithOneWindowRow builds a Home whose cursor sits on a single window
// sub-row of a "shell"-tool session — the #1369 case where the session Tool is
// non-claude but the window runs claude. windowTool is the detected tool for
// that window. flatItems is synthesized directly so it isn't rebuilt away.
func armHomeWithOneWindowRow(t *testing.T, windowTool string, windowIndex int) (*Home, *session.Instance, *quickApproveCapture) {
	t.Helper()

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	inst := session.NewInstanceWithTool("multiwin-session", "/tmp/multiwin", "shell")

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID = map[string]*session.Instance{inst.ID: inst}
	home.instancesMu.Unlock()

	home.flatItems = []session.Item{
		{
			Type:            session.ItemTypeWindow,
			WindowIndex:     windowIndex,
			WindowTool:      windowTool,
			WindowSessionID: inst.ID,
		},
	}
	home.cursor = 0

	capture := &quickApproveCapture{}
	home.quickApproveSink = capture.sink

	return home, inst, capture
}

// TestQuickApprove_ClaudeWindowRow_TargetsThatWindow: pressing `a` on a window
// sub-row whose WindowTool is claude must dispatch to that exact window index —
// even though the parent session's Tool is "shell".
func TestQuickApprove_ClaudeWindowRow_TargetsThatWindow(t *testing.T) {
	home, inst, capture := armHomeWithOneWindowRow(t, "claude", 3)

	home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	if len(capture.calls) != 1 {
		t.Fatalf("expected exactly 1 quick-approve dispatch, got %d: %+v", len(capture.calls), capture.calls)
	}
	got := capture.calls[0]
	if got.sessionID != inst.ID {
		t.Fatalf("dispatch targeted session %q, want %q", got.sessionID, inst.ID)
	}
	if got.windowIndex != 3 {
		t.Fatalf("dispatch targeted window %d, want 3", got.windowIndex)
	}
}

// TestQuickApprove_NonClaudeWindowRow_NoOp: pressing `a` on a window sub-row
// whose WindowTool is not Claude-compatible must NOT dispatch — the gate keys
// off the window's tool, so a stray press on e.g. a shell window is inert.
func TestQuickApprove_NonClaudeWindowRow_NoOp(t *testing.T) {
	home, _, capture := armHomeWithOneWindowRow(t, "shell", 2)

	home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	if len(capture.calls) != 0 {
		t.Fatalf("expected no dispatch for a non-claude window, got %+v", capture.calls)
	}
}

// TestQuickApprove_RemoteSession_NotHandled documents that quick-approve does
// not act on remote sessions. This predates #1369 — the original handler only
// matched ItemTypeSession — and remains out of scope here: remote key delivery
// goes through the SSH/remote send path, not *tmux.Session. Tracked as a
// possible follow-up.
func TestQuickApprove_RemoteSession_NotHandled(t *testing.T) {
	t.Skip("remote quick-approve is unsupported (pre-existing); out of scope for #1369")
}
