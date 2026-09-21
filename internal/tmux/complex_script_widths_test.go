package tmux

import (
	"fmt"
	"strings"
	"testing"
)

// #2334: on a tmux with codepoint-widths, agent-deck gives every Indic
// spacing vowel sign zero width in its own fixed slots with -o (never
// overwriting an occupant), so tmux sizes का/कि/को as one cell like Claude
// Code (Bun.stringWidth) and utf8proc tmux do. Older tmux and a user override get nothing.
func TestComplexScriptWidthArgs_Issue2334(t *testing.T) {
	args := complexScriptWidthArgs(nil, "3.6a")
	joined := strings.Join(args, " ")
	for _, mark := range []rune{'ा', 'ि', 'ी', 'ो', 'ौ', 'ः', 'া', 'ি', 'ா', 'ி'} {
		if !strings.Contains(joined, fmt.Sprintf(" U+%04X=0", mark)) {
			t.Errorf("vowel sign %q (U+%04X) not set to width 0", mark, mark)
		}
	}
	for _, notMc := range []rune{'क', '्', 'ं', '़', 'े'} { // consonant, virama, anusvara, nukta, Mn vowel sign
		if strings.Contains(joined, fmt.Sprintf(" U+%04X=", notMc)) {
			t.Errorf("non-Mc code point %q (U+%04X) must keep tmux's width", notMc, notMc)
		}
	}
	if len(args) != 5*len(indicSpacingMarks) || len(indicSpacingMarks) < 90 {
		t.Fatalf("got %d args for %d marks", len(args), len(indicSpacingMarks))
	}
	for i := 0; i < len(args); i += 5 {
		chunk := args[i : i+5]
		r := indicSpacingMarks[i/5]
		want := []string{";", "set-option", "-soq", fmt.Sprintf("codepoint-widths[%d]", codepointWidthsBaseIndex+int(r-indicBlocksFirst)), fmt.Sprintf("U+%04X=0", r)}
		if strings.Join(chunk, " ") != strings.Join(want, " ") {
			t.Fatalf("chunk %d = %q, want prefix %q", i/5, chunk, want)
		}
	}
	if again := complexScriptWidthArgs(nil, "3.7b"); strings.Join(again, " ") != joined {
		t.Error("slots must be stable across calls and versions (idempotent re-application)")
	}

	if last := codepointWidthsBaseIndex + indicBlocksLast - indicBlocksFirst; last > 2147483647 {
		t.Fatalf("highest slot %d overflows tmux's int32 array index", last)
	}
	for _, ver := range []string{"3.4", "3.5a", "3.3a", ""} {
		if got := complexScriptWidthArgs(nil, ver); got != nil {
			t.Errorf("tmux %q has no codepoint-widths, got %d args", ver, len(got))
		}
	}
	if got := complexScriptWidthArgs(map[string]string{"codepoint-widths": ""}, "3.6a"); got != nil {
		t.Errorf("user override must opt out, got %d args", len(got))
	}
}

func TestComplexScriptWidthCheck_Issue2334(t *testing.T) {
	for _, tc := range []struct{ ver, goos, state, mention string }{
		{"3.6a", "linux", "ok", "codepoint-widths"},
		{"3.7b", "darwin", "ok", "codepoint-widths"},
		{"master", "linux", "ok", "codepoint-widths"},
		{"3.4", "linux", "warn", "3.6+"},
		{"3.5a", "linux", "warn", "3.6+"},
		{"3.5a", "darwin", "ok", "utf8proc"},
		{"", "linux", "unknown", "unknown"},
	} {
		got := complexScriptWidthCheck(tc.ver, tc.goos)
		if got.State != tc.state || !strings.Contains(got.Detail, tc.mention) {
			t.Errorf("tmux %q on %s: got %+v, want state %q mentioning %q", tc.ver, tc.goos, got, tc.state, tc.mention)
		}
	}
}
