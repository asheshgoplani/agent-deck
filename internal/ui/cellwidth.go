package ui

import (
	"strings"
	"unicode/utf8"

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

// fitCellWidth returns s truncated or space-padded so it occupies exactly width
// terminal cells, measured by cellWidth (ansi cells, keycap-aware).
//
// Used by clampViewToViewport on the final, already-joined frame so every row
// fully overwrites the previous frame's row on incremental redraw. Without the
// pad, when a shorter line replaces a longer one the terminal keeps the stale
// trailing glyphs — the iTerm2 "ghost line" artifact on session-list scroll
// (#607 row-offset drift class). Truncation reuses cellTruncate so keycap
// clusters (#937) are cut at their true 2-cell width.
//
// This stays on cellWidth deliberately: clampViewToViewport runs AFTER
// lipgloss.JoinHorizontal, so it is a terminal-cell safety net, not part of the
// JoinHorizontal width-measurement path. The pre-join equalizer ensureExactWidth
// must NOT use cellWidth — it has to agree with lipgloss.Width (see #182).
func fitCellWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := cellWidth(s)
	if w > width {
		return cellTruncate(s, width, "")
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// terminalSafeWidth reports the most cells s can occupy on any terminal the
// deck may be drawn on, whichever width convention that terminal follows
// (#2334).
//
// cellWidth counts a grapheme cluster by its base (Claude Code's
// Bun.stringWidth agrees). Terminals disagree: xterm, glibc-built tmux and
// Ghostty's legacy mode advance per code point, giving every Indic spacing
// vowel sign (ा ि ी ो, Unicode category Mc) its own cell and drawing the
// parts of a ZWJ emoji or a jamo-spelled Hangul syllable side by side, and
// Ghostty 1.3's default unicode mode widens a cluster to 2 cells whenever a
// vowel sign or a second consonant joins it. A Devanagari reply line that
// measures 103 cells here takes 114 to 116 there, so a row padded to the pane
// by cellWidth auto-wraps. The upper bound is taken per cluster, because the
// conventions disagree in opposite directions (a VS16 emoji is wider as a
// cluster, a vowel sign wider per code point) and whole-string totals can
// cancel.
func terminalSafeWidth(s string) int {
	if widthConventionsAgree(s) {
		return cellWidth(s)
	}
	s = ansi.Strip(s)
	n := 0
	for s != "" {
		cluster, w := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		if cluster == "" { // defensive: never spin on a malformed tail
			break
		}
		n += max(w, ansi.StringWidthWc(cluster))
		s = s[len(cluster):]
	}
	return n
}

// terminalSafeTruncate returns the longest prefix of s (via cellTruncate, so
// ANSI and keycap handling are unchanged) whose terminalSafeWidth is <= width.
// terminalSafeWidth only grows with the prefix, so a binary search over the
// cellTruncate budget finds it in a handful of passes.
func terminalSafeTruncate(s string, width int) string {
	if terminalSafeWidth(s) <= width {
		return s
	}
	best := ""
	for lo, hi := 0, width; lo <= hi; {
		mid := (lo + hi) / 2
		if out := cellTruncate(s, mid, ""); terminalSafeWidth(out) <= width {
			best, lo = out, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// fitTerminalRow fits one final frame row to width so that it cannot
// auto-wrap on any terminal (#2334).
//
// A row every convention measures the same (ASCII, box drawing, CJK) keeps the
// exact fitCellWidth pad. A row the conventions disagree on has no single
// correct pad: it is cut to width-1 cells under the widest convention and
// finished with erase-to-end-of-line. On a terminal that draws it narrower,
// the erase clears the stale cells the pad no longer reaches; on the widest
// terminal the last column stays blank, so the erase (which acts on the
// cursor's own cell) never eats a glyph.
func fitTerminalRow(s string, width int) string {
	if width <= 0 {
		return ""
	}
	// Equality is exact per cluster: terminalSafeWidth takes each cluster's
	// max with the cellWidth figure, so a row that measures the same cannot
	// hide a disagreement.
	if widthConventionsAgree(s) || terminalSafeWidth(s) == cellWidth(s) {
		return fitCellWidth(s, width)
	}
	row := terminalSafeTruncate(s, width-1)
	return row + strings.Repeat(" ", max(0, width-1-terminalSafeWidth(row))) + "\x1b[0m" + ansi.EraseLineRight
}

// widthConventionsAgree reports, without segmenting, that every rune of s
// comes from blocks where the grapheme-cluster and per-code-point widths are
// the same and no two runes can join into one cluster: Latin through
// U+02FF, general punctuation, super/subscripts and currency, and U+2100 to
// U+2BFF (letterlike, arrows, math, technical, box drawing, blocks, shapes,
// misc symbols, dingbats). That covers the deck's own chrome (│ ─ ● ○ ◐ ■ ×
// ▶ ⚙ ⛁ ▪ ⇅), so ordinary rows skip the per-cluster walk (#2334).
// TestWidthConventionsAgree_WhitelistIsExact checks every pair of these runes
// against the pinned width tables. Escape sequences are ASCII and pass.
func widthConventionsAgree(s string) bool {
	for i := 0; i < len(s); {
		if s[i] < utf8.RuneSelf {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if !widthConventionsAgreeRune(r) {
			return false
		}
		i += size
	}
	return true
}

func widthConventionsAgreeRune(r rune) bool {
	switch {
	case r < 0x0300,
		r >= 0x2010 && r <= 0x2027,
		r >= 0x2030 && r <= 0x205E,
		r >= 0x2070 && r <= 0x20CF,
		r >= 0x2100 && r <= 0x2BFF:
		return true
	}
	return false
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

// wrapIndented word-wraps pre-rendered pieces (already styled; joined by sep
// when they share a line) into rows of at most width cells, prefixing every
// row — including the first — with indent. Unlike lipgloss's Width-based
// wrap, which reflows one logical line of plain text and has no notion of
// "this line's indent" to carry forward, this wraps whole pieces (never
// splitting one) and applies indent to each resulting row, so a continuation
// line lands under the first line instead of flush against the left edge.
// A piece wider than the available space still gets its own row rather than
// being dropped or split.
func wrapIndented(pieces []string, sep, indent string, width int) string {
	// When the indent alone eats the whole width, fall back to the raw width
	// (and at minimum one cell) rather than a non-positive budget.
	avail := width - cellWidth(indent)
	if avail < 1 {
		avail = max(width, 1)
	}
	sepWidth := cellWidth(sep)

	var lines []string
	var cur []string
	curWidth := 0
	flush := func() {
		lines = append(lines, indent+strings.Join(cur, sep))
		cur = nil
		curWidth = 0
	}
	for _, p := range pieces {
		pieceWidth := cellWidth(p)
		if len(cur) > 0 {
			if curWidth+sepWidth+pieceWidth > avail {
				flush()
			} else {
				curWidth += sepWidth
			}
		}
		cur = append(cur, p)
		curWidth += pieceWidth
	}
	if len(cur) > 0 {
		flush()
	}
	return strings.Join(lines, "\n")
}

// Chrome of the shared dialog box (DialogBoxStyle: RoundedBorder + Padding(1,2)).
const (
	// dialogBorderWidth is the rounded border's horizontal cost — 1 cell each
	// side. lipgloss draws it OUTSIDE the value passed to .Width(), so the
	// rendered box is .Width() + dialogBorderWidth wide.
	dialogBorderWidth = 2
	// dialogScreenMargin is how far a dialog's .Width() stays below the terminal
	// width on a narrow screen, leaving a comfortable gutter around the box.
	dialogScreenMargin = 10
)

// fitDialogWidth returns the value to pass to a dialog's lipgloss .Width(),
// clamped so the rendered box (this width + the rounded border) always fits
// within termWidth. preferred is the width the dialog wants on a roomy screen;
// minWidth is the smallest it should use before the terminal forces it smaller.
// On a narrow terminal the dialog shrinks toward termWidth-dialogScreenMargin
// but not below minWidth, then a final hard cap (termWidth-dialogBorderWidth)
// guarantees it never overflows even when minWidth alone would. termWidth <= 0
// (unknown) disables clamping.
//
// This consolidates the width-clamp every DialogBoxStyle dialog used to
// hand-roll. Routing them all through one function removes the class of bug
// where a fixed minimum overflowed a very narrow terminal — e.g. a floor of 56
// rendered a 58-cell box on a 57-cell split pane (only codeblock had guarded
// against it). It reproduces the old `min(preferred, max(minWidth, width-10))`
// for every non-overflowing terminal; only the overflow case changes.
func fitDialogWidth(preferred, minWidth, termWidth int) int {
	w := preferred
	if w < minWidth {
		w = minWidth
	}
	if termWidth > 0 {
		if shrunk := termWidth - dialogScreenMargin; shrunk < w {
			w = shrunk
		}
		if w < minWidth {
			w = minWidth
		}
		if hardCap := termWidth - dialogBorderWidth; w > hardCap {
			w = hardCap
		}
	}
	if w < 1 {
		w = 1
	}
	return w
}

// terminalTabWidth is the tab-stop interval every terminal we target uses: a
// TAB advances the cursor to the next column that is a multiple of 8.
const terminalTabWidth = 8

// expandTabs replaces every TAB in s with the spaces a terminal would render in
// its place, counting columns from the start of each line.
//
// Why this exists. ansi.StringWidth — and therefore cellWidth, fitCellWidth and
// every width gate built on them — measures a TAB as ZERO cells, while the
// terminal expands it to the next multiple-of-8 column. A captured pane line
// carrying tabs (git status, `ls` column output) therefore passes the frame's
// width clamp measuring exactly h.width while rendering 8 to 90 cells wider.
// That row wraps, the alternate screen scrolls, and the header / filter bar /
// SESSIONS title are pushed off the top. Bubble Tea's renderer repaints only
// rows whose content changed, so the static rows never come back — they stay
// blank until an attach/detach forces a full repaint. Expanding tabs to spaces
// makes the measurement and the rendering agree, which is the only way a
// fixed-cell frame can hold a tab at all.
//
// Escape sequences occupy no columns and are copied through untouched, so an
// ANSI-coloured line expands at the same stops as its plain-text equivalent.
// Column accounting is per grapheme cluster via cellWidth, so a wide glyph
// before a tab moves the stop by the cells it actually occupies. CR and LF
// reset the column, which makes the function safe on whole multi-line captures
// as well as on single frame rows.
func expandTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}

	var b strings.Builder
	b.Grow(len(s) + terminalTabWidth)

	col := 0
	state := -1
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			n := escapeSequenceLen(s[i:])
			b.WriteString(s[i : i+n])
			i += n
			continue
		}

		cluster, _, _, newState := uniseg.FirstGraphemeClusterInString(s[i:], state)
		state = newState
		if cluster == "" { // defensive: never spin on a malformed tail
			b.WriteString(s[i:])
			break
		}
		i += len(cluster)

		switch cluster {
		case "\t":
			pad := terminalTabWidth - col%terminalTabWidth
			b.WriteString(strings.Repeat(" ", pad))
			col += pad
		case "\n", "\r", "\r\n":
			b.WriteString(cluster)
			col = 0
		default:
			b.WriteString(cluster)
			col += cellWidth(cluster)
		}
	}

	return b.String()
}

// escapeSequenceLen returns the byte length of the escape sequence at the start
// of s, which must begin with ESC. Handles CSI (ESC [ … final byte 0x40-0x7E),
// OSC (ESC ] … BEL or ST) — the two shapes captured pane content actually
// carries, SGR colour and OSC 8 hyperlinks — and treats anything else as a
// two-byte escape. An unterminated sequence consumes the remainder, which
// matches how a terminal would swallow it.
func escapeSequenceLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']':
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	default:
		return 2
	}
}
