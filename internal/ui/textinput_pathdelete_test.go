package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
)

func TestDeleteWordBackwardPath(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		cursor    int
		wantValue string
		wantPos   int
	}{
		{
			name:      "trailing segment of absolute path",
			value:     "/Users/dmitry/code/foo",
			cursor:    len("/Users/dmitry/code/foo"),
			wantValue: "/Users/dmitry/code/",
			wantPos:   len("/Users/dmitry/code/"),
		},
		{
			name:      "trailing slash collapses to previous segment",
			value:     "/Users/dmitry/code/",
			cursor:    len("/Users/dmitry/code/"),
			wantValue: "/Users/dmitry/",
			wantPos:   len("/Users/dmitry/"),
		},
		{
			name:      "no slash falls back to clearing whole word",
			value:     "myfolder",
			cursor:    len("myfolder"),
			wantValue: "",
			wantPos:   0,
		},
		{
			name:      "tilde expands cleanly",
			value:     "~/code/foo",
			cursor:    len("~/code/foo"),
			wantValue: "~/code/",
			wantPos:   len("~/code/"),
		},
		{
			name:      "cursor at start is a no-op",
			value:     "/foo/bar",
			cursor:    0,
			wantValue: "/foo/bar",
			wantPos:   0,
		},
		{
			name:      "cursor mid-path deletes only behind cursor",
			value:     "/foo/bar/baz",
			cursor:    len("/foo/bar/"),
			wantValue: "/foo/baz",
			wantPos:   len("/foo/"),
		},
		{
			name:      "preserves text after cursor when deleting at root",
			value:     "/foo",
			cursor:    len("/foo"),
			wantValue: "/",
			wantPos:   1,
		},
		{
			name:      "branch-style slash works the same",
			value:     "feature/issue-896",
			cursor:    len("feature/issue-896"),
			wantValue: "feature/",
			wantPos:   len("feature/"),
		},
		{
			name:      "whitespace boundary still works",
			value:     "hello world",
			cursor:    len("hello world"),
			wantValue: "hello ",
			wantPos:   len("hello "),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti := textinput.New()
			ti.SetValue(tt.value)
			ti.SetCursor(tt.cursor)

			deleteWordBackwardPath(&ti)

			if got := ti.Value(); got != tt.wantValue {
				t.Errorf("value = %q, want %q", got, tt.wantValue)
			}
			if got := ti.Position(); got != tt.wantPos {
				t.Errorf("cursor = %d, want %d", got, tt.wantPos)
			}
		})
	}
}
