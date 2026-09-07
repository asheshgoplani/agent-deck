package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputBudgetPreservesPlainUTF8(t *testing.T) {
	text := "ÛX ۛZ 日本 👩🏽‍💻"
	got, truncated := prepareAgentBoundaryOutput(text, defaultOutputMaxTokens, "fixture.txt")
	if got != text || truncated || !utf8.ValidString(got) {
		t.Fatalf("uncapped text=%q truncated=%t", got, truncated)
	}
	long := strings.Repeat(text+" ", 1000)
	got, truncated = prepareAgentBoundaryOutput(long, 100, "fixture.txt")
	if !truncated || !utf8.ValidString(got) {
		t.Fatalf("capped text must remain valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, text) || !strings.Contains(got, text+" \n\n… output truncated") {
		t.Errorf("cap damaged retained ends: %q", got)
	}
}
