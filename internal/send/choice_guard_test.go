package send

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// menuPane is an AskUserQuestion decision menu as Claude renders it: the
// selected option carries the same "❯" marker the composer uses, so the
// composer-block parser reads the whole option list as though a human had
// typed it.
const menuPane = `  Tell me how you want each handled and I'll run it.

────────────────────────────────────────────────────────── bronze-crow-081ded03 ─
❯ 1. Reclaim now + fix the defect (Recommended)
  2. Fix the defect first, let the sweeper reclaim
  3. Reclaim now, file the defect, don't fix it
  4. Hold — I'll look at the sweeper myself
─────────────────────────────────────────────────────────────────────────────────
   Model: Opus 5  Ctx: 175.5k  ⎇ USS-3617-linear-writer  (+0,-0)  𖠰 main`

// ComposerDraft must report NO draft for a rendered menu. Everything
// destructive downstream keys off a non-empty draft: GuardComposerDraft saves
// it, Ctrl+C's the composer (which dismisses the question), and executeSend
// types the saved text back afterwards.
//
// Regression for the 2026-08-20 orchestrate stall: a conductor's heartbeat ate
// two decision prompts in 45 minutes, each time restoring the option list into
// the composer as literal characters, and the run sat blocked for over an hour
// behind a question its human was never shown.
func TestComposerDraft_MenuIsNotADraft(t *testing.T) {
	draft, visible := ComposerDraft(menuPane, tmux.StripANSI)
	if !visible {
		t.Fatal("composer region should still be visible")
	}
	if draft != "" {
		t.Fatalf("a rendered selection menu must not be treated as an operator draft; got %q", draft)
	}
	if ComposerHasDraft(menuPane, tmux.StripANSI) {
		t.Error("ComposerHasDraft must be false for a selection menu")
	}
}

// The guard's contract for a menu: capture, decide, return — no Ctrl+C. A
// single Ctrl+C is what dismissed the question.
func TestGuardComposerDraft_NeverClearsAMenu(t *testing.T) {
	target := &recordingGuardTarget{pane: menuPane}
	res := GuardComposerDraft(target, ComposerGuardOptions{
		HoldWait: 0,
		Strip:    tmux.StripANSI,
	})
	if target.ctrlC != 0 {
		t.Errorf("guard pressed Ctrl+C %d time(s) on a selection menu — that dismisses the question", target.ctrlC)
	}
	if res.SavedDraft != "" {
		t.Errorf("guard saved the menu as a draft: %q", res.SavedDraft)
	}
	if res.DraftCleared {
		t.Error("guard reported clearing a composer that held no draft")
	}
}

// The protection for genuine operator input is untouched: real typed text is
// still held, saved and cleared so an automated send cannot merge into it.
func TestGuardComposerDraft_StillGuardsRealDraft(t *testing.T) {
	const drafted = `  Done.

──────────────────────────────────────────────────────────── impl-disp-break1 ─
❯ push the branch and open the PR
───────────────────────────────────────────────────────────────────────────────
   Model: Sonnet 5  Ctx: 166.5k`

	if !ComposerHasDraft(drafted, tmux.StripANSI) {
		t.Fatal("a real operator draft must still register as a draft")
	}
	draft, _ := ComposerDraft(drafted, tmux.StripANSI)
	if !strings.Contains(draft, "push the branch") {
		t.Fatalf("operator draft not extracted, got %q", draft)
	}
}

// recordingGuardTarget is a ComposerGuardTarget that counts Ctrl+C presses and
// returns a fixed pane.
type recordingGuardTarget struct {
	pane  string
	ctrlC int
}

func (r *recordingGuardTarget) CapturePaneFresh() (string, error) { return r.pane, nil }

func (r *recordingGuardTarget) SendCtrlC() error {
	r.ctrlC++
	return nil
}
