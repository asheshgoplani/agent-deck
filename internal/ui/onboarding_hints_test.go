package ui

// ASK-211 onboarding (Sharjeel's prototype): the one-key hint bar after the
// first session. Steps 1-4 (Enter, Ctrl+Q, n, ?), the "You know the basics"
// end, Esc for good, and once per machine through the seen-file.

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// hintsHome is a Home on a fresh, isolated install with the bar armed.
func hintsHome(t *testing.T, width, height int) *Home {
	t.Helper()
	setXDGTestHome(t)
	h := NewHome()
	h.Update(tea.WindowSizeMsg{Width: width, Height: height})
	h.initialLoading = false // past the splash
	h.armOnboardingHints(true, true)
	return h
}

func hintBarLine(h *Home) string {
	if !h.hintBarVisible() {
		return ""
	}
	return stripAnsi(h.renderHintBar())
}

func TestOnboardingHints_ArmedOnlyOnEmptyFreshInstall(t *testing.T) {
	setXDGTestHome(t)
	h := NewHome()
	h.armOnboardingHints(false, false)
	if h.hints.armed {
		t.Fatal("a profile with sessions armed the hint bar")
	}
	h.armOnboardingHints(true, true)
	if !h.hints.armed {
		t.Fatal("an empty fresh install did not arm the hint bar")
	}
	// The first load of an empty profile arms it too.
	h2 := startDeck(t)
	if !h2.hints.armed {
		t.Fatal("startDeck on an empty profile did not arm the hint bar")
	}
}

func TestOnboardingHints_FirstSessionStartsStepOne(t *testing.T) {
	h := hintsHome(t, 120, 40)
	if h.hintBarVisible() {
		t.Fatal("bar visible before any session exists")
	}
	h.Update(sessionCreatedMsg{err: os.ErrNotExist})
	if h.hintBarVisible() {
		t.Fatal("a failed create started the bar")
	}
	h.startOnboardingHints("my-app")
	line := hintBarLine(h)
	for _, want := range []string{"Step 1 of 4", "●○○○", "Press Enter to open my-app.", "Esc hide tips"} {
		if !strings.Contains(line, want) {
			t.Fatalf("step 1 bar lacks %q: %q", want, line)
		}
	}
	// The bar sits right above the footer and the frame keeps its height.
	frame := strings.Split(stripAnsi(h.View()), "\n")
	if len(frame) != 40 {
		t.Fatalf("frame is %d rows, want 40", len(frame))
	}
	found := -1
	for i, l := range frame {
		if strings.Contains(l, "Step 1 of 4") {
			found = i
		}
	}
	if found < 0 || found < 40-4 {
		t.Fatalf("hint bar not just above the footer (row %d):\n%s", found, strings.Join(frame, "\n"))
	}
}

