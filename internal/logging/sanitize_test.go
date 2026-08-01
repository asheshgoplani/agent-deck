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
