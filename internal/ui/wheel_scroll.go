package ui

// listWheelScrollRows is how many session-list rows one mouse-wheel notch
// moves the cursor. It matches the 3 lines per notch the scrollback pager
// uses (#1491) and typical terminal wheel UX; the list previously moved a
// single row per notch, which made trackpad scrolling of a long list crawl.
const listWheelScrollRows = 3

// wheelListCursor returns the cursor position after one wheel notch. dir is
// -1 for wheel-up and +1 for wheel-down, rows is listWheelScrollRows. The
// result is clamped into [0, n-1] so a notch near either end lands on the
// boundary row instead of overshooting, and equals cursor when the cursor is
// already parked at that end (the caller treats that as a no-op).
//
// Divider skipping is deliberately NOT done here: it needs h.flatItems, so it
// stays with (*Home).skipDivider, which the caller runs on the clamped result.
func wheelListCursor(cursor, n, dir, rows int) int {
	if n <= 0 {
		return 0
	}
	if rows < 1 {
		rows = 1
	}
	next := cursor + dir*rows
	if next < 0 {
		next = 0
	}
	if next > n-1 {
		next = n - 1
	}
	return next
}
