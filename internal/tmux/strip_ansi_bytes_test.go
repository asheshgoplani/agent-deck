package tmux

import "testing"

// TestStripANSIByteExactUTF8 pins the exact bytes StripANSI emits for
// multi-byte and combining characters around every escape form it handles,
// plus invalid bytes, which must pass through untouched rather than be
// consumed as controls.
func TestStripANSIByteExactUTF8(t *testing.T) {
	const (
		esc = "\x1b["
		csi = "\x9b" // 8-bit CSI introducer; also the continuation byte of Û (C3 9B)
	)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"continuation byte 0x9B inside Û", "\xc3\x9bX", "\xc3\x9bX"},
		{"Û between 7-bit CSI", esc + "1mÛ" + esc + "0m", "Û"},
		{"Û between 8-bit CSI", csi + "1mÛ" + csi + "0m", "Û"},
		{"combining acute on e", "e\u0301" + esc + "0m" + "e\u0301", "e\u0301e\u0301"},
		{"combining Arabic mark", csi + "31m" + "ۛZ" + csi + "0m", "ۛZ"},
		{"Devanagari conjunct", esc + "32m" + "क्ष" + esc + "0m", "क्ष"},
		{"ZWJ emoji sequence", "👩🏽\u200d💻" + esc + "K", "👩🏽\u200d💻"},
		{"CJK with OSC title", "\x1b]0;日本\x07日本", "日本"},
		{"CJK with OSC ST", "\x1b]0;日本\x1b\\日本", "日本"},
		{"Ü byte 0x9C is not CSI", "\xc3\x9c", "\xc3\x9c"},
		{"bare 8-bit CSI still stripped", "a" + csi + "31mb", "ab"},
		{"lone continuation byte passes through", "a\x80b", "a\x80b"},
		{"truncated lead byte at end passes through", "ab\xc3", "ab\xc3"},
		{"lone continuation 0x9B is CSI", "a\x9b" + "1mb", "ab"},
		{"invalid then valid", "\xff" + esc + "0m" + "é", "\xffé"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Fatalf("StripANSI(%q) = %q (% x), want %q (% x)", tt.in, got, got, tt.want, tt.want)
			}
		})
	}
}
