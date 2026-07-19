package send

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// The fixtures below are the VERBATIM bytes captured from a live Claude Code
// 2.1.212 session (agent-deck v1.9.73) via `tmux capture-pane -p -e` while
// reproducing the ghost-submit bug on 2026-07-19. Keep them byte-exact —
// they are the regression evidence for this class of defect.
const (
	// Composer holding Claude's dim autosuggestion (the ghost).
	fixtureGhostComposer = "\x1b[39m❯ \x1b[2mNow reply with exactly the word: pineapple\x1b[0m"

	// Composer holding a real operator draft — no dim attribute anywhere.
	fixtureRealDraftComposer = "\x1b[39m❯ REALDRAFT typed by a human"

	// Composer with nothing in it.
	fixtureEmptyComposer = "\x1b[39m❯ "

	// An ALREADY-SUBMITTED message line (scrollback history), rendered in
	// bright white on a highlight background. Must never read as dim.
	fixtureSubmittedHistory = "\x1b[38;5;239m\x1b[48;5;237m❯ \x1b[38;5;231mNow reply with exactly the word: mango\x1b[39m"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// pane wraps composer lines in the surrounding pane furniture.
func pane(composerLines ...string) string {
	div := "\x1b[38;5;244m" + strings.Repeat("─", 40) + "\x1b[39m"
	return "some prior output\n" + fixtureSubmittedHistory + "\n" + div + "\n" +
		strings.Join(composerLines, "\n") + "\n" + div + "\n  auto mode on\n"
}

func TestComposerBodyIsSuggestion_DimGhostIsDetected(t *testing.T) {
	if !ComposerBodyIsSuggestion(pane(fixtureGhostComposer)) {
		t.Fatal("dim autosuggestion must be detected as a suggestion")
	}
}

func TestComposerBodyIsSuggestion_RealDraftIsNotASuggestion(t *testing.T) {
	if ComposerBodyIsSuggestion(pane(fixtureRealDraftComposer)) {
		t.Fatal("a real operator draft must NOT be classified as a suggestion")
	}
}

func TestComposerBodyIsSuggestion_EmptyComposer(t *testing.T) {
	if ComposerBodyIsSuggestion(pane(fixtureEmptyComposer)) {
		t.Fatal("empty composer must not be classified as a suggestion")
	}
}

// The composer is the LAST marker line; a dim-rendered line earlier in the
// scrollback must not leak into the classification.
func TestComposerBodyIsSuggestion_UsesLastMarkerLine(t *testing.T) {
	stale := "\x1b[39m❯ \x1b[2mstale suggestion from earlier\x1b[0m"
	if ComposerBodyIsSuggestion(pane(stale, fixtureRealDraftComposer)) {
		t.Fatal("classification must use the last marker line, not stale scrollback")
	}
}

// Regression: SGR 38;5;N is an extended-colour introducer. Colour index 2
// must not be mistaken for the dim attribute.
func TestComposerBodyIsSuggestion_ExtendedColourIndexTwoIsNotDim(t *testing.T) {
	line := "\x1b[39m❯ \x1b[38;5;2mgreen but genuinely typed"
	if ComposerBodyIsSuggestion(pane(line)) {
		t.Fatal("38;5;2 is colour index 2, not the dim attribute")
	}
}

// Regression: truecolour 38;2;R;G;B likewise consumes its own parameters.
func TestComposerBodyIsSuggestion_TrueColourIsNotDim(t *testing.T) {
	line := "\x1b[39m❯ \x1b[38;2;10;20;30mtruecolour typed text"
	if ComposerBodyIsSuggestion(pane(line)) {
		t.Fatal("38;2;R;G;B is truecolour, not the dim attribute")
	}
}

func TestComposerBodyIsSuggestion_DimResetBeforeBodyIsNotASuggestion(t *testing.T) {
	line := "\x1b[2m\x1b[22m❯ text after dim was reset"
	if ComposerBodyIsSuggestion(pane(line)) {
		t.Fatal("SGR 22 resets dim; body must read as real input")
	}
}

func TestComposerDraft_GhostYieldsNoDraft(t *testing.T) {
	draft, visible := ComposerDraft(pane(fixtureGhostComposer), stripANSI)
	if !visible {
		t.Fatal("expected the composer to be visible")
	}
	if draft != "" {
		t.Fatalf("autosuggestion must yield an empty draft, got %q", draft)
	}
}

func TestComposerDraft_RealDraftPreserved(t *testing.T) {
	draft, visible := ComposerDraft(pane(fixtureRealDraftComposer), stripANSI)
	if !visible {
		t.Fatal("expected the composer to be visible")
	}
	if draft != "REALDRAFT typed by a human" {
		t.Fatalf("real operator draft must be preserved verbatim, got %q", draft)
	}
}

func TestComposerHasDraft_DimVsReal(t *testing.T) {
	if ComposerHasDraft(pane(fixtureGhostComposer), stripANSI) {
		t.Fatal("autosuggestion must not report a draft")
	}
	if !ComposerHasDraft(pane(fixtureRealDraftComposer), stripANSI) {
		t.Fatal("real draft must report a draft")
	}
}

// THE regression test for the reported bug: with only a dim autosuggestion in
// the composer, the guard must pass straight through — no hold, no Ctrl+C, and
// crucially no SavedDraft, because SavedDraft is what executeSend later types
// back into the composer as real committable text.
func TestGuardComposerDraft_IgnoresDimAutosuggestion(t *testing.T) {
	target := &fakeGuardTarget{captures: []string{pane(fixtureGhostComposer)}}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: time.Second, PollInterval: time.Millisecond, ClearWait: time.Millisecond,
		Strip: stripANSI,
	})
	if res.SavedDraft != "" {
		t.Fatalf("autosuggestion must never be saved as a draft (it would be retyped as real text), got %q", res.SavedDraft)
	}
	if res.DraftCleared || res.ClearFailed {
		t.Fatalf("autosuggestion must not trigger the clear path, got %+v", res)
	}
	if target.ctrlCCalls != 0 {
		t.Fatalf("must not Ctrl+C for an autosuggestion, got %d calls", target.ctrlCCalls)
	}
	if target.captureCalls != 1 {
		t.Fatalf("expected a single capture, got %d", target.captureCalls)
	}
}

// The #1409 protection must survive intact: a real draft carrying ANSI colour
// is still held, saved and cleared.
func TestGuardComposerDraft_StillGuardsRealDraftWithANSI(t *testing.T) {
	target := &fakeGuardTarget{
		captures:     []string{pane(fixtureRealDraftComposer)},
		clearOnCtrlC: true,
	}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: 10 * time.Millisecond, PollInterval: time.Millisecond, ClearWait: 50 * time.Millisecond,
		Strip: stripANSI,
	})
	if res.SavedDraft != "REALDRAFT typed by a human" {
		t.Fatalf("real draft must still be saved for restore, got %q", res.SavedDraft)
	}
	if !res.DraftCleared {
		t.Fatalf("real draft must still be cleared before delivery, got %+v", res)
	}
	if target.ctrlCCalls == 0 {
		t.Fatal("expected Ctrl+C for a real operator draft")
	}
}
