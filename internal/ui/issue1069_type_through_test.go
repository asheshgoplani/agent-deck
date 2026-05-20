package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #1069 feature 1 (by @ddorman-dn): TUI insert mode for direct
// type-through to the focused session's tmux pane. These tests exercise the
// mode transitions and key routing through the same public Update() path the
// TUI uses at runtime, with an injected sink to capture forwarded keys
// instead of running real tmux.

// insertSinkCapture is a test fake that records every key dispatch made by
// insert mode so assertions can observe exactly what the focused session
// would have received.
type insertSinkCapture struct {
	calls []insertSinkCall
}

type insertSinkCall struct {
	sessionID string
	text      string
	sendEnter bool
}

func (c *insertSinkCapture) sink(inst *session.Instance, text string, sendEnter bool) error {
	c.calls = append(c.calls, insertSinkCall{
		sessionID: inst.ID,
		text:      text,
		sendEnter: sendEnter,
	})
	return nil
}

// armHomeWithOneSession sets up a Home with a single session at the cursor,
// the test sink installed, and dimensions large enough for View() to render.
func armHomeWithOneSession(t *testing.T) (*Home, *session.Instance, *insertSinkCapture) {
	t.Helper()

	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false

	inst := session.NewInstanceWithTool("focused-session", "/tmp/focused", "claude")

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID = map[string]*session.Instance{inst.ID: inst}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	// Position the cursor on the session row (the first session item after
	// any group headers in the flat list).
	for i, item := range home.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == inst.ID {
			home.cursor = i
			break
		}
	}

	capture := &insertSinkCapture{}
	home.insertKeySink = capture.sink

	// Disable insert-mode batching (#1094) so per-rune assertions in this
	// file stay deterministic. Batching is exercised separately in
	// issue1094_insert_mode_ux_test.go.
	home.insertBatchDuration = -1

	return home, inst, capture
}

// TestIssue1069_PressIEntersInsertMode verifies the toggle: pressing `I` on a
// focused session sets the insertMode flag and records the target session ID.
func TestIssue1069_PressIEntersInsertMode(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)

	if home.insertMode {
		t.Fatal("insertMode should default to false")
	}

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	got := model.(*Home)

	if !got.insertMode {
		t.Fatal("pressing 'I' on a focused session should enter insert mode")
	}
	if got.insertModeSessionID != inst.ID {
		t.Errorf("insertModeSessionID = %q, want %q", got.insertModeSessionID, inst.ID)
	}
}

// TestIssue1069_TypedRunesAreForwardedToFocusedSession verifies that, while
// in insert mode, each KeyRunes message is forwarded verbatim to the focused
// session's sink — not interpreted as a TUI command.
func TestIssue1069_TypedRunesAreForwardedToFocusedSession(t *testing.T) {
	home, inst, capture := armHomeWithOneSession(t)

	// Enter insert mode.
	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)

	// Type "hi" one rune at a time, the way a real terminal delivers them.
	for _, r := range "hi" {
		model, _ = home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		home = model.(*Home)
	}

	if len(capture.calls) != 2 {
		t.Fatalf("expected 2 sink calls (one per rune), got %d", len(capture.calls))
	}
	for i, want := range []string{"h", "i"} {
		call := capture.calls[i]
		if call.sessionID != inst.ID {
			t.Errorf("call[%d].sessionID = %q, want %q", i, call.sessionID, inst.ID)
		}
		if call.text != want {
			t.Errorf("call[%d].text = %q, want %q", i, call.text, want)
		}
		if call.sendEnter {
			t.Errorf("call[%d].sendEnter = true, want false (only Enter key should send newline)", i)
		}
	}
}

// TestIssue1069_NormalModeKeysStillWorkWhenNotTyping confirms we didn't break
// existing TUI bindings: pressing 'q' in normal mode still triggers the
// quit path (returns tea.Quit), it isn't swallowed by insert mode.
func TestIssue1069_NormalModeKeysStillWorkWhenNotTyping(t *testing.T) {
	home, _, capture := armHomeWithOneSession(t)

	// 'q' in normal mode → not forwarded to the session sink.
	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	if len(capture.calls) != 0 {
		t.Fatalf("normal-mode 'q' should NOT be forwarded to the session sink; got %d calls", len(capture.calls))
	}
}

