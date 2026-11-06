package ui

// The one-key hint bar (onboarding, ASK-211; plan and prototype by Sharjeel).
//
// After the first session of a fresh install exists, a one-line bar above the
// footer walks four keys, one at a time: Enter (open the session), Ctrl+Q
// (come back), n (new session), ? (help). Each step advances when the user
// does it. After step 4 the bar says "You know the basics" for a few seconds
// and is gone for good; Esc hides it for good at any step. It shows once per
// machine: a seen-file next to the Welcome's (.onboarding-hints-seen) holds
// the current step while the tour runs (a restart resumes it) and "done"
// once it ends or Esc hides it.
//
// While a session is attached, tmux owns the screen and this bar is not
// drawn: step 1 names Ctrl+Q too, and step 2 goes into the attached
// session's own status-left ("Step 2 of 4 · Ctrl+Q comes back here").

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
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
	// statusTmux is the attached session whose own tmux status-left
	// carries step 2 while the bar cannot be drawn; cleared on return.
	statusTmux *tmux.Session
}

// Seams so tests can watch the attached session's status-left hint without
// a tmux server.
var (
	onboardingSetStatusHint   = func(s *tmux.Session, text string) { _ = s.SetSessionStatusLeft(text) }
	onboardingClearStatusHint = func(s *tmux.Session) { _ = s.ClearSessionStatusLeft() }
)

// statusBackHintText is step 2 as the attached session's status-left: the
// bar is not drawn while tmux owns the screen, and the global status line
// (the quick-switch list) can crowd out "ctrl+q detach" (ASK-211 round 2).
func (h *Home) statusBackHintText() string {
	detach := DetachByteLabel(DetachByteFromBinding(h.actionKey(hotkeyDetach)))
	return "#[bold] Step 2 of 4 · " + tmux.FormatLiteral(detach) + " comes back here #[default]│ "
}

// showBackHintInStatus puts step 2 in the attached session's status line
// while the bar is at step 2 (called on attach).
func (h *Home) showBackHintInStatus(s *tmux.Session) {
	if s == nil || h.hints.step != hintStepBack || onboardingSetStatusHint == nil {
		return
	}
	onboardingSetStatusHint(s, h.statusBackHintText())
	h.hints.statusTmux = s
}

// clearBackHintInStatus removes the step 2 status-left again (on return).
func (h *Home) clearBackHintInStatus() {
	if h.hints.statusTmux == nil {
		return
	}
	if onboardingClearStatusHint != nil {
		onboardingClearStatusHint(h.hints.statusTmux)
	}
	h.hints.statusTmux = nil
}

func onboardingHintsSeenPath() string {
	path, err := agentpaths.EffectiveDataPath(onboardingHintsSeenName, onboardingHintsSeenName)
	if err != nil {
		return ""
	}
	return path
}

// The seen-file holds the bar's progress: "step N" while the tour is under
// way (written at each step, so a restart resumes it), anything else (the
// end, or Esc) means done for good (ASK-211 round 3: a user who quit at
// step 1 or 2 used to lose steps 3 and 4).
const onboardingHintsStepPrefix = "step "

// onboardingHintsSavedStep returns the step a quit tour stopped at (1-4),
// or 0 when there is none.
func onboardingHintsSavedStep() int {
	path := onboardingHintsSeenPath()
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(string(data)), onboardingHintsStepPrefix)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < hintStepOpen || n > hintStepHelp {
		return 0
	}
	return n
}

// onboardingHintsSeen reports whether the bar is done on this machine (shown
// to the end, or hidden with Esc). An unresolvable path counts as seen (never
// nag); a tour in progress does not.
func onboardingHintsSeen() bool {
	path := onboardingHintsSeenPath()
	if path == "" {
		return true
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return onboardingHintsSavedStep() == 0
}

func writeOnboardingHintsFile(content string) {
	path := onboardingHintsSeenPath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(content+"\n"), 0o644)
}

// markOnboardingHintsSeen records the bar as done for good.
func markOnboardingHintsSeen() {
	writeOnboardingHintsFile(time.Now().UTC().Format(time.RFC3339))
}

// saveOnboardingHintsStep records the current step so a restart resumes it.
func saveOnboardingHintsStep(step int) {
	writeOnboardingHintsFile(onboardingHintsStepPrefix + strconv.Itoa(step))
}

// armOnboardingHints is called on the first load: a machine that has not
// seen the bar gets it once its first session exists, on a fresh install's
// first run (maybeStartOnboarding: no config.toml, no sessions) and on any
// later start with no sessions yet after onboarding finished first run here
// (the user quit on the welcome before making one). An existing user, whose
// install predates onboarding, does not get it in an empty profile. A tour
// the user quit half-way is armed again (resumeOnboardingHints picks it up
// when sessions exist).
func (h *Home) armOnboardingHints(freshInstall, noSessions bool) {
	if onboardingHintsSeen() {
		return
	}
	if freshInstall || onboardingHintsSavedStep() > 0 || (noSessions && onboardingFirstRunFinished()) {
		h.hints.armed = true
	}
}

