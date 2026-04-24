package ui

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ClaudeOptionsPanel is a UI panel for Claude-specific launch options
// Used in both ForkDialog and NewDialog
type ClaudeOptionsPanel struct {
	// Session mode: 0=new, 1=continue, 2=resume
	sessionMode int
	// Resume session ID input (only for mode=resume)
	resumeIDInput textinput.Model
	// Extra claude CLI tokens (space-separated in input; persisted as []string).
	// NewDialog only — fork inherits parent's ExtraArgs implicitly via builder.
	extraArgsInput textinput.Model
	// Start query (#725, v1.7.67): claude-code's positional startup query.
	// Held as one string (NEVER split on spaces). NewDialog only — per-session,
	// not persisted to SQLite. Fork inherits nothing here (fork resumes an
	// existing session; the query has already been consumed).
	startQueryInput textinput.Model
	// Checkbox states
	skipPermissions      bool
	allowSkipPermissions bool
	autoMode             bool
	useChrome            bool
	useTeammateMode      bool
	// Focus tracking
	focusIndex int
	// Whether this panel is for fork dialog (fewer options)
	isForkMode bool
	// Total number of focusable elements
	focusCount int
}

// Focus indices for NewDialog mode:
// 0: Session mode (radio)
// 1: Resume ID input (only when mode=resume)
// 2: Skip permissions checkbox
// 3: Chrome checkbox

// Focus indices for ForkDialog mode:
// 0: Skip permissions checkbox
// 1: Chrome checkbox

// NewClaudeOptionsPanel creates a new panel for NewDialog
func NewClaudeOptionsPanel() *ClaudeOptionsPanel {
	resumeInput := textinput.New()
	resumeInput.Placeholder = "session_id..."
	resumeInput.CharLimit = 64
	resumeInput.Width = 30

	extraArgsInput := textinput.New()
	extraArgsInput.Placeholder = "--agent reviewer --model opus"
	extraArgsInput.CharLimit = 512
	extraArgsInput.Width = 44

	startQueryInput := textinput.New()
	startQueryInput.Placeholder = "initial prompt (not split on spaces)"
	startQueryInput.CharLimit = 1024
	startQueryInput.Width = 44

	return &ClaudeOptionsPanel{
		sessionMode:     0, // new
		resumeIDInput:   resumeInput,
		extraArgsInput:  extraArgsInput,
		startQueryInput: startQueryInput,
		isForkMode:      false,
		focusCount:      7, // session, skip, auto, chrome, teammate, extra-args, start-query
	}
}

// NewClaudeOptionsPanelForFork creates a panel for ForkDialog (fewer options)
func NewClaudeOptionsPanelForFork() *ClaudeOptionsPanel {
	return &ClaudeOptionsPanel{
		sessionMode:     0,
		resumeIDInput:   textinput.New(), // Not used in fork mode
		extraArgsInput:  textinput.New(), // Not used in fork mode
		startQueryInput: textinput.New(), // Not used in fork mode
		isForkMode:      true,
		focusCount:      3, // skip, chrome, teammate
	}
}

// SetDefaults applies default values from config
func (p *ClaudeOptionsPanel) SetDefaults(config *session.UserConfig) {
	if config != nil {
		p.skipPermissions = config.Claude.GetDangerousMode()
		p.allowSkipPermissions = config.Claude.AllowDangerousMode
		p.autoMode = config.Claude.AutoMode
	}
}

// SetFromOptions applies persisted ClaudeOptions to the panel fields.
func (p *ClaudeOptionsPanel) SetFromOptions(opts *session.ClaudeOptions) {
	if opts == nil {
		return
	}
	switch opts.SessionMode {
	case "continue":
		p.sessionMode = 1
	case "resume":
		p.sessionMode = 2
		p.resumeIDInput.SetValue(opts.ResumeSessionID)
	default:
		p.sessionMode = 0
	}
	p.skipPermissions = opts.SkipPermissions
	p.allowSkipPermissions = opts.AllowSkipPermissions
	p.autoMode = opts.AutoMode
	p.useChrome = opts.UseChrome
	p.useTeammateMode = opts.UseTeammateMode
	p.updateInputFocus()
	p.focusCount = p.getFocusCount()
}

// Focus sets focus to this panel
func (p *ClaudeOptionsPanel) Focus() {
	p.focusIndex = 0
	p.updateInputFocus()
}

// Blur removes focus from this panel
func (p *ClaudeOptionsPanel) Blur() {
	p.focusIndex = -1
	p.resumeIDInput.Blur()
	p.extraArgsInput.Blur()
	p.startQueryInput.Blur()
}

