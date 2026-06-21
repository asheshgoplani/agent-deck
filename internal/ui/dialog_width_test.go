package ui

import (
	"strings"
	"testing"
)

// TestFitDialogWidth pins the shared dialog width clamp: it reproduces the old
// per-dialog behavior on roomy/narrow terminals and, crucially, never lets the
// rendered box (width + border) exceed the terminal — the class of bug where a
// fixed floor (e.g. skill_dialog's 56) overflowed a narrow split pane.
func TestFitDialogWidth(t *testing.T) {
	cases := []struct {
		name                      string
		preferred, minWidth, term int
		want                      int
	}{
		{"roomy uses preferred", 44, 30, 80, 44},
		{"wide caps at preferred", 60, 36, 200, 60},
		{"narrowish shrinks with margin", 44, 30, 50, 40}, // 50-10=40, above the floor
		{"narrow floors at minWidth", 44, 30, 35, 30},     // 35-10=25 -> floor 30 (box 32 fits)
		{"hard cap prevents overflow", 86, 56, 57, 55},    // skill floor 56 would render 58 > 57
		{"tiny terminal", 44, 30, 24, 22},                 // 24-2: box exactly fills the screen
		{"unknown width returns preferred", 44, 30, 0, 44},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fitDialogWidth(c.preferred, c.minWidth, c.term); got != c.want {
				t.Errorf("fitDialogWidth(%d, %d, %d) = %d, want %d", c.preferred, c.minWidth, c.term, got, c.want)
			}
		})
	}

	// Invariant: across every real dialog's (preferred, minWidth) pair and every
	// plausible terminal width, the bordered box must fit on screen. This is the
	// guarantee the hand-rolled per-dialog floors used to violate.
	pairs := [][2]int{{44, 30}, {60, 36}, {64, 50}, {70, 35}, {70, 40}, {86, 56}, {80, 35}}
	for _, p := range pairs {
		for term := 14; term <= 240; term++ {
			got := fitDialogWidth(p[0], p[1], term)
			if got+dialogBorderWidth > term {
				t.Fatalf("overflow: fitDialogWidth(%d, %d, %d) = %d, box %d > term %d",
					p[0], p[1], term, got, got+dialogBorderWidth, term)
			}
		}
	}
}

// TestDialogWidth_RemoteSessionsNotApplicable documents, per the internal/ui
// RemoteSession coverage guideline, that nothing here needs a RemoteSession
// case: fitDialogWidth and centerInScreen are pure layout geometry — they take
// a terminal width and string content and never branch on session type, so
// local vs. remote (SSH) sessions are indistinguishable to them. Mirrors
// TestSessionSwitcher_RemoteSessionsUnsupported's documented-skip convention.
func TestDialogWidth_RemoteSessionsNotApplicable(t *testing.T) {
	t.Skip("not applicable: fitDialogWidth/centerInScreen are session-agnostic geometry helpers")
}

// TestCenterInScreen_MeasuresDisplayCellsNotBytes guards the centering fix:
// content is centered by its display width (cellWidth), not its byte length.
// "日本語" is 6 cells but 9 bytes, so byte-based centering would under-pad it.
func TestCenterInScreen_MeasuresDisplayCellsNotBytes(t *testing.T) {
	const content = "日本語" // 3 wide glyphs => 6 cells, 9 bytes
	if cellWidth(content) != 6 || len(content) != 9 {
		t.Fatalf("precondition failed: cellWidth=%d len=%d", cellWidth(content), len(content))
	}
	out := centerInScreen(content, 20, 1)
	line := strings.Split(out, "\n")[0]
	leading := len(line) - len(strings.TrimLeft(line, " "))
	if want := (20 - 6) / 2; leading != want {
		t.Errorf("leading pad = %d, want %d (centered by cell width, not bytes)", leading, want)
	}
}
