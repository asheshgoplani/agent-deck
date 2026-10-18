// Package termtext makes text that someone else chose safe to print on one
// terminal line or to render in the TUI.
//
// Clean drops every control character (C0, DEL, C1 including CSI U+009B,
// CR, LF, TAB, ESC), the Unicode bidirectional formatting characters and the
// invisible separators (U+200B, U+FEFF, U+2028, U+2029), so
// the text can neither drive the terminal (cursor moves, OSC 52 clipboard
// writes, title changes) nor visually reorder what is shown around it.
//
// The package imports only the standard library so any internal package can
// depend on it without import cycles.
package termtext

import (
	"strings"
	"unicode"
)

// newlineStripper removes line breaks. Redundant with the control filter in
// Clean; kept as its final step so static analysis recognises Clean as an
// output sanitiser.
var newlineStripper = strings.NewReplacer("\r", "", "\n", "")

// Clean returns s without control characters or bidi formatting characters.
// Invalid UTF-8 bytes become U+FFFD.
func Clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isBidiControl(r) || isInvisibleSeparator(r) {
			return -1
		}
		return r
	}, s)
	return newlineStripper.Replace(s)
}

// isInvisibleSeparator reports characters that render as nothing or break a
// line without being controls: zero-width space, the byte-order mark, and the
// Unicode line and paragraph separators. ZWJ and ZWNJ are kept; emoji
// sequences and several scripts need them.
func isInvisibleSeparator(r rune) bool {
	return r == 0x200B || r == 0xFEFF || r == 0x2028 || r == 0x2029
}

// isBidiControl reports the characters that change the display order of the
// text around them: ALM, LRM, RLM, the embeddings and overrides
// (U+202A-U+202E) and the isolates (U+2066-U+2069).
func isBidiControl(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