// GetExtraArgs returns the parsed extra-args tokens (whitespace-split, empties dropped).
// Callers assign the result to Instance.ExtraArgs. Tokens with embedded spaces
// cannot be expressed through this input — use CLI `--extra-arg` for that.
func (p *ClaudeOptionsPanel) GetExtraArgs() []string {
	raw := strings.TrimSpace(p.extraArgsInput.Value())
	if raw == "" {
		return nil
	}
	tokens := strings.Fields(raw)
	if len(tokens) == 0 {
		return nil
	}
	return tokens
}

// SetExtraArgs pre-fills the input from a persisted slice.
func (p *ClaudeOptionsPanel) SetExtraArgs(tokens []string) {
	p.extraArgsInput.SetValue(strings.Join(tokens, " "))
}

// GetStartQuery returns the trimmed raw input, un-split. Callers assign
// the result to Instance.StartupQuery which emits it as a single
// shell-quoted positional arg on the claude command line. This is the
// core v1.7.67 contract — never split on spaces here (#725).
func (p *ClaudeOptionsPanel) GetStartQuery() string {
	return strings.TrimSpace(p.startQueryInput.Value())
}

// SetStartQuery pre-fills the input (used by tests; the field is not
// persisted, so there is no production "restore" path).
func (p *ClaudeOptionsPanel) SetStartQuery(query string) {
	p.startQueryInput.SetValue(query)
}

// ResetStartQuery clears the start-query input. Called by NewDialog on each
// open so the per-session StartupQuery (Instance.StartupQuery, json:"-") does
// not leak across dialog invocations (#741).
func (p *ClaudeOptionsPanel) ResetStartQuery() {
	p.startQueryInput.SetValue("")
}

// IsFocused returns true if any element in the panel has focus
func (p *ClaudeOptionsPanel) IsFocused() bool {
	return p.focusIndex >= 0
}

// AtTop returns true if focus is on the first element
func (p *ClaudeOptionsPanel) AtTop() bool {
	return p.focusIndex <= 0
}

// GetOptions returns current options as ClaudeOptions
func (p *ClaudeOptionsPanel) GetOptions() *session.ClaudeOptions {
	opts := &session.ClaudeOptions{
		SkipPermissions:      p.skipPermissions,
		AllowSkipPermissions: p.allowSkipPermissions,
		AutoMode:             p.autoMode,
		UseChrome:            p.useChrome,
		UseTeammateMode:      p.useTeammateMode,
	}

	if !p.isForkMode {
		switch p.sessionMode {
		case 0:
			opts.SessionMode = "new"
		case 1:
			opts.SessionMode = "continue"
		case 2:
			opts.SessionMode = "resume"
			opts.ResumeSessionID = p.resumeIDInput.Value()
		}
	}

	return opts
}

// Update handles key events
func (p *ClaudeOptionsPanel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up":
			p.focusIndex--
			if p.focusIndex < 0 {
				p.focusIndex = p.getFocusCount() - 1
			}
			p.updateInputFocus()
			return nil

		case "down", "tab":
			p.focusIndex++
			if p.focusIndex >= p.getFocusCount() {
				p.focusIndex = 0
			}
			p.updateInputFocus()
			return nil

		case "shift+tab":
			p.focusIndex--
			if p.focusIndex < 0 {
				p.focusIndex = p.getFocusCount() - 1
			}
			p.updateInputFocus()
			return nil

		case " ":
			// Don't intercept space when focused on a text input
			if p.isResumeInputFocused() || p.isExtraArgsInputFocused() || p.isStartQueryInputFocused() {
				break // Let it fall through to text input handling
			}
			// Toggle checkbox or radio at current focus
			p.handleSpaceKey()
			return nil

		case "left", "right":
			// For session mode radio buttons
			if !p.isForkMode && p.focusIndex == 0 {
				if msg.String() == "left" {
					p.sessionMode--
					if p.sessionMode < 0 {
						p.sessionMode = 2
					}
				} else {
					p.sessionMode = (p.sessionMode + 1) % 3
				}
				return nil
			}
		}
	}

	// Update text inputs if focused
	if p.isResumeInputFocused() {
		var cmd tea.Cmd
		p.resumeIDInput, cmd = p.resumeIDInput.Update(msg)
		return cmd
	}
	if p.isExtraArgsInputFocused() {
		var cmd tea.Cmd
		p.extraArgsInput, cmd = p.extraArgsInput.Update(msg)
		return cmd
	}
	if p.isStartQueryInputFocused() {
		var cmd tea.Cmd
		p.startQueryInput, cmd = p.startQueryInput.Update(msg)
		return cmd
	}

	return nil
}

