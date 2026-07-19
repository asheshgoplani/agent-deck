package send

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// renderComposer renders a minimal Claude-style composer block holding text as
// the current input. An empty text renders the empty composer ("❯ ").
func renderComposer(text string) string {
	div := strings.Repeat("─", 40)
	return "some prior output\n" + div + "\n❯ " + text + "\n" + div + "\n  auto mode on\n"
}

func TestComposerDraft_EmptyComposer(t *testing.T) {
	draft, visible := ComposerDraft(renderComposer(""), nil)
	if !visible {
		t.Fatal("expected composer to be visible")
	}
	if draft != "" {
		t.Fatalf("expected empty draft, got %q", draft)
	}
}

func TestComposerDraft_WithOperatorDraft(t *testing.T) {
	draft, visible := ComposerDraft(renderComposer("instruct deploy ag"), nil)
	if !visible {
		t.Fatal("expected composer to be visible")
	}
	if draft != "instruct deploy ag" {
		t.Fatalf("expected operator draft, got %q", draft)
	}
}

func TestComposerDraft_SuggestionPlaceholderIsNotADraft(t *testing.T) {
	// Claude's idle composer shows a hint suggestion like:
	//   ❯ Try "write a test for <filepath>"
	// which must not be treated as operator input (issue #1409).
	draft, visible := ComposerDraft(renderComposer(`Try "write a test for <filepath>"`), nil)
	if !visible {
		t.Fatal("expected composer to be visible")
	}
	if draft != "" {
		t.Fatalf("expected placeholder to yield empty draft, got %q", draft)
	}
}

func TestComposerDraft_NoComposerVisible(t *testing.T) {
	_, visible := ComposerDraft("plain shell output\nno prompt here\n", nil)
	if visible {
		t.Fatal("did not expect a composer in plain output")
	}
}

func TestComposerHasDraft(t *testing.T) {
	if ComposerHasDraft(renderComposer(""), nil) {
		t.Fatal("empty composer must not report a draft")
	}
	if !ComposerHasDraft(renderComposer("half-typed message"), nil) {
		t.Fatal("expected composer draft to be reported")
	}
	if ComposerHasDraft(renderComposer(`Try "fix lint errors"`), nil) {
		t.Fatal("placeholder must not report a draft")
	}
	if ComposerHasDraft("plain output, no composer", nil) {
		t.Fatal("no composer must not report a draft")
	}
}

// fakeGuardTarget scripts pane captures for GuardComposerDraft. captures[i] is
// returned by the i-th CapturePaneFresh call; the last entry repeats once
// exhausted. When clearOnCtrlC is set, every capture after a SendCtrlC call
// returns an empty composer (simulating Claude clearing the input on Ctrl+C).
type fakeGuardTarget struct {
	captures     []string
	captureErr   error
	ctrlCErr     error
	clearOnCtrlC bool

	captureCalls int
	ctrlCCalls   int
}

func (f *fakeGuardTarget) CapturePaneFresh() (string, error) {
	i := f.captureCalls
	f.captureCalls++
	if f.captureErr != nil {
		return "", f.captureErr
	}
	if f.clearOnCtrlC && f.ctrlCCalls > 0 {
		return renderComposer(""), nil
	}
	if len(f.captures) == 0 {
		return "", nil
	}
	if i >= len(f.captures) {
		i = len(f.captures) - 1
	}
	return f.captures[i], nil
}

func (f *fakeGuardTarget) SendCtrlC() error {
	f.ctrlCCalls++
	return f.ctrlCErr
}

func TestGuardComposerDraft_ProceedsImmediatelyWhenComposerEmpty(t *testing.T) {
	target := &fakeGuardTarget{captures: []string{renderComposer("")}}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: time.Second, PollInterval: time.Millisecond, ClearWait: time.Millisecond,
	})
	if res.SavedDraft != "" || res.DraftCleared || res.ClearFailed {
		t.Fatalf("expected clean pass-through, got %+v", res)
	}
	if target.captureCalls != 1 {
		t.Fatalf("expected a single capture, got %d", target.captureCalls)
	}
	if target.ctrlCCalls != 0 {
		t.Fatalf("must not Ctrl+C an empty composer, got %d calls", target.ctrlCCalls)
	}
}

