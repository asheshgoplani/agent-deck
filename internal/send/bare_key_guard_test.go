package send

import (
	"errors"
	"strings"
	"testing"
)

// scriptedSuggestionPane returns captures in order (the last one repeats) and
// counts Escapes.
type scriptedSuggestionPane struct {
	captures []string
	calls    int
	escapes  int
}

func (p *scriptedSuggestionPane) capture() (string, error) {
	i := min(p.calls, len(p.captures)-1)
	p.calls++
	return p.captures[i], nil
}

func (p *scriptedSuggestionPane) escape() error { p.escapes++; return nil }

// Claude 2.1.295 keeps the suggestion after Escape: one Escape, then refuse.
func TestClearSuggestionBeforeBareKey_EscapeThenWithhold(t *testing.T) {
	for name, row := range ghostFixtures() {
		p := &scriptedSuggestionPane{captures: []string{pane(row)}}
		err := ClearSuggestionBeforeBareKey(p.capture, p.escape, 0)
		if !errors.Is(err, ErrSuggestionShown) {
			t.Errorf("%s: want ErrSuggestionShown, got %v", name, err)
		}
		if p.escapes != 1 {
			t.Errorf("%s: want exactly one Escape, got %d", name, p.escapes)
		}
	}
}

// A build or plugin whose Escape does clear the suggestion lets the key go.
func TestClearSuggestionBeforeBareKey_EscapeClears(t *testing.T) {
	p := &scriptedSuggestionPane{captures: []string{pane(fixture2295GhostFocused), pane(fixtureEmptyComposer)}}
	if err := ClearSuggestionBeforeBareKey(p.capture, p.escape, 0); err != nil {
		t.Fatalf("suggestion cleared by Escape must allow the key: %v", err)
	}
	if p.escapes != 1 {
		t.Fatalf("want one Escape, got %d", p.escapes)
	}
}

// No suggestion: no Escape, the key goes out. Typed text is not a suggestion.
func TestClearSuggestionBeforeBareKey_NoSuggestionNoEscape(t *testing.T) {
	for name, composer := range map[string]string{
		"empty":            fixtureEmptyComposer,
		"typed text":       "❯ deploy the staging build",
		"no composer line": "",
	} {
		p := &scriptedSuggestionPane{captures: []string{pane(composer)}}
		if err := ClearSuggestionBeforeBareKey(p.capture, p.escape, 0); err != nil {
			t.Errorf("%s: unexpected refusal: %v", name, err)
		}
		if p.escapes != 0 {
			t.Errorf("%s: Escape sent with no suggestion shown", name)
		}
	}
}

// Every spelling tmux resolves to Enter, Tab or Right (key names are
// case-insensitive, ^X and C-X are the same control key, hex codes and raw
// CR/TAB bytes are typed as those keys) is guarded; modified keys and other
// keys are not.
func TestBareKeyActsOnSuggestion(t *testing.T) {
	for key, want := range map[string]bool{
		// Enter
		"Enter": true, "enter": true, "ENTER": true, "eNtEr": true,
		"KPEnter": true, "kpenter": true, "KPENTER": true,
		"C-m": true, "c-m": true, "C-M": true, "c-M": true, "^M": true, "^m": true,
		"0xd": true, "0xD": true, "0x0d": true, "\r": true,
		// Tab
		"Tab": true, "tab": true, "TAB": true,
		"C-i": true, "c-i": true, "C-I": true, "^I": true, "^i": true,
		"0x9": true, "0x09": true, "\t": true,
		// Right
		"Right": true, "right": true, "RIGHT": true,
		// Not the bare key, or not a key that acts on a suggestion
		"BSpace": false, "Up": false, "Down": false, "Left": false, "left": false,
		"C-c": false, "^C": false, "Escape": false, "BTab": false, "btab": false,
		"S-Enter": false, "M-Enter": false, "M-Right": false, "S-Tab": false,
		"C-Enter": false, "C-Right": false, "C-Tab": false, "^Enter": false,
		"M-C-m": false, "m": false, "i": false, "0xa": false, "0xzz": false,
		"\n": false, "Space": false, "": false,
	} {
		if got := BareKeyActsOnSuggestion(key); got != want {
			t.Errorf("BareKeyActsOnSuggestion(%q) = %v, want %v", key, got, want)
		}
	}
}

// A literal --text that is one keypress on its own is guarded like the key;
// real text, or a key inside real text, is not.
func TestLiteralTextKey(t *testing.T) {
	for text, want := range map[string]string{
		"\r": "Enter", "\t": "Tab", "\x1b[C": "Right", "\x1bOC": "Right",
		"\n": "", "hello": "", "Enter": "", "tab": "", "\thello": "", "hello\r": "", "\x1b": "",
	} {
		if got := LiteralTextKey(text); got != want {
			t.Errorf("LiteralTextKey(%q) = %q, want %q", text, got, want)
		}
	}
}

// Escape interrupts a running turn, so a suggestion or placeholder painted
// while Claude's busy line is shown is refused with no Escape (review N3),
// with or without the "esc to interrupt" hint (Claude 2.1.296 drops it).
// The hint in older output beyond busyLookback is not a running turn.
func TestClearSuggestionBeforeBareKey_NoEscapeWhileTurnRuns(t *testing.T) {
	for _, busy := range []string{
		"\x1b[38;5;174m✻ Thinking… (2s · esc to interrupt)\x1b[39m",
		"\x1b[38;5;174m✽ Thundering… (34s · ↓ 135 tokens)\x1b[39m",
	} {
		p := &scriptedSuggestionPane{captures: []string{pane(fixture2295GhostFocused)}}
		p.captures[0] = strings.Replace(p.captures[0], "\n\x1b[38;5;244m─", "\n"+busy+"\n\n\x1b[38;5;244m─", 1)
		if !ClaudeTurnRunning(p.captures[0]) {
			t.Fatalf("fixture must show a running turn: %q", p.captures[0])
		}
		if err := ClearSuggestionBeforeBareKey(p.capture, p.escape, 0); !errors.Is(err, ErrSuggestionShown) {
			t.Fatalf("%q: want ErrSuggestionShown, got %v", busy, err)
		}
		if p.escapes != 0 {
			t.Fatalf("%q: Escape pressed during a running turn", busy)
		}
	}

	old := "earlier output: esc to interrupt\n" + strings.Repeat("⏺ answer line\n", busyLookback) + pane(fixture2295GhostFocused)
	if ClaudeTurnRunning(old) {
		t.Fatal("the hint in older output is not a running turn")
	}
	if ClaudeTurnRunning(pane(fixture2295GhostFocused)) {
		t.Fatal("idle pane read as a running turn")
	}
	done := strings.Replace(pane(fixture2295GhostFocused), "\n\x1b[38;5;244m─", "\n✻ Baked for 3m 5s\n\n\x1b[38;5;244m─", 1)
	if ClaudeTurnRunning(done) {
		t.Fatal("a finished turn summary read as a running turn")
	}
}
