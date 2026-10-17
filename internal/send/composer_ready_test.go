package send

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// readyPane renders a Claude Code 2.1.296 screen the way tmux capture-pane -e
// returns it: transcript lines, the composer between two dividers, then the
// footer (status line or picker rows).
func readyPane(transcript []string, composerBody string, footer ...string) string {
	div := strings.Repeat("─", 60)
	lines := append([]string{" ▐▛███▛█   Claude Code v2.1.296"}, transcript...)
	lines = append(lines, div, "\x1b[39m❯ "+composerBody, div)
	if len(footer) == 0 {
		footer = []string{"  [personal] ashesh@mac:/tmp/x | [Haiku 5.5] ctx:0%", "  ⏵⏵ bypass permissions on (shift+tab to cycle)"}
	}
	return strings.Join(append(lines, footer...), "\n")
}

func TestClaudeComposerReady_EmptyComposerIsReady(t *testing.T) {
	r := ClaudeComposerReady(readyPane(nil, ""))
	if !r.Ready || r.Code != "" {
		t.Fatalf("empty idle composer must be ready, got %+v", r)
	}
}

func TestClaudeComposerReady_GhostSuggestionCountsAsEmpty(t *testing.T) {
	// Live capture, lab/evidence/exp11-ghost-live.ansi.
	r := ClaudeComposerReady(readyPane([]string{"⏺ calc.py defines add(a, b)."}, "\x1b[2madd a subtract function\x1b[0m"))
	if !r.Ready || !r.Ghost {
		t.Fatalf("dim ghost suggestion must read as an empty composer, got %+v", r)
	}
}

func TestClaudeComposerReady_IdleHintCountsAsEmpty(t *testing.T) {
	r := ClaudeComposerReady(readyPane(nil, `Try "how do I log an error?"`))
	if !r.Ready || !r.Ghost {
		t.Fatalf("Try hint must read as an empty composer, got %+v", r)
	}
}

func TestClaudeComposerReady_OperatorDraftRefuses(t *testing.T) {
	r := ClaudeComposerReady(readyPane(nil, "half typed note to self"))
	if r.Ready || r.Code != ComposerCodeDraft {
		t.Fatalf("operator draft must refuse with %q, got %+v", ComposerCodeDraft, r)
	}
}

func TestClaudeComposerReady_BusyTurnRefuses(t *testing.T) {
	pane := readyPane([]string{"✻ Brewing… (3s · esc to interrupt)"}, "")
	r := ClaudeComposerReady(pane)
	if r.Ready || r.Code != ComposerCodeBusy {
		t.Fatalf("busy turn must refuse with %q, got %+v", ComposerCodeBusy, r)
	}
}

func TestClaudeComposerReady_BusyTextFarUpInTranscriptIsIgnored(t *testing.T) {
	transcript := []string{"❯ what does esc to interrupt mean?"}
	for i := 0; i < 20; i++ {
		transcript = append(transcript, "⏺ line of the answer")
	}
	r := ClaudeComposerReady(readyPane(transcript, ""))
	if !r.Ready {
		t.Fatalf("an old transcript mention of the busy hint must not block, got %+v", r)
	}
}

func TestClaudeComposerReady_SelectionDialogRefuses(t *testing.T) {
	// Live capture of the folder trust dialog: Enter here selects "No, exit".
	pane := strings.Join([]string{
		" Quick safety check: Is this a project you created or one you trust?",
		" ❯ No, exit",
		"   Yes, I trust this folder",
		" Enter to confirm · Esc to cancel",
	}, "\n")
	r := ClaudeComposerReady(pane)
	if r.Ready || r.Code != ComposerCodeDialog {
		t.Fatalf("selection dialog must refuse with %q, got %+v", ComposerCodeDialog, r)
	}
}

func TestClaudeComposerReady_NoComposerRefuses(t *testing.T) {
	r := ClaudeComposerReady("ashesh@mac ~ % ")
	if r.Ready || r.Code != ComposerCodeNoPrompt {
		t.Fatalf("pane without a composer must refuse with %q, got %+v", ComposerCodeNoPrompt, r)
	}
}

func TestClaudeComposerReady_BusyFoundUnderATitledTopBorder(t *testing.T) {
	// The spinner is only reachable through the composer's top border: the
	// footer below the composer (status line plus a background agent list)
	// is longer than busyLookback, so a dashes-only border match falls back
	// to the bottom lines and misses it.
	div := strings.Repeat("─", 60)
	lines := []string{"⏺ working on it", "✻ Skedaddling… (12s · ↓ 1.2k tokens)", div + " my-session ─", "❯ ", div}
	for i := 0; i < busyLookback+2; i++ {
		lines = append(lines, "  ◯ background agent "+strings.Repeat("x", i))
	}
	if r := ClaudeComposerReady(strings.Join(lines, "\n")); r.Code != ComposerCodeBusy {
		t.Fatalf("spinner above a titled top border must read as busy, got %+v", r)
	}
}

