package ui

// The detached embedded view renders a terminal, whereas the classic preview
// is an informational scrollback view. Fit only the visible terminal target;
// attach clients own their own geometry while input is routed to them.
func (h *Home) detachedPreviewSize(key string) embeddedTerminalSize {
	if !h.embeddedLayout || h.embeddedMode || h.width < 1 || h.height < 1 || h.getLayoutMode() == LayoutModeSingle {
		return embeddedTerminalSize{}
	}
	_, selectedKey, _ := h.selectedPreviewTarget()
	if selectedKey != key {
		return embeddedTerminalSize{}
	}
	return h.embeddedTerminalSize()
}
