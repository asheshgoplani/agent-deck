package ui

import (
	"fmt"

	deckterminal "github.com/asheshgoplani/agent-deck/internal/terminal"
	tea "github.com/charmbracelet/bubbletea"
)

type embeddedStartMsg struct {
	generation uint64
	terminal   *embeddedTerminal
	err        error
}

type embeddedFrameMsg struct {
	generation uint64
	terminal   *embeddedTerminal
	alive      bool
	err        error
}

// SetSessionIO installs the production stdin/stdout routing seams. Keeping
// their construction in main preserves Bubble Tea's ownership of the real TTY
// while Home owns only mode transitions and pane geometry.
func (h *Home) SetSessionIO(input *SessionInputRouter, output *SessionOutput) {
	h.sessionInput = input
	h.sessionOutput = output
}

// EmbeddedTerminalEnabled reports the resolved startup layout so main can
// preserve the classic Bubble Tea terminal protocol when the opt-in is off.
func (h *Home) EmbeddedTerminalEnabled() bool {
	return h.embeddedLayout
}

func (h *Home) embeddedPaneRect() terminalCellRect {
	helpBarHeight := 2
	filterBarHeight := 1
	updateBannerHeight := 0
	if h.shouldRenderUpdateNudge() {
		updateBannerHeight = 1
	}
	maintenanceBannerHeight := 0
	if h.maintenanceMsg != "" {
		maintenanceBannerHeight = 1
	}
	debugBarHeight := 0
	if h.debugMode {
		debugBarHeight = 1
	}
	contentHeight := h.height - 1 - helpBarHeight - filterBarHeight -
		updateBannerHeight - maintenanceBannerHeight - debugBarHeight
	contentY := 1 + filterBarHeight + updateBannerHeight + maintenanceBannerHeight
	if h.embeddedSidebarHidden {
		return terminalCellRect{
			X:      1, // rounded border
			Y:      contentY + 1,
			Width:  max(1, h.width-2),
			Height: max(1, contentHeight-2),
		}
	}

	if h.getLayoutMode() == LayoutModeDual {
		leftWidth, rightWidth := h.splitPaneWidths()
		return terminalCellRect{
			X:      leftWidth + paneSeparatorWidth + 1, // separator + rounded border
			Y:      contentY + 1,                       // top border
			Width:  max(1, rightWidth-2),
			Height: max(1, contentHeight-2),
		}
	}

	listHeight := h.stackedListHeight(contentHeight)
	previewHeight := max(3, contentHeight-listHeight-1)
	return terminalCellRect{
		X:      0,
		Y:      contentY + listHeight + 1 + 2, // separator + two-line PREVIEW title
		Width:  max(1, h.width),
		Height: max(1, previewHeight-2),
	}
}

func (h *Home) embeddedTerminalSize() embeddedTerminalSize {
	rect := h.embeddedPaneRect()
	return embeddedTerminalSize{Cols: rect.Width, Rows: rect.Height}
}

func (h *Home) startEmbeddedTerminalCmd() tea.Cmd {
	generation := h.embeddedGeneration
	req := h.embeddedRequest
	size := h.embeddedTerminalSize()
	ctx := h.ctx
	return func() tea.Msg {
		terminal, err := startEmbeddedTerminal(ctx, req, size)
		return embeddedStartMsg{generation: generation, terminal: terminal, err: err}
	}
}

func waitEmbeddedTerminalCmd(generation uint64, terminal *embeddedTerminal) tea.Cmd {
	if terminal == nil {
		return nil
	}
	return func() tea.Msg {
		alive, err := terminal.Wait()
		return embeddedFrameMsg{
			generation: generation,
			terminal:   terminal,
			alive:      alive,
			err:        err,
		}
	}
}

func (h *Home) installEmbeddedTerminal(msg embeddedStartMsg) tea.Cmd {
	h.embeddedRefreshPending = false
	if msg.generation != h.embeddedGeneration || !h.embeddedMode {
		if msg.terminal != nil {
			_ = msg.terminal.Close()
		}
		return nil
	}
	if msg.err != nil {
		h.exitInsertMode()
		h.setError(fmt.Errorf("open embedded session: %w", msg.err))
		return nil
	}
	h.embeddedTerminal = msg.terminal
	// The PTY was created at the size the pane had when Enter was pressed;
	// start from an unknown applied rectangle so the install pass resizes it
	// if the chrome moved during the connect.
	h.embeddedAppliedRect = terminalCellRect{}
	if h.sessionInput != nil {
		h.sessionInput.Activate(h.embeddedTerminal, h.embeddedPaneRect(), h.embeddedSwitchByte())
	}
	h.syncEmbeddedTerminalGeometry()
	return waitEmbeddedTerminalCmd(h.embeddedGeneration, h.embeddedTerminal)
}

