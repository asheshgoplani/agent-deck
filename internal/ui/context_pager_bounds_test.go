package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The pager is a full-screen overlay, so its render contract is exact: it must
// occupy the terminal's height precisely and never a column more than its
// width. A row too many pushes the status bar off the screen; a row too few
// leaves the previous frame visible underneath. These tests are pure renders —
// they construct a report in memory and touch no file, no HOME and no tmux.

// contextPagerSizes are the terminals the overlay is asserted against: a normal
// one, a small one, and two that are narrower or shorter than the chrome itself.
var contextPagerSizes = []struct {
	name          string
	width, height int
}{
	{"wide", 160, 48},
	{"normal", 120, 30},
	{"small", 80, 24},
	{"tiny", 40, 12},
	{"absurd", 20, 8},
}

// countRenderedRows returns how many lines a rendered frame occupies.
func countRenderedRows(view string) int {
	if view == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSuffix(view, "\n"), "\n"))
}

// TestContextPagerFillsTheTerminalExactly asserts the height contract on every
// tab, at every drill depth, at every size.
func TestContextPagerFillsTheTerminalExactly(t *testing.T) {
	for _, size := range contextPagerSizes {
		t.Run(size.name, func(t *testing.T) {
			p := NewContextPager()
			p.Show("demo", "session-1", "claude", size.width, size.height)
			p.SetReport(buildContextTestReport(), nil)

			for tab := 0; tab < contextPagerTabCount; tab++ {
				p.SetTab(tab)
				for depth := 0; depth < 3; depth++ {
					got := countRenderedRows(p.View())
					if got != size.height {
						t.Fatalf("tab %d depth %d: %d rows, want exactly %d", tab, p.Depth(), got, size.height)
					}
					if !p.Descend() {
						break
					}
				}
				for p.Ascend() {
				}
			}
		})
	}
}

// TestContextPagerNeverExceedsWidthOnAnyTab is the companion to the height
// contract: a line wider than the terminal wraps, which shifts every row below
// it and breaks the layout the height contract just guaranteed.
func TestContextPagerNeverExceedsWidthOnAnyTab(t *testing.T) {
	for _, size := range contextPagerSizes {
		t.Run(size.name, func(t *testing.T) {
			p := NewContextPager()
			p.Show("a-rather-long-session-title-for-the-header", "session-1", "claude", size.width, size.height)
			p.SetReport(buildContextTestReport(), []string{"a resolution warning long enough to need truncating on a narrow terminal"})

			for tab := 0; tab < contextPagerTabCount; tab++ {
				p.SetTab(tab)
				p.Bottom()
				for i, line := range strings.Split(p.View(), "\n") {
					if w := cellWidth(ansi.Strip(line)); w > size.width {
						t.Fatalf("tab %d line %d is %d cells wide, terminal is %d: %q", tab, i, w, size.width, ansi.Strip(line))
					}
				}
				for p.Ascend() {
				}
			}
		})
	}
}

// TestContextPagerPagingStaysInBounds exercises the hand-rolled scroll
// arithmetic directly. There is no viewport component in this repository, so
// every clamp is ours to get wrong.
func TestContextPagerPagingStaysInBounds(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	p.SetTab(contextPagerTabVerify) // the longest body

	p.Top()
	if got := p.current().offset; got != 0 {
		t.Fatalf("Top left offset at %d", got)
	}

	p.PageUp()
	if got := p.current().offset; got != 0 {
		t.Fatalf("paging up from the top moved to %d", got)
	}

	for i := 0; i < 50; i++ {
		p.PageDown()
	}
	max := p.maxOffset()
	if got := p.current().offset; got != max {
		t.Fatalf("paging past the end left offset at %d, want the maximum %d", got, max)
	}

	p.Bottom()
	if got := p.current().offset; got != max {
		t.Fatalf("Bottom left offset at %d, want %d", got, max)
	}

	p.PageUp()
	if got := p.current().offset; got < 0 || got > max {
		t.Fatalf("paging up left offset out of range: %d (max %d)", got, max)
	}
}

// TestContextPagerScrollingNeverBleedsANSI: the body carries content captured
// from a harness, which can contain colour codes. Every emitted line must end
// with a reset or a colour leaks into the chrome and into whatever renders
// after the overlay closes.
func TestContextPagerScrollingNeverBleedsANSI(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())

	for tab := 0; tab < contextPagerTabCount; tab++ {
		p.SetTab(tab)
		for _, move := range []func(){p.Top, p.PageDown, p.Bottom, p.PageUp} {
			move()
			for i, line := range strings.Split(strings.TrimSuffix(p.View(), "\n"), "\n") {
				if !strings.Contains(line, "\x1b[") {
					continue // a plain line needs no reset
				}
				if !strings.Contains(line, "\x1b[0m") {
					t.Fatalf("tab %d line %d carries styling with no SGR reset: %q", tab, i, line)
				}
			}
		}
	}
}

// TestContextPagerUnsupportedStateIsStillNavigable: an unmeasurable harness
// gets a populated inventory screen, not an error, so scrolling and tab
// switching must behave there exactly as they do on a full report.
func TestContextPagerUnsupportedStateIsStillNavigable(t *testing.T) {
	p := newContextPagerForTest(t, buildContextUnsupportedReport())

	for tab := 0; tab < contextPagerTabCount; tab++ {
		p.SetTab(tab)
		p.Bottom()
		if got := p.current().offset; got < 0 || got > p.maxOffset() {
			t.Fatalf("tab %d: offset %d is outside [0,%d]", tab, got, p.maxOffset())
		}
		if out := ansi.Strip(p.View()); strings.Contains(out, "(0.0%)") {
			t.Fatalf("tab %d shows a percentage with no window and no total:\n%s", tab, out)
		}
	}
}

// TestContextPagerResizeToDegenerateSizesDoesNotPanic: SetSize is driven by
// terminal events, which arrive with whatever the terminal emulator felt like
// reporting, including zero and negative values mid-resize.
func TestContextPagerResizeToDegenerateSizesDoesNotPanic(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	for _, size := range [][2]int{{0, 0}, {-5, -5}, {1, 1}, {3, 2}, {120, 30}} {
		p.SetSize(size[0], size[1])
		p.Bottom()
		_ = p.View()
		if got := p.current().offset; got < 0 || got > p.maxOffset() {
			t.Fatalf("size %v left offset %d outside [0,%d]", size, got, p.maxOffset())
		}
	}
}

// TestContextPagerVerifyTabPointsAtTheExplicitVerb is the safety property of
// the whole feature's UI half: the panel is read-only. It must tell the reader
// where the live comparison lives, and say that opening the panel is not it.
func TestContextPagerVerifyTabPointsAtTheExplicitVerb(t *testing.T) {
	p := newContextPagerForTest(t, buildContextTestReport())
	p.SetTab(contextPagerTabVerify)
	p.Bottom()

	var all strings.Builder
	p.Top()
	for {
		all.WriteString(ansi.Strip(p.View()))
		if p.current().offset >= p.maxOffset() {
			break
		}
		p.PageDown()
	}
	out := all.String()

	if !strings.Contains(out, "--verify") {
		t.Errorf("the Verify tab must name the live-comparison verb:\n%s", out)
	}
	if !strings.Contains(out, "sends nothing to the agent") {
		t.Errorf("the Verify tab must state that opening the panel mutates nothing:\n%s", out)
	}
}
