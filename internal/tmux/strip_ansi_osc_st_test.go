package tmux

import (
	"strings"
	"testing"
)

// TestStripANSIOSCEndsAtFirstTerminator pins that an OSC sequence ends at
// whichever terminator comes first, BEL or ST (ESC \). Looking for BEL first
// let an ST-terminated title swallow the visible text up to a later BEL.
func TestStripANSIOSCEndsAtFirstTerminator(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ST before later BEL", "\x1b]0;t\x1b\\hello\x07 world", "hello\x07 world"},
		{"BEL before later ST", "\x1b]0;t\x07hello\x1b\\ world", "hello world"},
		{"BEL only", "\x1b]0;t\x07hello world", "hello world"},
		{"ST only", "\x1b]0;t\x1b\\hello world", "hello world"},
		{"two ST titles around text", "\x1b]0;a\x1b\\x\x1b]2;b\x1b\\y\x07", "xy\x07"},
		{"OSC 8 hyperlink with ST", "\x1b]8;;http://e.x\x1b\\link\x1b]8;;\x1b\\ done\x07", "link done\x07"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Fatalf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// BenchmarkStripANSI_OSCHyperlinks covers a capture full of BEL-terminated
// OSC 8 links with no ST anywhere: finding the terminator must not rescan the
// rest of the content for every sequence.
func BenchmarkStripANSI_OSCHyperlinks(b *testing.B) {
	content := strings.Repeat("\x1b]8;;http://e.x/a\x07link\x1b]8;;\x07 text\n", 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = StripANSI(content)
	}
}
