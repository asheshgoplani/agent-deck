package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// A remote group row in a narrow preview pane panicked the live TUI
// (v1.16.28): renderPreviewPane(width 10) -> renderRemotePreview ->
// renderEmptyStateResponsive sliced the hint with hint[:room-3] while room
// was 2. Every empty state must render at any pane size, never split a rune
// and never draw a subtitle or hint wider than the pane.
func TestRenderEmptyStateResponsiveNarrowPanes(t *testing.T) {
	configs := map[string]EmptyStateConfig{
		"remote group": {
			Icon:     "⬡",
			Title:    "Remote: sbbox",
			Subtitle: "Host: sbbox.tailnet — 12 sessions",
			Hints:    []string{"Press Enter on a session to attach via SSH"},
		},
		"minimal subtitle and hints": {
			Icon:                "◇",
			Title:               "No sessions match",
			Subtitle:            "status: waiting · last 24 hours · ünïcödé ⚡",
			ShowSubtitleMinimal: true,
			ShowAllHintsMinimal: true,
			Hints:               []string{"0 show all", "* time range — ⚡ quick"},
		},
	}
	for name, config := range configs {
		for width := -2; width <= 60; width++ {
			for _, height := range []int{0, 1, 5, 12, 45} {
				out := renderNoPanic(t, name, config, width, height)
				if !utf8.ValidString(out) {
					t.Fatalf("%s %dx%d: split a rune: %q", name, width, height, out)
				}
				for _, text := range append([]string{config.Subtitle}, config.Hints...) {
					for _, line := range strings.Split(out, "\n") {
						if width > 0 && strings.Contains(line, text[:4]) && lipgloss.Width(strings.TrimSpace(line)) > max(width, lipgloss.Width(config.Title)) {
							t.Fatalf("%s %dx%d: line wider than the pane: %q", name, width, height, line)
						}
					}
				}
			}
		}
	}
}

func renderNoPanic(t *testing.T, name string, config EmptyStateConfig, width, height int) (out string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s %dx%d: renderEmptyStateResponsive panicked: %v", name, width, height, r)
		}
	}()
	return renderEmptyStateResponsive(config, width, height)
}

func TestRenderPanelTitleAnyWidth(t *testing.T) {
	h := &Home{}
	for width := -3; width <= 12; width++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("width %d: renderPanelTitle panicked: %v", width, r)
				}
			}()
			if out := h.renderPanelTitle("Sessions — ünïcödé", width); !utf8.ValidString(out) {
				t.Fatalf("width %d: split a rune: %q", width, out)
			}
		}()
	}
}
