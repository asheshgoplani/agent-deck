package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// The CLI table helpers must cut by display width, never inside a rune
// (ASK-102: a CJK title in `agent-deck list` printed as invalid UTF-8, and
// the multi-byte tree prefix in `group list` truncated short names).
func TestTableTruncateHelpers_UTF8Safe(t *testing.T) {
	helpers := map[string]func(string, int) string{
		"truncate":          truncate,
		"truncateGroupName": truncateGroupName,
		"truncateString":    truncateString,
	}
	for name, f := range helpers {
		got := f("日本語のプロジェクト名です", 20)
		if !utf8.ValidString(got) {
			t.Errorf("%s: invalid UTF-8 %q", name, got)
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("%s: %q lacks ellipsis", name, got)
		}
		if w := runewidth.StringWidth(got); w > 20 {
			t.Errorf("%s: width %d > 20 for %q", name, w, got)
		}
		if got := f("  └── frontend-app", 20); got != "  └── frontend-app" {
			t.Errorf("%s: 18-column string truncated to %q", name, got)
		}
		if got := f("abcdefghijklmnopqrstuvwxyz", 20); got != "abcdefghijklmnopq..." {
			t.Errorf("%s: ASCII regression %q", name, got)
		}
		if got := f("abcdef", 2); got != "ab" {
			t.Errorf("%s: small max %q", name, got)
		}
		if got := f("日本語", 3); !utf8.ValidString(got) || runewidth.StringWidth(got) > 3 {
			t.Errorf("%s: small max on wide runes %q", name, got)
		}
	}
}

func TestFirstLineClipped_NeverSplitsRune(t *testing.T) {
	got := firstLineClipped(strings.Repeat("日", 100), derivedPurposeLimit) // 300 bytes
	if !utf8.ValidString(got) {
		t.Fatalf("clipped purpose is invalid UTF-8: %q", got)
	}
	if len(got) != 198 {
		t.Fatalf("len=%d, want 198 (66 whole runes within 200 bytes)", len(got))
	}
	emoji := firstLineClipped(strings.Repeat("😀", 10), 6) // 4-byte runes
	if emoji != "😀" {
		t.Fatalf("emoji clip = %q, want one whole rune", emoji)
	}
	if got := firstLineClipped("  plain ascii line  \nsecond", 5); got != "plain" {
		t.Fatalf("ASCII clip = %q, want %q", got, "plain")
	}
}
