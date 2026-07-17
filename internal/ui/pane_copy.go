package ui

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

type paneCaptureFunc func(*session.Instance) (string, error)
type paneClipboardFunc func(string, bool) (*clipboard.CopyResult, error)

type copyPaneResultMsg struct {
	sessionTitle string
	lineCount    int
	empty        bool
	err          error
}

// normalizeVisiblePane turns ANSI-rich tmux capture-pane output into plain
// clipboard text. It preserves layout inside the visible pane while removing
// terminal controls and tmux's right and bottom padding.
func normalizeVisiblePane(raw string) string {
	plain := ansi.Strip(strings.ReplaceAll(raw, "\r\n", "\n"))
	plain = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, plain)

	lines := strings.Split(plain, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func captureVisiblePane(inst *session.Instance) (string, error) {
	if inst == nil || inst.GetTmuxSession() == nil {
		return "", fmt.Errorf("session has no tmux pane")
	}
	return inst.GetTmuxSession().CapturePaneFresh()
}

// copyVisiblePane captures exactly the selected local session's visible pane
// and copies a plain-text snapshot through the existing native/OSC52 chain.
func (h *Home) copyVisiblePane(inst *session.Instance) tea.Cmd {
	return func() tea.Msg {
		capture := h.paneCapture
		if capture == nil {
			capture = captureVisiblePane
		}
		copyText := h.paneClipboard
		if copyText == nil {
			copyText = clipboard.Copy
		}

		raw, err := capture(inst)
		if err != nil {
			return copyPaneResultMsg{sessionTitle: inst.Title, err: err}
		}
		payload := normalizeVisiblePane(raw)
		if payload == "" {
			return copyPaneResultMsg{sessionTitle: inst.Title, empty: true}
		}

		result, err := copyText(payload, tmux.GetTerminalInfo().SupportsOSC52)
		if err != nil {
			return copyPaneResultMsg{sessionTitle: inst.Title, err: err}
		}
		return copyPaneResultMsg{sessionTitle: inst.Title, lineCount: result.LineCount}
	}
}
