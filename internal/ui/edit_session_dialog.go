package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
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
	// editFieldEnv is a multi-line textarea holding one "KEY=VALUE" per line
	// (mirrors the New Session dialog's env textarea). Newline-separation lets a
	// value contain spaces (e.g. "MSG=hello world") — a single-line, space-split
	// view could not round-trip that. GetChanges emits one FieldEnv upsert Change
	// per line plus a "KEY=" Change per removed key.
	editFieldEnv
)

// isTextLike reports whether a field kind is edited via a single-line text input
// (focus, typing, and rendering). editFieldEnv is handled separately because it
// is backed by a multi-line textarea, not a textinput.
func isTextLike(kind editFieldKind) bool {
	return kind == editFieldText
}

type editField struct {
	key   string
	label string
	kind  editFieldKind
	input textinput.Model
	// area backs editFieldEnv (multi-line KEY=VALUE, one per line). Unused for
	// every other kind.
	area textarea.Model
	// orig is the field's initial value at dialog-build time. Used by Validate
	// and the GetChanges no-op guard to skip a field the user has not touched
	// (for editFieldEnv, orig is the newline-joined env list).
	orig        string
	pillOptions []string
	// pillLabels, when set, supplies human display text for each pillOption
	// (e.g. "Off"/"Top"/"Bottom" for the pin field). The committed value is
	// still pillOptions[cursor]; only the rendering differs. nil = render the
	// pillOptions verbatim as tool pills.
	pillLabels []string
	pillCursor int
	checked    bool
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
		// Pin position — anchors the session to the top/bottom of its group,
		// exempt from the status/recency sort (pin-sessions feature). Applies
		// to every tool, so it lives in the shared field block.
		{key: session.FieldPin, label: "Pin position", kind: editFieldPills,
			pillOptions: []string{string(session.PinNone), string(session.PinTop), string(session.PinBottom)},
			pillLabels:  []string{"Off", "Top", "Bottom"},
			pillCursor:  pinCursorFor(inst.Pin)},
		// Per-session env (all tools). Multi-line textarea, one KEY=VALUE per
		// line (matches the New Session dialog) so values may contain spaces.
		{key: session.FieldEnv,
			label: "Env (restart) — one KEY=VALUE per line; stored in plaintext",
			kind:  editFieldEnv,
			orig:  strings.Join(inst.Env, "\n"),
			area:  mkEnvArea(strings.Join(inst.Env, "\n"), d.width)},
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

// pinCursorFor maps the session's current pin mode to its pill index so the
// dialog opens with the active option highlighted.
func pinCursorFor(pin session.PinMode) int {
	switch pin {
	case session.PinTop:
		return 1
	case session.PinBottom:
		return 2
	default:
		return 0
	}
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

// mkEnvArea builds the multi-line env textarea (one KEY=VALUE per line). Mirrors
// the New Session dialog's env textarea so both surfaces round-trip values that
// contain spaces. dialogWidth is the outer dialog width (0 before first layout);
// the textarea width is derived from it, matching the View's rendering box.
func mkEnvArea(initial string, dialogWidth int) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "KEY=VALUE (one per line)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 4096
	ta.SetHeight(3)
	ta.SetWidth(envAreaWidth(dialogWidth))
	ta.SetValue(initial)
	ta.Blur()
	return ta
}

// envAreaWidth derives the env textarea's inner width from the outer dialog
// width, clamped to a sane range so it is usable before the first SetSize.
func envAreaWidth(dialogWidth int) int {
	w := dialogWidth - 16
	if w < 40 {
		w = 40
	}
	if w > 64 {
		w = 64
	}
	return w
}

func (d *EditSessionDialog) Hide() {
	d.visible = false
	for i := range d.fields {
		switch {
		case isTextLike(d.fields[i].kind):
			d.fields[i].input.Blur()
		case d.fields[i].kind == editFieldEnv:
			d.fields[i].area.Blur()
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

func (d *EditSessionDialog) SessionID() string { return d.sessionID }
func (d *EditSessionDialog) SetSize(w, h int) {
	d.width, d.height = w, h
	// Keep the env textarea width in sync with the (possibly resized) dialog.
	for i := range d.fields {
		if d.fields[i].kind == editFieldEnv {
			d.fields[i].area.SetWidth(envAreaWidth(w))
		}
	}
}
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
		case editFieldEnv:
			// Untouched guard FIRST (before canonicalization): if the textarea
			// still holds exactly what Show() seeded, emit nothing. This preserves
			// a value that canonicalization would otherwise mutate — e.g. a stored
			// "FOO=bar " (trailing space in the value) must survive an untouched
			// save rather than being trimmed to "FOO=bar".
			if f.area.Value() == f.orig {
				continue
			}
			// This textarea holds the WHOLE env list (one KEY=VALUE per line).
			// Canonicalize — trim each line, drop blank/unparseable lines — so a
			// cosmetic edit (trailing newline, blank line, padded line) that leaves
			// the effective env unchanged does not emit a spurious change.
			desired := make([]string, 0)
			for _, line := range strings.Split(f.area.Value(), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if _, _, err := session.ParseSessionEnvPair(line); err != nil {
					continue
				}
				desired = append(desired, line)
			}
			// No-op guard: the canonical desired list equals the current env
			// (inst.Env is already canonical — validated on the way in).
			joined := strings.Join(desired, "\n")
			if joined == strings.Join(inst.Env, "\n") {
				continue
			}
			// Emit ONE whole-list Change (newline-joined). The commit path replaces
			// inst.Env wholesale (like the web PATCH) rather than routing rows
			// through SetField's single-pair upsert/unset — which would treat a
			// desired empty-valued entry ("FOO=") as an unset and drop it.
			changes = append(changes, Change{Field: session.FieldEnv, Value: joined, IsLive: isLive})
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
		if f.kind == editFieldText && f.key == session.FieldTitle {
			if strings.TrimSpace(f.input.Value()) == "" {
				return "Title cannot be empty"
			}
		}
		// Env field: validate each KEY=VALUE line so an invalid entry surfaces a
		// clear error instead of silently dropping pairs / unsetting existing keys
		// (the commit path only applies GetChanges when Validate passes). Skip when
		// unchanged so the GetChanges no-op guard leaves it untouched.
		if f.kind == editFieldEnv && f.area.Value() != f.orig {
			for _, line := range strings.Split(f.area.Value(), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if _, _, err := session.ParseSessionEnvPair(line); err != nil {
					return fmt.Sprintf("Invalid env entry %q (use KEY=VALUE, one per line)", line)
				}
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
	case session.FieldEnv:
		return strings.Join(inst.Env, "\n")
	case session.FieldPlugins:
		return strings.Join(inst.Plugins, ",")
	case session.FieldSkipPermissions:
		skip, _ := readClaudeFlags(inst)
		return strconv.FormatBool(skip)
	case session.FieldAutoMode:
		_, auto := readClaudeFlags(inst)
		return strconv.FormatBool(auto)
	case session.FieldPin:
		return string(inst.Pin)
	}
	return ""
}

func (d *EditSessionDialog) updateFocus() {
	for i := range d.fields {
		focused := i == d.focusIndex
		switch {
		case isTextLike(d.fields[i].kind):
			if focused {
				d.fields[i].input.Focus()
			} else {
				d.fields[i].input.Blur()
			}
		case d.fields[i].kind == editFieldEnv:
			if focused {
				d.fields[i].area.Focus()
			} else {
				d.fields[i].area.Blur()
			}
		}
	}
}

// envFieldFocused reports whether the currently focused field is the multi-line
// env textarea. The outer key router (home.go) uses this so Enter inserts a
// newline in the textarea instead of committing the dialog.
func (d *EditSessionDialog) envFieldFocused() bool {
	return d.focusIndex >= 0 && d.focusIndex < len(d.fields) &&
		d.fields[d.focusIndex].kind == editFieldEnv
}

func (d *EditSessionDialog) focusNext() {
	if len(d.fields) > 0 {
		d.focusIndex = (d.focusIndex + 1) % len(d.fields)
	}
	d.updateFocus()
}

func (d *EditSessionDialog) focusPrev() {
	if len(d.fields) > 0 {
		d.focusIndex = (d.focusIndex - 1 + len(d.fields)) % len(d.fields)
	}
	d.updateFocus()
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
	case "tab":
		d.focusNext()
		return d, nil

	case "shift+tab":
		d.focusPrev()
		return d, nil

	case "down":
		// Inside the multi-line env textarea, ↓ moves the cursor between lines;
		// fall through to the textarea update. Elsewhere it navigates fields
		// (Tab/Shift+Tab always navigate, so the env field is still escapable).
		if !d.envFieldFocused() {
			d.focusNext()
			return d, nil
		}

	case "up":
		if !d.envFieldFocused() {
			d.focusPrev()
			return d, nil
		}

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

	case "esc":
		return d, nil

	case "enter":
		// When the env textarea is focused, Enter inserts a newline (one
		// KEY=VALUE per line) — fall through to the textarea update below. The
		// router (home.go) only delegates Enter here in that case; otherwise it
		// handles Enter as save and this dialog never sees it. ^S saves from the
		// textarea.
		if !d.envFieldFocused() {
			return d, nil
		}
	}

	if d.focusIndex >= 0 && d.focusIndex < len(d.fields) {
		switch {
		case isTextLike(d.fields[d.focusIndex].kind):
			var cmd tea.Cmd
			d.fields[d.focusIndex].input, cmd = d.fields[d.focusIndex].input.Update(msg)
			return d, cmd
		case d.fields[d.focusIndex].kind == editFieldEnv:
			var cmd tea.Cmd
			d.fields[d.focusIndex].area, cmd = d.fields[d.focusIndex].area.Update(msg)
			return d, cmd
		}
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
		case editFieldEnv:
			// Indent every textarea line to match the field column (the "\n  "
			// prefix above indents only the first line).
			content.WriteString(strings.ReplaceAll(f.area.View(), "\n", "\n  "))
		case editFieldPills:
			if f.pillLabels != nil {
				content.WriteString(renderLabelPills(f.pillLabels, f.pillCursor))
			} else {
				content.WriteString(renderToolPills(f.pillOptions, f.pillCursor))
			}
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
	// The env textarea consumes Enter as a newline, so advertise ^S for save
	// while it is focused (mirrors the New Session dialog).
	helpText := "Enter save │ ^S save │ Esc cancel │ Tab next │ ←/→ options │ Space toggle"
	if d.envFieldFocused() {
		helpText = "^S save │ Enter newline │ Esc cancel │ Tab next"
	}
	content.WriteString(helpStyle.Render(helpText))

	dialog := dialogStyle.Render(content.String())
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

// renderLabelPills renders a row of plain-text pills (no tool icons) for
// fields whose options are simple labels, e.g. the pin position. Visual
// styling matches renderToolPills so the two pill kinds read identically.
func renderLabelPills(labels []string, cursor int) string {
	if len(labels) == 0 {
		return ""
	}
	selected := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent).Bold(true).Padding(0, 2)
	idle := lipgloss.NewStyle().Foreground(ColorTextDim).Background(ColorSurface).Padding(0, 2)
	buttons := make([]string, len(labels))
	for i, label := range labels {
		if i == cursor {
			buttons[i] = selected.Render(label)
		} else {
			buttons[i] = idle.Render(label)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, buttons...)
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