func TestClaudeComposerReady_SwitchModelConfirmationIsADialog(t *testing.T) {
	// Live capture lab/evidence/exp16-switch-model-confirm.txt: no footer hint.
	pane := strings.Join([]string{
		"❯ /model claude-fable-5-1",
		strings.Repeat("─", 60),
		"  Switch model?",
		"  Your next response will be slower and use more tokens",
		"  ❯ 1. Yes, switch to Fable 5.1",
		"    2. No, go back",
	}, "\n")
	r := ClaudeComposerReady(pane)
	if r.Ready || r.Code != ComposerCodeDialog {
		t.Fatalf("numbered selection must refuse as a dialog, got %+v", r)
	}
	if sel, ok := DialogSelection(pane); !ok || sel != "Yes, switch to Fable 5.1" {
		t.Fatalf("DialogSelection = %q, %v", sel, ok)
	}
}

// Live captures from Claude Code 2.1.296 (harness-control lab): a short screen
// right after /clear whose top border carries the session name, and an idle
// composer showing a dim ghost suggestion.
func TestClaudeComposerReady_LiveCaptures(t *testing.T) {
	for _, c := range []struct {
		file  string
		ghost bool
	}{
		{"testdata/after-clear-2.1.296.ansi", false},
		{"testdata/ghost-2.1.296.ansi", true},
	} {
		raw, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		r := ClaudeComposerReady(string(raw))
		if !r.Ready || r.Ghost != c.ghost {
			t.Errorf("%s: want ready (ghost=%v), got %+v", c.file, c.ghost, r)
		}
	}
}

func TestCurrentComposerPrompt_TitledTopBorderOnAShortScreen(t *testing.T) {
	raw, err := os.ReadFile("testdata/after-clear-2.1.296.ansi")
	if err != nil {
		t.Fatal(err)
	}
	body, ok := CurrentComposerPrompt(tmux.StripANSI(string(raw)))
	if !ok || body != "" {
		t.Fatalf("composer must be found and empty, got %q, %v", body, ok)
	}
}

// Claude Code 2.1.296 shows a running turn (here a tool call) as a spinner
// line with an ellipsis and elapsed time, without "esc to interrupt".
func TestClaudeComposerReady_SpinnerWithoutInterruptHintIsBusy(t *testing.T) {
	raw, err := os.ReadFile("testdata/busy-tool-running-2.1.296.ansi")
	if err != nil {
		t.Fatal(err)
	}
	if r := ClaudeComposerReady(string(raw)); r.Code != ComposerCodeBusy {
		t.Fatalf("live spinner capture must read as busy, got %+v", r)
	}
}

func TestClaudeComposerReady_FinishedTurnLineIsNotBusy(t *testing.T) {
	r := ClaudeComposerReady(readyPane([]string{"⏺ 2 3 5", "✻ Brewed for 6s · done 4:11 PM"}, ""))
	if !r.Ready {
		t.Fatalf("a finished-turn summary line must not block, got %+v", r)
	}
}

// Composer states from the earlier idle check, as one table: a
// prompt suggestion in either rendering, a typed draft, a pasted text marker
// (an operator draft like any other text), a permission menu, a pane with no
// composer, and the spinner forms with and without the interrupt hint.
func TestClaudeComposerReady_ComposerStates(t *testing.T) {
	const rule = "────────────────────────────────────────"
	frame := func(above, composer string) string {
		return above + "\n" + rule + "\n" + composer + "\n" + rule + "\n  ⏵⏵ bypass permissions on · ← for agents\n"
	}
	done := "⏺ Done.\n\n✻ Crunched for 45s · done 10:20 PM"
	cases := []struct {
		name  string
		raw   string
		code  string
		ghost bool
	}{
		{"empty idle composer", frame(done, "❯ "), "", false},
		{"ghost suggestion unfocused", frame(done, fixture2295GhostUnfocused), "", true},
		{"ghost suggestion with inverse cursor", frame(done, fixture2295GhostFocused), "", true},
		{"typed draft", frame(done, "❯ fix the flaky test"), ComposerCodeDraft, false},
		{"pasted text marker", frame(done, "❯ [Pasted text #1 +20 lines]"), ComposerCodeDraft, false},
		{"turn running with interrupt hint", frame("✻ Crunching… (12s · esc to interrupt)", "❯ "), ComposerCodeBusy, false},
		{"turn running without interrupt hint", frame("⏺ Running the lanes.\n\n✽ Thundering… (34s · ↓ 135 tokens)", "❯ "), ComposerCodeBusy, false},
		{"finished turn in minutes", frame("⏺ Done.\n\n✻ Baked for 3m 5s", "❯ "), "", false},
		{"permission menu", "⏺ Bash(make test)\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n", ComposerCodeDialog, false},
		{"no composer", "$ ls\nREADME.md\n", ComposerCodeNoPrompt, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := ClaudeComposerReady(tc.raw)
			if r.Code != tc.code || r.Ready != (tc.code == "") || r.Ghost != tc.ghost {
				t.Fatalf("got %+v, want code %q ghost %v", r, tc.code, tc.ghost)
			}
		})
	}
}