// handleSpaceKey handles space key for toggling checkboxes/radios
func (p *ClaudeOptionsPanel) handleSpaceKey() {
	if p.isForkMode {
		switch p.focusIndex {
		case 0:
			p.skipPermissions = !p.skipPermissions
		case 1:
			p.autoMode = !p.autoMode
		case 2:
			p.useChrome = !p.useChrome
		case 3:
			p.useTeammateMode = !p.useTeammateMode
		}
	} else {
		// NewDialog mode
		switch p.getFocusType() {
		case "sessionMode":
			// Cycle through modes on space
			p.sessionMode = (p.sessionMode + 1) % 3
		case "skipPermissions":
			p.skipPermissions = !p.skipPermissions
		case "autoMode":
			p.autoMode = !p.autoMode
		case "chrome":
			p.useChrome = !p.useChrome
		case "teammateMode":
			p.useTeammateMode = !p.useTeammateMode
		}
	}
}

// getFocusType returns what type of element is currently focused
func (p *ClaudeOptionsPanel) getFocusType() string {
	if p.isForkMode {
		switch p.focusIndex {
		case 0:
			return "skipPermissions"
		case 1:
			return "autoMode"
		case 2:
			return "chrome"
		case 3:
			return "teammateMode"
		}
	} else {
		idx := p.focusIndex
		// 0: session mode
		if idx == 0 {
			return "sessionMode"
		}
		// 1: resume input (only if mode == resume)
		if p.sessionMode == 2 {
			if idx == 1 {
				return "resumeInput"
			}
			idx-- // Adjust for missing resume input
		}
		// 2: skip permissions
		if idx == 1 {
			return "skipPermissions"
		}
		// 3: auto mode
		if idx == 2 {
			return "autoMode"
		}
		// 4: chrome
		if idx == 3 {
			return "chrome"
		}
		// 5: teammate mode
		if idx == 4 {
			return "teammateMode"
		}
		// 6: extra-args input
		if idx == 5 {
			return "extraArgsInput"
		}
		// 7: start-query input (v1.7.67)
		if idx == 6 {
			return "startQueryInput"
		}
	}
	return ""
}

// getFocusCount returns the number of focusable elements
func (p *ClaudeOptionsPanel) getFocusCount() int {
	if p.isForkMode {
		return 4 // skip, auto, chrome, teammate
	}

	count := 7 // session mode, skip, auto, chrome, teammate, extra-args, start-query
	if p.sessionMode == 2 {
		count++ // resume input
	}
	return count
}

// isResumeInputFocused returns true if resume input is focused
func (p *ClaudeOptionsPanel) isResumeInputFocused() bool {
	return !p.isForkMode && p.sessionMode == 2 && p.focusIndex == 1
}

// isExtraArgsInputFocused returns true if extra-args input is focused.
// Index shifts by +1 when resume mode is active (resume ID input adds a row).
func (p *ClaudeOptionsPanel) isExtraArgsInputFocused() bool {
	if p.isForkMode {
		return false
	}
	want := 5 // default: session(0) + skip(1) + auto(2) + chrome(3) + teammate(4) + extraArgs(5)
	if p.sessionMode == 2 {
		want = 6 // resume input inserts between session and skip
	}
	return p.focusIndex == want
}

// isStartQueryInputFocused returns true if start-query input is focused.
// Last focusable element in NewDialog mode (v1.7.67). Index shifts by +1
// when resume mode is active.
func (p *ClaudeOptionsPanel) isStartQueryInputFocused() bool {
	if p.isForkMode {
		return false
	}
	want := 6 // default: after extraArgs(5)
	if p.sessionMode == 2 {
		want = 7 // resume input adds one
	}
	return p.focusIndex == want
}

// updateInputFocus updates which text input has focus
func (p *ClaudeOptionsPanel) updateInputFocus() {
	p.resumeIDInput.Blur()
	p.extraArgsInput.Blur()
	p.startQueryInput.Blur()

	if p.isResumeInputFocused() {
		p.resumeIDInput.Focus()
	}
	if p.isExtraArgsInputFocused() {
		p.extraArgsInput.Focus()
	}
	if p.isStartQueryInputFocused() {
		p.startQueryInput.Focus()
	}
}

// View renders the options panel
func (p *ClaudeOptionsPanel) View() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	activeStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)
	headerStyle := lipgloss.NewStyle().Foreground(ColorComment)

	var content string

	if p.isForkMode {
		content = p.viewForkMode(labelStyle, activeStyle, dimStyle, headerStyle)
	} else {
		content = p.viewNewMode(labelStyle, activeStyle, dimStyle, headerStyle)
	}

	return content
}

