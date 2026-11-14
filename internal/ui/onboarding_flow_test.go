package ui

// ASK-211 onboarding end to end (Sharjeel's plan and prototype): the check
// page, the welcome, the New Session form, the hints after the first session
// and the short help work together once both halves are merged.

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// First run: check page -> welcome -> '?' opens the 5 keys view -> Esc ->
// Enter opens the filled form with the hooks line -> Enter creates -> the
// created session starts the hint bar at step 1.
func TestOnboardingFlow_CheckWelcomeFormHintsHelp(t *testing.T) {
	newOnboardingEnv(t, "claude", "codex")
	h := startDeck(t)
	// NewHome decides the hooks question only with background workers on
	// (off in tests); a fresh machine has it undecided.
	h.pendingHooksPrompt = true
	if !checkVisible(h) {
		t.Fatalf("fresh install did not open the check page:\n%s", onboardingScreen(h))
	}
	if !h.hints.armed {
		t.Fatal("an empty fresh install did not arm the hint bar")
	}
	h.Update(keyEnter)
	if !h.welcomeActive() {
		t.Fatalf("Enter on the check page did not reach the welcome:\n%s", onboardingScreen(h))
	}

	// '?' on the welcome opens the short help, not the full list.
	h.Update(runeKey('?'))
	if !h.helpOverlay.IsVisible() {
		t.Fatal("? on the welcome did not open help")
	}
	if screen := onboardingScreen(h); !strings.Contains(screen, "5 KEYS TO START") {
		t.Fatalf("help from the welcome is not the 5 keys view:\n%s", screen)
	}
	h.Update(keyEsc)
	if h.helpOverlay.IsVisible() || !h.welcomeActive() {
		t.Fatal("Esc did not close help back to the welcome")
	}

	// '?' left the pick on "Read the 5 keys"; j wraps back to New session.
	h.Update(runeKey('j'))
	if h.welcomeSel != int(welcomeNew) {
		t.Fatalf("j from the help action picked %d, want New session", h.welcomeSel)
	}
	// Enter opens the New Session form on the first installed agent, with
	// the hooks line ticked and the folder shown with ~.
	h.Update(keyEnter)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter on the welcome did not open the New Session form")
	}
	if got := h.newDialog.GetSelectedCommand(); got != "claude" {
		t.Fatalf("form tool = %q, want the first installed claude", got)
	}
	if asked, install := h.newDialog.HooksDecision(); !asked || !install {
		t.Fatalf("hooks line asked=%v ticked=%v, want shown and ticked", asked, install)
	}
	form := stripAnsi(h.newDialog.View())
	if !strings.Contains(form, hooksLineText[:30]) {
		t.Fatalf("form lacks the hooks line:\n%s", form)
	}
	if !strings.Contains(form, "~/billing-api") {
		t.Fatalf("form does not show the folder with ~:\n%s", form)
	}
	if strings.Contains(form, "Hooks") && strings.Contains(form, "Install") {
		t.Fatalf("the old Claude Code Hooks box is back in the form:\n%s", form)
	}
	if _, path, _ := h.newDialog.GetValues(); path != onboardingFolder() {
		t.Fatalf("form creates in %q, want %q", path, onboardingFolder())
	}
	_, cmd := h.Update(keyEnter)
	if !submitted(h, cmd) {
		t.Fatalf("Enter in the filled form did not create (error %q)", h.newDialog.validationErr)
	}

	// The create lands: the hint bar starts at step 1 for that session.
	inst := session.NewInstanceWithTool("billing-api", onboardingFolder(), "claude")
	h.Update(sessionCreatedMsg{instance: inst})
	if !h.hintBarVisible() {
		t.Fatal("the first session made from the welcome did not start the hint bar")
	}
	if line := hintBarLine(h); !strings.Contains(line, "Step 1 of 4") || !strings.Contains(line, "billing-api") {
		t.Fatalf("hint bar is not step 1 for the new session: %q", line)
	}
	if tourVisible(h) {
		t.Fatal(`"What you can do" opened after the first session`)
	}
}
