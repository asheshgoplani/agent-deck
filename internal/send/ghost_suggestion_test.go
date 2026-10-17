package send

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Claude Code 2.1.295 shows a prompt suggestion (a predicted next user
// message) in an EMPTY composer through its placeholder renderer:
//
//	o = dim(t); if (showCursor && focus && terminalFocus) o = invert(t[0]) + dim(t.slice(1))
//
// with invert = "\x1b[7m"+s+"\x1b[27m", or the identity when the terminal
// draws a native cursor. The fixtures are the composer rows tmux
// capture-pane -e returned for a pane painting exactly those bytes (tmux
// re-encodes the attribute change after the cursor cell as "\x1b[0;2m").
const (
	ghostSuggestionText = "Run the release checklist and tag the build now"

	fixture2295GhostFocused   = "❯ \x1b[7mR\x1b[0;2mun the release checklist and tag the build now\x1b[0m"
	fixture2295GhostUnfocused = "❯ \x1b[2mRun the release checklist and tag the build now\x1b[0m"
	fixture2295GhostNative    = "❯ R\x1b[2mun the release checklist and tag the build now\x1b[0m"
)

func ghostFixtures() map[string]string {
	return map[string]string{
		"focused (inverse cursor cell)": fixture2295GhostFocused,
		"unfocused (all dim)":           fixture2295GhostUnfocused,
		"native cursor (plain first)":   fixture2295GhostNative,
	}
}

func TestComposerBodyIsSuggestion_Claude2295Renderings(t *testing.T) {
	for name, row := range ghostFixtures() {
		if !ComposerBodyIsSuggestion(pane(row)) {
			t.Errorf("%s: a 2.1.295 prompt suggestion must read as a suggestion: %q", name, row)
		}
	}
}

// The suggestion is the composer's placeholder: the composer is EMPTY. It is
// never typed user text and never a draft that holds a send.
func TestComposerDraft_Claude2295SuggestionIsEmpty(t *testing.T) {
	for name, row := range ghostFixtures() {
		draft, visible := ComposerDraft(pane(row), stripANSI)
		if !visible || draft != "" {
			t.Errorf("%s: want empty visible composer, got draft=%q visible=%v", name, draft, visible)
		}
		if ComposerHasDraft(pane(row), stripANSI) {
			t.Errorf("%s: a suggestion must not count as a draft", name)
		}
	}
}

func TestGuardComposerDraft_Claude2295SuggestionDoesNotBlock(t *testing.T) {
	for name, row := range ghostFixtures() {
		target := &scriptedGuardTarget{captures: []string{pane(row)}}
		res := GuardComposerDraft(target, ComposerGuardOptions{HoldWait: 50 * time.Millisecond, PollInterval: 10 * time.Millisecond, Strip: stripANSI})
		if res.Refused {
			t.Errorf("%s: an idle composer showing a suggestion must not refuse delivery", name)
		}
		if !res.SuggestionShown {
			t.Errorf("%s: guard must report the suggestion so the caller gates its Enter", name)
		}
	}
	res := GuardComposerDraft(&scriptedGuardTarget{captures: []string{pane(fixtureEmptyComposer)}}, ComposerGuardOptions{HoldWait: 50 * time.Millisecond, Strip: stripANSI})
	if res.Refused || res.SuggestionShown {
		t.Fatalf("empty composer: want proceed without suggestion, got %+v", res)
	}
}

// Real input never becomes a suggestion just because it sits next to a
// cursor cell or an inline completion.
func TestComposerBodyIsSuggestion_TypedInputIsNot(t *testing.T) {
	for name, row := range map[string]string{
		"typed, cursor at end":                 "❯ run the tests\x1b[7m \x1b[0m",
		"typed, cursor on first char":          "❯ \x1b[7mr\x1b[0mun the tests",
		"slash command + inline completion":    "❯ /co\x1b[7mm\x1b[0;2mpact\x1b[0m",
		"one-char slash + dim completion":      "❯ /\x1b[2mcompact\x1b[0m",
		"one-char mention + dim completion":    "❯ @\x1b[2msrc/main.go\x1b[0m",
		"typed word then dim completion":       "❯ run\x1b[2m the tests\x1b[0m",
		"inverse cursor cell then typed text":  "❯ \x1b[7mR\x1b[27mun the tests",
		"single plain char and one dim glyph":  "❯ R\x1b[2mu\x1b[0m",
		"inverse cell alone (empty composer)":  "❯ \x1b[7m \x1b[0m",
		"inverse first then mixed plain tail":  "❯ \x1b[7mR\x1b[0;2mun\x1b[0m the tests",
		"submitted history line (bright text)": fixtureSubmittedHistory,
	} {
		if ComposerBodyIsSuggestion(pane(row)) {
			t.Errorf("%s: must not read as a suggestion: %q", name, row)
		}
	}
}

// The incident hazard: no automated bare Enter while a suggestion is shown.
func TestNudgeEnter_WithheldWhileSuggestionShown(t *testing.T) {
	for name, row := range ghostFixtures() {
		presses := 0
		p := enterPresserFunc(func() error { presses++; return nil })
		if attrib().NudgeEnter(p, Captured(pane(row)), stripANSI) || presses != 0 {
			t.Errorf("%s: bare Enter pressed while a prompt suggestion was shown (presses=%d)", name, presses)
		}
		if !attrib().BareEnterBlocked(Captured(pane(row)), stripANSI) {
			t.Errorf("%s: BareEnterBlocked must hold while a suggestion is shown", name)
		}
	}
	for name, row := range map[string]string{
		"older dim autosuggestion": fixtureGhostComposer,
		"grey autosuggestion":      fixtureGreyBrightBlackComposer,
	} {
		presses := 0
		p := enterPresserFunc(func() error { presses++; return nil })
		if attrib().NudgeEnter(p, Captured(pane(row)), stripANSI) || presses != 0 {
			t.Errorf("%s: bare Enter pressed while a suggestion was shown", name)
		}
	}
	// Still pressed where it is the recovery: our own message stuck in the
	// composer, or an empty composer.
	presses := 0
	p := enterPresserFunc(func() error { presses++; return nil })
	if !attrib().NudgeEnter(p, Captured(pane("\x1b[39m❯ "+attribMessage)), stripANSI) {
		t.Fatal("our own stuck message must still receive the recovery Enter")
	}
	if !attrib().NudgeEnter(p, Captured(pane(fixtureEmptyComposer)), stripANSI) || presses != 2 {
		t.Fatalf("empty composer must still be nudgeable (presses=%d)", presses)
	}
}

// A suggestion can repeat the message just sent. It is not that message
// stuck in the composer, so no "unsent" recovery Enter may aim at it.
func TestComposerHoldsUnsentMessage_SuggestionEchoingMessageIsNot(t *testing.T) {
	echo := "❯ \x1b[7mR\x1b[0;2mun the release checklist and tag the build now\x1b[0m"
	if !HasUnsentComposerPrompt(stripANSI(pane(echo)), ghostSuggestionText) {
		t.Fatal("precondition: the stripped pane loosely matches the message")
	}
	if ComposerHoldsUnsentMessage(pane(echo), ghostSuggestionText, stripANSI) {
		t.Fatal("a suggestion echoing the message must not read as our unsent message")
	}
	if !ComposerHoldsUnsentMessage(pane("\x1b[39m❯ "+ghostSuggestionText), ghostSuggestionText, stripANSI) {
		t.Fatal("the message typed as plain text is unsent")
	}
}

func TestTypedComposerCheck(t *testing.T) {
	const msg = "deploy the docs site"
	typed := pane("\x1b[39m❯ " + msg)
	ghost := pane(fixture2295GhostFocused)
	other := pane("\x1b[39m❯ something the operator typed")

	t.Run("typed message passes on the first capture", func(t *testing.T) {
		calls := 0
		ok, err := TypedComposerCheck(func() (string, error) { calls++; return typed, nil }, msg, stripANSI, nil)(typed, nil)
		if !ok || err != nil || calls != 0 {
			t.Fatalf("ok=%v err=%v recaptures=%d", ok, err, calls)
		}
	})
	t.Run("waits for the repaint that replaces the suggestion", func(t *testing.T) {
		seq := []string{ghost, typed}
		i := 0
		ok, err := TypedComposerCheck(func() (string, error) { s := seq[i]; i++; return s, nil }, msg, stripANSI, nil)(ghost, nil)
		if !ok || err != nil {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	})
	t.Run("suggestion still shown defers Enter without failing", func(t *testing.T) {
		ok, err := TypedComposerCheck(func() (string, error) { return ghost, nil }, msg, stripANSI, nil)(ghost, nil)
		if ok || !errors.Is(err, ErrSubmitDeferred) || errors.Is(err, ErrComposerNotOurMessage) {
			t.Fatalf("want Enter deferred with ErrSubmitDeferred, got ok=%v err=%v", ok, err)
		}
	})
	t.Run("other text withholds Enter", func(t *testing.T) {
		ok, err := TypedComposerCheck(func() (string, error) { return other, nil }, msg, stripANSI, nil)(other, nil)
		if ok || !errors.Is(err, ErrComposerNotOurMessage) {
			t.Fatalf("want withheld, got ok=%v err=%v", ok, err)
		}
	})
	t.Run("capture failure and empty composer proceed; inner verdict kept", func(t *testing.T) {
		innerCalls := 0
		inner := func(string, error) (bool, error) { innerCalls++; return false, errors.New("inner") }
		if ok, _ := TypedComposerCheck(nil, msg, stripANSI, nil)("", errors.New("gone")); !ok {
			t.Fatal("capture failure must proceed")
		}
		if ok, _ := TypedComposerCheck(nil, msg, stripANSI, nil)(pane(fixtureEmptyComposer), nil); !ok {
			t.Fatal("empty composer must proceed")
		}
		if ok, err := TypedComposerCheck(nil, msg, stripANSI, inner)(typed, nil); ok || err == nil || err.Error() != "inner" || innerCalls != 1 {
			t.Fatalf("inner verdict must be returned: ok=%v err=%v calls=%d", ok, err, innerCalls)
		}
	})
	t.Run("wrapped long message passes", func(t *testing.T) {
		long := strings.Repeat("word ", 30)
		wrapped := pane("\x1b[39m❯ "+long[:60], "  "+long[60:])
		if ok, err := TypedComposerCheck(nil, long, stripANSI, nil)(wrapped, nil); !ok {
			t.Fatalf("wrapped typed message must pass: %v", err)
		}
	})
}

type scriptedGuardTarget struct {
	captures []string
	i        int
}

func (s *scriptedGuardTarget) CapturePaneFresh() (string, error) {
	c := s.captures[len(s.captures)-1]
	if s.i < len(s.captures) {
		c = s.captures[s.i]
	}
	s.i++
	return c, nil
}

type countingPresser struct{ enters int }

func (c *countingPresser) SendEnter() error { c.enters++; return nil }

// SubmitWhenTyped finishes a deferred submit: one Enter once the composer
// holds our message, none while a suggestion is shown, none at other text.
func TestSubmitWhenTyped(t *testing.T) {
	const msg = "deploy the docs site"
	typed := pane("\x1b[39m❯ " + msg)
	ghost := pane(fixture2295GhostFocused)
	other := pane("\x1b[39m❯ something the operator typed")
	seqCapture := func(seq ...string) func() (string, error) {
		i := 0
		return func() (string, error) {
			s := seq[len(seq)-1]
			if i < len(seq) {
				s = seq[i]
			}
			i++
			return s, nil
		}
	}

	t.Run("presses once after a slow repaint", func(t *testing.T) {
		p := &countingPresser{}
		seq := []string{}
		for i := 0; i < 30; i++ {
			seq = append(seq, ghost)
		}
		seq = append(seq, typed)
		if pressed, err := SubmitWhenTyped(p, seqCapture(seq...), msg, stripANSI, nil, 40, 0); !pressed || err != nil || p.enters != 1 {
			t.Fatalf("want exactly one Enter once the message is painted, got %d", p.enters)
		}
	})
	t.Run("never presses while only the suggestion is shown", func(t *testing.T) {
		p := &countingPresser{}
		if pressed, _ := SubmitWhenTyped(p, seqCapture(ghost), msg, stripANSI, nil, 20, 0); pressed || p.enters != 0 {
			t.Fatalf("bare Enter pressed %d time(s) at a prompt suggestion", p.enters)
		}
	})
	t.Run("never presses at other text", func(t *testing.T) {
		p := &countingPresser{}
		if pressed, _ := SubmitWhenTyped(p, seqCapture(other), msg, stripANSI, nil, 20, 0); pressed || p.enters != 0 {
			t.Fatalf("Enter pressed %d time(s) at text that is not ours", p.enters)
		}
	})
	// A deferred submit keeps the #2079 truncation guard a direct Enter has:
	// a paste marker declaring fewer line breaks than the message refuses,
	// and an intact one is left to the caller's provenance-aware verify loop.
	marker := func(lines int) string {
		return pane("\x1b[39m❯ [Pasted text #1 +" + strconv.Itoa(lines) + " lines]")
	}
	t.Run("refuses a truncated paste marker", func(t *testing.T) {
		p := &countingPresser{}
		pressed, err := SubmitWhenTyped(p, seqCapture(ghost, marker(2)), msg, stripANSI, PasteTruncationCheck(5), 20, 0)
		if pressed || p.enters != 0 || err == nil || !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("truncated paste: pressed=%v enters=%d err=%v; want a refusal and no Enter", pressed, p.enters, err)
		}
	})
	t.Run("hands an intact paste marker to the verify loop", func(t *testing.T) {
		p := &countingPresser{}
		pressed, err := SubmitWhenTyped(p, seqCapture(ghost, marker(5)), msg, stripANSI, PasteTruncationCheck(5), 20, 0)
		if pressed || p.enters != 0 || err != nil {
			t.Fatalf("intact paste: pressed=%v enters=%d err=%v; want no Enter and no error", pressed, p.enters, err)
		}
	})
}
