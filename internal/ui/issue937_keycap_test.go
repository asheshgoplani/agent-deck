package ui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Regression tests for the #937 v2 reopen by @jennings.
//
// PR #948 swapped the home.go width/truncate callsites from go-runewidth to
// github.com/charmbracelet/x/ansi on the theory that ansi (uniseg-backed) is
// uniseg grapheme-cluster aware and therefore correctly classifies any
// <codepoint>+U+FE0F sequence as 2 cells. That holds for the four emoji
// @maxfi reported (🏷️ 🛠️ ⚙️ 🗂️) and the unit tests in
// issue937_emoji_vs16_test.go pinned the post-fix contract using ansi.
//
// @jennings re-opened the issue against v1.9.3 (commit 68dba73d) with a
// different class of emoji: keycap sequences such as #️⃣ (U+0023 U+FE0F
// U+20E3) and the plain wide emoji 🔁 (U+1F501) appearing in pane content,
// not just session titles. ansi.StringWidth still reports keycap sequences
// as 1 cell while every terminal we tested renders 2 — so the prior fix
// didn't cover them and the drift survived.
//
// Ground truth: the cell count a real terminal renders, not what any
// width library reports. cellWidth (introduced by this fix) bridges the
// uniseg/terminal disagreement by promoting any grapheme cluster
// containing U+20E3 (COMBINING ENCLOSING KEYCAP) to width 2.

// keycapCases is jennings's reported set plus the full digit keycap family
// and a sanity-check input that mixes a keycap with surrounding ASCII —
// the case that drives the renderNotesSection / truncatePath drift.
var keycapCases = []struct {
	name   string
	in     string
	want   int
	report string
}{
	{"hash_keycap", "#️⃣", 2, "U+0023+VS16+U+20E3 — jennings (#937 v2, 2026-05-13)"},
	{"asterisk_keycap", "*️⃣", 2, "U+002A+VS16+U+20E3 — keycap family"},
	{"zero_keycap", "0️⃣", 2, "U+0030+VS16+U+20E3 — keycap family"},
	{"one_keycap", "1️⃣", 2, "U+0031+VS16+U+20E3 — keycap family"},
	{"nine_keycap", "9️⃣", 2, "U+0039+VS16+U+20E3 — keycap family"},
	{"keycap_in_text", "a#️⃣b", 4, "1 + 2 + 1 — ASCII shoulders, the renderNotesSection case"},
	{"repeat_emoji", "\U0001F501", 2, "U+1F501 — control: emoji-default, no VS16, already wide in ansi"},
}

// Test_Issue937v2_KeycapWidth_MatchesTerminal pins the empirical contract
// that any keycap-bearing grapheme cluster is reported as 2 cells.
// Fails pre-fix (ansi.StringWidth returns 1 for keycap sequences),
// passes post-fix when cellWidth promotes them.
func Test_Issue937v2_KeycapWidth_MatchesTerminal(t *testing.T) {
	for _, tc := range keycapCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cellWidth(tc.in); got != tc.want {
				t.Fatalf(
					"cellWidth(%q) = %d, want %d (%s)\n"+
						"For reference, ansi.StringWidth = %d — the library "+
						"that #948 relied on, which the keycap class slips "+
						"past because uniseg does not classify keycap "+
						"clusters as wide.",
					tc.in, got, tc.want, tc.report, ansi.StringWidth(tc.in),
				)
			}
		})
	}
}

// Test_Issue937v2_AnsiStringWidth_FailsKeycap documents the upstream
// disagreement that motivated cellWidth. If a future ansi (or uniseg)
// release fixes keycap classification, this test starts failing — that's
// the signal to delete cellWidth and route callsites back to ansi.
// Informational; refuses to silently lock keycap to width 1.
func Test_Issue937v2_AnsiStringWidth_FailsKeycap(t *testing.T) {
	for _, tc := range keycapCases {
		if tc.name == "repeat_emoji" {
			continue
		}
		// Inputs in keycapCases that are pure keycap (no ASCII shoulders)
		// should still under-report under ansi.StringWidth.
		if tc.name != "keycap_in_text" {
			if got := ansi.StringWidth(tc.in); got >= tc.want {
				t.Errorf(
					"ansi.StringWidth(%q) = %d, expected < %d — upstream "+
						"may have fixed keycap classification; revisit cellWidth",
					tc.in, got, tc.want,
				)
			}
		}
	}
}

// Test_Issue937v2_CellTruncate_FitsKeycapNote drives a realistic
// renderNotesSection / pane-content callsite: a notes line that contains
// a keycap sequence. cellTruncate must keep the post-truncate output
// inside the requested cell budget, measured by cellWidth (the function
// that mirrors terminal rendering).
//
// Pre-fix: ansi.Truncate("step #️⃣1 done", 8, "...") returns "step #️⃣...",
// which ansi measures at 8 but terminals render at 9 — the cell that
// drifts. Post-fix: cellTruncate measures the cluster as 2 and truncates
// earlier so the output fits in 8 cells.
func Test_Issue937v2_CellTruncate_FitsKeycapNote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
	}{
		{"keycap_in_notes", "step #️⃣1 done", 8},
		{"two_keycaps", "1️⃣ 2️⃣ start", 8},
		{"keycap_at_edge", "tag #️⃣", 5},
		{"keycap_with_repeat", "loop 🔁 #️⃣", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := cellTruncate(tc.in, tc.max, "...")
			if got := cellWidth(out); got > tc.max {
				t.Fatalf(
					"cellTruncate(%q, %d, \"...\") = %q, cellWidth = %d cells; "+
						"want <= %d. Truncation gate is still using width that "+
						"under-counts keycap; oversized output will wrap and "+
						"reproduce #937's per-frame row-offset drift.",
					tc.in, tc.max, out, got, tc.max,
				)
			}
		})
	}
}

// Test_Issue937v2_TruncatePath_FitsKeycapTitle is the integration
// regression: a real production callsite (truncatePath) with a keycap
// title. Pre-fix this returned an output that ansi-measured at maxLen
// but terminal-rendered at maxLen+1 — exactly the drift cell. Post-fix
// the output is correctly measured at maxLen and trimmed to fit.
func Test_Issue937v2_TruncatePath_FitsKeycapTitle(t *testing.T) {
	in := "#️⃣ /Users/foo/keycap-channel"
	const maxLen = 20
	out := truncatePath(in, maxLen)
	if got := cellWidth(out); got > maxLen {
		t.Fatalf(
			"truncatePath(%q, %d) = %q with cellWidth = %d cells; "+
				"want <= %d. truncatePath is still using a width function "+
				"that under-counts keycap sequences, which lets oversized "+
				"titles past the truncation gate and produces #937's drift.",
			in, maxLen, out, got, maxLen,
		)
	}
}