func (h *Home) updateEmbeddedFrame(msg embeddedFrameMsg) tea.Cmd {
	if msg.generation != h.embeddedGeneration || msg.terminal != h.embeddedTerminal {
		return nil
	}
	if !msg.alive {
		h.exitInsertMode()
		if msg.err != nil {
			h.setError(fmt.Errorf("embedded session ended: %w", msg.err))
		}
		return nil
	}
	h.syncEmbeddedCursor()
	return waitEmbeddedTerminalCmd(h.embeddedGeneration, h.embeddedTerminal)
}

// syncEmbeddedTerminalGeometry re-derives the pane rectangle from the current
// dashboard chrome and pushes it to the child PTY, the input router, and the
// hardware cursor. It is safe to call on every event that might have moved the
// pane: the PTY is resized only when the rectangle actually changed, so a
// redundant call costs one comparison, never a SIGWINCH storm into tmux.
func (h *Home) syncEmbeddedTerminalGeometry() {
	if h.embeddedTerminal == nil {
		return
	}
	rect := h.embeddedPaneRect()
	if rect != h.embeddedAppliedRect {
		if err := h.embeddedTerminal.Resize(embeddedTerminalSize{Cols: rect.Width, Rows: rect.Height}); err != nil {
			h.setError(fmt.Errorf("embedded session: %w", err))
		} else {
			h.embeddedAppliedRect = rect
		}
	}
	if h.sessionInput != nil {
		h.sessionInput.UpdateRect(rect)
	}
	h.syncEmbeddedCursor()
}

func (h *Home) syncEmbeddedCursor() {
	if h.embeddedTerminal == nil {
		return
	}
	rect := h.embeddedPaneRect()
	if h.sessionInput != nil {
		h.sessionInput.UpdateRect(rect)
	}
	if h.sessionOutput != nil {
		h.sessionOutput.SetEmbeddedCursor(rect, h.embeddedTerminal.Cursor())
	}
}

func (h *Home) toggleEmbeddedSidebar() {
	if !h.embeddedMode || h.sessionInput == nil {
		return
	}
	h.embeddedSidebarHidden = !h.embeddedSidebarHidden
	h.syncEmbeddedTerminalGeometry()
}

func (h *Home) embeddedAttachRequest(target insertTargetRef) (deckterminal.AttachRequest, bool) {
	if target.isRemote() {
		return buildRemoteAttachRequest(target.remoteName, target.remoteID, "")
	}
	if target.local == nil {
		return deckterminal.AttachRequest{}, false
	}
	tmuxSession := target.local.GetTmuxSession()
	if tmuxSession == nil {
		return deckterminal.AttachRequest{}, false
	}
	name := tmuxSession.Name
	if target.hasWindow {
		name = fmt.Sprintf("%s:%d", name, target.windowIndex)
	}
	// The embedded client inherits this process's locale, so it forces UTF-8
	// the way the full-screen attach in internal/tmux/pty.go does.
	return deckterminal.AttachRequest{Name: name, SocketName: tmuxSession.SocketName, ForceUTF8: true}, true
}

func (h *Home) renderEmbeddedTerminalContent(width, height int) string {
	if h.embeddedTerminal == nil {
		return ensureExactWidth(ensureExactHeight("Connecting to session…", height), width)
	}
	return ensureExactWidth(ensureExactHeight(h.embeddedTerminal.Render(), height), width)
}

func (h *Home) renderDetachedTerminalContent(preview string, width, height int) string {
	if preview == "" {
		waiting := "Waiting for terminal output…"
		return ensureExactWidth(ensureExactHeight(waiting, height), width)
	}
	rendered, scrollOffset := renderTerminalSnapshot(
		preview,
		embeddedTerminalSize{Cols: width, Rows: height},
		h.previewScrollOffset,
	)
	h.previewScrollOffset = scrollOffset
	return rendered
}
