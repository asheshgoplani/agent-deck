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

// telemetryDismissMsg closes the dialog after the confirmation line.
type telemetryDismissMsg struct{}

// telemetrySentMsg carries the result of a background MaybeSend. It is
// informational only; the TUI never surfaces it.
type telemetrySentMsg struct{ result telemetry.SendResult }

// TelemetryDialog is the one-time consent prompt for opt-in usage telemetry.
//
// Rules (docs/TELEMETRY-DESIGN.md section 4): it is shown only when
// telemetry.ShouldPrompt says so; `y` grants, every other key declines and
// is remembered; there is no timer that answers on the user's behalf, and
// nothing is sent until the granted state has been written to disk.
type TelemetryDialog struct {
	visible    bool
	step       telemetryStep
	width      int
	height     int
	version    string
	state      *telemetry.State
	saveErr    error
	shownAt    time.Time
	endpoint   string
	canConsent func() bool

	// Seams for tests.
	saveState    func(*telemetry.State) error
	declineState func(*telemetry.State) error
	sendCmd      func(version string) tea.Cmd
	now          func() time.Time
}

// NewTelemetryDialog creates a hidden dialog wired to the real package.
func NewTelemetryDialog() *TelemetryDialog {
	return &TelemetryDialog{
		saveState:    telemetry.SaveState,
		declineState: func(s *telemetry.State) error { return telemetry.Disable(s.ConsentVersion, time.Now()) },
		sendCmd:      telemetrySendCmd,
		now:          time.Now,
		canConsent:   func() bool { return telemetry.Interactive() && !telemetry.HardDisabled() },
	}
}

// telemetryKeyGrace is how long after the dialog appears keystrokes are
// ignored, so a key queued during the splash or typed a moment before the
// dialog landed cannot answer the question. A person reads for longer than
// this; a stray key does not wait.
const telemetryKeyGrace = 750 * time.Millisecond

// telemetrySendCmd runs the single outbound path in the background.
func telemetrySendCmd(version string) tea.Cmd {
	return func() tea.Msg {
		return telemetrySentMsg{result: telemetry.MaybeSend(context.Background(), version)}
	}
}

// IsVisible reports whether the dialog is on screen.
func (d *TelemetryDialog) IsVisible() bool { return d.visible }

// Show opens the prompt if, and only if, ShouldPrompt(state) is true.
func (d *TelemetryDialog) Show(version string, st *telemetry.State) bool {
	if !telemetry.ShouldPrompt(st) {
		return false
	}
	d.visible = true
	d.step = telemetryStepAsk
	d.version = version
	d.state = st
	d.saveErr = nil
	d.shownAt = d.now()
	d.endpoint = telemetry.Endpoint()
	return true
}

// Hide closes the dialog.
func (d *TelemetryDialog) Hide() { d.visible = false }

// SetSize records the terminal size for centering.
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
	if d.now().Sub(d.shownAt) < telemetryKeyGrace {
		// Too soon to be a considered answer: ignore, keep asking.
		return d, nil
	}
	switch msg.String() {
	case "y", "Y":
		if msg.Paste || !d.disclosureFits() || !d.canConsent() || d.endpoint != telemetry.Endpoint() {
			return d, nil
		}
		if err := telemetry.Grant(d.state, d.version, d.now()); err != nil {
			d.saveErr = err
			d.step = telemetryStepDeclined
			return d, telemetryDismissAfter()
		}
		if err := d.saveState(d.state); err != nil {
			// Consent that did not reach disk is not consent: stay off.
			d.saveErr = err
			d.state.Consent = telemetry.ConsentUndecided
			d.state.InstallID = ""
			d.step = telemetryStepDeclined
			return d, telemetryDismissAfter()
		}
		d.step = telemetryStepGranted
		// Send only now, after the granted state is durably on disk.
		return d, tea.Batch(d.sendCmd(d.version), telemetryDismissAfter())
	default:
		// n, Esc, q, Enter, anything: a remembered "no".
		telemetry.Decline(d.state, d.version, d.now())
		d.saveErr = d.declineState(d.state)
		d.step = telemetryStepDeclined
		return d, telemetryDismissAfter()
	}
}

// The full disclosure and key choices must be visible before accepting yes.
func (d *TelemetryDialog) disclosureFits() bool {
	return d.width >= 106 && d.height >= 36 && len(d.endpoint) <= 95
}

func telemetryDismissAfter() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return telemetryDismissMsg{} })
}

// View renders the dialog, or "" when hidden.
func (d *TelemetryDialog) View() string {
	if !d.visible {
		return ""
	}
	const dialogWidth = 100

	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	greenStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)

	var content string
	switch d.step {
	case telemetryStepAsk:
		if !d.disclosureFits() {
			return "Telemetry is OFF.\nResize to 106 columns and 36 rows to read the consent question,\nor use agent-deck telemetry enable from your own shell.\n[n] Decline (remembered). No other key can enable here."
		}
		lines := strings.Split(telemetry.PromptText(d.endpoint), "\n")
		title := titleStyle.Render(lines[0])
		body := textStyle.Render(strings.Join(lines[1:], "\n"))
		hint := dimStyle.Render(telemetry.PromptChoices)
		content = lipgloss.JoinVertical(lipgloss.Left, title, body, "", hint)
	case telemetryStepGranted:
		content = lipgloss.JoinVertical(lipgloss.Left,
			greenStyle.Render("Thank you. Anonymous usage reports are on."),
			textStyle.Render("Inspect: agent-deck telemetry show-last    Turn off: agent-deck telemetry disable"),
		)
	case telemetryStepDeclined:
		msg := "Telemetry stays off. You will not be asked again."
		if d.saveErr != nil {
			msg = "Choice not saved (could not save your choice). Check: agent-deck telemetry status."
		}
		content = lipgloss.JoinVertical(lipgloss.Left,
			textStyle.Render(msg),
			dimStyle.Render("Change your mind later: agent-deck telemetry enable"),
		)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogWidth).
		Render(content)

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}