func TestOnboardingHints_WalksFourStepsThenDone(t *testing.T) {
	h := hintsHome(t, 120, 40)
	h.startOnboardingHints("my-app")

	// Step 1: Enter opens the session (attachSession advances it).
	h.advanceOnboardingHint(hintStepOpen)
	if h.hints.step != hintStepBack || !strings.Contains(hintBarLine(h), "Step 2 of 4") {
		t.Fatalf("after open: step %d %q", h.hints.step, hintBarLine(h))
	}
	if !strings.Contains(hintBarLine(h), "Press Ctrl+Q to come back to the list. my-app keeps running.") {
		t.Fatalf("step 2 text: %q", hintBarLine(h))
	}

	// Step 2: coming back from the attached session (Ctrl+Q).
	h.Update(statusUpdateMsg{attachedSessionID: "s1"})
	if h.hints.step != hintStepNew || !strings.Contains(hintBarLine(h), "Press n to start another session.") {
		t.Fatalf("after Ctrl+Q: step %d %q", h.hints.step, hintBarLine(h))
	}

	// Step 3: n, which still opens the New Session form.
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if !h.newDialog.IsVisible() {
		t.Fatal("n no longer opens the New Session form")
	}
	if h.hints.step != hintStepHelp {
		t.Fatalf("after n: step %d", h.hints.step)
	}
	h.newDialog.Hide()
	if !strings.Contains(hintBarLine(h), "Step 4 of 4") || !strings.Contains(hintBarLine(h), "Press ? to see the 5 keys you need.") {
		t.Fatalf("step 4 bar: %q", hintBarLine(h))
	}

	// Step 4: ? opens the help; the bar waits while it is open.
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !h.helpOverlay.IsVisible() {
		t.Fatal("? no longer opens the help")
	}
	if h.hintBarVisible() {
		t.Fatal("bar drawn while the help is open")
	}
	_, cmd := h.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.helpOverlay.IsVisible() {
		t.Fatal("Esc did not close the help")
	}
	if cmd == nil {
		t.Fatal("closing the help after step 4 started no done timer")
	}
	if !strings.Contains(hintBarLine(h), "You know the basics") {
		t.Fatalf("done line missing: %q", hintBarLine(h))
	}
	h.Update(onboardingHintsDoneMsg{gen: h.hints.doneGen})
	if h.hintBarVisible() || h.hints.step != hintStepOff {
		t.Fatal("the bar did not go away after the done line")
	}
}

func TestOnboardingHints_StepsDoNotSkipAhead(t *testing.T) {
	h := hintsHome(t, 120, 40)
	h.startOnboardingHints("my-app")
	// n at step 1 does not jump to step 4.
	h.onboardingHintKey("n")
	h.onboardingHintKey("?")
	if h.hints.step != hintStepOpen {
		t.Fatalf("step = %d, want still 1", h.hints.step)
	}
}

func TestOnboardingHints_EscHidesForGood(t *testing.T) {
	h := hintsHome(t, 120, 40)
	h.startOnboardingHints("my-app")
	h.advanceOnboardingHint(hintStepOpen)
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyEsc})
	if h.hintBarVisible() || h.hints.step != hintStepOff {
		t.Fatal("Esc did not hide the bar")
	}
	if !onboardingHintsSeen() {
		t.Fatal("Esc did not record the bar as seen")
	}
	// A later fresh start on this machine never shows it again.
	h2 := NewHome()
	h2.armOnboardingHints(true, true)
	h2.startOnboardingHints("other")
	if h2.hintBarVisible() {
		t.Fatal("the bar came back after Esc")
	}
}

func TestOnboardingHints_SeenFileOncePerMachine(t *testing.T) {
	h := hintsHome(t, 120, 40)
	if onboardingHintsSeen() {
		t.Fatal("precondition: fresh machine has no seen-file")
	}
	h.startOnboardingHints("my-app")
	path := onboardingHintsSeenPath()
	if !strings.HasSuffix(path, onboardingHintsSeenName) {
		t.Fatalf("seen-file path %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("starting the bar did not write the seen-file: %v", err)
	}
	// Round 3: starting the bar records progress, not done.
	if onboardingHintsSeen() {
		t.Fatal("starting the bar marked it done")
	}
	h.hideOnboardingHints()
	h2 := NewHome()
	h2.armOnboardingHints(true, true)
	if h2.hints.armed {
		t.Fatal("a second start on the same machine armed the bar again")
	}
}

func TestOnboardingHints_NarrowTerminalUsesShortText(t *testing.T) {
	h := hintsHome(t, 60, 24)
	h.startOnboardingHints("my-app")
	line := hintBarLine(h)
	if !strings.Contains(line, "Enter opens it · Ctrl+Q comes back") || strings.Contains(line, "Esc hide tips") {
		t.Fatalf("narrow bar: %q", line)
	}
	if w := len([]rune(line)); w > 60 {
		t.Fatalf("narrow bar is %d cells", w)
	}
}
