package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// cellWidth reports the number of terminal cells that s occupies when
// rendered, bridging a gap between Unicode grapheme-cluster width tables
// and what real terminals draw.
//
// Why this exists. The codebase ships through github.com/charmbracelet/x/ansi
// (uniseg-backed) for grapheme-aware width measurement. ansi.StringWidth
// correctly classifies <codepoint>+U+FE0F sequences such as 🏷️ 🛠️ ⚙️ 🗂️ as
// 2 cells — that's the contract issue #937 / PR #948 pinned. It does *not*
// classify "keycap" sequences ( base + U+FE0F + U+20E3, e.g. #️⃣ 0️⃣–9️⃣ *️⃣ )
// as wide; uniseg reports them at 1 cell. Every terminal we test (Ghostty,
// Terminal.app, iTerm2, Warp, Termius) renders 2 cells for those clusters,
// so the agent-deck row layout drifts by one cell per keycap glyph in the
// pane content @jennings re-opened #937 against.
//
// cellWidth walks s as a sequence of extended grapheme clusters and, for
// any cluster that contains U+20E3 (COMBINING ENCLOSING KEYCAP), promotes
// the cluster width to max(2, uniseg.Width()). All other clusters are
// reported at their uniseg width, which keeps cellWidth a strict superset
// of ansi.StringWidth's behavior for non-keycap input.
func cellWidth(s string) int {
	if s == "" {
		return 0
	}
	g := uniseg.NewGraphemes(s)
	total := 0
	for g.Next() {
		w := g.Width()
		if w < 2 && clusterIsKeycap(g.Runes()) {
			w = 2
		}
		total += w
	}
	return total
}

// cellTruncate is the truncation analog of cellWidth: it returns the longest
// prefix of s whose cellWidth is <= width, appending tail (also measured by
// cellWidth) if any truncation occurred. Used at the same callsites as
// cellWidth so the width measurement and the truncation gate agree on
// keycap sequences and the layout cannot drift past the cell budget.
func cellTruncate(s string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	if cellWidth(s) <= width {
		return s
	}
	tailW := cellWidth(tail)
	budget := width - tailW
	if budget <= 0 {
		// Tail alone already overruns the budget; degrade to ansi.Truncate
		// without a tail. cellWidth(out) == width worst case.
		return ansi.Truncate(s, width, "")
	}
	var b strings.Builder
	b.Grow(len(s))
	g := uniseg.NewGraphemes(s)
	used := 0
	for g.Next() {
		w := g.Width()
		if w < 2 && clusterIsKeycap(g.Runes()) {
			w = 2
		}
		if used+w > budget {
			break
		}
		b.WriteString(string(g.Runes()))
		used += w
	}
	b.WriteString(tail)
	return b.String()
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
