package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Dialog height fitting (the vertical twin of fitDialogWidth).
//
// A dialog that is taller than the terminal used to be centred with
// lipgloss.Place / centerInScreen anyway, which cut off its top border, its
// title and its first rows at 80x24. renderFittedDialog keeps every dialog
// inside the screen: a dialog that fits renders byte-for-byte as before; one
// that does not first gives back its vertical padding, then scrolls its body
// inside the box so the focused section stays visible, with the head (title)
// and the foot (key hints) pinned. Scrolling follows the dialog's own focus,
// so no key changes.

const (
	dialogMoreAbove = "▲ more above"
	dialogMoreBelow = "▼ more below"
)

// dialogSections is a dialog's content split at line boundaries. The full
// content is every element joined with "\n", in order head, body, foot.
// Each element may span several lines. focus indexes body (the element that
// must stay visible); a negative focus keeps the top of the body.
type dialogSections struct {
	head  []string
	body  []string
	focus int
	foot  []string
}

// content is the unfitted dialog content, identical to what the dialog
// rendered before it was split into sections.
func (s dialogSections) content() string {
	all := make([]string, 0, len(s.head)+len(s.body)+len(s.foot))
	all = append(all, s.head...)
	all = append(all, s.body...)
	all = append(all, s.foot...)
	return strings.Join(all, "\n")
}

// fitDialogHeight returns how many content rows a box drawn with style may
// use so the whole box (border, padding and margins included) fits within
// termHeight rows. termHeight <= 0 (unknown) disables the limit and returns 0.
func fitDialogHeight(style lipgloss.Style, termHeight int) int {
	if termHeight <= 0 {
		return 0
	}
	return max(termHeight-style.GetVerticalFrameSize(), 1)
}

// renderFittedDialog renders s inside style (which carries the dialog's
// Width) so the box is never taller than termHeight. In order:
//  1. the box fits: rendered exactly as style.Render(s.content());
//  2. the vertical padding shrinks to 1, then to 0;
//  3. the body scrolls inside the box around s.focus.
func renderFittedDialog(style lipgloss.Style, termHeight int, s dialogSections) string {
	box := style.Render(s.content())
	if termHeight <= 0 || lipgloss.Height(box) <= termHeight {
		return box
	}
	top, bottom := style.GetPaddingTop(), style.GetPaddingBottom()
	for _, pad := range []int{1, 0} {
		if top <= pad && bottom <= pad {
			continue
		}
		top, bottom = min(top, pad), min(bottom, pad)
		style = style.PaddingTop(top).PaddingBottom(bottom)
		box = style.Render(s.content())
		if lipgloss.Height(box) <= termHeight {
			return box
		}
	}
	return style.Render(fitDialogRows(dialogWrapWidth(style), fitDialogHeight(style, termHeight), s))
}

// dialogWrapWidth is the width lipgloss wraps a style's content at: its
// Width less the horizontal padding (0 when the style sets no width).
func dialogWrapWidth(style lipgloss.Style) int {
	w := style.GetWidth()
	if w <= 0 {
		return 0
	}
	return max(w-style.GetHorizontalPadding(), 1)
}

