package ui

// ASK-211 onboarding, review round 3 (TUI walk / power-user checks).

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A user who quits at step 1 or 2 is not marked done: the next start (with
// the session there) resumes the tour; it is done only at the end or on Esc.
func TestOnboardingRound3_HintsResumeAfterQuit(t *testing.T) {
	h := hintsHome(t, 120, 40)
	h.startOnboardingHints("my-app")
	if onboardingHintsSeen() {
		t.Fatal("step 1 marked the bar as seen")
	}
	if got := onboardingHintsSavedStep(); got != hintStepOpen {
		t.Fatalf("saved step = %d, want 1", got)
	}

	// Restart at step 1: resumes at step 1 on the first load.
	h2 := startDeck(t, session.NewInstanceWithTool("my-app", t.TempDir(), "shell"))
	if h2.hints.step != hintStepOpen || !strings.Contains(hintBarLine(h2), "Step 1 of 4") {
		t.Fatalf("restart at step 1: step=%d bar=%q", h2.hints.step, hintBarLine(h2))
	}

	// Enter (step 2), then quit while away: resumes at step 3 (back on the list).
	h2.advanceOnboardingHint(hintStepOpen)
	h3 := startDeck(t, session.NewInstanceWithTool("my-app", t.TempDir(), "shell"))
	if h3.hints.step != hintStepNew || !strings.Contains(hintBarLine(h3), "Step 3 of 4") {
		t.Fatalf("restart at step 2: step=%d bar=%q", h3.hints.step, hintBarLine(h3))
	}
	h3.onboardingHintKey("n")
	if got := onboardingHintsSavedStep(); got != hintStepHelp {
		t.Fatalf("saved step after n = %d, want 4", got)
	}
	h3.onboardingHintKey("?")
	if !onboardingHintsSeen() {
		t.Fatal("the end of the tour did not mark the bar done")
	}
	h4 := startDeck(t, session.NewInstanceWithTool("my-app", t.TempDir(), "shell"))
	if h4.hintBarVisible() || h4.hints.armed {
		t.Fatal("the bar came back after the tour ended")
	}
}

// Esc at step 1 marks the bar done for good (no resume).
func TestOnboardingRound3_EscAtStepOneIsForGood(t *testing.T) {
	h := hintsHome(t, 120, 40)
	h.startOnboardingHints("my-app")
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !onboardingHintsSeen() || onboardingHintsSavedStep() != 0 {
		t.Fatal("Esc did not record the bar as done")
	}
	h2 := startDeck(t, session.NewInstanceWithTool("my-app", t.TempDir(), "shell"))
	if h2.hintBarVisible() {
		t.Fatalf("bar came back after Esc: %q", hintBarLine(h2))
	}
}

// An install command is never wrapped: on the check page and the welcome it
// sits whole on one line (copying it gives a working command), at 80 and 60
// columns.
func TestOnboardingRound3_InstallCommandOnOneLine(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {60, 20}, {120, 40}} {
		newOnboardingEnv(t)
		h := startDeck(t)
		h.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		if !checkVisible(h) {
			t.Fatal("precondition: check page")
		}
		cmd := h.onboarding.agent("claude").Install
		if !screenHasWholeLine(onboardingScreen(h), cmd) {
			t.Fatalf("%dx%d check page: %q not whole on one line:\n%s", size[0], size[1], cmd, onboardingScreen(h))
		}
		h.Update(keyEnter) // Plain shell -> welcome
		screen := onboardingScreen(h)
		if !strings.Contains(screen, "No agent found yet") {
			t.Fatalf("%dx%d: welcome without the no-agent hint:\n%s", size[0], size[1], screen)
		}
		if !screenHasWholeLine(screen, cmd) {
			t.Fatalf("%dx%d welcome: %q not whole on one line:\n%s", size[0], size[1], cmd, screen)
		}
	}
}

func screenHasWholeLine(screen, cmd string) bool {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, cmd) {
			return true
		}
	}
	return false
}

