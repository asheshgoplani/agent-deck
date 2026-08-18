package ui

// listWheelScrollRows is how many session-list rows one mouse-wheel notch
// moves the cursor.
//
// One row, deliberately — this is NOT the scrollback pager's 3-lines-per-notch
// (#1491), and the two must not be "made consistent". The pager scrolls text:
// a notch that moves 3 lines is 3 lines of reading. The session list scrolls a
// *selection*: every notch changes which session is selected, resets
// previewScrollOffset, and schedules a preview fetch. Multiplying that by 3
// makes a trackpad — which emits a burst of notches per gesture, with momentum
// — skip whole groups of sessions between frames, so the list reads as jumping
// rather than scrolling. Reported directly after the 3-row version shipped.
//
// Keyboard j/k and the wheel therefore agree: one notch, one session.
const listWheelScrollRows = 1

// wheelListCursor returns the cursor position after one wheel notch. dir is
// -1 for wheel-up and +1 for wheel-down, rows is listWheelScrollRows. The
// result is clamped into [0, n-1] so a notch near either end lands on the
// boundary row instead of overshooting, and equals cursor when the cursor is
// already parked at that end (the caller treats that as a no-op).
//
// rows stays a parameter rather than reading the constant directly: it is what
// lets the clamp behaviour be tested independently of the per-notch step, so a
// future change to the step cannot silently break the boundary handling.
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
