package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type editFieldKind int

const (
	editFieldText editFieldKind = iota
	editFieldPills
	editFieldCheckbox
)

type editField struct {
	key         string
	label       string
	kind        editFieldKind
	input       textinput.Model
	pillOptions []string
	pillCursor  int
	checked     bool
}

// EditSessionDialog edits the slim set of session fields users iterate on
// at runtime. Rare flags (TitleLocked, NoTransitionNotify, Wrapper,
// Channels, etc.) stay accessible via `agent-deck session set <field>`.
type EditSessionDialog struct {
	visible       bool
	sessionID     string
	sessionTitle  string
	groupName     string
	width         int
	height        int
	fields        []editField
	focusIndex    int
	validationErr string
}

func NewEditSessionDialog() *EditSessionDialog {
	return &EditSessionDialog{}
}

// Show rebuilds the field slice. Claude-only rows are hidden for
// non-claude tools — friendlier than letting SetField reject the submit.
func (d *EditSessionDialog) Show(inst *session.Instance) {
	d.visible = true
	d.sessionID = inst.ID
	d.sessionTitle = inst.Title
	d.groupName = displayGroupName(inst.GroupPath)
	d.validationErr = ""
	d.focusIndex = 0

	tools, toolCursor := toolPillsForInstance(inst.Tool)

	d.fields = []editField{
		{key: session.FieldTitle, label: "Title", kind: editFieldText,
			input: mkInput("Session title", MaxNameLength, inst.Title)},
		{key: session.FieldTool, label: "Tool (restart)", kind: editFieldPills,
			pillOptions: tools, pillCursor: toolCursor},
	}
	if session.IsClaudeCompatible(inst.Tool) {
		skip, auto := readClaudeFlags(inst)
		d.fields = append(d.fields,
			editField{key: session.FieldSkipPermissions,
				label: "Skip permissions (restart, claude)", kind: editFieldCheckbox,
				checked: skip},
			editField{key: session.FieldAutoMode,
				label: "Auto mode (restart, claude)", kind: editFieldCheckbox,
				checked: auto},
			editField{key: session.FieldExtraArgs,
				label: "Extra args (restart, claude) — space-separated",
				kind:  editFieldText,
				input: mkInput("--model opus --verbose", 512, strings.Join(inst.ExtraArgs, " "))},
		)
		// Plugins (RFC docs/rfc/PLUGIN_ATTACH.md §4.8). v1 ships a CSV
		// text input matching the ExtraArgs shape; full multi-checkbox
		// widget is a v1.1 follow-up. Validation runs in the mutator at
		// save time — invalid catalog names produce a session-set error
		// shown via validationErr.
		if len(session.GetAvailablePluginNames()) > 0 {
			placeholder := "octopus,discord  (catalog: " + strings.Join(session.GetAvailablePluginNames(), ", ") + ")"
			d.fields = append(d.fields,
				editField{key: session.FieldPlugins,
					label: "Plugins (restart, claude) — comma-separated catalog names",
					kind:  editFieldText,
					input: mkInput(placeholder, 512, strings.Join(inst.Plugins, ","))},
			)
		}
	}
	d.updateFocus()
}

// readClaudeFlags returns the effective Skip/Auto state, mirroring
// buildClaudeCommand's fallback: empty ToolOptionsJSON means the launcher
// reads from config.toml at start time, so the dialog must too — otherwise
// a session running with --dangerously-skip-permissions (via global
// config) would show `[ ]`.
func readClaudeFlags(inst *session.Instance) (skip, auto bool) {
	if opts, err := session.UnmarshalClaudeOptions(inst.ToolOptionsJSON); err == nil && opts != nil {
		return opts.SkipPermissions, opts.AutoMode
	}
	cfg, _ := session.LoadUserConfig()
	if cfg == nil {
		return false, false
	}
	return cfg.Claude.GetDangerousMode(), cfg.Claude.AutoMode
}

// displayGroupName returns the human label for a group path. Mirrors
// session.extractGroupName (unexported there); empty path → DefaultGroupName.
func displayGroupName(groupPath string) string {
	if groupPath == "" {
		return session.DefaultGroupName
	}
	if idx := strings.LastIndex(groupPath, "/"); idx != -1 {
		return groupPath[idx+1:]
	}
	return groupPath
}

// toolPillsForInstance returns the pill list + cursor index for `tool`.
// Unknown tools (custom tool removed from config, claude-trace, etc.)
// are appended so save-without-edit stays a no-op — otherwise the cursor
// would default to slot 0 (`""` = shell) and silently wipe Tool on Enter.
func toolPillsForInstance(tool string) ([]string, int) {
	presets := buildPresetCommands()
	for i, p := range presets {
		if p == tool {
			return presets, i
		}
	}
	presets = append(presets, tool)
	return presets, len(presets) - 1
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

// IsVisible is nil-safe — some unit tests build a Home literal without
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
	Field  string
	Value  string
	IsLive bool // false = applies on next Restart()
}