// fitDialogRows returns s's content as at most maxRows rows when wrapped at
// wrapWidth (0 = no wrapping): head and foot pinned, the body windowed so the
// focused element is visible, with "▲ more above" / "▼ more below" taking
// the edge rows of the window. Content that already fits is returned as is.
// When the pinned rows leave too little room, squeezeDialogPins gives rows
// back from the head first, so the key hints and the focused row stay.
func fitDialogRows(wrapWidth, maxRows int, s dialogSections) string {
	full := s.content()
	if maxRows <= 0 {
		return full
	}
	wrap := func(elems []string) (rows []string, starts []int) {
		for _, e := range elems {
			starts = append(starts, len(rows))
			if wrapWidth > 0 {
				e = lipgloss.NewStyle().Width(wrapWidth).Render(e)
			}
			rows = append(rows, strings.Split(e, "\n")...)
		}
		return rows, starts
	}
	head, _ := wrap(s.head)
	body, starts := wrap(s.body)
	foot, _ := wrap(s.foot)
	if len(head)+len(body)+len(foot) <= maxRows {
		return full
	}
	// The focused element spans rows [first, last] of the body.
	first, last := 0, 0
	if s.focus >= 0 && s.focus < len(starts) {
		first = starts[s.focus]
		last = len(body) - 1
		if s.focus+1 < len(starts) {
			last = starts[s.focus+1] - 1
		}
	}
	// Ask for the whole focused element between the two scroll indicators.
	head, foot = squeezeDialogPins(head, foot, maxRows, min(max(3, last-first+3), len(body)))
	budget := maxRows - len(head) - len(foot)
	rows := append([]string{}, head...)
	if budget < 3 {
		// Too short for the scroll indicators: the focused rows alone.
		start := max(min(first, len(body)-budget), 0)
		rows = append(rows, body[start:min(start+max(budget, 0), len(body))]...)
		rows = append(rows, foot...)
		// Only a frame smaller than title + hint + one row gets here with
		// too many rows; the hints at the bottom are kept.
		return strings.Join(rows[max(len(rows)-maxRows, 0):], "\n")
	}
	window := dialogWindow(len(body), budget, first, last)
	window = snapDialogWindow(window, len(body), first, starts)

	// The rows of an element cut at the top of the window (the tail of a
	// wrapped line) are left blank rather than shown out of context.
	cutUntil := 0
	if window.start > 0 {
		cutUntil = len(body)
		for _, st := range starts {
			if st > window.start {
				cutUntil = st
				break
			}
		}
	}
	indicator := lipgloss.NewStyle().Foreground(ColorComment)
	for i := window.start; i < window.end; i++ {
		switch {
		case i == window.start && window.start > 0:
			rows = append(rows, indicator.Render(dialogMoreAbove))
		case i == window.end-1 && window.end < len(body):
			rows = append(rows, indicator.Render(dialogMoreBelow))
		case i < cutUntil:
			rows = append(rows, "")
		default:
			rows = append(rows, body[i])
		}
	}
	rows = append(rows, foot...)
	return strings.Join(rows, "\n")
}

// squeezeDialogPins removes pinned rows until want body rows fit in maxRows,
// least useful first: blank head rows (bottom up), blank foot rows, then, if
// even one body row does not fit, the head's other rows from the bottom up.
// The head's first row (the title) goes only when nothing else is left, and
// the foot's last row (the key hints) is never removed, so Esc and the
// focused row stay on screen.
func squeezeDialogPins(head, foot []string, maxRows, want int) ([]string, []string) {
	head, foot = append([]string{}, head...), append([]string{}, foot...)
	fits := func(n int) bool { return len(head)+len(foot)+n <= maxRows }
	blank := func(row string) bool { return strings.TrimSpace(stripAnsi(row)) == "" }
	for i := len(head) - 1; i > 0 && !fits(want); i-- {
		if blank(head[i]) {
			head = append(head[:i], head[i+1:]...)
		}
	}
	for i := len(foot) - 2; i >= 0 && !fits(want); i-- {
		if blank(foot[i]) {
			foot = append(foot[:i], foot[i+1:]...)
		}
	}
	want = min(want, 1)
	for len(head) > 1 && !fits(want) {
		head = head[:len(head)-1]
	}
	for len(foot) > 1 && !fits(want) {
		foot = foot[1:]
	}
	if !fits(want) {
		head = nil
	}
	return head, foot
}

type dialogRowWindow struct{ start, end int }

// dialogWindow picks budget consecutive rows out of total so the rows
// [first, last] are visible together with one row of context below them.
// The window's first row becomes "▲ more above" when start > 0 and its last
// row "▼ more below" when end < total, so neither may hold focused rows.
func dialogWindow(total, budget, first, last int) dialogRowWindow {
	if total <= budget {
		return dialogRowWindow{0, total}
	}
	maxStart := total - budget
	// Smallest start keeping last+1 (context) above the "more below" row.
	start := min(max(last+1-(budget-2), 0), maxStart)
	// A focused element taller than the window shows its top.
	if start > 0 && first < start+1 {
		start = max(first-1, 0)
	}
	return dialogRowWindow{start, start + budget}
}

