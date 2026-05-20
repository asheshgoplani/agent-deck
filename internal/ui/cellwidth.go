package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// cellWidth reports the number of terminal cells that s occupies when rendered.
//
// History (#937 v2, @jennings, 2026-05-13). Earlier versions of
// github.com/charmbracelet/x/ansi (and the uniseg grapheme tables beneath it)
// classified keycap sequences such as #️⃣ 0️⃣–9️⃣ *️⃣ — i.e. base + U+FE0F +
// U+20E3 clusters — as 1 cell, while every terminal we tested rendered them
// as 2. cellWidth bridged that gap by promoting any cluster ending in U+20E3
// by one cell on top of ansi.StringWidth.
//
// As of charmbracelet/x/ansi 0.11.7 (PR #1070 dep bump) ansi.StringWidth now
// classifies keycap clusters as 2 cells natively, matching the terminal
// contract pinned by Test_Issue937v2_KeycapWidth_MatchesTerminal. The
// +keycapCount adjustment is no longer needed for measurement and was
// double-counting. cellWidth is now a thin shim over ansi.StringWidth, kept
// to avoid churning the dozen callsites in home.go that route width math
// through it.
func cellWidth(s string) int {
	return ansi.StringWidth(s)
}

// cellTruncate returns a prefix of s whose cellWidth is <= width, appending
// tail (also measured by cellWidth) if any truncation occurred.
//
// Why this is not a one-liner over ansi.Truncate. ansi 0.11.7 fixed
// ansi.StringWidth so keycap clusters are reported as 2 cells, but
// ansi.Truncate's internal cell accounting still measures keycap clusters
// as 1 cell when deciding where to cut. That means ansi.Truncate(s, w, t)
// can return a string whose ansi.StringWidth exceeds w when s contains
// keycap clusters (verified: ansi.Truncate("1️⃣ 2️⃣ start", 8, "...") returns
// "1️⃣ 2️⃣ s..." which ansi.StringWidth measures at 10).
//
// To keep cellWidth(out) <= width, we shrink the budget passed to
// ansi.Truncate by the keycap count of the input. Worst case every keycap
// survives the truncation and each costs +1 cell vs ansi.Truncate's
// internal measurement, so:
//
//	  cellWidth(out)
//	= ansi.StringWidth(out)
//	= ansi_truncate_internal(out) + keycapCount(out)
//	<= (width - keycapCount(s)) + keycapCount(out)
//	<= width                       [since keycapCount(out) <= keycapCount(s)]
//
// ansi.Truncate already handles ANSI escape sequence boundaries — never cuts
// mid-CSI, preserves SGR state through the visible prefix, and skips
// invisible bytes when budgeting — which keeps the #699 SGR-bleed
// invariant intact. The adjustment above is purely additive on top of that.
func cellTruncate(s string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	if cellWidth(s) <= width {
		return s
	}
	n := keycapCount(s)
	if n == 0 {
		return ansi.Truncate(s, width, tail)
	}
	adj := width - n
	// If shrinking the budget would push tail off the right edge,
	// drop the tail. cellWidth(out) <= width still holds because
	// ansi.Truncate keeps its own internal-width(out) <= width, and
	// out contains at most n keycaps. The output may not exactly
	// fill the budget in that edge case, but it will never exceed it.
	if adj < cellWidth(tail) {
		return ansi.Truncate(s, width, "")
	}
	return ansi.Truncate(s, adj, tail)
}

// keycapCount returns the number of extended grapheme clusters in s that end
// with U+20E3 (COMBINING ENCLOSING KEYCAP). Used by cellTruncate to shrink
// the budget passed to ansi.Truncate — ansi.Truncate's internal cell
// accounting under-counts keycap clusters by exactly 1 each (see
// cellTruncate doc).
//
// ANSI escape sequences are stripped before the grapheme walk so they
// neither inflate the cluster count nor split a keycap cluster across an
// escape boundary.
//
// Fast-path: a keycap cluster always contains U+20E3 verbatim, so we can
// skip the full grapheme walk when that codepoint is absent.
func keycapCount(s string) int {
	if !strings.ContainsRune(s, 0x20E3) {
		return 0
	}
	g := uniseg.NewGraphemes(ansi.Strip(s))
	n := 0
	for g.Next() {
		runes := g.Runes()
		if len(runes) > 0 && runes[len(runes)-1] == 0x20E3 {
			n++
		}
	}
	return n
}
