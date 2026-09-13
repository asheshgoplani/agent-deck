package tmux

import (
	"testing"
	"unicode/utf8"
)

func TestStripANSIPreservesUTF8ContinuationBytes(t *testing.T) {
	for _, text := range []string{"ÛX", "Û", "ۛZ", "日本 👩🏽‍💻 café"} {
		for _, input := range []string{text, "\x1b[31m" + text + "\x1b[0m", "\x9b31m" + text + "\x9b0m"} {
			got := StripANSI(input)
			if got != text || !utf8.ValidString(got) {
				t.Errorf("StripANSI(%q)=%q, want %q", input, got, text)
			}
		}
	}
}
