package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The session list moved one row per wheel notch while the scrollback pager
// moved three, so trackpad scrolling of the list crawled. Pin the notch size
// to the pager's.
func TestWheelListCursor_MovesThreeRowsPerNotch(t *testing.T) {
	if listWheelScrollRows != 3 {
		t.Fatalf("listWheelScrollRows = %d, want 3 (match the scrollback pager)", listWheelScrollRows)
	}
	if got := wheelListCursor(10, 100, 1, listWheelScrollRows); got != 13 {
		t.Fatalf("wheel down from 10 = %d, want 13", got)
	}
	if got := wheelListCursor(10, 100, -1, listWheelScrollRows); got != 7 {
		t.Fatalf("wheel up from 10 = %d, want 7", got)
	}
}

// A notch near either end must clamp to the boundary row, never overshoot out
// of range, and must report "no move" when already parked there.
func TestWheelListCursor_ClampsAtBothEnds(t *testing.T) {
	cases := []struct {
		name           string
		cursor, n, dir int
		want           int
	}{
		{"down near end clamps", 8, 10, 1, 9},
		{"down at end is a no-op", 9, 10, 1, 9},
		{"up near start clamps", 1, 10, -1, 0},
		{"up at start is a no-op", 0, 10, -1, 0},
		{"empty list stays at 0", 0, 0, 1, 0},
	}
	for _, tc := range cases {
		if got := wheelListCursor(tc.cursor, tc.n, tc.dir, listWheelScrollRows); got != tc.want {
			t.Errorf("%s: wheelListCursor(%d, %d, %d) = %d, want %d",
				tc.name, tc.cursor, tc.n, tc.dir, got, tc.want)
		}
	}
}

// A three-row jump can land on a divider, which is not selectable. The wheel
// path must run the same skipDivider pass the arrow keys do.
func TestWheelScroll_NeverLandsOnDivider(t *testing.T) {
	h := &Home{}
	h.flatItems = []session.Item{
		{Type: session.ItemTypeSession},
		{Type: session.ItemTypeSession},
		{Type: session.ItemTypeSession},
		{Type: session.ItemTypeDivider},
		{Type: session.ItemTypeSession},
	}
	h.cursor = 0
	h.cursor = wheelListCursor(h.cursor, len(h.flatItems), 1, listWheelScrollRows)
	if h.cursor != 3 {
		t.Fatalf("pre-skip cursor = %d, want 3 (the divider row)", h.cursor)
	}
	h.skipDivider(1)
	if h.flatItems[h.cursor].Type == session.ItemTypeDivider {
		t.Fatalf("cursor %d landed on a divider after a wheel notch", h.cursor)
	}
	if h.cursor != 4 {
		t.Fatalf("cursor = %d, want 4 (first selectable row past the divider)", h.cursor)
	}
}
