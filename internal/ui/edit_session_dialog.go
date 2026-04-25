package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type editFieldKind int

const (
	editFieldText editFieldKind = iota
	editFieldCheckbox
)

type editField struct {
	key     string
	label   string
	kind    editFieldKind
	input   textinput.Model
	checked bool
}

// EditSessionDialog is a pure-UI form: GetChanges returns a diff for the
// caller (home.go) to apply via session.SetField. Restart sequencing,
// instancesMu, and pendingTitleChanges live on the caller side.
type EditSessionDialog struct {
	visible       bool
	sessionID     string
	sessionTitle  string
	sessionTool   string
	width         int
	height        int
	fields        []editField
	focusIndex    int
	validationErr string
}

func NewEditSessionDialog() *EditSessionDialog {
	return &EditSessionDialog{}
}

// Show rebuilds the field slice from `inst`. Channels/extra-args are
// claude-only — hiding them when Tool != "claude" is friendlier than
// surfacing SetField's "claude only" error after submit.
func (d *EditSessionDialog) Show(inst *session.Instance) {
	d.visible = true
	d.sessionID = inst.ID
	d.sessionTitle = inst.Title
	d.sessionTool = inst.Tool
	d.validationErr = ""
	d.focusIndex = 0

	d.fields = []editField{
		{key: session.FieldTitle, label: "Title", kind: editFieldText,
			input: mkInput("Session title", MaxNameLength, inst.Title)},
		{key: session.FieldColor, label: "Color (live)", kind: editFieldText,
			input: mkInput("#RRGGBB or ANSI 0..255 or empty", 7, inst.Color)},
		{key: session.FieldNotes, label: "Notes (live)", kind: editFieldText,
			input: mkInput("Free-form notes", 500, inst.Notes)},
		{key: session.FieldTitleLocked, label: "Title locked (live) — blocks Claude auto-rename",
			kind: editFieldCheckbox, checked: inst.TitleLocked},
		{key: session.FieldNoTransitionNotify, label: "Suppress transition notify (live)",
			kind: editFieldCheckbox, checked: inst.NoTransitionNotify},
		{key: session.FieldCommand, label: "Command (restart)", kind: editFieldText,
			input: mkInput("claude", 256, inst.Command)},
		{key: session.FieldWrapper, label: "Wrapper (restart) — use {command} placeholder", kind: editFieldText,
			input: mkInput("nvim +'terminal {command}'", 256, inst.Wrapper)},
		{key: session.FieldTool, label: "Tool (restart)", kind: editFieldText,
			input: mkInput("claude / gemini / codex / shell", 32, inst.Tool)},
	}
	if inst.Tool == "claude" {
		d.fields = append(d.fields,
			editField{key: session.FieldChannels, label: "Channels (restart, claude) — CSV", kind: editFieldText,
				input: mkInput("plugin:telegram@owner/repo,plugin:slack@...", 512, strings.Join(inst.Channels, ","))},
			editField{key: session.FieldExtraArgs, label: "Extra args (restart, claude) — space-separated", kind: editFieldText,
				input: mkInput("--model opus --verbose", 512, strings.Join(inst.ExtraArgs, " "))},
		)
	}
	d.updateFocus()
}

func mkInput(placeholder string, charLimit int, initial string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = charLimit
	ti.Width = 48
	ti.SetValue(initial)
	ti.Blur()
	return ti
}

func (d *EditSessionDialog) Hide() {
	d.visible = false
	for i := range d.fields {
		if d.fields[i].kind == editFieldText {
			d.fields[i].input.Blur()
		}
	}
}

// IsVisible is nil-safe: some unit tests build a Home literal without
// NewHome, so d may be nil when the main key router runs.
func (d *EditSessionDialog) IsVisible() bool {
	if d == nil {
		return false
	}
	return d.visible
}

func (d *EditSessionDialog) SessionID() string   { return d.sessionID }
func (d *EditSessionDialog) SetSize(w, h int)    { d.width, d.height = w, h }
func (d *EditSessionDialog) SetError(msg string) { d.validationErr = msg }
func (d *EditSessionDialog) ClearError()         { d.validationErr = "" }

type Change struct {
	Field    string
	Value    string
	IsLive   bool // false = applies on next Restart()
	Checkbox bool // Value is "true"/"false"
}

// GetChanges returns only fields whose value differs from `inst`. The
// returned shape lets home.go batch the save and decide on a restart hint
// without the dialog having to know about persistence.
func (d *EditSessionDialog) GetChanges(inst *session.Instance) []Change {
	var changes []Change
	for _, f := range d.fields {
		isLive := session.RestartPolicyFor(f.key) == session.FieldLive
		switch f.kind {
		case editFieldText:
			newVal := f.input.Value()
			if newVal != fieldInitialValue(inst, f.key) {
				changes = append(changes, Change{Field: f.key, Value: newVal, IsLive: isLive})
			}
		case editFieldCheckbox:
			newVal := "false"
			if f.checked {
				newVal = "true"
			}
			oldVal := fieldInitialValue(inst, f.key)
			if newVal != oldVal {
				changes = append(changes, Change{Field: f.key, Value: newVal, IsLive: isLive, Checkbox: true})
			}
		}
	}
	return changes
}

