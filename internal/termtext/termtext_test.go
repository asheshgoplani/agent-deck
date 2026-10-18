package termtext

import "testing"

func TestClean(t *testing.T) {
	tests := map[string]string{
		"plain name":               "plain name",
		"\x1b]52;c;aGk=\x07clip":   "]52;c;aGk=clip",
		"\x1b[2J\x1b[Hwipe":        "[2J[Hwipe",
		"a\r\nb\tc":                "abc",
		"csi\u009b31m":             "csi31m",
		"del\x7f":                  "del",
		"evil‮gnp.exe":             "evilgnp.exe",
		"⁦iso⁩ ‏mark":              "iso mark",
		"emoji 🦀 and ünïcode stay": "emoji 🦀 and ünïcode stay",
		"\xff":                     "�",
	}
	for in, want := range tests {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}
