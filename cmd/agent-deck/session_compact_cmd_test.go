package main

import (
	"strings"
	"testing"
)

func TestCompactMessage(t *testing.T) {
	tests := []struct {
		name         string
		instructions string
		want         string
	}{
		{"bare", "", "/compact"},
		{"whitespace only", "   \t\n  ", "/compact"},
		{"instructions appended", "keep the task list", "/compact keep the task list"},
		{"surrounding space trimmed", "  keep the task list  ", "/compact keep the task list"},
		{
			// The payload is typed into a composer as a single line. An embedded
			// newline submits early, compacting with half the instructions and
			// leaving the rest as a stray prompt afterwards.
			"newlines collapsed",
			"keep the run dir\nand the open questions",
			"/compact keep the run dir and the open questions",
		},
		{"runs of whitespace collapsed", "keep   PR   urls", "/compact keep PR urls"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactMessage(tc.instructions); got != tc.want {
				t.Fatalf("compactMessage(%q) = %q, want %q", tc.instructions, got, tc.want)
			}
		})
	}
}

func TestCompactMessage_NeverEmitsNewline(t *testing.T) {
	// Belt and braces on the property that actually matters, independent of the
	// table above: whatever comes in, one line goes out.
	for _, in := range []string{
		"a\nb", "a\r\nb", "a\n\n\nb", "\nleading", "trailing\n", "a\tb",
	} {
		got := compactMessage(in)
		if strings.ContainsAny(got, "\r\n") {
			t.Fatalf("compactMessage(%q) = %q, which contains a newline", in, got)
		}
		if !strings.HasPrefix(got, "/compact") {
			t.Fatalf("compactMessage(%q) = %q, which is not a /compact command", in, got)
		}
	}
}
