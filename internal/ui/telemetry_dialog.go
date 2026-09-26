package ui

import (
	"context"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// telemetryStep is the dialog phase.
type telemetryStep int

const (
	telemetryStepAsk telemetryStep = iota
	telemetryStepGranted
	telemetryStepDeclined
)

// Button focus of the ask step.
const (
	telemetryFocusAccept = iota
	telemetryFocusNo
)

// telemetryDismissMsg closes the dialog after the confirmation line.
type telemetryDismissMsg struct{}

// telemetryUploadMsg carries the result of a background MaybeUpload. It is
// informational only; the TUI never surfaces it.
type telemetryUploadMsg struct{ result telemetry.UploadResult }

// telemetryGrantedMsg tells the home model consent was durably granted, so
// it can record the consent events (it knows the fleet) and start sampling.
type telemetryGrantedMsg struct {
	source   telemetry.ConsentSource
	previous string
}

// TelemetryDialog is the one-time consent prompt for opt-in usage telemetry.
//
// Rules (docs/TELEMETRY-DESIGN.md): shown only when telemetry.ShouldPrompt
// says so. Two buttons, Accept focused: Enter confirms the focused button,
// y accepts, n or Esc declines (remembered), Ctrl-C closes and asks again at
// the next TUI start. Every other key and pasted input is ignored, as is any
// key in the first 750 ms. Accept works only when the whole question is
// visible, and nothing is recorded until the grant is durably on disk.
type TelemetryDialog struct {
	visible    bool
	step       telemetryStep
	focus      int
	width      int
	height     int
	version    string
	state      *telemetry.State
	previous   string
	v1Declined bool
	source     telemetry.ConsentSource
	saveErr    error
	shownAt    time.Time
	endpoint   string
	canConsent func() bool

	// Seams for tests.
	saveState    func(*telemetry.State) error
	declineState func(*telemetry.State) error
	now          func() time.Time
}

// NewTelemetryDialog creates a hidden dialog wired to the real package.
func NewTelemetryDialog() *TelemetryDialog {
	return &TelemetryDialog{
		saveState:    telemetry.SaveState,
		declineState: func(s *telemetry.State) error { return telemetry.Disable(s.ConsentVersion, time.Now()) },
		now:          time.Now,
		canConsent: func() bool {
			return telemetry.Interactive() && !telemetry.HardDisabled() && !telemetry.LogMode()
		},
	}
}

// telemetryKeyGrace is how long after the dialog appears keystrokes are
// ignored, so a key queued during the splash or typed a moment before the
// dialog landed cannot answer the question.
const telemetryKeyGrace = 750 * time.Millisecond

// telemetryUploadCmd runs the upload path in the background. MaybeUpload
// re-reads consent and every gate itself, so this is a no-op for everyone
// who has not said yes.
func telemetryUploadCmd() tea.Cmd {
	return func() tea.Msg {
		return telemetryUploadMsg{result: telemetry.MaybeUpload(context.Background())}
	}
}

// IsVisible reports whether the dialog is on screen.
func (d *TelemetryDialog) IsVisible() bool { return d.visible }

// Show opens the first-run prompt if, and only if, ShouldPrompt(state) is true.
func (d *TelemetryDialog) Show(version string, st *telemetry.State) bool {
	if !telemetry.ShouldPrompt(st) {
		return false
	}
	d.open(version, st, telemetry.SourceTUIFirstRun)
	return true
}

// ShowFromSettings opens the prompt from the Settings privacy row. It is
// the same question with the same rules, for any state but granted.
func (d *TelemetryDialog) ShowFromSettings(version string, st *telemetry.State) bool {
	if st == nil || st.Consent == telemetry.ConsentGranted || !d.canConsent() || telemetry.ValidateEndpoint(telemetry.Endpoint()) != nil {
		return false
	}
	d.open(version, st, telemetry.SourceTUISettings)
	d.v1Declined = false
	return true
}

func (d *TelemetryDialog) open(version string, st *telemetry.State, src telemetry.ConsentSource) {
	d.visible = true
	d.step = telemetryStepAsk
	d.focus = telemetryFocusAccept
	d.version = version
	d.state = st
	d.previous = st.Previous()
	d.v1Declined = st.V1Declined()
	d.source = src
	d.saveErr = nil
	d.shownAt = d.now()
	d.endpoint = telemetry.Endpoint()
}

// Hide closes the dialog.
func (d *TelemetryDialog) Hide() { d.visible = false }

// SetSize records the terminal size for centering and the fit rule.
func (d *TelemetryDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
}

// Update handles a key press. Returns a command to run afterwards.
func (d *TelemetryDialog) Update(msg tea.KeyMsg) (*TelemetryDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}
	if d.step != telemetryStepAsk {
		// Confirmation line is showing; the timer closes it. Any key closes early.
		d.Hide()
		return d, nil
	}
	if d.now().Sub(d.shownAt) < telemetryKeyGrace || msg.Paste {
		return d, nil
	}
	fits := d.fits()
	switch msg.String() {
	case "ctrl+c":
		// Ask me later: state stays as it was; the TUI keeps running.
		d.Hide()
		return d, nil
	case "n", "N", "esc":
		return d, d.decline()
	case "y", "Y":
		if fits {
			return d, d.accept()
		}
	case "enter":
		if !fits {
			return d, nil
		}
		if d.focus == telemetryFocusAccept {
			return d, d.accept()
		}
		return d, d.decline()
	case "tab", "shift+tab", "left", "right", "h", "l":
		if fits {
			d.focus = 1 - d.focus
		}
	}
	return d, nil
}

