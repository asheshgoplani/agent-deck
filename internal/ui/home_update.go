package ui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Update implements tea.Model. It delegates to updateInner and, when
// fullRepaint is enabled, appends tea.ClearScreen on KeyMsg and mouse-wheel
// MouseMsg events to prevent incremental-redraw drift between the tick-based
// clears (issue #607). Under the default (full_repaint = false) this wrapper
// is a pass-through — no regression for users who never opt in.
func (h *Home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Terminal reads may combine rapid printable keystrokes. Route them in
	// order so a shortcut can open the text field that receives the tail.
	// Bracketed paste and Alt input retain their original event semantics.
	// Each single-rune recursive call goes through this same Update method,
	// so the session-input receipt below is still acquired per keystroke.
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyRunes && len(key.Runes) > 1 && !key.Alt && !key.Paste {
		var commands []tea.Cmd
		for _, r := range key.Runes {
			_, cmd := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			commands = append(commands, cmd)
			if h.isQuitting {
				break
			}
		}
		return h, tea.Sequence(commands...)
	}
	if input := h.sessionInput; input != nil {
		receipt := input.InputReceipt(msg)
		defer input.FinishInput(receipt)
	}
	defer h.recordFocusedSession()
	model, cmd := h.updateInner(msg)
	if !h.fullRepaint {
		return model, cmd
	}
	switch m := msg.(type) {
	case tea.KeyMsg:
		_ = m
		return model, appendClearScreen(cmd)
	case tea.MouseMsg:
		if m.Button == tea.MouseButtonWheelUp || m.Button == tea.MouseButtonWheelDown {
			return model, appendClearScreen(cmd)
		}
	}
	return model, cmd
}

// appendClearScreen batches tea.ClearScreen onto cmd, preserving nil-safety.
func appendClearScreen(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return tea.ClearScreen
	}
	return tea.Batch(cmd, tea.ClearScreen)
}

// hookCleanupCmd keeps best-effort filesystem cleanup outside Update. Registry
// failures return nil cleanup, preserving the handler's existing error logging.
func hookCleanupCmd(cleanup func(), done chan struct{}) tea.Cmd {
	if cleanup == nil {
		if done != nil {
			close(done)
		}
		return nil
	}
	return func() tea.Msg {
		if done != nil {
			defer close(done)
		}
		cleanup()
		return nil
	}
}
