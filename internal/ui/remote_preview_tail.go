package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// fitRemotePreviewTail fits a remote session's captured pane into rows lines
// of at most width cells, keeping the bottom: the harness status line and its
// footer are the last lines of the pane, and the remote preview used to write
// up to remotePreviewMaxLines lines that ensureExactHeight then cut from the
// bottom, so no remote session ever showed them (owner report 2026-10-18).
// Like the local preview, trailing blank lines go first, a cut is marked
// "⋮ N more lines above", and each line is cleaned and truncated the same way.
func fitRemotePreviewTail(content string, rows, width int) []string {
	if rows < 1 {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	var marker string
	if len(lines) > rows {
		keep := rows - 1
		if keep < 1 {
			keep = 1
		}
		hidden := len(lines) - keep
		lines = lines[len(lines)-keep:]
		if rows > 1 {
			marker = lipgloss.NewStyle().Foreground(ColorText).Italic(true).
				Render(fmt.Sprintf("⋮ %d more lines above", hidden))
		}
	}
	maxWidth := width - 4
	if maxWidth < 10 {
		maxWidth = 10
	}
	isLightTheme := GetCurrentTheme() == ThemeLight
	out := make([]string, 0, len(lines)+1)
	if marker != "" {
		out = append(out, marker)
	}
	for _, line := range lines {
		safe := stripDisplayErasingEscapes(stripControlCharsPreserveANSI(line))
		if isLightTheme {
			safe = remapANSIBackground(safe, previewSurfaceANSI())
		}
		if cellWidth(safe) > maxWidth {
			safe = cellTruncate(safe, maxWidth-3, "...")
		}
		out = append(out, safe)
	}
	return out
}
