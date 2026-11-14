package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// A badge must occupy the same cells in the layout and in terminals that
// advance per code point (including tmux built against glibc). Otherwise a
// padded session row puts its divider one column before its neighbours.
func TestViewersBadgeDividerAlignment(t *testing.T) {
	forceTrueColorProfile()
	viewers := goldenViewers(time.Now(), 2)
	const paneWidth = 40
	for _, selected := range []bool{false, true} {
		for _, budget := range []int{-1, 0, 1, 2, 3, 4, 5, 6} {
			t.Run(fmt.Sprintf("selected=%t/budget=%d", selected, budget), func(t *testing.T) {
				badge, width := fitViewersBadge(viewers, true, selected, budget)
				if width != ansi.StringWidthWc(badge) {
					t.Errorf("badge %q: layout width=%d, terminal width=%d", badge, width, ansi.StringWidthWc(badge))
				}
				if budget >= 0 && width > budget {
					t.Errorf("badge width %d exceeds budget %d", width, budget)
				}
				row := ensureExactWidth("demo shell"+badge, paneWidth)
				for name, measure := range map[string]func(string) int{
					"layout":   lipgloss.Width,
					"terminal": ansi.StringWidthWc,
				} {
					if got := measure(row); got != paneWidth {
						t.Errorf("%s places divider at column %d, want %d", name, got, paneWidth)
					}
				}
			})
		}
	}
}

func TestViewersPreviewWidthAgreement(t *testing.T) {
	now := time.Now()
	for _, known := range []bool{false, true} {
		for n := 0; n <= 2; n++ {
			t.Run(fmt.Sprintf("known=%t/viewers=%d", known, n), func(t *testing.T) {
				line := viewersLine(goldenViewers(now, n), known, now)
				if layout, terminal := cellWidth(line), ansi.StringWidthWc(line); layout != terminal {
					t.Errorf("preview %q: layout width=%d, terminal width=%d", line, layout, terminal)
				}
			})
		}
	}
}
