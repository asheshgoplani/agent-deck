package ui

// The one-key hint bar (onboarding, ASK-211; plan and prototype by Sharjeel).
//
// After the first session of a fresh install exists, a one-line bar above the
// footer walks four keys, one at a time: Enter (open the session), Ctrl+Q
// (come back), n (new session), ? (help). Each step advances when the user
// does it. After step 4 the bar says "You know the basics" for a few seconds
// and is gone for good; Esc hides it for good at any step. It shows once per
// machine: a seen-file next to the Welcome's (.onboarding-hints-seen).
//
// While a session is attached, tmux owns the screen and this bar is not
// drawn; the attached session's status line already carries the
// "ctrl+q detach" hint, so step 1 names Ctrl+Q too.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// onboardingHintsSeenName is the marker file (under the effective data dir)
// whose presence means the hint bar has been shown on this machine.
const onboardingHintsSeenName = ".onboarding-hints-seen"

// Hint bar steps. 0 is off.
const (
	hintStepOff   = 0
	hintStepOpen  = 1 // Enter opens the session
	hintStepBack  = 2 // Ctrl+Q comes back
	hintStepNew   = 3 // n starts another session
	hintStepHelp  = 4 // ? shows the 5 keys
	hintStepDone  = 5 // "You know the basics", then off
	hintStepCount = 4
)

// onboardingHintsDoneFor is how long "You know the basics" stays up.
var onboardingHintsDoneFor = 4 * time.Second

// onboardingHintsDoneMsg ends the "You know the basics" line; gen guards
// against a stale tick.
type onboardingHintsDoneMsg struct{ gen int }

// onboardingHints is the hint bar's state on Home.
type onboardingHints struct {
	armed   bool   // a fresh install: start at the first session
	step    int    // hintStep*
	session string // the session name the bar talks about
	doneGen int
}

func onboardingHintsSeenPath() string {
	path, err := agentpaths.EffectiveDataPath(onboardingHintsSeenName, onboardingHintsSeenName)
	if err != nil {
		return ""
	}
	return path
}

// onboardingHintsSeen reports whether the bar was already shown on this
// machine. An unresolvable path counts as seen (never nag).
func onboardingHintsSeen() bool {
	path := onboardingHintsSeenPath()
	if path == "" {
		return true
	}
	_, err := os.Stat(path)
	return err == nil
}

func markOnboardingHintsSeen() {
	path := onboardingHintsSeenPath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// armOnboardingHints is called on the first load: a profile that starts
// empty on a machine that has not seen the bar gets it once its first
// session exists.
func (h *Home) armOnboardingHints(emptyFirstLoad bool) {
	if emptyFirstLoad && !onboardingHintsSeen() {
		h.hints.armed = true
	}
}

// startOnboardingHints shows step 1 for a newly created session when armed.
// The seen-file is written now, so the bar is once per machine even if the
// user quits half-way.
func (h *Home) startOnboardingHints(sessionName string) {
	if !h.hints.armed || h.hints.step != hintStepOff {
		return
	}
	h.hints.armed = false
	h.hints.step = hintStepOpen
	h.hints.session = sessionName
	markOnboardingHintsSeen()
}

// advanceOnboardingHint moves to the next step when the user did the key of
// step from.
func (h *Home) advanceOnboardingHint(from int) {
	if h.hints.step == from {
		h.hints.step++
	}
}

// hideOnboardingHints turns the bar off for good (Esc, or the end).
func (h *Home) hideOnboardingHints() {
	h.hints.step = hintStepOff
	h.hints.armed = false
	markOnboardingHintsSeen()
}

// onboardingHintsDoneCmd starts the timer that clears "You know the basics".
func (h *Home) onboardingHintsDoneCmd() tea.Cmd {
	h.hints.doneGen++
	gen := h.hints.doneGen
	return tea.Tick(onboardingHintsDoneFor, func(time.Time) tea.Msg { return onboardingHintsDoneMsg{gen: gen} })
}

// handleOnboardingHintsDone clears the done line when its timer fires.
func (h *Home) handleOnboardingHintsDone(msg onboardingHintsDoneMsg) {
	if msg.gen == h.hints.doneGen && h.hints.step == hintStepDone {
		h.hints.step = hintStepOff
	}
}

// onboardingHintKey handles the hint bar's part of a main-screen key (key is
// the normalized key). It returns true when the key was used up (Esc hiding
// the bar). Step advances never consume the key: n still opens the form.
func (h *Home) onboardingHintKey(key string) bool {
	switch h.hints.step {
	case hintStepOff:
		return false
	case hintStepDone:
		// Any key after the end clears the done line.
		h.hints.step = hintStepOff
		return false
	}
	switch key {
	case "esc":
		h.hideOnboardingHints()
		return true
	case "n", "N":
		h.advanceOnboardingHint(hintStepNew)
	case "?":
		if h.hints.step == hintStepHelp {
			h.hints.step = hintStepDone
		}
	}
	return false
}

// hintBarVisible reports whether the bar takes a row above the footer.
func (h *Home) hintBarVisible() bool {
	switch {
	case h.hints.step >= hintStepOpen && h.hints.step <= hintStepHelp:
		return true
	case h.hints.step == hintStepDone:
		return !h.helpOverlay.IsVisible()
	}
	return false
}

// hintBarHeight is the rows the bar takes (0 or 1).
func (h *Home) hintBarHeight() int {
	if h.hintBarVisible() {
		return 1
	}
	return 0
}

// hintBarText returns the step label and the long and short sentences.
func (h *Home) hintBarText() (step, long, short string) {
	name := h.hints.session
	if name == "" {
		name = "the session"
	}
	newKey := h.actionKey(hotkeyNewSession)
	if newKey == "" {
		newKey = "n"
	}
	helpKey := h.actionKey(hotkeyHelp)
	if helpKey == "" {
		helpKey = "?"
	}
	switch h.hints.step {
	case hintStepOpen:
		return "Step 1 of 4", "Press Enter to open " + name + ". Ctrl+Q brings you back here.", "Press Enter to open " + name + "."
	case hintStepBack:
		return "Step 2 of 4", "Press Ctrl+Q to come back to the list. " + name + " keeps running.", "Ctrl+Q comes back here. " + name + " keeps running."
	case hintStepNew:
		return "Step 3 of 4", "Press " + newKey + " to start another session.", "Press " + newKey + " for another session."
	case hintStepHelp:
		return "Step 4 of 4", "Press " + helpKey + " to see the 5 keys you need.", "Press " + helpKey + " for the 5 keys."
	}
	return "", "", ""
}

// renderHintBar renders the one-line bar at the full width.
func (h *Home) renderHintBar() string {
	width := max(1, h.width)
	if h.hints.step == hintStepDone {
		style := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorGreen).Bold(true).Width(width).MaxWidth(width)
		return style.Render(truncateDisplay(" ✓ You know the basics. Press ? any time for the keys.", width, "…"))
	}
	stepLabel, long, short := h.hintBarText()
	bar := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorCyan)
	dots := strings.Repeat("●", h.hints.step) + strings.Repeat("○", hintStepCount-h.hints.step)
	left := bar.Bold(true).Render(" "+stepLabel) + bar.Render(" "+dots+"  ")
	right := ""
	if width >= 100 {
		right = "Esc hide tips "
	}
	room := width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	text := long
	if lipgloss.Width(text) > room {
		text = short
	}
	text = truncateDisplay(text, max(0, room), "…")
	gap := max(0, width-lipgloss.Width(left)-lipgloss.Width(text)-lipgloss.Width(right))
	return left + bar.Render(text+strings.Repeat(" ", gap)+right)
}