func (d *TelemetryDialog) accept() tea.Cmd {
	if !d.canConsent() || d.endpoint != telemetry.Endpoint() {
		return nil
	}
	if err := telemetry.Grant(d.state, d.version, d.now()); err != nil {
		d.saveErr = err
		d.step = telemetryStepDeclined
		return telemetryDismissAfter()
	}
	if err := d.saveState(d.state); err != nil {
		// Consent that did not reach disk is not consent: stay off.
		d.saveErr = err
		d.state.Consent = telemetry.ConsentUndecided
		d.state.InstallID = ""
		d.step = telemetryStepDeclined
		return telemetryDismissAfter()
	}
	d.step = telemetryStepGranted
	// Record only now, after the granted state is durably on disk.
	granted := telemetryGrantedMsg{source: d.source, previous: d.previous}
	return tea.Batch(func() tea.Msg { return granted }, telemetryDismissAfter())
}

func (d *TelemetryDialog) decline() tea.Cmd {
	telemetry.Decline(d.state, d.version, d.now())
	d.saveErr = d.declineState(d.state)
	d.step = telemetryStepDeclined
	return telemetryDismissAfter()
}

// fits reports whether the whole question and both buttons are visible.
func (d *TelemetryDialog) fits() bool {
	return d.width >= telemetry.PromptWidth && d.height >= telemetry.PromptHeight && telemetry.PromptFits(d.endpoint)
}

func telemetryDismissAfter() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return telemetryDismissMsg{} })
}

// telemetryBoxWidth is the outer width of the question box (78 columns).
const telemetryBoxWidth = telemetry.PromptWidth - 2

// View renders the dialog, or "" when hidden.
func (d *TelemetryDialog) View() string {
	if !d.visible {
		return ""
	}
	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	greenStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)

	padV := 1
	var content string
	switch d.step {
	case telemetryStepAsk:
		if !d.fits() {
			return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center,
				lipgloss.NewStyle().Width(min(d.width, telemetryBoxWidth)).Render(
					telemetry.PromptTooSmall+"\n"+dimStyle.Render("n / Esc: no · Ctrl-C: ask me later")))
		}
		lines := strings.Split(telemetry.PromptText(d.endpoint), "\n")
		parts := []string{titleStyle.Render(lines[0]), textStyle.Render(strings.Join(lines[1:], "\n")), ""}
		if d.v1Declined {
			parts = append(parts, dimStyle.Render(telemetry.PromptV1Declined), "")
			padV = 0
		}
		parts = append(parts, d.buttons(), "", dimStyle.Render(" "+telemetry.PromptLegend))
		content = lipgloss.JoinVertical(lipgloss.Left, parts...)
	case telemetryStepGranted:
		content = greenStyle.Render(telemetry.GrantedLine)
	case telemetryStepDeclined:
		msg := telemetry.DeclinedLine
		if d.saveErr != nil {
			msg = "Choice not saved (could not save your choice). Check: agent-deck telemetry status."
		}
		content = textStyle.Render(msg)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(padV, 2).
		Width(telemetryBoxWidth).
		Render(content)

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

// buttons renders the two same-size buttons with the focus marker.
func (d *TelemetryDialog) buttons() string {
	on := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Reverse(true)
	off := lipgloss.NewStyle().Foreground(ColorText)
	render := func(label string, focused bool) string {
		if focused {
			return "▶ " + on.Render("[ "+label+" ]")
		}
		return "  " + off.Render("[ "+label+" ]")
	}
	return "       " + render(telemetry.PromptAccept, d.focus == telemetryFocusAccept) +
		"        " + render(telemetry.PromptNo, d.focus == telemetryFocusNo)
}
