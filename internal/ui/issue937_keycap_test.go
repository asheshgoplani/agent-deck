package ui

import (
	"strings"
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

// Test_Issue937v2_CellTruncate_PreservesAnsiBoundariesAndKeycap is the joint
// regression for the two failure modes that cellTruncate must handle at the
// same time:
//
//  1. **Keycap width (#937 v2 — @jennings):** an input whose visible cell
//     count is greater than ansi.StringWidth's count by exactly the number
//     of keycap clusters it contains. The output's cellWidth must fit
//     inside the requested cell budget.
//
//  2. **ANSI-escape boundaries (#699 — @javierciccarelli):** an input
//     containing SGR escape sequences (e.g. "\x1b[43m...") must never have
//     its escape sequence cut mid-byte by truncation, and the output must
//     either not contain "\x1b" at all OR carry an exposed "\x1b" only as
//     part of a complete escape sequence that the downstream renderPreview
//     pane #699 reset can safely close.
//
// An earlier draft of cellTruncate walked the raw bytes through uniseg, which
// split "\x1b[43m" into five clusters with non-zero width, both over-counting
// invisible bytes AND letting a truncate land between the ESC and the rest of
// the CSI sequence — producing dangling/partial SGR state that the row-end
// reset at internal/ui/home.go:13407 (the #699 fix) could not safely close,
// re-introducing the bleed eval failure (TestEval_FullViewDoesNotLeakSGRAcrossRows_Issue699).
//
// Post-fix: cellTruncate delegates to ansi.Truncate (which is escape-aware)
// with a budget adjusted by the input's keycap count. The output preserves
// every ANSI escape boundary AND keeps cellWidth(out) within the budget.
//
// Each row of this test pins one combination of {has keycap, has ANSI, has
// both, is truncated, fits whole}. The two invariants checked per row are:
//   - cellWidth(out) <= budget    (keycap-aware width gate)
//   - no half-escape:             (every "\x1b" is followed by a complete CSI/SGR sequence)
func Test_Issue937v2_CellTruncate_PreservesAnsiBoundariesAndKeycap(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		budget int
	}{
		{"styled_only_truncated", "\x1b[43m> tell me about ghostty", 10},
		{"styled_keycap_truncated", "\x1b[43m#️⃣ tell me about ghostty", 10},
		{"styled_keycap_fits", "\x1b[43m#️⃣ tag", 10},
		{"styled_two_keycaps_truncated", "\x1b[41m1️⃣ 2️⃣ 3️⃣ go", 6},
		{"styled_only_fits", "\x1b[43mhi", 10},
		{"keycap_no_style_truncated", "step #️⃣1 done", 6},
		{"reset_in_middle", "\x1b[43mhi\x1b[0m there", 6},
		{"reset_keycap_mix", "\x1b[43m#️⃣\x1b[0m kc", 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := cellTruncate(tc.in, tc.budget, "...")

			// Invariant 1: cellWidth fits the budget.
			if got := cellWidth(out); got > tc.budget {
				t.Fatalf(
					"cellTruncate(%q, %d, \"...\") = %q has cellWidth = %d, want <= %d "+
						"— keycap-aware width gate broke.",
					tc.in, tc.budget, out, got, tc.budget,
				)
			}

			// Invariant 2: no half-escape. Every ESC byte must be followed by
			// the start of a CSI/OSC sequence and a terminator within `out`.
			// We walk the string the same way the #699 sgrActiveAt helper
			// does: a CSI sequence is "\x1b[" ... final-byte in 0x40..0x7e.
			// (We don't validate OSC here — preview pane content uses SGR.)
			for i := 0; i < len(out); i++ {
				if out[i] != 0x1b {
					continue
				}
				if i+1 >= len(out) {
					t.Fatalf(
						"cellTruncate(%q, %d, \"...\") = %q ends with bare ESC "+
							"(byte %d) — escape sequence was truncated mid-CSI and "+
							"will bleed SGR state into the next row (#699).",
						tc.in, tc.budget, out, i,
					)
				}
				if out[i+1] != '[' {
					t.Fatalf(
						"cellTruncate(%q, %d, \"...\") = %q has ESC at byte %d "+
							"followed by %q, not '[' — escape sequence is malformed.",
						tc.in, tc.budget, out, i, string(out[i+1]),
					)
				}
				// Find CSI terminator.
				j := i + 2
				for j < len(out) && !(out[j] >= 0x40 && out[j] <= 0x7e) {
					j++
				}
				if j >= len(out) {
					t.Fatalf(
						"cellTruncate(%q, %d, \"...\") = %q has CSI starting at "+
							"byte %d with no terminator — escape sequence was cut "+
							"mid-parameter list (#699 cause).",
						tc.in, tc.budget, out, i,
					)
				}
				i = j
			}

			// Invariant 3 (belt-and-suspenders): if input contains keycap and
			// the output drops it entirely, the kept ASCII prefix should
			// reflect that — sanity-check that truncation produced a sensible
			// non-empty result when budget allows.
			if tc.budget > cellWidth("...") && out == "" {
				t.Fatalf(
					"cellTruncate(%q, %d, \"...\") returned empty for non-zero budget — "+
						"adjust-by-keycap floor logic over-clamped.",
					tc.in, tc.budget,
				)
			}

			// Defensive: when the input contains "\x1b", the output that
			// reaches renderPreviewPane's #699 post-fix at home.go:13407
			// must STILL contain "\x1b" so that the ContainsRune-gated
			// "\x1b[0m" reset still fires. cellTruncate must not strip
			// ANSI escape sequences silently.
			if strings.ContainsRune(tc.in, 0x1b) && !strings.ContainsRune(out, 0x1b) && out != "..." {
				t.Fatalf(
					"cellTruncate(%q, %d, \"...\") = %q dropped all ESC bytes "+
						"from styled input — the #699 row-end reset will not fire "+
						"and SGR state will leak from the input's styled prefix.",
					tc.in, tc.budget, out,
				)
			}
		})
	}
}