// viewForkMode renders options for ForkDialog
func (p *ClaudeOptionsPanel) viewForkMode(labelStyle, activeStyle, dimStyle, headerStyle lipgloss.Style) string {
	var content string
	content += headerStyle.Render("─ Advanced Options ─") + "\n"
	content += renderCheckboxLine("Skip permissions", p.skipPermissions, p.focusIndex == 0)
	content += renderCheckboxLine("Auto mode", p.autoMode, p.focusIndex == 1)
	if p.autoMode && p.skipPermissions {
		content += dimStyle.Render("    ↑ overridden by skip permissions") + "\n"
	}
	content += renderCheckboxLine("Chrome mode", p.useChrome, p.focusIndex == 2)
	content += renderCheckboxLine("Teammate mode", p.useTeammateMode, p.focusIndex == 3)
	return content
}

// viewNewMode renders options for NewDialog
func (p *ClaudeOptionsPanel) viewNewMode(labelStyle, activeStyle, dimStyle, headerStyle lipgloss.Style) string {
	var content string
	content += headerStyle.Render("─ Claude Options ─") + "\n"

	// Session mode radio buttons
	focusIdx := 0
	radioLabel := "  Session: "
	if p.focusIndex == focusIdx {
		radioLabel = activeStyle.Render("▶ Session: ")
	}
	content += radioLabel
	content += p.renderRadio("New", p.sessionMode == 0, p.focusIndex == focusIdx) + "  "
	content += p.renderRadio("Continue", p.sessionMode == 1, p.focusIndex == focusIdx) + "  "
	content += p.renderRadio("Resume", p.sessionMode == 2, p.focusIndex == focusIdx) + "\n"
	focusIdx++

	// Resume ID input (only if resume mode)
	if p.sessionMode == 2 {
		if p.focusIndex == focusIdx {
			content += activeStyle.Render("    ▶ ID: ") + p.resumeIDInput.View() + "\n"
		} else {
			content += "      ID: " + p.resumeIDInput.View() + "\n"
		}
		focusIdx++
	}

	// Skip permissions checkbox
	content += renderCheckboxLine("Skip permissions", p.skipPermissions, p.focusIndex == focusIdx)
	focusIdx++

	// Auto mode checkbox
	content += renderCheckboxLine("Auto mode", p.autoMode, p.focusIndex == focusIdx)
	if p.autoMode && p.skipPermissions {
		content += dimStyle.Render("    ↑ overridden by skip permissions") + "\n"
	}
	focusIdx++

	// Chrome checkbox
	content += renderCheckboxLine("Chrome mode", p.useChrome, p.focusIndex == focusIdx)
	focusIdx++

	// Teammate mode checkbox
	content += renderCheckboxLine("Teammate mode", p.useTeammateMode, p.focusIndex == focusIdx)
	focusIdx++

	// Extra args input (free-form space-separated claude CLI tokens).
	if p.focusIndex == focusIdx {
		content += activeStyle.Render("  ▶ Extra args: ") + p.extraArgsInput.View() + "\n"
	} else {
		content += "    Extra args: " + p.extraArgsInput.View() + "\n"
	}
	focusIdx++

	// Start query input (v1.7.67, #725): single positional arg for claude.
	// Not split on spaces; not persisted (per-session only).
	if p.focusIndex == focusIdx {
		content += activeStyle.Render("  ▶ Start query: ") + p.startQueryInput.View() + "\n"
	} else {
		content += "    Start query: " + p.startQueryInput.View() + "\n"
	}

	return content
}

// renderCheckboxMark renders a checkbox mark [x] or [ ] with consistent styling.
// Shared across all tool option panels for visual consistency.
func renderCheckboxMark(checked, focused bool) string {
	style := lipgloss.NewStyle()
	if focused {
		style = style.Foreground(ColorAccent).Bold(true)
	}
	if checked {
		return style.Render("[x]")
	}
	return style.Render("[ ]")
}

// renderCheckboxLine renders a complete checkbox line with label, matching Claude options panel style.
// Used by Gemini and Codex options in NewDialog for visual consistency with Claude.
func renderCheckboxLine(label string, checked, focused bool) string {
	activeStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)

	cb := renderCheckboxMark(checked, focused)
	if focused {
		return activeStyle.Render("▶ ") + cb + " " + label + "\n"
	}
	return "  " + cb + " " + labelStyle.Render(label) + "\n"
}

// renderRadio renders a radio button (•) or ( )
func (p *ClaudeOptionsPanel) renderRadio(label string, selected, focused bool) string {
	style := lipgloss.NewStyle()
	if focused && selected {
		style = style.Foreground(ColorAccent).Bold(true)
	} else if selected {
		style = style.Foreground(ColorCyan)
	} else {
		style = style.Foreground(ColorComment)
	}

	if selected {
		return style.Render("(•) " + label)
	}
	return style.Render("( ) " + label)
}