// onboardingCommandLine keeps the indent when it fits, drops it when that
// makes the command fit, and cuts with … only as the last resort.
func TestOnboardingRound3_CommandLineFitting(t *testing.T) {
	cmd := "npm install -g @openai/codex" // 28 cells
	if got := onboardingCommandLine("    ", cmd, 40); got != "    "+cmd {
		t.Fatalf("fits with indent: %q", got)
	}
	if got := onboardingCommandLine("    ", cmd, 30); got != cmd {
		t.Fatalf("fits without indent: %q", got)
	}
	if got := onboardingCommandLine("    ", cmd, 20); !strings.HasSuffix(got, "…") || len([]rune(got)) > 20 {
		t.Fatalf("cannot fit: %q", got)
	}
}

// The Agent row's footer says "agent" as the form does and fits on one row
// at 80 columns (it wrapped "Esc / cancel" with a yolo-capable agent).
func TestOnboardingRound3_AgentFooterFitsAt80(t *testing.T) {
	for _, tool := range []string{"codex", "claude", "gemini"} {
		for _, first := range []bool{false, true} {
			d := NewNewDialog()
			d.SetDefaultTool(tool)
			d.SetSize(80, 24)
			d.Show()
			d.SetFirstSession(first)
			d.focusIndex = d.indexOf(focusCommand)
			d.updateFocus()
			if d.GetSelectedCommand() != tool {
				t.Fatalf("precondition: selected %q, want %q", d.GetSelectedCommand(), tool)
			}
			view := stripAnsi(d.View())
			var footer []string
			for _, l := range strings.Split(view, "\n") {
				if strings.Contains(l, "←→") || strings.Contains(l, "Esc") || strings.Contains(l, "cancel") {
					footer = append(footer, strings.TrimSpace(l))
				}
			}
			if len(footer) != 1 {
				t.Fatalf("%s first=%v: footer is %d rows %q:\n%s", tool, first, len(footer), footer, view)
			}
			if !strings.Contains(footer[0], "←→ agent") || strings.Contains(footer[0], "tool") || !strings.Contains(footer[0], "Esc cancel") {
				t.Fatalf("%s first=%v: footer %q", tool, first, footer[0])
			}
			if first && strings.Contains(footer[0], "w worktree") {
				t.Fatalf("first form advertises folded options: %q", footer[0])
			}
		}
	}
}

// On narrow terminals step 1 keeps "Ctrl+Q brings you back here" and
// shortens the session name instead.
func TestOnboardingRound3_Step1ShortensNameNotKeys(t *testing.T) {
	h := hintsHome(t, 80, 24)
	h.startOnboardingHints("spring-mesa")
	line := hintBarLine(h)
	if !strings.Contains(line, "Ctrl+Q brings you back here.") || !strings.Contains(line, "Press Enter to open spring") {
		t.Fatalf("80-col step 1: %q", line)
	}
	if w := len([]rune(line)); w > 80 {
		t.Fatalf("bar is %d cells", w)
	}
	h2 := hintsHome(t, 80, 24)
	h2.startOnboardingHints("spring-mesa-and-the-actual-project")
	if line := hintBarLine(h2); !strings.Contains(line, "Ctrl+Q brings you back here.") || !strings.Contains(line, "…") {
		t.Fatalf("80-col step 1, long name: %q", line)
	}
	// Step 2 too keeps its key words with a long name.
	h2.advanceOnboardingHint(hintStepOpen)
	if line := hintBarLine(h2); !strings.Contains(line, "Ctrl+Q comes back here.") || !strings.Contains(line, "keeps running.") {
		t.Fatalf("80-col step 2, long name: %q", line)
	}
}

// An internal performance warning does not reach a new user while the hint
// bar is up; it shows as before once the tour is over.
func TestOnboardingRound3_HealthWarningHiddenDuringTour(t *testing.T) {
	h := hintsHome(t, 120, 40)
	h.startOnboardingHints("my-app")
	h.healthWarningText, h.healthWarningAt = "Health: tmux calls exceed twice the session count", time.Now()
	if w := h.renderHealthWarning(); w != "" {
		t.Fatalf("health warning shown during the tour: %q", stripAnsi(w))
	}
	h.hideOnboardingHints()
	if w := h.renderHealthWarning(); !strings.Contains(stripAnsi(w), "tmux calls exceed") {
		t.Fatalf("health warning missing after the tour: %q", stripAnsi(w))
	}
}
