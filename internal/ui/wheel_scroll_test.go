package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The wheel moves the session-list SELECTION, not text: every notch changes
// the selected session, resets the preview offset, and schedules a preview
// fetch. A trackpad emits a burst of notches per gesture, so a multi-row step
// skips whole groups between frames and reads as jumping rather than
// scrolling. Pin one row per notch, matching the j/k keys — and explicitly NOT
// the scrollback pager's 3 lines, which scrolls text and is a different job.
func TestWheelListCursor_MovesOneRowPerNotch(t *testing.T) {
	if listWheelScrollRows != 1 {
		t.Fatalf("listWheelScrollRows = %d, want 1 (one notch, one session)", listWheelScrollRows)
	}
	if got := wheelListCursor(10, 100, 1, listWheelScrollRows); got != 11 {
		t.Fatalf("wheel down from 10 = %d, want 11", got)
	}
	if got := wheelListCursor(10, 100, -1, listWheelScrollRows); got != 9 {
		t.Fatalf("wheel up from 10 = %d, want 9", got)
	}
}

// A notch near either end must clamp to the boundary row, never overshoot out
// of range, and must report "no move" when already parked there. The step is
// passed explicitly (including steps larger than listWheelScrollRows) so the
// clamp stays covered no matter what the per-notch constant is set to.
func TestWheelListCursor_ClampsAtBothEnds(t *testing.T) {
	cases := []struct {
		name                 string
		cursor, n, dir, rows int
		want                 int
	}{
		{"down near end clamps", 8, 10, 1, 1, 9},
		{"down at end is a no-op", 9, 10, 1, 1, 9},
		{"up near start clamps", 1, 10, -1, 1, 0},
		{"up at start is a no-op", 0, 10, -1, 1, 0},
		{"empty list stays at 0", 0, 0, 1, 1, 0},
		{"big step down still clamps", 8, 10, 1, 5, 9},
		{"big step up still clamps", 1, 10, -1, 5, 0},
		{"zero step is treated as one", 4, 10, 1, 0, 5},
	}
	for _, tc := range cases {
		if got := wheelListCursor(tc.cursor, tc.n, tc.dir, tc.rows); got != tc.want {
			t.Errorf("%s: wheelListCursor(%d, %d, %d, %d) = %d, want %d",
				tc.name, tc.cursor, tc.n, tc.dir, tc.rows, got, tc.want)
		}
	}
}

// A wheel notch can land on a divider, which is not selectable. The wheel path
// must run the same skipDivider pass the arrow keys do. The cursor starts
// immediately before the divider so the landing happens at any notch size.
func TestWheelScroll_NeverLandsOnDivider(t *testing.T) {
	h := &Home{}
	h.flatItems = []session.Item{
		{Type: session.ItemTypeSession},
		{Type: session.ItemTypeSession},
		{Type: session.ItemTypeSession},
		{Type: session.ItemTypeDivider},
		{Type: session.ItemTypeSession},
	}
	const divider = 3
	h.cursor = divider - listWheelScrollRows
	h.cursor = wheelListCursor(h.cursor, len(h.flatItems), 1, listWheelScrollRows)
	if h.cursor != divider {
		t.Fatalf("pre-skip cursor = %d, want %d (the divider row)", h.cursor, divider)
	}
	h.skipDivider(1)
	if h.flatItems[h.cursor].Type == session.ItemTypeDivider {
		t.Fatalf("cursor %d landed on a divider after a wheel notch", h.cursor)
	}
	if h.cursor != divider+1 {
		t.Fatalf("cursor = %d, want %d (first selectable row past the divider)", h.cursor, divider+1)
	}
}
