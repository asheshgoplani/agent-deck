package logging

import "testing"

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
		// Rebuilding rune by rune replaces undecodable bytes. Intentional: raw
		// invalid bytes are exactly what corrupts a log stream.
		{name: "invalid_utf8_replaced", in: "bad-\xff-byte", want: "bad-\uFFFD-byte", dirty: true},
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

// TestSanitizeValue_CleanInputRoundTrips pins that removing the old
// pass-through fast paths did not change results for input needing no
// escaping: the value must still come back byte-identical, only now rebuilt
// rather than returned as-is.
//
// The rebuild is what makes the function a sanitizer barrier — dataflow
// analysis follows a `return s` and sees the tainted value reaching the log
// sink untouched, which is why go/log-injection kept firing at every call site
// while the fast paths existed. That property is enforced by CodeQL in CI
// rather than asserted here: probing string backing storage would be testing
// an allocation detail the language does not promise.
func TestSanitizeValue_CleanInputRoundTrips(t *testing.T) {
	for _, in := range []string{"", "my-session-42", "col1\tcol2", "sess-\u00e9\u00e8", "/path/to/thing:42"} {
		if got := SanitizeValue(in); got != in {
			t.Fatalf("SanitizeValue(%q) = %q, want the value unchanged", in, got)
		}
	}
}
