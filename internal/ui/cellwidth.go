package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// cellWidth reports the number of terminal cells that s occupies when rendered,
// bridging a gap between the uniseg grapheme-cluster width table and what real
// terminals draw — while preserving ANSI-escape-aware measurement.
//
// Why this exists. The codebase ships through github.com/charmbracelet/x/ansi
// (uniseg-backed) for grapheme-aware width measurement. ansi.StringWidth
// correctly classifies <codepoint>+U+FE0F sequences such as 🏷️ 🛠️ ⚙️ 🗂️ as
// 2 cells — that's the contract issue #937 / PR #948 pinned — and it skips
// ANSI escape sequences (SGR, OSC, CSI) so styled content measures by visible
// width, not byte length. ansi.StringWidth does *not* classify "keycap"
// sequences ( base + U+FE0F + U+20E3, e.g. #️⃣ 0️⃣–9️⃣ *️⃣ ) as wide; uniseg
// reports them at 1 cell. Every terminal we test (Ghostty, Terminal.app,
// iTerm2, Warp, Termius) renders 2 cells for those clusters, so the
// agent-deck row layout drifts by one cell per keycap glyph in the pane
// content @jennings re-opened #937 against.
//
// Implementation: ansi.StringWidth(s) gives the ANSI-aware base width;
// keycapCount(s) walks the ANSI-stripped string and counts the grapheme
// clusters ending in U+20E3 (COMBINING ENCLOSING KEYCAP). Each such
// cluster contributes one extra cell beyond what uniseg reported, so the
// total visual width is base + keycapCount.
//
// Critically this preserves ANSI escape sequence boundaries — earlier
// drafts walked the raw input as uniseg clusters, which broke
// "\x1b[43m..." into ESC + [ + 4 + 3 + m as separate clusters with
// non-zero width. That over-counted invisible bytes AND let a downstream
// truncation cut mid-escape, leaving malformed SGR state and bleeding
// highlight into the next row (regression caught by the #699 SGR-bleed
// behavioral eval).
func cellWidth(s string) int {
	if s == "" {
		return 0
	}
	return ansi.StringWidth(s) + keycapCount(s)
}

// cellTruncate is the truncation analog of cellWidth: it returns a prefix of s
// whose cellWidth is <= width, appending tail (also measured by cellWidth) if
// any truncation occurred.
//
// Implementation: ansi.Truncate already handles ANSI escape sequence
// boundaries — it never cuts mid-CSI, preserves SGR state through the visible
// prefix, and skips invisible bytes when budgeting. cellTruncate adjusts the
// width argument it passes to ansi.Truncate by the keycap count of the input,
// so the output's cellWidth ( = ansi.StringWidth(out) + keycapCount(out) )
// cannot exceed the requested width:
//
//	  cellWidth(out)
//	= ansi.StringWidth(out) + keycapCount(out)
//	<= (width - keycapCount(s)) + keycapCount(out)
//	<= width                    [since keycapCount(out) <= keycapCount(s)]
//
// The adjustment is conservative — if every keycap in s gets truncated away,
// cellWidth(out) can be up to keycapCount(s) below width. That under-fill is
// the safety margin against the "keycap kept after truncation" case, where
// each kept keycap costs +1 cell.
//
// Tail width is treated identically: ansi.Truncate already subtracts its
// own measurement of tail (ansi.StringWidth), and the floor below guards
// against tail-width > budget, which would make ansi.Truncate emit only
// the tail and return a string whose cellWidth could exceed `width` if
// the tail itself contained keycap glyphs (in practice it does not — our
// callers use "..." / "…" — but the floor keeps the contract honest).
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
	// If the bonus is so large that adj would push tail off the right
	// edge, fall back to ansi.Truncate without a tail and let the caller
	// decide. cellWidth(out) <= width still holds because ansi.Truncate
	// keeps ansi.StringWidth(out) <= width and out may contain at most
	// n keycaps — but if both are at max, the inequality is loose.
	if adj < cellWidth(tail) {
		return ansi.Truncate(s, width, "")
	}
	return ansi.Truncate(s, adj, tail)
}

// keycapCount returns the number of extended grapheme clusters in s that end
// with U+20E3 (COMBINING ENCLOSING KEYCAP) — i.e. keycap emoji clusters that
// uniseg under-counts as 1 cell. ANSI escape sequences are stripped before
// the grapheme walk so they neither inflate the cluster count nor split a
// keycap cluster across an escape boundary.
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
		if clusterIsKeycap(g.Runes()) {
			n++
		}
	}
	return n
}

// clusterIsKeycap reports whether the rune sequence ends with the
// COMBINING ENCLOSING KEYCAP (U+20E3), which marks the cluster as a
// keycap sequence (e.g. #️⃣ 0️⃣–9️⃣ *️⃣). The base codepoint and the
// optional U+FE0F variation selector are not validated; uniseg has
// already grouped them into one extended grapheme cluster, so any
// cluster ending in U+20E3 is by construction a keycap glyph that
// terminals render at 2 cells.
func clusterIsKeycap(runes []rune) bool {
	if len(runes) == 0 {
		return false
	}
	return runes[len(runes)-1] == 0x20E3
}
