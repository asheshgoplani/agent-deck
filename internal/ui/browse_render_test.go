package ui

import (
	"testing"
	"time"
)

func TestBrowseRelTime(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"now", now.Add(-10 * time.Second), "now"},
		{"minutes", now.Add(-3 * time.Minute), "3m"},
		{"hours", now.Add(-2 * time.Hour), "2h"},
		{"days", now.Add(-5 * 24 * time.Hour), "5d"},
	}
	for _, c := range cases {
		if got := browseRelTime(c.when, now); got != c.want {
			t.Errorf("%s: browseRelTime = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBrowseCaretGlyph(t *testing.T) {
	if browseCaretGlyph(true) == browseCaretGlyph(false) {
		t.Fatal("expanded and collapsed carets must differ")
	}
}
