package ui

import (
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
)

// deleteWordBackwardPath performs ctrl+w-style backward deletion that is
// path-aware: it stops at '/' as well as whitespace, so deleting one word from
// "/foo/bar/baz" yields "/foo/bar/" instead of clearing the whole field (which
// is what bubbles' default deleteWordBackward does, since the path contains no
// whitespace). Issue #896.
func deleteWordBackwardPath(ti *textinput.Model) {
	pos := ti.Position()
	if pos == 0 {
		return
	}
	val := []rune(ti.Value())
	if pos > len(val) {
		pos = len(val)
	}

	i := pos
	// eat trailing separators (spaces and slashes) right behind the cursor
	for i > 0 && isPathWordBoundary(val[i-1]) {
		i--
	}
	// then eat the word itself, up to the next boundary
	for i > 0 && !isPathWordBoundary(val[i-1]) {
		i--
	}

	newVal := string(val[:i]) + string(val[pos:])
	ti.SetValue(newVal)
	ti.SetCursor(i)
}

func isPathWordBoundary(r rune) bool {
	return r == '/' || unicode.IsSpace(r)
}
