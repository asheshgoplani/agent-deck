package logging

import (
	"testing"
	"unsafe"
)

func TestSanitizeValue(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		dirty bool // sanity check: dirty input must not equal itself after sanitizing
	}{
		{name: "empty", in: "", want: ""},
		{name: "clean", in: "my-session-42", want: "my-session-42"},
		{name: "newline", in: "evil\nfake_log_line=1", want: `evil\nfake_log_line=1`, dirty: true},
		{name: "carriage_return", in: "evil\r\ninjected", want: `evil\r\ninjected`, dirty: true},
		{name: "tab_preserved", in: "col1\tcol2", want: "col1\tcol2"},
		{name: "control_char", in: "bad\x00value", want: "bad�value", dirty: true},
		{name: "unicode_preserved", in: "sess-éè", want: "sess-éè"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeValue(tc.in)
			if got != tc.want {
				t.Fatalf("SanitizeValue(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if tc.dirty && got == tc.in {
				t.Fatalf("SanitizeValue(%q) left the injected control character untouched", tc.in)
			}
		})
	}
}

// TestSanitizeValue_NeverAliasesItsInput pins the property that makes
// SanitizeValue usable as a sanitizer barrier: no input, however clean, is
// returned as-is. The value must always come from the builder.
//
// This is not stylistic. While the function short-circuited with `return s`
// for empty and already-clean inputs — behaviorally identical results —
// dataflow analysis followed those returns and saw the tainted argument
// reaching the log sink unchanged, so CodeQL's go/log-injection kept firing on
// every call site regardless of how many were wrapped. Restoring a
// pass-through would silently un-fix all of them, so assert against the string
// data pointer, which equality alone cannot catch.
func TestSanitizeValue_NeverAliasesItsInput(t *testing.T) {
	for _, in := range []string{"", "my-session-42", "col1\tcol2", "sess-éè"} {
		got := SanitizeValue(in)
		if got != in {
			t.Fatalf("SanitizeValue(%q) = %q, want the value unchanged", in, got)
		}
		if len(in) > 0 && unsafe.StringData(got) == unsafe.StringData(in) {
			t.Fatalf("SanitizeValue(%q) returned its input rather than a built string; "+
				"that pass-through defeats CodeQL sanitizer detection at every call site", in)
		}
	}
}
