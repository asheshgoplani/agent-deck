// Issue #1113 — Stale insert-mode buffer after session switch.
//
// Reporter: @ddorman-dn against v1.9.24. After typing in insert mode on
// session A, pressing Enter, exiting via Esc, navigating to session B,
// and re-entering insert mode, the previous typed text could leak into
// the new session's buffer (#1113). The defensive fix is to reset
// insertBuf on every transition into insert mode AND on Esc, so the
// buffer is always empty at the start of a new insert session.

package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue1113_EnterInsertModeResetsBuffer reproduces the bug class:
// when insertBuf has leftover content from any interrupted path,
// entering insert mode again must clear it so the next keystroke
// doesn't include stale text.
func TestIssue1113_EnterInsertModeResetsBuffer(t *testing.T) {
	home, _, _ := armHomeWithOneSession(t)

	home.insertBuf.WriteString("stale-abc")

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)

	if !home.insertMode {
		t.Fatal("test setup: failed to enter insert mode")
	}
	if got := home.insertBuf.Len(); got != 0 {
		t.Fatalf("insertBuf not reset on enter: len=%d content=%q", got,
			home.insertBuf.String())
	}
}

// TestIssue1113_SessionSwitchClearsBuffer walks the full reported flow:
// type + Enter + Esc + switch session + re-enter insert mode. Even if
// any step left bytes in the buffer (race, retry, future regression),
// the new session must start with an empty buffer.
func TestIssue1113_SessionSwitchClearsBuffer(t *testing.T) {
	home, instA, _ := armHomeWithOneSession(t)

	instB := session.NewInstanceWithTool("session-b", "/tmp/session-b", "claude")
	home.instancesMu.Lock()
	home.instances = append(home.instances, instB)
	home.instanceByID[instB.ID] = instB
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)
	if home.insertModeSessionID != instA.ID {
		t.Fatalf("insert mode bound to wrong session: %q want %q",
			home.insertModeSessionID, instA.ID)
	}
	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a', 'b', 'c'}})
	home = model.(*Home)
	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)
	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyEsc})
	home = model.(*Home)
	if home.insertMode {
		t.Fatal("Esc should exit insert mode")
	}

	// Inject the regression: pretend something left bytes in the buffer.
	home.insertBuf.WriteString("leftover")

	for i, item := range home.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == instB.ID {
			home.cursor = i
			break
		}
	}

	model, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)
	if !home.insertMode {
		t.Fatal("failed to re-enter insert mode on session B")
	}
	if home.insertModeSessionID != instB.ID {
		t.Fatalf("re-entered insert mode bound to %q, want session B %q",
			home.insertModeSessionID, instB.ID)
	}
	if got := home.insertBuf.Len(); got != 0 {
		t.Fatalf("insertBuf carried over %d bytes from previous session: %q",
			got, home.insertBuf.String())
	}
}

// TestIssue1113_EscClearsBufferAndPending verifies the Esc path resets
// buffer + flush flag (boundary case: Esc on a mid-batch buf).
func TestIssue1113_EscClearsBufferAndPending(t *testing.T) {
	home, _, _ := armHomeWithOneSession(t)

	home.insertBatchDuration = 0
	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)

	home.insertBuf.WriteString("pending")
	home.insertFlushPending = true

	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyEsc})
	home = model.(*Home)

	if home.insertMode {
		t.Fatal("Esc should exit insert mode")
	}
	if got := home.insertBuf.Len(); got != 0 {
		t.Fatalf("Esc left %d bytes in buffer: %q", got, home.insertBuf.String())
	}
	if home.insertFlushPending {
		t.Fatal("Esc should clear insertFlushPending")
	}
}
