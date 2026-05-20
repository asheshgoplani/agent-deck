package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Insert mode (#1069 feature 1, by @ddorman-dn): vim-style modal type-through
// for the TUI. After pressing `I` on a focused session, subsequent keystrokes
// are forwarded directly to that session's tmux pane via send-keys, instead of
// being interpreted as TUI commands. Esc returns to normal mode.

// enterInsertMode arms insert mode if the cursor is on a session whose tmux
// pane exists. Returns true on success. Errors are surfaced via setError so
// the user sees why nothing happened.
func (h *Home) enterInsertMode() bool {
	inst := h.getSelectedSession()
	if inst == nil {
		h.setError(fmt.Errorf("insert mode: select a session first"))
		return false
	}
	if inst.GetTmuxSession() == nil {
		h.setError(fmt.Errorf("insert mode: session %q has no tmux pane", inst.Title))
		return false
	}
	h.insertMode = true
	h.insertModeSessionID = inst.ID
	return true
}

// exitInsertMode returns the TUI to normal navigation mode.
func (h *Home) exitInsertMode() {
	h.insertMode = false
	h.insertModeSessionID = ""
}

// handleInsertModeKey is the keyboard handler used while insert mode is
// active. Esc exits, Enter sends a newline to the target session, and
// printable runes (and the space key) are forwarded literally. Other
// meta-keys (arrows, ctrl combos, Tab, Backspace, function keys) are
// intentionally ignored in v1 — they remain reserved for future expansion
// and shouldn't accidentally leak into the session's input stream.
func (h *Home) handleInsertModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		h.exitInsertMode()
		return h, nil
	case tea.KeyEnter:
		h.dispatchInsertKey("", true)
		return h, nil
	case tea.KeySpace:
		h.dispatchInsertKey(" ", false)
		return h, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return h, nil
		}
		h.dispatchInsertKey(string(msg.Runes), false)
		return h, nil
	default:
		// Drop arrows, ctrl combos, Tab, Backspace, function keys etc. for v1.
		return h, nil
	}
}

// dispatchInsertKey forwards literal text (optionally followed by Enter) to
// the target session's tmux pane via the registered sink (real send-keys by
// default; tests override via h.insertKeySink).
func (h *Home) dispatchInsertKey(text string, sendEnter bool) {
	inst := h.resolveInsertTarget()
	if inst == nil {
		return
	}
	if h.insertKeySink != nil {
		if err := h.insertKeySink(inst, text, sendEnter); err != nil {
			h.setError(fmt.Errorf("insert mode send failed: %w", err))
		}
		return
	}
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		h.exitInsertMode()
		h.setError(fmt.Errorf("insert mode: tmux session vanished"))
		return
	}
	if text != "" {
		if err := tmuxSess.SendKeys(text); err != nil {
			h.setError(fmt.Errorf("insert mode send-keys failed: %w", err))
			return
		}
	}
	if sendEnter {
		if err := tmuxSess.SendEnter(); err != nil {
			h.setError(fmt.Errorf("insert mode send-enter failed: %w", err))
		}
	}
}

// resolveInsertTarget returns the instance for the session insert mode is
// targeting, or nil if it has disappeared (in which case insert mode is also
// exited so the user isn't stranded).
func (h *Home) resolveInsertTarget() *session.Instance {
	if h.insertModeSessionID == "" {
		h.exitInsertMode()
		h.setError(fmt.Errorf("insert mode: no target session"))
		return nil
	}
	inst := h.getInstanceByID(h.insertModeSessionID)
	if inst == nil {
		h.exitInsertMode()
		h.setError(fmt.Errorf("insert mode: target session no longer exists"))
		return nil
	}
	return inst
}

// renderInsertModeBar renders the bottom-of-screen indicator shown while
// insert mode is active. It replaces the standard help bar so the indicator
// is visible at every terminal width and so the help text (with its TUI
// navigation hints) doesn't mislead the user into thinking those bindings
// still apply.
func (h *Home) renderInsertModeBar() string {
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(repeatRune('─', max(0, h.width)))

	targetTitle := ""
	if inst := h.getInstanceByID(h.insertModeSessionID); inst != nil {
		targetTitle = inst.Title
	}

	badge := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorYellow).
		Bold(true).
		Padding(0, 1).
		Render(" -- INSERT -- ")

	infoStyle := lipgloss.NewStyle().Foreground(ColorText)
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)

	line := badge
	if targetTitle != "" {
		line += " " + infoStyle.Render("→ "+targetTitle)
	}
	line += "  " + hintStyle.Render("Esc to exit · Enter to submit")

	return lipgloss.JoinVertical(lipgloss.Left, border, line)
}

// repeatRune is a thin wrapper so insert_mode.go doesn't introduce strings
// into the import set just for one call (matches the rest of home.go's
// pattern of building border lines).
func repeatRune(r rune, n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]rune, n)
	for i := range buf {
		buf[i] = r
	}
	return string(buf)
}