// TestIssue1069_NormalModeNavigationKeysNotSwallowed checks that the
// space-key, 'k', 'j', etc. still navigate in normal mode (i.e. the insert-
// mode guard fires only when insertMode is actually set).
func TestIssue1069_NormalModeNavigationKeysNotSwallowed(t *testing.T) {
	home, _, capture := armHomeWithOneSession(t)

	// Normal-mode Space — should not be routed to the session.
	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeySpace})
	// Normal-mode 'j' — same.
	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	if len(capture.calls) != 0 {
		t.Fatalf("normal-mode nav keys should NOT be forwarded; got %d calls", len(capture.calls))
	}
}

// TestIssue1069_EnterSendsNewlineToFocusedSession verifies the Enter key, in
// insert mode, dispatches with sendEnter=true so users can submit messages.
func TestIssue1069_EnterSendsNewlineToFocusedSession(t *testing.T) {
	home, _, capture := armHomeWithOneSession(t)

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)

	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)

	if len(capture.calls) != 1 {
		t.Fatalf("Enter should produce exactly 1 sink call, got %d", len(capture.calls))
	}
	if !capture.calls[0].sendEnter {
		t.Errorf("Enter dispatch should have sendEnter=true")
	}
	if capture.calls[0].text != "" {
		t.Errorf("Enter dispatch text = %q, want empty (Enter is named-key, not literal)", capture.calls[0].text)
	}
	if !home.insertMode {
		t.Error("Enter should NOT exit insert mode — only Esc does")
	}
}

// TestIssue1069_EscExitsInsertMode confirms Esc returns the TUI to normal
// navigation and clears the target session ID.
func TestIssue1069_EscExitsInsertMode(t *testing.T) {
	home, _, capture := armHomeWithOneSession(t)

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)

	if !home.insertMode {
		t.Fatal("expected insert mode to be active before Esc")
	}

	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyEsc})
	home = model.(*Home)

	if home.insertMode {
		t.Error("Esc should exit insert mode")
	}
	if home.insertModeSessionID != "" {
		t.Errorf("insertModeSessionID = %q, want empty after Esc", home.insertModeSessionID)
	}
	// Esc itself must not be forwarded to the session.
	for _, c := range capture.calls {
		if c.text == "\x1b" || c.text == "esc" {
			t.Errorf("Esc should NOT be forwarded to the session, but sink got %+v", c)
		}
	}
}

// TestIssue1069_SpaceForwardedAsLiteral checks that the space key — which
// bubbletea delivers as KeySpace rather than KeyRunes — is forwarded as a
// literal " " when in insert mode.
func TestIssue1069_SpaceForwardedAsLiteral(t *testing.T) {
	home, _, capture := armHomeWithOneSession(t)

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)

	model, _ = home.Update(tea.KeyMsg{Type: tea.KeySpace})
	_ = model.(*Home) // assert type, value unused for remaining assertions

	if len(capture.calls) != 1 {
		t.Fatalf("Space should produce 1 sink call, got %d", len(capture.calls))
	}
	if capture.calls[0].text != " " {
		t.Errorf("Space dispatch text = %q, want %q", capture.calls[0].text, " ")
	}
}

// TestIssue1069_IndicatorRenderedInView verifies the visual indicator shows
// up at the bottom of the rendered view while insert mode is active, so the
// user has unambiguous feedback that subsequent typing is being routed away.
func TestIssue1069_IndicatorRenderedInView(t *testing.T) {
	home, _, _ := armHomeWithOneSession(t)

	// Render once in normal mode — expect no INSERT marker.
	if strings.Contains(home.View(), "INSERT") {
		t.Fatal("View() should not contain INSERT marker in normal mode")
	}

	// Enter insert mode and re-render.
	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)

	view := home.View()
	if !strings.Contains(view, "INSERT") {
		t.Errorf("View() should contain INSERT marker while in insert mode; got: %q", lastNonEmptyLine(view))
	}
	if !strings.Contains(view, "focused-session") {
		t.Errorf("View() insert-mode indicator should name the target session; got: %q", lastNonEmptyLine(view))
	}
}

// TestIssue1069_PressingCapitalIWithoutSelectionIsSafe ensures pressing `I`
// with no session under the cursor (empty list) does not crash and does not
// enter insert mode (would have no valid target).
func TestIssue1069_PressingCapitalIWithoutSelectionIsSafe(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 40
	home.initialLoading = false
	// No instances, no flatItems.

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("pressing 'I' with no selection panicked: %v", r)
		}
	}()

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	got := model.(*Home)

	if got.insertMode {
		t.Error("insert mode should NOT activate when no session is selected")
	}
}

// lastNonEmptyLine returns the last non-empty line of s, useful for surfacing
// what the help/status footer actually looked like when an assertion fails.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}
