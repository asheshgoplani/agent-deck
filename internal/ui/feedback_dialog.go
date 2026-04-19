package ui

import (
	"fmt"
	"os/exec"
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
	stepConfirm
	stepSent
	stepDismissed
)

// feedbackSentMsg is returned by the async send tea.Cmd when the send completes.
type feedbackSentMsg struct{ err error }

// feedbackDismissMsg is returned by the 2-second auto-dismiss timer after stepSent.
type feedbackDismissMsg struct{}

// ghUserLogin returns the authenticated GitHub account login from the local `gh` CLI,
// or "" on any error. Used by stepConfirm to show users which account will carry the
// post before they consent (issue #679, v1.7.37 TUI fix). Overridable for tests.
var ghUserLogin = func() string {
	out, err := exec.Command("gh", "api", "user", "-q", ".login").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// FeedbackDialog is a self-contained in-app feedback popup.
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

	// Captured before entering stepConfirm so the disclosure view and the eventual
	// gh post operate on the same snapshot.
	pendingComment string
	pendingBody    string
	ghLogin        string

	// sentErr is populated by OnSent after a y-confirm send resolves; stepSent's
	// view renders either a success line or an explicit error line off it.
	sentErr    error
	sentResult bool // true once OnSent has been called with a feedbackSentMsg
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
func (d *FeedbackDialog) Show(version string, st *feedback.State, sender *feedback.Sender) {
	d.visible = true
	d.step = stepRating
	d.rating = 0
	d.version = version
	d.state = st
	d.sender = sender
	d.pendingComment = ""
	d.pendingBody = ""
	d.ghLogin = ""
	d.sentErr = nil
	d.sentResult = false
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

// OnSent records the result of a send attempt so stepSent can render success or
// an explicit error message. Called by the TUI runtime on feedbackSentMsg.
func (d *FeedbackDialog) OnSent(msg feedbackSentMsg) {
	d.sentErr = msg.err
	d.sentResult = true
}

// Update handles key events for the dialog.
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
			// Capture the comment + build the exact body that will be posted.
			// Do NOT send yet — advance to stepConfirm so the user sees the
			// disclosure (URL, gh login, full body) before anything leaves
			// this machine. See v1.7.37 / issue #679.
			d.pendingComment = d.commentInput.Value()
			d.pendingBody = feedback.FormatComment(d.version, d.rating, runtime.GOOS, runtime.GOARCH, d.pendingComment)
			d.ghLogin = ghUserLogin()
			d.step = stepConfirm
			return d, nil
		case tea.KeyEsc:
			// Esc at stepComment cancels — no post, no silent fallback.
			d.step = stepDismissed
			return d, dismissAfter2s()
		default:
			var cmd tea.Cmd
			d.commentInput, cmd = d.commentInput.Update(msg)
			return d, cmd
		}

	case stepConfirm:
		switch msg.String() {
		case "y", "Y":
			d.step = stepSent
			return d, tea.Batch(d.sendCmd(d.pendingComment), dismissAfter2s())
		default:
			// Anything else — 'n', 'N', Esc, Enter, stray keys — declines.
			d.step = stepDismissed
			return d, dismissAfter2s()
		}

	case stepSent, stepDismissed:
		// Timer handles the rest; consume keys silently.
	}

	return d, nil
}

// sendCmd returns a tea.Cmd that posts the formatted body directly via the injected
// GhCmd. It deliberately bypasses Sender.Send() — that path has a 3-tier clipboard/
// browser fallback which would silently publish feedback even when the user's `gh`
// is broken. On the confirm-y path we want: gh succeeds → post; gh fails → surface
// the error, post nothing.
func (d *FeedbackDialog) sendCmd(comment string) tea.Cmd {
	body := feedback.FormatComment(d.version, d.rating, runtime.GOOS, runtime.GOARCH, comment)
	gh := d.sender.GhCmd
	return func() tea.Msg {
		const ghQuery = `mutation($id:ID!,$body:String!){addDiscussionComment(input:{discussionId:$id,body:$body}){comment{id}}}`
		err := gh(
			"api", "graphql",
			"-f", "query="+ghQuery,
			"-f", "id="+feedback.DiscussionNodeID,
			"-f", "body="+body,
		)
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

	// 80 cols keeps the disclosure URL (62 chars) on a single line after the
	// "  Where:  " prefix, border, and padding — narrower dialogs wrap the URL
	// across two lines and break the test (and readability).
	const dialogWidth = 80

	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	greenStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	warnStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)

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
		hint := dimStyle.Render("[Enter] Continue  [Esc] Cancel")
		content = lipgloss.JoinVertical(lipgloss.Left, header, "", commentView, "", hint)

	case stepConfirm:
		content = d.renderConfirmView(warnStyle, textStyle, dimStyle)

	case stepSent, stepDismissed:
		content = d.renderSentView(greenStyle, errStyle, dimStyle)
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

// renderConfirmView builds the disclosure block shown before any gh post fires. It
// mirrors the CLI's renderFeedbackDisclosure wording so both surfaces give users the
// same information (URL, gh login, full body preview) before requiring an explicit y.
func (d *FeedbackDialog) renderConfirmView(warnStyle, textStyle, dimStyle lipgloss.Style) string {
	asLine := "  As:     your GitHub account   (visible to anyone on the discussion page)"
	if d.ghLogin != "" {
		asLine = fmt.Sprintf("  As:     @%s   (your own GitHub account — visible publicly)", d.ghLogin)
	}

	warning := warnStyle.Render("This feedback will be posted PUBLICLY on GitHub.")

	details := textStyle.Render(strings.Join([]string{
		"  Where:  https://github.com/asheshgoplani/agent-deck/discussions/600",
		"  How:    via the `gh` CLI (already authenticated on this machine)",
		asLine,
	}, "\n"))

	bodyHeader := textStyle.Render("Exact content that will be posted:")
	var bodyLines []string
	for _, line := range strings.Split(d.pendingBody, "\n") {
		bodyLines = append(bodyLines, "    "+line)
	}
	bodyPreview := textStyle.Render(strings.Join(bodyLines, "\n"))

	hint := dimStyle.Render("[y] Post    [n/Esc] Don't send")

	return lipgloss.JoinVertical(lipgloss.Left,
		warning,
		"",
		details,
		"",
		bodyHeader,
		bodyPreview,
		"",
		hint,
	)
}

// renderSentView shows the outcome of the send. Until OnSent fires the view shows a
// neutral "posting..." line; after OnSent it shows either success (with Discussion
// number) or the explicit error path — NEVER the ambiguous "Sent!" without a result.
func (d *FeedbackDialog) renderSentView(greenStyle, errStyle, dimStyle lipgloss.Style) string {
	if d.step == stepDismissed && !d.sentResult {
		return dimStyle.Render("Not posted.")
	}
	if !d.sentResult {
		return dimStyle.Render("Posting to Discussion #600 via gh...")
	}
	if d.sentErr != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			errStyle.Render("Error: could not post via gh. Not sent."),
			"",
			dimStyle.Render("Check `gh auth status` and try again with ctrl+e."),
		)
	}
	return greenStyle.Render("Posted to Discussion #600. Thanks!")
}
