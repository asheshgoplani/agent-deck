package ui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// Regression tests for #937 — Session titles AND pane content with
// emoji+VS16 (e.g. 🏷️ 🛠️ ⚙️ 🗂️) cause per-frame row-offset drift in the
// agent-deck Bubble Tea TUI.
//
// Root cause: go-runewidth and terminal emulators disagree on the cell
// width of <codepoint>+U+FE0F (Variation Selector 16) sequences.
// runewidth reports width 1; terminals (Ghostty, Terminal.app, Warp,
// Termius) render 2 cells. This is a stable, cross-terminal desync.
//
// Fix: replace the remaining `runewidth.StringWidth` / `runewidth.Truncate`
// callsites in internal/ui/home.go with `ansi.StringWidth` /
// `ansi.Truncate` (uniseg grapheme-cluster aware, same family as
// `lipgloss.Width` which this codebase already uses elsewhere — see the
// comment at internal/ui/home.go in `ensureExactWidth`).
//
// Reporters: maxfi (#937 original), jennings (scope expansion to pane
// content).

// vs16Cases pairs each VS16-suffixed emoji string with the cell count
// that real terminals render it at. runewidth disagrees with every
// non-control entry; ansi.StringWidth agrees with all.
var vs16Cases = []struct {
	name   string
	in     string
	want   int
	report string
}{
	{"label_vs16", "🏷️", 2, "U+1F3F7 + VS16 — maxfi (#937, 2026-04-17)"},
	{"hammer_wrench_vs16", "🛠️", 2, "U+1F6E0 + VS16 — maxfi (#937, 2026-05-11)"},
	{"gear_vs16", "⚙️", 2, "U+2699 + VS16 — example in #937 summary"},
	{"card_index_vs16", "🗂️", 2, "U+1F5C2 + VS16 — example in #937 summary"},
	{"label_vs16_with_text", "🏷️ session", 10, "title prefix + ASCII suffix"},
	{"emoji_default_robot", "🤖", 2, "control: emoji-default presentation, no VS16"},
	{"plain_ascii", "hello", 5, "control: ASCII baseline"},
}

// Test_Issue937_RunewidthDesync_Documented captures the empirical desync
// in go-runewidth that motivates the swap. Informational — it asserts
// the *bug* in the upstream library; if a future runewidth release fixes
// VS16, this test fails and the shim becomes redundant.
func Test_Issue937_RunewidthDesync_Documented(t *testing.T) {
	for _, tc := range vs16Cases {
		if tc.name == "emoji_default_robot" || tc.name == "plain_ascii" {
			continue
		}
		if got := runewidth.StringWidth(tc.in); got >= tc.want {
			t.Errorf(
				"runewidth.StringWidth(%q) = %d, expected < %d — upstream "+
					"may have fixed VS16; revisit the ansi.StringWidth shim",
				tc.in, got, tc.want,
			)
		}
	}
}

// Test_Issue937_AnsiStringWidth_HandlesVS16 locks in that the width
// function used by the fixed call sites (ansi.StringWidth) agrees with
// terminal rendering for every <codepoint>+VS16 sequence reported in
// #937. If a future refactor swaps the sites back to runewidth, this
// test fails and surfaces row-offset drift before it ships.
func Test_Issue937_AnsiStringWidth_HandlesVS16(t *testing.T) {
	for _, tc := range vs16Cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ansi.StringWidth(tc.in); got != tc.want {
				t.Fatalf(
					"ansi.StringWidth(%q) = %d, want %d (%s)",
					tc.in, got, tc.want, tc.report,
				)
			}
		})
	}
}

// Test_Issue937_TruncatePath_FitsVS16Title exercises a real production
// home.go function with a VS16-bearing input and asserts the post-
// truncate output fits inside the requested cell budget, as measured by
// the same uniseg-aware function the terminal uses.
//
// Pre-fix: truncatePath called runewidth.StringWidth, which reported
// width 20 for the input below — equal to maxLen, so no truncation
// happened. The caller then printed 21 cells into a 20-cell slot, the
// row wrapped, and subsequent lines drifted down by one row per
// offending title. Post-fix (ansi.StringWidth + ansi.Truncate), the same
// input is correctly measured at 21 and truncated to fit.
func Test_Issue937_TruncatePath_FitsVS16Title(t *testing.T) {
	in := "🏷️ /Users/foo/project" // ansi width = 21, runewidth = 20
	const maxLen = 20
	out := truncatePath(in, maxLen)
	if got := ansi.StringWidth(out); got > maxLen {
		t.Fatalf(
			"truncatePath(%q, %d) = %q with ansi.StringWidth = %d cells; "+
				"want <= %d. The function is still measuring width with "+
				"go-runewidth, which under-counts emoji+VS16 sequences by "+
				"1 cell and lets oversized output past the truncation gate, "+
				"producing #937's row-offset drift.",
			in, maxLen, out, got, maxLen,
		)
	}
}
