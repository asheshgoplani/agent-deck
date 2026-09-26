package tmux

import (
	"fmt"
	"strings"
	"testing"
)

// #2334: Indic zero-width marks are opt-in only. A session built without the
// config adds nothing and removes nothing; with it on, tmux >= 3.6 gets every
// Indic spacing vowel sign at width 0 in agent-deck's fixed slots, with -o so
// an occupant is never overwritten; a [tmux] options "codepoint-widths"
// override still wins.
func TestIndicZeroWidthArgs_OptInOnly_Issue2334(t *testing.T) {
	session := func(set, enabled bool, overrides map[string]string) *Session {
		s := &Session{OptionOverrides: overrides}
		if set {
			s.SetIndicZeroWidthMarks(enabled)
		}
		return s
	}
	// Not configured (tmux discovery) and opted in with an override: nothing,
	// not even the off-path cleanup, on any tmux.
	if got := session(false, false, nil).indicZeroWidthMarksArgs(); got != nil {
		t.Errorf("unconfigured session emitted %d args", len(got))
	}
	if got := session(true, true, map[string]string{"codepoint-widths": ""}).indicZeroWidthMarksArgs(); got != nil {
		t.Errorf("a codepoint-widths override must win, got %d args", len(got))
	}
	for _, ver := range []string{"3.3a", "3.4", "3.5a", ""} {
		if got := indicZeroWidthArgs(ver); got != nil {
			t.Errorf("tmux %q has no codepoint-widths, got %d args", ver, len(got))
		}
	}

	args := indicZeroWidthArgs("3.6a")
	joined := strings.Join(args, " ")
	for _, mark := range []rune{'ा', 'ि', 'ी', 'ो', 'ौ', 'ः', 'া', 'ি', 'ா', 'ி'} {
		if !strings.Contains(joined, fmt.Sprintf(" U+%04X=0", mark)) {
			t.Errorf("vowel sign %q (U+%04X) not set to width 0", mark, mark)
		}
	}
	for _, notMc := range []rune{'क', '्', 'ं', '़', 'े'} {
		if strings.Contains(joined, fmt.Sprintf(" U+%04X=", notMc)) {
			t.Errorf("non-Mc code point %q (U+%04X) must keep tmux's width", notMc, notMc)
		}
	}
	if len(args) != 5*len(indicSpacingMarks) || len(indicSpacingMarks) < 90 {
		t.Fatalf("got %d args for %d marks", len(args), len(indicSpacingMarks))
	}
	for i := 0; i < len(args); i += 5 {
		r := indicSpacingMarks[i/5]
		want := fmt.Sprintf("; set-option -soq codepoint-widths[%d] U+%04X=0", codepointWidthsBaseIndex+int(r-indicBlocksFirst), r)
		if got := strings.Join(args[i:i+5], " "); got != want {
			t.Fatalf("chunk %d = %q, want %q", i/5, got, want)
		}
	}
	if last := codepointWidthsBaseIndex + indicBlocksLast - indicBlocksFirst; last > 2147483647 {
		t.Fatalf("highest slot %d overflows tmux's int32 array index", last)
	}
}

// The off path removes only slots that still hold exactly agent-deck's value:
// never a user's entry at a low index, never a foreign value that took one of
// our slots, never anything outside our range.
func TestOwnedIndicZeroWidthSlots_Issue2334(t *testing.T) {
	aa := codepointWidthsBaseIndex + 0x3E // U+093E
	ii := codepointWidthsBaseIndex + 0x3F // U+093F
	out := fmt.Sprintf("codepoint-widths[0] U+093E=1\n"+
		"codepoint-widths[%d] U+093E=0\n"+
		"codepoint-widths[%d] U+093F=1\n"+ // foreign value in our slot
		"codepoint-widths[%d] \"U+0940=0\"\n"+ // ours, quoted form
		"codepoint-widths[%d] U+0DF3=0\n"+
		"codepoint-widths[%d] U+0E00=0\n", // outside our range
		aa, ii, codepointWidthsBaseIndex+0x40, codepointWidthsBaseIndex+0x4F3, codepointWidthsBaseIndex+0x500)
	got := ownedIndicZeroWidthSlots([]byte(out))
	want := []int{aa, codepointWidthsBaseIndex + 0x40, codepointWidthsBaseIndex + 0x4F3}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("owned slots = %v, want %v", got, want)
	}
	if got := ownedIndicZeroWidthSlots(nil); len(got) != 0 {
		t.Fatalf("empty array yielded %v", got)
	}
	unset := strings.Join(indicZeroWidthUnsetArgs(want), " ")
	wantUnset := fmt.Sprintf("set-option -suq codepoint-widths[%d] ; set-option -suq codepoint-widths[%d] ; set-option -suq codepoint-widths[%d]", want[0], want[1], want[2])
	if unset != wantUnset {
		t.Fatalf("unset args = %q, want %q", unset, wantUnset)
	}
}

func TestIndicZeroWidthMarksInfo_Issue2334(t *testing.T) {
	on := indicZeroWidthMarksInfo("3.6a")
	if !on.Applied || !strings.Contains(on.Detail, "misaligns Codex/shell/vim") {
		t.Errorf("3.6a: %+v", on)
	}
	for _, ver := range []string{"3.4", ""} {
		off := indicZeroWidthMarksInfo(ver)
		if off.Applied || !strings.Contains(off.Detail, "needs tmux >= 3.6") {
			t.Errorf("%q: %+v", ver, off)
		}
	}
}
