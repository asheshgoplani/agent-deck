package ui

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// renderPills owns the entire row: buttons, surface-coloured separators, and
// optional wrapping. Callers provide labels rather than joining styled fragments
// themselves. A plain separator after a button's SGR reset exposes the terminal
// background (the v1.16.11 black-gap regression).
//
// width <= 0 leaves wrapping to the containing dialog. Otherwise whole pills
// wrap with the same indent on every line, without splitting tool names.
func renderPills(labels []string, cursor int, indent string, width int) string {
	if len(labels) == 0 {
		return ""
	}
	selected := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent).Bold(true).Padding(0, 2)
	idle := lipgloss.NewStyle().Foreground(ColorTextDim).Background(ColorSurface).Padding(0, 2)
	buttons := make([]string, len(labels))
	for i, label := range labels {
		style := idle
		if i == cursor {
			style = selected
		}
		buttons[i] = style.Render(label)
	}
	separator := lipgloss.NewStyle().Background(ColorSurface).Render(" ")
	if width > 0 {
		return wrapIndented(buttons, separator, indent, width)
	}
	return indent + strings.Join(buttons, separator)
}

// toolPillLabels resolves tool names and custom icons once for both dialogs.
func toolPillLabels(presets []string) []string {
	labels := make([]string, len(presets))
	for i, cmd := range presets {
		if cmd == "" {
			labels[i] = "shell"
			continue
		}
		labels[i] = displayCommandPreset(cmd)
		if def := session.GetToolDef(cmd); def != nil && def.Icon != "" {
			labels[i] = def.Icon + " " + labels[i]
		}
	}
	return labels
}

func renderLabelPills(labels []string, cursor int) string {
	return renderPills(labels, cursor, "", 0)
}

func renderToolPills(presets []string, cursor int) string {
	return renderLabelPills(toolPillLabels(presets), cursor)
}