func (d *EditSessionDialog) HasRestartRequiredChanges(inst *session.Instance) bool {
	for _, c := range d.GetChanges(inst) {
		if !c.IsLive {
			return true
		}
	}
	return false
}

// Validate is best-effort pre-flight feedback; SetField re-validates
// authoritatively at commit time.
func (d *EditSessionDialog) Validate() string {
	for _, f := range d.fields {
		if f.kind != editFieldText {
			continue
		}
		switch f.key {
		case session.FieldTitle:
			if strings.TrimSpace(f.input.Value()) == "" {
				return "Title cannot be empty"
			}
		case session.FieldColor:
			if !session.IsValidSessionColor(strings.TrimSpace(f.input.Value())) {
				return "Invalid color — use #RRGGBB, ANSI 0..255, or empty"
			}
		}
	}
	return ""
}

// fieldInitialValue mirrors the string form Show() puts into each input,
// so GetChanges can diff against it.
func fieldInitialValue(inst *session.Instance, field string) string {
	switch field {
	case session.FieldTitle:
		return inst.Title
	case session.FieldColor:
		return inst.Color
	case session.FieldNotes:
		return inst.Notes
	case session.FieldCommand:
		return inst.Command
	case session.FieldWrapper:
		return inst.Wrapper
	case session.FieldTool:
		return inst.Tool
	case session.FieldChannels:
		return strings.Join(inst.Channels, ",")
	case session.FieldExtraArgs:
		return strings.Join(inst.ExtraArgs, " ")
	case session.FieldTitleLocked:
		if inst.TitleLocked {
			return "true"
		}
		return "false"
	case session.FieldNoTransitionNotify:
		if inst.NoTransitionNotify {
			return "true"
		}
		return "false"
	}
	return ""
}

func (d *EditSessionDialog) updateFocus() {
	for i := range d.fields {
		if d.fields[i].kind == editFieldText {
			if i == d.focusIndex {
				d.fields[i].input.Focus()
			} else {
				d.fields[i].input.Blur()
			}
		}
	}
}

// Update returns nil cmd on esc/enter so the outer key router can decide
// commit vs cancel.
func (d *EditSessionDialog) Update(msg tea.Msg) (*EditSessionDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "tab", "down":
		if len(d.fields) > 0 {
			d.focusIndex = (d.focusIndex + 1) % len(d.fields)
		}
		d.updateFocus()
		return d, nil

	case "shift+tab", "up":
		if len(d.fields) > 0 {
			d.focusIndex = (d.focusIndex - 1 + len(d.fields)) % len(d.fields)
		}
		d.updateFocus()
		return d, nil

	case " ":
		// Toggle checkbox; otherwise fall through to text input so a
		// literal space reaches the bubble (don't add `fallthrough` —
		// it would land in esc/enter and swallow the key).
		if d.focusIndex >= 0 && d.focusIndex < len(d.fields) && d.fields[d.focusIndex].kind == editFieldCheckbox {
			d.fields[d.focusIndex].checked = !d.fields[d.focusIndex].checked
			return d, nil
		}

	case "esc", "enter":
		return d, nil
	}

	if d.focusIndex >= 0 && d.focusIndex < len(d.fields) && d.fields[d.focusIndex].kind == editFieldText {
		var cmd tea.Cmd
		d.fields[d.focusIndex].input, cmd = d.fields[d.focusIndex].input.Update(msg)
		return d, cmd
	}

	return d, nil
}

func (d *EditSessionDialog) View() string {
	if !d.visible {
		return ""
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorCyan)
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	activeLabelStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	dialogWidth := 64
	if d.width > 0 && d.width < dialogWidth+10 {
		dialogWidth = d.width - 10
		if dialogWidth < 40 {
			dialogWidth = 40
		}
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogWidth)

	var b strings.Builder
	b.WriteString(titleStyle.Render("Edit Session Settings"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("  session: %s  │ tool: %s", d.sessionTitle, d.sessionTool)))
	b.WriteString("\n\n")

	for i, f := range d.fields {
		focused := i == d.focusIndex
		label := f.label
		if focused {
			b.WriteString(activeLabelStyle.Render("▶ " + label + ":"))
		} else {
			b.WriteString(labelStyle.Render("  " + label + ":"))
		}
		b.WriteString("\n")

		switch f.kind {
		case editFieldText:
			b.WriteString("  " + f.input.View())
		case editFieldCheckbox:
			cb := "[ ]"
			if f.checked {
				cb = "[x]"
			}
			line := fmt.Sprintf("  %s space to toggle", cb)
			if focused {
				line = lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Render(line)
			} else {
				line = labelStyle.Render(line)
			}
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	if d.validationErr != "" {
		errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		b.WriteString("\n")
		b.WriteString(errStyle.Render("  ⚠ " + d.validationErr))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("Enter save │ Esc cancel │ Tab next │ Space toggle"))

	dialog := boxStyle.Render(b.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}
