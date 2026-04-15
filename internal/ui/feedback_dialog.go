package ui

import (
	"runtime"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type feedbackStep int

const (
	stepRating feedbackStep = iota
	stepComment
	stepSent
	stepDismissed
)

// feedbackSentMsg is returned by the async send tea.Cmd when the send completes.
type feedbackSentMsg struct{ err error }

// feedbackDismissMsg is returned by the 2-second auto-dismiss timer after stepSent.
type feedbackDismissMsg struct{}

// FeedbackDialog is a self-contained in-app feedback popup.
// It follows the same concrete struct pattern as ConfirmDialog, MCPDialog, etc.
type FeedbackDialog struct {
	visible      bool
	step         feedbackStep
	rating       int
	commentInput textarea.Model
	width        int
	height       int
	version      string
	state        *feedback.State
	sender       *feedback.Sender
}

// NewFeedbackDialog creates a new FeedbackDialog in hidden state.
func NewFeedbackDialog() *FeedbackDialog {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Blur()
	return &FeedbackDialog{commentInput: ta}
}

// IsVisible returns true when the dialog is shown.
func (d *FeedbackDialog) IsVisible() bool {
	return d.visible
}

// Show displays the dialog for the given version, state, and sender.
// It resets step to stepRating and clears any previous comment.
func (d *FeedbackDialog) Show(version string, st *feedback.State, sender *feedback.Sender) {
	d.visible = true
	d.step = stepRating
	d.rating = 0
	d.version = version
	d.state = st
	d.sender = sender
	d.commentInput.SetValue("")
	d.commentInput.Blur()
}

// Hide hides the dialog and resets internal state.
func (d *FeedbackDialog) Hide() {
	d.visible = false
	d.commentInput.Blur()
	d.commentInput.SetValue("")
}

// SetSize updates the dialog dimensions so it can center itself.
func (d *FeedbackDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
}

// Update handles key events for the dialog.
// It returns the updated dialog pointer and an optional tea.Cmd.
func (d *FeedbackDialog) Update(msg tea.KeyMsg) (*FeedbackDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}

	switch d.step {
	case stepRating:
		switch msg.String() {
		case "1", "2", "3", "4", "5":
			rating := int(msg.Runes[0] - '0')
			d.rating = rating
			feedback.RecordRating(d.state, d.version, rating)
			_ = feedback.SaveState(d.state)
			d.step = stepComment
			d.commentInput.SetValue("")
			d.commentInput.Focus()
		case "n":
			feedback.RecordOptOut(d.state)
			_ = feedback.SaveState(d.state)
			d.Hide()
		case "esc":
			d.Hide()
		}

	case stepComment:
		switch msg.Type {
		case tea.KeyEnter:
			comment := d.commentInput.Value()
			d.step = stepSent
			return d, tea.Batch(d.sendCmd(comment), dismissAfter2s())
		case tea.KeyEsc:
			d.step = stepSent
			return d, tea.Batch(d.sendCmd(""), dismissAfter2s())
		default:
			var cmd tea.Cmd
			d.commentInput, cmd = d.commentInput.Update(msg)
			return d, cmd
		}

	case stepSent, stepDismissed:
		// Timer handles the rest; consume keys silently.
	}

	return d, nil
}

// sendCmd returns a tea.Cmd that calls Sender.Send in a goroutine.
func (d *FeedbackDialog) sendCmd(comment string) tea.Cmd {
	ver, rat, sender := d.version, d.rating, d.sender
	return func() tea.Msg {
		err := sender.Send(ver, rat, runtime.GOOS, runtime.GOARCH, comment)
		return feedbackSentMsg{err: err}
	}
}

// dismissAfter2s returns a tea.Cmd that fires feedbackDismissMsg after 2 seconds.
func dismissAfter2s() tea.Cmd {
	return tea.Tick(2*time.Second, func(_ time.Time) tea.Msg {
		return feedbackDismissMsg{}
	})
}

// View renders the dialog. Returns "" when hidden.
func (d *FeedbackDialog) View() string {
	if !d.visible {
		return ""
	}

	const dialogWidth = 56

	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	greenStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)

	var content string
	switch d.step {
	case stepRating:
		title := titleStyle.Render("How's agent-deck v" + d.version + "? (1-5)")
		scale := textStyle.Render("1😞  2😐  3🙂  4😀  5🤩")
		hint := dimStyle.Render("[n] No thanks  [Esc] Ask later")
		content = lipgloss.JoinVertical(lipgloss.Left, title, "", scale, "", hint)

	case stepComment:
		emoji := feedback.RatingEmoji(d.rating)
		header := titleStyle.Render("Thanks! " + emoji + "  Add a comment? (optional)")
		var commentView string
		if d.commentInput.Value() == "" {
			commentView = lipgloss.JoinVertical(lipgloss.Left,
				dimStyle.Render("type a comment, then press Enter..."),
				d.commentInput.View(),
			)
		} else {
			commentView = d.commentInput.View()
		}
		hint := dimStyle.Render("[Enter] Send  [Esc] Skip")
		content = lipgloss.JoinVertical(lipgloss.Left, header, "", commentView, "", hint)

	case stepSent, stepDismissed:
		content = greenStyle.Render("Sent! Thanks for the feedback.")
	}

	dialogBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogWidth).
		Render(content)

	if d.width > 0 && d.height > 0 {
		dialogHeight := lipgloss.Height(dialogBox)
		dialogW := lipgloss.Width(dialogBox)

		padLeft := (d.width - dialogW) / 2
		if padLeft < 0 {
			padLeft = 0
		}
		padTop := (d.height - dialogHeight) / 2
		if padTop < 0 {
			padTop = 0
		}

		var b strings.Builder
		for i := 0; i < padTop; i++ {
			b.WriteString("\n")
		}
		for _, line := range strings.Split(dialogBox, "\n") {
			b.WriteString(strings.Repeat(" ", padLeft))
			b.WriteString(line)
			b.WriteString("\n")
		}
		return b.String()
	}

	return dialogBox
}