// resumeOnboardingHints restarts a tour the user quit half-way, at the step
// it stopped at, on a start that has sessions (sessionName is the one the
// bar talks about). Step 2 (come back from the session) resumes as step 3:
// the user is already back on the list.
func (h *Home) resumeOnboardingHints(sessionName string) {
	saved := onboardingHintsSavedStep()
	if !h.hints.armed || h.hints.step != hintStepOff || saved == 0 {
		return
	}
	if saved == hintStepBack {
		saved = hintStepNew
	}
	h.hints.armed = false
	h.hints.session = sessionName
	h.setHintStep(saved)
}

// setHintStep moves the bar to step and records it: steps 1-4 as progress,
// the end and off as done for good.
func (h *Home) setHintStep(step int) {
	h.hints.step = step
	if step >= hintStepOpen && step <= hintStepHelp {
		saveOnboardingHintsStep(step)
	} else {
		markOnboardingHintsSeen()
	}
}

// startOnboardingHints shows step 1 for a newly created session when armed.
// Only the step is written now; the seen-file says done at the end or on
// Esc, so a user who quits half-way gets the rest on the next start.
func (h *Home) startOnboardingHints(sessionName string) {
	if !h.hints.armed || h.hints.step != hintStepOff {
		return
	}
	h.hints.armed = false
	h.hints.session = sessionName
	h.setHintStep(hintStepOpen)
}

// advanceOnboardingHint moves to the next step when the user did the key of
// step from.
func (h *Home) advanceOnboardingHint(from int) {
	if h.hints.step == from {
		h.setHintStep(from + 1)
	}
}

// hideOnboardingHints turns the bar off for good (Esc, or the end).
func (h *Home) hideOnboardingHints() {
	h.hints.armed = false
	h.setHintStep(hintStepOff)
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
			h.setHintStep(hintStepDone)
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

// hintHelpKey is the help key as bound ("?" by default).
func (h *Home) hintHelpKey() string {
	if k := h.actionKey(hotkeyHelp); k != "" {
		return k
	}
	return "?"
}

// hintBarText returns the step label and the long and short sentences.
func (h *Home) hintBarText() (step, long, short string) {
	name := h.hints.session
	if name == "" {
		name = "the session"
	}
	return h.hintBarTextFor(name)
}

// hintBarTextFor is hintBarText with the session called name.
func (h *Home) hintBarTextFor(name string) (step, long, short string) {
	newKey := h.actionKey(hotkeyNewSession)
	if newKey == "" {
		newKey = "n"
	}
	helpKey := h.hintHelpKey()
	// The detach key as the user has it bound, as the short help shows it.
	detach := DetachByteLabel(DetachByteFromBinding(h.actionKey(hotkeyDetach)))
	switch h.hints.step {
	case hintStepOpen:
		return "Step 1 of 4", "Press Enter to open " + name + ". " + detach + " brings you back here.", "Enter opens it · " + detach + " comes back"
	case hintStepBack:
		return "Step 2 of 4", "Press " + detach + " to come back to the list. " + name + " keeps running.", detach + " comes back here. " + name + " keeps running."
	case hintStepNew:
		return "Step 3 of 4", "Press " + newKey + " to start another session.", "Press " + newKey + " for another session."
	case hintStepHelp:
		return "Step 4 of 4", "Press " + helpKey + " to see the 5 keys you need.", "Press " + helpKey + " for the 5 keys."
	}
	return "", "", ""
}

// hintNameMin is the fewest cells a shortened session name keeps.
const hintNameMin = 8

// fitHintText picks the sentence for room cells. A long session name is
// shortened (with …) before any key words go (ASK-211 round 3: step 1 lost
// "Ctrl+Q brings you back here" on narrow terminals): the long sentence
// with a shortened name, then the short one, then the short one with a
// shortened name, and only then a cut at the end.
func (h *Home) fitHintText(long, short string, room int) string {
	room = max(0, room)
	if lipgloss.Width(long) <= room {
		return long
	}
	name := h.hints.session
	if name == "" {
		name = "the session"
	}
	shorten := func(pick func(step, long, short string) string) (string, bool) {
		whole := pick(h.hintBarTextFor(name))
		if !strings.Contains(whole, name) {
			return whole, lipgloss.Width(whole) <= room
		}
		budget := room - (lipgloss.Width(whole) - lipgloss.Width(name))
		if budget < hintNameMin {
			return whole, false
		}
		text := pick(h.hintBarTextFor(truncateDisplay(name, budget, "…")))
		return text, lipgloss.Width(text) <= room
	}
	if text, ok := shorten(func(_, l, _ string) string { return l }); ok {
		return text
	}
	if lipgloss.Width(short) <= room {
		return short
	}
	if text, ok := shorten(func(_, _, s string) string { return s }); ok {
		return text
	}
	return truncateDisplay(short, room, "…")
}

// renderHintBar renders the one-line bar at the full width.
func (h *Home) renderHintBar() string {
	width := max(1, h.width)
	if h.hints.step == hintStepDone {
		style := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorGreen).Bold(true).Width(width).MaxWidth(width)
		return style.Render(truncateDisplay(" ✓ You know the basics. Press "+h.hintHelpKey()+" any time for the keys.", width, "…"))
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
	text := h.fitHintText(long, short, room)
	gap := max(0, width-lipgloss.Width(left)-lipgloss.Width(text)-lipgloss.Width(right))
	return left + bar.Render(text+strings.Repeat(" ", gap)+right)
}