// ComposerEmptyWhileBusy is the guarded send's check for a Claude turn that is
// running: Claude Code queues typed input, so a busy turn above an empty
// framed composer is accepted, and every other reason ClaudeComposerReady
// refuses for still refuses. The busy screen is the macOS app audit's
// Claude-like fake (F1-DECIDER run3), which draws Claude 2.1's spinner line
// without an "esc to interrupt" hint.
func TestComposerEmptyWhileBusy(t *testing.T) {
	const rule = "────────────────────────────────────────"
	frame := func(above, composer string) string {
		return above + "\n\n" + rule + "\n" + composer + "\n" + rule + "\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	}
	busy := "⏺ Running the lanes.\n\n✽ Thundering… (34s · ↓ 135 tokens)"
	legacyBusy := "⏺ Running the lanes.\n\n✻ Crunching… (12s · esc to interrupt)"
	cases := []struct {
		name  string
		raw   string
		code  string
		ghost bool
	}{
		{"busy empty framed composer", frame(busy, "❯ "), "", false},
		{"busy with esc to interrupt", frame(legacyBusy, "❯ "), "", false},
		{"busy under a titled top border", busy + "\n\n" + rule + " my-session ─\n❯ \n" + rule + "\n", "", false},
		{"idle empty composer", frame("⏺ Done.", "❯ "), "", false},
		{"busy ghost suggestion", frame(busy, fixture2295GhostFocused), "", true},
		{"busy operator draft", frame(busy, "❯ half typed operator note"), ComposerCodeDraft, false},
		{"busy pasted draft", frame(busy, "❯ [Pasted text #1 +12 lines]"), ComposerCodeDraft, false},
		{"busy permission dialog", busy + "\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n", ComposerCodeDialog, false},
		{"busy picker", busy + "\n\nWhich approach should I take?\n❯ 1. Yes\n  2. No\nEnter to select · Esc to cancel\n", ComposerCodeDialog, false},
		{"prompt glyph without a frame", busy + "\n\n❯ \n", ComposerCodeNoPrompt, false},
		{"no composer", "$ ls\nREADME.md\n", ComposerCodeNoPrompt, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := ComposerEmptyWhileBusy(tc.raw)
			if r.Code != tc.code || r.Ready != (tc.code == "") || r.Ghost != tc.ghost {
				t.Fatalf("got %+v, want code %q ghost %v", r, tc.code, tc.ghost)
			}
		})
	}
	// The idle check keeps refusing a running turn it can see, with or
	// without the interrupt hint.
	for _, above := range []string{busy, legacyBusy} {
		if r := ClaudeComposerReady(frame(above, "❯ ")); r.Code != ComposerCodeBusy {
			t.Fatalf("ClaudeComposerReady on a running turn: got %+v, want %q", r, ComposerCodeBusy)
		}
	}
}

func TestProbeComposerBeforeSend(t *testing.T) {
	const rule = "────────────────────────────────────────"
	pane := func(row string) func() (string, error) {
		return func() (string, error) { return "out\n" + rule + "\n" + row + "\n" + rule + "\n", nil }
	}
	if free, shown := ProbeComposerBeforeSend(pane(fixture2295GhostFocused), true); !free || !shown {
		t.Fatalf("claude ghost: pasteFree=%v suggestion=%v, want true true", free, shown)
	}
	if _, shown := ProbeComposerBeforeSend(pane(fixture2295GhostFocused), false); shown {
		t.Fatal("the suggestion rule must not apply to a non-Claude composer")
	}
	if free, _ := ProbeComposerBeforeSend(pane("❯ [Pasted text #1 +3 lines]"), true); free {
		t.Fatal("a parked paste marker is not paste-free")
	}
	if free, shown := ProbeComposerBeforeSend(func() (string, error) { return "", errors.New("gone") }, true); free || shown {
		t.Fatal("a capture failure must report nothing")
	}
}
