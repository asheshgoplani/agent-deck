package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// promptSubmitMsg is emitted when the operator submits a one-line prompt from
// the main list (issue #1410). Home routes it to the target session via the
// existing prompt-state-aware send path (the #1409/#1432 composer guard), with
// no attach. Delivery targets the session's default pane — the guarded send
// (deliverToConductorPane) does not address individual tmux windows — so there
// is no per-window target here.
type promptSubmitMsg struct {
	instanceID string
	text       string
}

// PromptInputDialog is a one-line input anchored at the bottom of the list that
// sends a prompt to the highlighted session without attaching (issue #1410,
// Lawrence-Dawson feedback). It mirrors the Search component: a focused
// textinput.Model that consumes keys while visible and surfaces submit/cancel.
type PromptInputDialog struct {
	input      textinput.Model
	visible    bool
	width      int
	height     int
	instanceID string
	title      string
}

// NewPromptInputDialog creates the inline prompt input (hidden).
func NewPromptInputDialog() *PromptInputDialog {
	ti := textinput.New()
	ti.Placeholder = "Type a prompt and press Enter to send (Esc to cancel)…"
	ti.CharLimit = 2000
	ti.Width = 60
	return &PromptInputDialog{input: ti}
}

// Show opens the input targeting the given session and focuses it.
func (d *PromptInputDialog) Show(instanceID, title string) {
	d.visible = true
	d.instanceID = instanceID
	d.title = title
	d.input.SetValue("")
	d.input.Focus()
}

// Hide closes the input and blurs it.
func (d *PromptInputDialog) Hide() {
	d.visible = false
	d.input.Blur()
	d.instanceID = ""
	d.title = ""
}

// IsVisible reports whether the input is open. Nil-safe: some test paths and
// early-init code construct a Home without this dialog, and IsVisible is called
// from the hot modal-dispatch path on every key.
func (d *PromptInputDialog) IsVisible() bool { return d != nil && d.visible }

// SetSize updates the layout dimensions and the input width.
func (d *PromptInputDialog) SetSize(width, height int) {
	if d == nil {
		return
	}
	d.width = width
	d.height = height
	w := width - 20
	if w < 20 {
		w = 20
	}
	if w > 120 {
		w = 120
	}
	d.input.Width = w
}

// Update handles a key while the input is visible. On Enter with non-empty
// trimmed text it returns a promptSubmitMsg and hides; Esc cancels; all other
// keys feed the textinput.
func (d *PromptInputDialog) Update(msg tea.KeyMsg) (*PromptInputDialog, tea.Cmd) {
	if d == nil || !d.visible {
		return d, nil
	}
	switch msg.String() {
	case "esc":
		d.Hide()
		return d, nil
	case "enter":
		text := strings.TrimSpace(d.input.Value())
		instanceID := d.instanceID
		if text == "" {
			d.Hide()
			return d, nil
		}
		d.Hide()
		return d, func() tea.Msg {
			return promptSubmitMsg{instanceID: instanceID, text: text}
		}
	default:
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return d, cmd
	}
}

// View overlays the prompt bar at the bottom of the (already rendered) list
// body, trimming the body so the composite fits the viewport height.
func (d *PromptInputDialog) View(listBody string) string {
	if d == nil || !d.visible {
		return listBody
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	barWidth := d.width - 2
	if barWidth < 1 {
		barWidth = d.width
	}
	label := "Prompt → " + d.title
	bar := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1).
		Width(barWidth).
		Render(labelStyle.Render(label) + "\n" + d.input.View() + "\n" +
			dimStyle.Render("Enter Send   Esc Cancel   (sends without attaching)"))

	// Reserve space for the bar at the bottom: trim the list body so the
	// composite stays within the viewport height.
	barHeight := lipgloss.Height(bar)
	bodyLines := strings.Split(listBody, "\n")
	maxBody := d.height - barHeight
	if maxBody < 0 {
		maxBody = 0
	}
	if len(bodyLines) > maxBody {
		bodyLines = bodyLines[:maxBody]
	}
	return strings.Join(bodyLines, "\n") + "\n" + bar
}
