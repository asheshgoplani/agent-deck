package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// promptSubmitMsg sends a message to the selected session without attaching.
// Delivery targets the session's managed agent pane, not a window sub-row.
type promptSubmitMsg struct {
	instanceID string
	text       string
	queue      bool
}

type promptEditorMsg struct {
	text string
	err  error
}

// PromptInputDialog is a multiline composer anchored above the dashboard footer.
type PromptInputDialog struct {
	input       textarea.Model
	visible     bool
	width       int
	height      int
	instanceID  string
	title       string
	queue       bool
	editorChord bool
}

// NewPromptInputDialog creates the inline prompt input (hidden).
func NewPromptInputDialog() *PromptInputDialog {
	ti := textarea.New()
	ti.Placeholder = "Type a message…"
	ti.CharLimit = 0
	ti.ShowLineNumbers = false
	ti.Prompt = ""
	ti.SetWidth(60)
	ti.SetHeight(2)
	return &PromptInputDialog{input: ti}
}

// Show opens the input targeting the given session and focuses it.
func (d *PromptInputDialog) Show(instanceID, title string) {
	d.visible = true
	d.instanceID = instanceID
	d.title = title
	d.queue = false
	d.editorChord = false
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
	d.input.SetWidth(w)
}

// Update handles a key while the input is visible. On Enter with non-empty
// trimmed text it returns a promptSubmitMsg and hides; Esc cancels; all other
// keys feed the textarea. Ctrl+J inserts a newline; Ctrl+E or Ctrl+X E opens an editor.
func (d *PromptInputDialog) Update(msg tea.KeyMsg) (*PromptInputDialog, tea.Cmd) {
	if d == nil || !d.visible {
		return d, nil
	}
	if d.editorChord {
		d.editorChord = false
		if msg.String() == "e" || msg.String() == "ctrl+e" {
			return d, editPrompt(d.input.Value())
		}
	}
	switch msg.String() {
	case "ctrl+e":
		return d, editPrompt(d.input.Value())
	case "ctrl+j", "shift+enter", "alt+enter":
		d.input.InsertString("\n")
		return d, nil
	case "ctrl+x":
		d.editorChord = true
		return d, nil
	case "esc":
		d.Hide()
		return d, nil
	case "enter":
		text := strings.TrimSpace(d.input.Value())
		instanceID := d.instanceID
		queue := d.queue
		if text == "" {
			d.Hide()
			return d, nil
		}
		d.Hide()
		return d, func() tea.Msg {
			return promptSubmitMsg{instanceID: instanceID, text: text, queue: queue}
		}
	default:
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return d, cmd
	}
}

// View appends the bar to a body that has already reserved space for it.
func (d *PromptInputDialog) View(listBody string) string {
	if !d.IsVisible() {
		return listBody
	}
	return listBody + "\n" + d.Bar()
}

// Bar is laid out in reserved rows above the footer, not over session info.
func (d *PromptInputDialog) Bar() string {
	if d == nil || !d.visible {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	barWidth := d.width - 4
	if barWidth < 1 {
		barWidth = d.width
	}
	action := "Send"
	if d.queue {
		action = "Queue"
	}
	label := action + " → " + cellTruncate(d.title, max(1, barWidth-12), "…")
	bar := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1).
		Width(barWidth).
		Render(labelStyle.Render(label) + "\n" + d.input.View() + "\n" +
			dimStyle.Render("Enter "+action+"   Ctrl+J Newline   Ctrl+X E Editor   Esc Cancel"))
	return bar
}

// ReservedHeight reports the rows the visible composer needs above the footer.
func (d *PromptInputDialog) ReservedHeight() int {
	if !d.IsVisible() {
		return 0
	}
	return lipgloss.Height(d.Bar())
}

// editPrompt opens a private temporary draft in VISUAL, EDITOR, or vi and removes it on return.
func editPrompt(text string) tea.Cmd {
	f, err := os.CreateTemp("", "agent-deck-message-*.txt")
	if err != nil {
		return func() tea.Msg { return promptEditorMsg{err: err} }
	}
	path := f.Name()
	_, writeErr := f.WriteString(text)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return func() tea.Msg {
			return promptEditorMsg{err: fmt.Errorf("write editor draft: %v / %v", writeErr, closeErr)}
		}
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command("sh", "-c", editor+" \"$1\"", "editor", path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(path)
		if err != nil {
			return promptEditorMsg{err: err}
		}
		data, err := os.ReadFile(path)
		return promptEditorMsg{text: string(data), err: err}
	})
}
