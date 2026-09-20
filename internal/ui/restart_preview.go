package ui

import "github.com/asheshgoplani/agent-deck/internal/session"

// detachedRestartPreviewSize returns the terminal viewport a restarted local
// session should fit while it is being shown as a detached embedded preview.
// Classic preview, full-screen/single layouts, and non-selected sessions do
// not own detached terminal geometry and therefore return the zero value.
func (h *Home) detachedRestartPreviewSize(key string) embeddedTerminalSize {
	if !h.embeddedLayout || h.embeddedMode || h.width < 1 || h.height < 1 || h.getLayoutMode() == LayoutModeSingle {
		return embeddedTerminalSize{}
	}
	_, selectedKey, _ := h.selectedPreviewTarget()
	if selectedKey != key {
		return embeddedTerminalSize{}
	}
	return h.embeddedTerminalSize()
}

func fitRestartedPreview(inst *session.Instance, size embeddedTerminalSize) error {
	if inst == nil || size.Cols < 1 || size.Rows < 1 {
		return nil
	}
	tmuxSession := inst.GetTmuxSession()
	if tmuxSession == nil {
		return nil
	}
	return tmuxSession.FitDetachedPreview(size.Cols, size.Rows)
}
