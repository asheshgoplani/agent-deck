package main

import (
	"path/filepath"
	"testing"
)

// FormatPath must only collapse HOME when the path is HOME itself or sits
// under HOME/. A sibling directory that merely shares the prefix
// (HOME=/home/ash, path=/home/ashesh/repo) must be left untouched.
func TestFormatPath_HomePrefixNeedsSeparator(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ash")
	t.Setenv("HOME", home)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"home itself", home, "~"},
		{"under home", filepath.Join(home, "x"), "~/x"},
		{"nested under home", filepath.Join(home, "a", "b"), "~/a/b"},
		{"sibling sharing prefix", home + "esh", home + "esh"},
		{"sibling child sharing prefix", filepath.Join(home+"esh", "x"), filepath.Join(home+"esh", "x")},
		{"outside home", "/opt/x", "/opt/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatPath(tt.in); got != tt.want {
				t.Errorf("FormatPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
