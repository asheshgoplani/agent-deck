package ui

import (
	"fmt"
	"strings"
	"testing"
)

// ASK-140, 2026-10-28: the owner's deck in Ghostty and in the Mac app pane
// showed every remotes/* header four to six times with different network
// times, three selected "astro" rows, and a blank band under the help bar,
// and the list "kept going up". Restarting the terminal cleared it.
//
// Cause: an attached tmux client scrolls a pane by setting a scroll region
// (DECSTBM) on the outer terminal and leaves it set. Ctrl+Q detach closes the
// client's PTY, so tmux's own reset on exit never reaches the terminal, and
// the region survives into the deck. Every later frame's line feeds then
// scroll only that region: rows move up, Bubble Tea skips the rows it thinks
// are unchanged, stale copies stay, and the rows under the region are never
// drawn. The deck has to reset the region itself on every full repaint.

// leakedScrollRegion is a region an attached Claude Code pane leaves behind:
// rows 1-49 of a 60-row terminal, its 11-row prompt and footer excluded.
const leakedScrollRegion = "\x1b[1;49r"

// The owner's shape: remote refreshes and footer toggles, and a Hindi
// (Devanagari) reply in the preview, each replayed after a leaked region.
func TestScrollRegionLeak_DeckFramesStayInPlace(t *testing.T) {
	t.Run("remote-refresh", func(t *testing.T) { assertFramesSurviveLeakedRegion(t, footerAndRefreshSteps) })
	t.Run("hindi-preview", func(t *testing.T) { assertFramesSurviveLeakedRegion(t, previewThenRemoteSteps) })
}

func assertFramesSurviveLeakedRegion(t *testing.T, steps []dupFrameStep) {
	const width, height = 200, 60
	frames, markers, names := dupFrameSequence(t, dupFrameHome(t), steps)
	chunks, upTo := renderFramesPerFlush(t, frames, markers, width, height)

	for _, c := range widthConventions {
		t.Run(c.name, func(t *testing.T) {
			for i := range frames {
				term := newWrapTerm(width, height, c.conv)
				_, _ = term.Write([]byte(leakedScrollRegion))
				for _, ch := range chunks[:upTo[i]] {
					_, _ = term.Write(ch)
				}
				screen, want := term.rows(), frameRows(frames[i])
				var errs []string
				for _, tok := range dupFrameTokens {
					if got, exp := rowsContaining(screen, tok), rowsContaining(want, tok); got != exp {
						errs = append(errs, fmt.Sprintf("%q on %d screen rows, frame draws it on %d", tok, got, exp))
					}
				}
				if last := strings.TrimSpace(want[height-1]); last != "" && strings.TrimSpace(screen[height-1]) == "" {
					errs = append(errs, fmt.Sprintf("bottom row blank on screen, frame draws %q", last))
				}
				if len(errs) > 0 {
					t.Fatalf("frame %d (%s) with a leaked scroll region:\n%s\nscreen:\n%s",
						i, names[i], strings.Join(errs, "\n"), strings.Join(screen, "\n"))
				}
			}
		})
	}
}

// Every frame the deck renders starts by resetting the scroll region, so a
// full repaint (start, resize, return from attach) always heals a leak.
func TestScrollRegionLeak_FrameStartsWithRegionReset(t *testing.T) {
	h := dupFrameHome(t)
	for _, size := range [][2]int{{200, 60}, {120, 40}, {70, 30}, {40, 20}} {
		h.width, h.height = size[0], size[1]
		if f := h.View(); !strings.HasPrefix(f, resetScrollRegion) {
			t.Errorf("%dx%d frame does not start with the scroll-region reset: %q", size[0], size[1], f[:min(len(f), 40)])
		}
	}
}
