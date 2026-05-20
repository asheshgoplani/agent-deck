package ui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// defaultInsertBatchDuration is the production debounce window for coalescing
// rune-by-rune typing into a single tmux send-keys call (#1094). Picked to
// be small enough that the user can't feel it (~one frame at 60Hz) but large
// enough that bursts of typing collapse into a single fork+exec.
const defaultInsertBatchDuration = 15 * time.Millisecond

// insertFlushMsg is dispatched by the tea.Tick scheduled when the first rune
// of a batch is buffered. When it arrives the buffered text is flushed to
// the focused session.
type insertFlushMsg struct{}

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

// exitInsertMode returns the TUI to normal navigation mode. Any pending
// keystrokes in the batch buffer are dropped — they should have been flushed
// by the caller via flushInsertBuf() if the user wanted them preserved.
func (h *Home) exitInsertMode() {
	h.insertMode = false
	h.insertModeSessionID = ""
	h.insertBuf.Reset()
	h.insertFlushPending = false
}

// handleInsertModeKey is the keyboard handler used while insert mode is
// active. Esc exits, Enter sends a newline, and printable runes (and the
// space key) are buffered then flushed in batches to amortize the fork+exec
// cost of tmux send-keys (#1094 latency). Backspace, arrow keys, Tab,
// ShiftTab, Ctrl-C, and Ctrl-D are forwarded as tmux named keys so users can
// edit input and navigate menus inside the focused session (claude often
// shows arrow-driven pickers).
func (h *Home) handleInsertModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		h.flushInsertBuf()
		h.exitInsertMode()
		return h, nil
	case tea.KeyEnter:
		h.flushInsertBuf()
		h.dispatchInsertKey("", true)
		return h, nil
	case tea.KeySpace:
		h.insertBuf.WriteString(" ")
		return h, h.scheduleInsertFlush()
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return h, nil
		}
		h.insertBuf.WriteString(string(msg.Runes))
		return h, h.scheduleInsertFlush()
	case tea.KeyBackspace:
		h.flushInsertBuf()
		h.dispatchInsertNamedKey("BSpace")
		return h, nil
	case tea.KeyUp:
		h.flushInsertBuf()
		h.dispatchInsertNamedKey("Up")
		return h, nil
	case tea.KeyDown:
		h.flushInsertBuf()
		h.dispatchInsertNamedKey("Down")
		return h, nil
	case tea.KeyLeft:
		h.flushInsertBuf()
		h.dispatchInsertNamedKey("Left")
		return h, nil
	case tea.KeyRight:
		h.flushInsertBuf()
		h.dispatchInsertNamedKey("Right")
		return h, nil
	case tea.KeyTab:
		h.flushInsertBuf()
		h.dispatchInsertNamedKey("Tab")
		return h, nil
	case tea.KeyShiftTab:
		h.flushInsertBuf()
		h.dispatchInsertNamedKey("BTab")
		return h, nil
	case tea.KeyCtrlC:
		h.flushInsertBuf()
		h.dispatchInsertNamedKey("C-c")
		return h, nil
	case tea.KeyCtrlD:
		h.flushInsertBuf()
		h.dispatchInsertNamedKey("C-d")
		return h, nil
	default:
		// Other keys (function keys, more exotic ctrl combos) intentionally
		// dropped — surface them only if a user actually reports needing them.
		return h, nil
	}
}

// scheduleInsertFlush returns a tea.Cmd that will deliver insertFlushMsg
// after the batching window, unless one is already pending or batching is
// disabled (insertBatchDuration <= 0, in which case the buffer flushes
// synchronously and no Cmd is returned).
func (h *Home) scheduleInsertFlush() tea.Cmd {
	if h.insertBatchDuration <= 0 {
		h.flushInsertBuf()
		return nil
	}
	if h.insertFlushPending {
		return nil
	}
	h.insertFlushPending = true
	d := h.insertBatchDuration
	return tea.Tick(d, func(time.Time) tea.Msg { return insertFlushMsg{} })
}

// flushInsertBuf dispatches any buffered runes to the focused session as a
// single send-keys call, then clears the buffer. Called from the periodic
// timer (insertFlushMsg) and synchronously before any non-rune key (Enter,
// Esc, Backspace, arrows, ...) so the keystroke ordering observed by the
// target pane matches the order in which the user pressed them.
func (h *Home) flushInsertBuf() {
	h.insertFlushPending = false
	if h.insertBuf.Len() == 0 {
		return
	}
	text := h.insertBuf.String()
	h.insertBuf.Reset()
	h.dispatchInsertKey(text, false)
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

// dispatchInsertNamedKey forwards a tmux named key (Up/Down/Left/Right/Tab/
// BTab/BSpace/C-c/C-d) to the focused session. In tests an override sink
// captures the key instead of running tmux.
func (h *Home) dispatchInsertNamedKey(key string) {
	inst := h.resolveInsertTarget()
	if inst == nil {
		return
	}
	if h.insertNamedKeySink != nil {
		if err := h.insertNamedKeySink(inst, key); err != nil {
			h.setError(fmt.Errorf("insert mode send named key failed: %w", err))
		}
		return
	}
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		h.exitInsertMode()
		h.setError(fmt.Errorf("insert mode: tmux session vanished"))
		return
	}
	if err := tmuxSess.SendNamedKey(key); err != nil {
		h.setError(fmt.Errorf("insert mode send-named-key failed: %w", err))
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
