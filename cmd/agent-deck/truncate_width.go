package main

import "github.com/mattn/go-runewidth"

// truncateWidth shortens s to at most max display columns, ending in "..."
// when there is room for it. It cuts on rune boundaries and counts wide
// (CJK) and multi-byte (box-drawing) runes by their column width, so table
// cells never contain a split rune.
func truncateWidth(s string, max int) string {
	if runewidth.StringWidth(s) <= max {
		return s
	}
	if max <= 3 {
		return runewidth.Truncate(s, max, "")
	}
	return runewidth.Truncate(s, max, "...")
}