func TestGuardComposerDraft_ProceedsWhenNoComposerVisible(t *testing.T) {
	target := &fakeGuardTarget{captures: []string{"codex>\nplain output\n"}}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: time.Second, PollInterval: time.Millisecond, ClearWait: time.Millisecond,
	})
	if res.SavedDraft != "" || target.ctrlCCalls != 0 {
		t.Fatalf("expected pass-through for non-introspectable pane, got %+v (ctrlC=%d)", res, target.ctrlCCalls)
	}
}

func TestGuardComposerDraft_HoldsUntilOperatorDraftClears(t *testing.T) {
	target := &fakeGuardTarget{captures: []string{
		renderComposer("instruct deploy ag"),
		renderComposer("instruct deploy ag"),
		renderComposer(""), // operator submitted or cleared their draft
	}}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: 5 * time.Second, PollInterval: time.Millisecond, ClearWait: time.Millisecond,
	})
	if res.SavedDraft != "" {
		t.Fatalf("draft cleared on its own; nothing should be saved, got %q", res.SavedDraft)
	}
	if target.ctrlCCalls != 0 {
		t.Fatalf("must not Ctrl+C when the hold succeeds, got %d calls", target.ctrlCCalls)
	}
	if target.captureCalls != 3 {
		t.Fatalf("expected 3 captures (held twice), got %d", target.captureCalls)
	}
}

func TestGuardComposerDraft_SaveClearAtHoldBound(t *testing.T) {
	target := &fakeGuardTarget{
		captures:     []string{renderComposer("instruct deploy ag")},
		clearOnCtrlC: true,
	}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: 0, PollInterval: time.Millisecond, ClearWait: 50 * time.Millisecond,
	})
	if res.SavedDraft != "instruct deploy ag" {
		t.Fatalf("expected operator draft to be saved, got %q", res.SavedDraft)
	}
	if !res.DraftCleared {
		t.Fatal("expected DraftCleared after Ctrl+C emptied the composer")
	}
	if res.ClearFailed {
		t.Fatal("did not expect ClearFailed")
	}
	if target.ctrlCCalls != 1 {
		t.Fatalf("expected exactly 1 Ctrl+C, got %d", target.ctrlCCalls)
	}
}

func TestGuardComposerDraft_ClearFailureIsBoundedAndReported(t *testing.T) {
	// Composer never clears even after Ctrl+C: the guard must give up after a
	// bounded number of attempts and report ClearFailed (delivery proceeds —
	// watchers/conductors depend on the send going through).
	target := &fakeGuardTarget{captures: []string{renderComposer("stuck draft")}}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: 0, PollInterval: time.Millisecond, ClearWait: time.Millisecond,
	})
	if res.SavedDraft != "stuck draft" {
		t.Fatalf("expected draft saved even when clear fails, got %q", res.SavedDraft)
	}
	if !res.ClearFailed || res.DraftCleared {
		t.Fatalf("expected ClearFailed without DraftCleared, got %+v", res)
	}
	if target.ctrlCCalls != 2 {
		t.Fatalf("clear attempts must be bounded at 2, got %d", target.ctrlCCalls)
	}
}

func TestGuardComposerDraft_CaptureErrorProceedsWithoutBlocking(t *testing.T) {
	target := &fakeGuardTarget{captureErr: errors.New("pane gone")}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: time.Second, PollInterval: time.Millisecond, ClearWait: time.Millisecond,
	})
	if res.SavedDraft != "" || res.ClearFailed || target.ctrlCCalls != 0 {
		t.Fatalf("capture errors must not block or mutate the pane, got %+v (ctrlC=%d)", res, target.ctrlCCalls)
	}
}

func TestGuardComposerDraft_StripIsApplied(t *testing.T) {
	// The guard receives raw pane bytes; when a Strip function is supplied it
	// must be applied before composer introspection.
	marker := "\x1b[31m"
	target := &fakeGuardTarget{captures: []string{marker + renderComposer("")}}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: time.Second, PollInterval: time.Millisecond, ClearWait: time.Millisecond,
		Strip: func(s string) string { return strings.ReplaceAll(s, marker, "") },
	})
	if res.SavedDraft != "" || target.ctrlCCalls != 0 {
		t.Fatalf("expected stripped empty composer to pass through, got %+v", res)
	}
}