// snapDialogWindow moves a scrolled window forward so the first row under
// "▲ more above" starts a body element instead of showing the tail of a
// wrapped one, as long as the focused rows (from first) and the window
// still fit.
func snapDialogWindow(win dialogRowWindow, total, first int, starts []int) dialogRowWindow {
	if win.start == 0 {
		return win
	}
	budget := win.end - win.start
	for _, s := range starts {
		if s < win.start+1 {
			continue
		}
		if s > first || s-1+budget > total {
			break
		}
		return dialogRowWindow{s - 1, s - 1 + budget}
	}
	return win
}

// renderDialogFooter joins key hints with sep on one row of at most width
// cells, so a dialog footer never wraps mid-item. It is
// renderDialogFooterRows with one row.
func renderDialogFooter(width int, sep string, items []string, drop ...int) string {
	return renderDialogFooterRows(width, 1, sep, items, drop...)
}

// renderDialogFooterRows lays key hints out on at most maxRows rows of at
// most width cells, breaking only between items (rows are balanced, in item
// order), so a footer never wraps mid-item. Only when the hints do not fit
// in maxRows rows are items dropped, in the order drop lists them (indexes
// into items, least important first). Items not listed are never dropped:
// if they still do not fit they take extra rows rather than being cut, and
// only a single item wider than width is clipped with "…". A footer that
// fits on one row is exactly strings.Join(items, sep); width <= 0 disables
// fitting. Rows after the first keep the first item's leading indent.
func renderDialogFooterRows(width, maxRows int, sep string, items []string, drop ...int) string {
	row := strings.Join(items, sep)
	if width <= 0 || lipgloss.Width(row) <= width {
		return row
	}
	indent := ""
	if len(items) > 0 {
		indent = items[0][:len(items[0])-len(strings.TrimLeft(items[0], " "))]
	}
	kept := items
	dropped := make(map[int]bool, len(drop))
	for stage := 0; ; stage++ {
		if rows, ok := packDialogFooter(width, maxRows, sep, indent, kept); ok {
			return strings.Join(rows, "\n")
		}
		if stage == len(drop) {
			break
		}
		dropped[drop[stage]] = true
		kept = make([]string, 0, len(items))
		for i, item := range items {
			if !dropped[i] {
				kept = append(kept, item)
			}
		}
	}
	rows, _ := packDialogFooter(width, len(kept), sep, indent, kept)
	for i, r := range rows {
		rows[i] = clipDialogCells(r, width)
	}
	return strings.Join(rows, "\n")
}

// packDialogFooter splits items into the fewest rows (at most maxRows) that
// fit width, balancing the rows' widths. ok is false when they do not fit;
// rows is then the best split into maxRows rows.
func packDialogFooter(width, maxRows int, sep, indent string, items []string) (rows []string, ok bool) {
	join := func(part []string, first bool) string {
		r := strings.Join(part, sep)
		if !first {
			r = indent + r
		}
		return r
	}
	var best []string
	bestWidth := -1
	// split returns the balanced rows of items[from:] in n rows.
	var split func(from, n int, acc []string)
	split = func(from, n int, acc []string) {
		if n == 1 {
			candidate := append(append([]string{}, acc...), join(items[from:], from == 0))
			w := 0
			for _, r := range candidate {
				w = max(w, lipgloss.Width(r))
			}
			if bestWidth < 0 || w < bestWidth {
				best, bestWidth = candidate, w
			}
			return
		}
		for end := from + 1; end <= len(items)-n+1; end++ {
			split(end, n-1, append(acc, join(items[from:end], from == 0)))
		}
	}
	for n := 1; n <= min(maxRows, len(items)); n++ {
		best, bestWidth = nil, -1
		split(0, n, nil)
		if bestWidth <= width {
			return best, true
		}
	}
	return best, false
}

// clipDialogCells truncates plain text to width cells, ending in "…".
func clipDialogCells(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if lipgloss.Width(b.String()+string(r)) > width-1 {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "…"
}
