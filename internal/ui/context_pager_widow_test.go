package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The widow rule: a section heading that announces content ("reading the
// columns:", "caveats") must never be the last visible line of a frame while
// its body sits below the fold. At 80x24 the Overview's legend heading landed
// on exactly that fold (G3 claude-unknown-version-narrow, 2026-07-29 — three
// frames tripped the Blank Detector's empty-label rule), pushed there by the
// longer no-model sentence. The screen was honest; its last line was a label
// promising rows the reader could not see.

// widowHeadings are the trimmed forms of the lines under the rule.
var widowHeadings = []string{"reading the columns:", "caveats"}

// lastBodyLine returns the frame's final non-empty scrolling-body row.
// Layout contract: three header rows, then bodyHeight rows, then two footers.
func lastBodyLine(view string, height int) string {
	rows := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
	if len(rows) != height {
		return "FRAME BROKE THE HEIGHT CONTRACT"
	}
	last := ""
	for _, r := range rows[3 : height-2] {
		if s := strings.TrimSpace(ansi.Strip(r)); s != "" {
			last = s
		}
	}
	return last
}

// TestContextPagerHeadingNeverSitsOnTheFold walks every scroll position of
// every tab at every size and asserts no frame ends on a widowed heading.
func TestContextPagerHeadingNeverSitsOnTheFold(t *testing.T) {
	for _, size := range contextPagerSizes {
		t.Run(size.name, func(t *testing.T) {
			p := NewContextPager()
			p.Show("demo", "session-1", "claude", size.width, size.height)
			p.SetReport(buildContextTestReport(), nil)

			for tab := 0; tab < contextPagerTabCount; tab++ {
				p.SetTab(tab)
				// Walk the whole buffer one line at a time. ScrollDown clamps,
				// so a generous bound simply parks at the end.
				for step := 0; step < 400; step++ {
					last := lastBodyLine(p.View(), size.height)
					for _, h := range widowHeadings {
						if last == h {
							t.Fatalf("tab %d, scroll step %d: the frame ends on the heading %q with its body below the fold", tab, step, h)
						}
					}
					p.ScrollDown(1)
				}
				p.ScrollUp(1 << 20)
			}
		})
	}
}

// TestContextPagerHeadingStillRendersWithItsBody guards the other direction:
// widow control must withhold the heading only at the fold, never lose it.
// Once the buffer's end is on screen, the heading and its first body line are
// both present.
func TestContextPagerHeadingStillRendersWithItsBody(t *testing.T) {
	p := NewContextPager()
	p.Show("demo", "session-1", "claude", 80, 24)
	p.SetReport(buildContextTestReport(), nil)

	// Scroll to the very end: everything, including the legend, is above or at
	// the fold now, so the heading must be visible somewhere in the walk.
	seen := false
	for step := 0; step < 400 && !seen; step++ {
		view := ansi.Strip(p.View())
		if strings.Contains(view, "reading the columns:") {
			seen = true
		}
		p.ScrollDown(1)
	}
	if !seen {
		t.Fatal("the legend heading never rendered at any scroll position: widow control lost it instead of moving it")
	}
}