// GetChanges returns only fields whose value differs from `inst`. The shape
// lets home.go batch saves and decide on the restart hint without the
// dialog touching persistence.
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
		case editFieldPills:
			if f.pillCursor < 0 || f.pillCursor >= len(f.pillOptions) {
				continue
			}
			newVal := f.pillOptions[f.pillCursor]
			if newVal != fieldInitialValue(inst, f.key) {
				changes = append(changes, Change{Field: f.key, Value: newVal, IsLive: isLive})
			}
		case editFieldCheckbox:
			newVal := strconv.FormatBool(f.checked)
			if newVal != fieldInitialValue(inst, f.key) {
				changes = append(changes, Change{Field: f.key, Value: newVal, IsLive: isLive})
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
		if f.key == session.FieldTitle {
			if strings.TrimSpace(f.input.Value()) == "" {
				return "Title cannot be empty"
			}
		}
	}
	return ""
}

// fieldInitialValue mirrors the string form Show() puts into each field, so
// GetChanges can diff against it.
func fieldInitialValue(inst *session.Instance, field string) string {
	switch field {
	case session.FieldTitle:
		return inst.Title
	case session.FieldTool:
		return inst.Tool
	case session.FieldExtraArgs:
		return strings.Join(inst.ExtraArgs, " ")
	case session.FieldPlugins:
		return strings.Join(inst.Plugins, ",")
	case session.FieldSkipPermissions:
		skip, _ := readClaudeFlags(inst)
		return strconv.FormatBool(skip)
	case session.FieldAutoMode:
		_, auto := readClaudeFlags(inst)
		return strconv.FormatBool(auto)
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

	case "left":
		if d.isPillsFocused() {
			f := &d.fields[d.focusIndex]
			f.pillCursor--
			if f.pillCursor < 0 {
				f.pillCursor = len(f.pillOptions) - 1
			}
			return d, nil
		}

	case "right":
		if d.isPillsFocused() {
			f := &d.fields[d.focusIndex]
			f.pillCursor = (f.pillCursor + 1) % len(f.pillOptions)
			return d, nil
		}

	case " ":
		// Toggle a focused checkbox; otherwise fall through so the literal
		// space reaches the focused text input below.
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

func (d *EditSessionDialog) isPillsFocused() bool {
	return d.focusIndex >= 0 && d.focusIndex < len(d.fields) &&
		d.fields[d.focusIndex].kind == editFieldPills &&
		len(d.fields[d.focusIndex].pillOptions) > 0
}

func (d *EditSessionDialog) View() string {
	if !d.visible {
		return ""
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorCyan).MarginBottom(1)
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	activeLabelStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	groupInfoStyle := lipgloss.NewStyle().Foreground(ColorPurple)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)
	helpStyle := lipgloss.NewStyle().Foreground(ColorComment).MarginTop(1)

	dialogWidth := 60
	if d.width > 0 && d.width < dialogWidth+10 {
		dialogWidth = d.width - 10
		if dialogWidth < 40 {
			dialogWidth = 40
		}
	}

	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorCyan).
		Background(ColorSurface).
		Padding(2, 4).
		Width(dialogWidth)

	var content strings.Builder
	content.WriteString(titleStyle.Render("Edit Session"))
	content.WriteString("\n")
	content.WriteString(groupInfoStyle.Render("  in group: " + d.groupName))
	content.WriteString("\n")
	content.WriteString(dimStyle.Render("  session: " + d.sessionTitle))
	content.WriteString("\n\n")

	for i, f := range d.fields {
		focused := i == d.focusIndex

		if f.kind == editFieldCheckbox {
			// renderCheckboxLine emits a single compact "▶ [x] Label\n" row,
			// matching the New Session dialog's options panel.
			content.WriteString(renderCheckboxLine(f.label, f.checked, focused))
			continue
		}

		if focused {
			content.WriteString(activeLabelStyle.Render("▶ " + f.label + ":"))
		} else {
			content.WriteString(labelStyle.Render("  " + f.label + ":"))
		}
		content.WriteString("\n  ")

		switch f.kind {
		case editFieldText:
			content.WriteString(f.input.View())
		case editFieldPills:
			content.WriteString(renderToolPills(f.pillOptions, f.pillCursor))
		}
		content.WriteString("\n")
	}

	if d.validationErr != "" {
		errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		content.WriteString("\n")
		content.WriteString(errStyle.Render("  ⚠ " + d.validationErr))
		content.WriteString("\n")
	}

	content.WriteString("\n")
	content.WriteString(helpStyle.Render("Enter save │ Esc cancel │ Tab next │ ←/→ tool │ Space toggle"))

	dialog := dialogStyle.Render(content.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

// renderToolPills mirrors newdialog's command pills (selected =
// ColorAccent background) so the new/edit pair feels visually identical.
func renderToolPills(presets []string, cursor int) string {
	if len(presets) == 0 {
		return ""
	}
	selected := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent).Bold(true).Padding(0, 2)
	idle := lipgloss.NewStyle().Foreground(ColorTextDim).Background(ColorSurface).Padding(0, 2)
	buttons := make([]string, len(presets))
	for i, cmd := range presets {
		name := cmd
		if name == "" {
			name = "shell"
		} else {
			name = displayCommandPreset(cmd)
			if def := session.GetToolDef(cmd); def != nil && def.Icon != "" {
				name = def.Icon + " " + name
			}
		}
		if i == cursor {
			buttons[i] = selected.Render(name)
		} else {
			buttons[i] = idle.Render(name)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, buttons...)
}
