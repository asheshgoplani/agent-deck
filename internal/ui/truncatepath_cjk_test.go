package ui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncatePath_WideRunesFitWidth: truncatePath measures display width but
// used to keep maxLen/3 runes at the start and maxLen*2/3-3 runes at the end.
// For double-width runes (CJK) that is twice the columns, so a 40-rune CJK
// path cut to 30 came back 57 columns wide and wrapped the preview pane.
func TestTruncatePath_WideRunesFitWidth(t *testing.T) {
	for _, tc := range []struct {
		in     string
		maxLen int
	}{
		{strings.Repeat("项", 40), 30},
		{"/home/用户/文档/项目/" + strings.Repeat("代码", 20), 40},
		{"/Users/foo/" + strings.Repeat("目录", 30) + "/end", 25},
	} {
		out := truncatePath(tc.in, tc.maxLen)
		if got := cellWidth(out); got > tc.maxLen || !utf8.ValidString(out) || !strings.Contains(out, "...") {
			t.Errorf("truncatePath(%q, %d) = %q, %d columns; want a cut path of at most %d columns",
				tc.in, tc.maxLen, out, got, tc.maxLen)
		}
	}
}

// TestTruncatePath_ASCIIUnchanged pins the ASCII output: start, "...", end.
func TestTruncatePath_ASCIIUnchanged(t *testing.T) {
	for _, tc := range []struct {
		in     string
		maxLen int
		want   string
	}{
		{"/Users/foo/short", 30, "/Users/foo/short"},
		{"/Users/someone/projects/agent-deck/internal/ui", 30, "/Users/som...-deck/internal/ui"},
		{"/a/very/long/path/to/some/project", 20, "/a/ver...me/project"},
	} {
		if got := truncatePath(tc.in, tc.maxLen); got != tc.want {
			t.Errorf("truncatePath(%q, %d) = %q, want %q", tc.in, tc.maxLen, got, tc.want)
		}
	}
}

// TestTruncatePath_KeycapsAndClustersFitWidth: cellTruncate shrank its budget
// by every keycap in the string, not only the kept ones, and fell back to an
// unshrunk cut, so a run of keycaps came back up to twice maxLen. The end was
// cut per code point, so a stray U+FE0F or keycap mark could join the "..."
// and add a column.
func TestTruncatePath_KeycapsAndClustersFitWidth(t *testing.T) {
	for _, tc := range []struct {
		in     string
		maxLen int
	}{
		{strings.Repeat("1️⃣", 30), 20},
		{strings.Repeat("1️⃣", 30), 30},
		{"/home/user/#️⃣/1️⃣", 12},
		{"/home/user/❤️bbbbb", 12},         // ❤️ lands on the cut
		{"/home/user/aa❤️bbbbbbbbbbb", 21}, // ❤️ lands on the cut
	} {
		out := truncatePath(tc.in, tc.maxLen)
		if got := cellWidth(out); got > tc.maxLen || !utf8.ValidString(out) {
			t.Errorf("truncatePath(%q, %d) = %q, %d columns; want at most %d",
				tc.in, tc.maxLen, out, got, tc.maxLen)
		}
	}
}

// TestCellTruncate_CountsOnlyKeptKeycaps: 30 keycaps cut to 6 cells keep 3.
func TestCellTruncate_CountsOnlyKeptKeycaps(t *testing.T) {
	in := strings.Repeat("1️⃣", 30)
	for _, w := range []int{1, 2, 6, 20} {
		out := cellTruncate(in, w, "")
		if got, want := cellWidth(out), w/2*2; got != want {
			t.Errorf("cellTruncate(30 keycaps, %d) = %q, %d columns; want %d", w, out, got, want)
		}
	}
}
