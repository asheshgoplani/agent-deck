package ui

import (
	"fmt"
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
	sourceTool    string
	sourceAccount string
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
	d.sourceTool = inst.Tool
	d.sourceAccount = inst.Account
	d.validationErr = ""
	d.focusIndex = 0

	tools, toolCursor := toolPillsForInstance(inst.Tool)

	d.fields = []editField{
		{key: session.FieldTitle, label: "Title", kind: editFieldText,
			input: mkInput("Session title", MaxNameLength, inst.Title)},
		{key: session.FieldTool, label: "Harness (choose destination first)", kind: editFieldPills,
			pillOptions: tools, pillCursor: toolCursor},
		// Pin position — anchors the session to the top/bottom of its group,
		// exempt from the status/recency sort (pin-sessions feature). Applies
		// to every tool, so it lives in the shared field block.
		{key: session.FieldPin, label: "Pin position", kind: editFieldPills,
			pillOptions: []string{string(session.PinNone), string(session.PinTop), string(session.PinBottom)},
			pillLabels:  []string{"Off", "Top", "Bottom"},
			pillCursor:  pinCursorFor(inst.Pin)},
	}
	// Account slots are shown when the source can participate in a supported
	// Claude/Codex/Pi transfer. The picker is a configured slot picker, not an
	// OAuth verifier: labels disclose that authentication is not checked until
	// the destination harness starts.
	if session.IsClaudeCompatible(inst.Tool) || session.IsCodexCompatible(inst.Tool) || inst.Tool == "pi" {
		cfg, _ := session.LoadUserConfig()
		// Create the target account row whenever any supported destination has
		// configured slots. Pi has no source-account abstraction, but changing
		// its target tool to Claude or Codex must expose that target's slots.
		// The initial target is still the current tool, and tool-pill changes
		// refresh the options below.
		if len(session.ConfiguredAccountNamesForSwitch(cfg)) > 0 {
			accounts := session.ConfiguredAccountNamesForHarness(cfg, inst.Tool)
			opts, labels, cursor := accountPillsForInstance(inst.Account, accounts)
			d.fields = append(d.fields, editField{
				key:         session.FieldAccount,
				label:       accountFieldLabel(inst.Tool),
				kind:        editFieldPills,
				pillOptions: opts,
				pillLabels:  labels,
				pillCursor:  cursor,
			})
		}
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

// accountPillsForInstance returns the account row's options, their display
// labels, and the cursor for the session's stored slot. Index 0 is always
// "inherit" (the empty slot, i.e. the conductor/group/env chain).
//
// A stored slot whose profile has since been dropped from config.toml is
// appended as its own pill rather than folded into "inherit" — the same guard
// toolPillsForInstance applies to an unknown tool, and for the same reason: a
// save-without-editing must stay a no-op instead of silently rewriting the
// field to whatever slot 0 happens to be.
func accountPillsForInstance(account string, accounts []string) (opts, labels []string, cursor int) {
	opts = append([]string{""}, accounts...)
	labels = append([]string{"inherit"}, accounts...)
	for i, name := range accounts {
		if name == account {
			return opts, labels, i + 1
		}
	}
	if account != "" {
		opts = append(opts, account)
		labels = append(labels, account+" (not configured)")
		return opts, labels, len(opts) - 1
	}
	return opts, labels, 0
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
	case session.FieldPin:
		return string(inst.Pin)
	case session.FieldAccount:
		return inst.Account
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
			if f.key == session.FieldTool {
				d.refreshTargetAccountPills(f.pillOptions[f.pillCursor])
			}
			return d, nil
		}

	case "right":
		if d.isPillsFocused() {
			f := &d.fields[d.focusIndex]
			f.pillCursor = (f.pillCursor + 1) % len(f.pillOptions)
			if f.key == session.FieldTool {
				d.refreshTargetAccountPills(f.pillOptions[f.pillCursor])
			}
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

func (d *EditSessionDialog) refreshTargetAccountPills(targetHarness string) {
	cfg, _ := session.LoadUserConfig()
	accounts := session.ConfiguredAccountNamesForHarness(cfg, targetHarness)
	for index := range d.fields {
		field := &d.fields[index]
		if field.key != session.FieldAccount {
			continue
		}
		selected := ""
		if field.pillCursor >= 0 && field.pillCursor < len(field.pillOptions) {
			selected = field.pillOptions[field.pillCursor]
		}
		opts, labels, cursor := accountPillsForInstance(selected, accounts)
		// A slot unavailable on the selected target is not carried forward as
		// a hidden stale choice; the confirmation/backend receives only a
		// target-harness configured account or the explicit default.
		if selected != "" {
			found := false
			for _, account := range accounts {
				found = found || account == selected
			}
			if !found {
				opts, labels, cursor = accountPillsForInstance("", accounts)
			}
		}
		field.pillOptions, field.pillLabels, field.pillCursor = opts, labels, cursor
		field.label = accountFieldLabel(targetHarness)
		return
	}
}

func accountFieldLabel(harness string) string {
	switch session.CanonicalSwitchHarnessForUI(harness) {
	case "pi":
		return "Account for Pi (default only)"
	case "claude", "codex":
		return "Account for selected harness (configured; auth unverified)"
	default:
		return "Account (select a supported harness first)"
	}
}

// FocusField moves focus to the row with the given field key (no-op when the
// row is not shown), so a cancelled switch confirmation lands the user back
// on the row they were changing.
func (d *EditSessionDialog) FocusField(key string) {
	for i := range d.fields {
		if d.fields[i].key == key {
			d.focusIndex = i
			d.updateFocus()
			return
		}
	}
}

// switchPending reports whether saving now would run a harness/account
// switch (the transactional path in handleEditSessionDialogKey) rather than
// plain field writes.
func (d *EditSessionDialog) switchPending() bool {
	if target := d.selectedPill(session.FieldTool); target != "" && target != d.sourceTool {
		return true
	}
	account := d.selectedPill(session.FieldAccount)
	return account != "" && account != d.sourceAccount
}

// footerHint says what the keys do on the focused row. The harness and
// account rows are where "save" becomes a switch (restart, conversation
// carried over), so their footer says that Enter asks first.
func (d *EditSessionDialog) footerHint(compact bool) string {
	if d.focusIndex < 0 || d.focusIndex >= len(d.fields) {
		return "Enter save │ Esc cancel │ Tab next"
	}
	f := d.fields[d.focusIndex]
	sep := " │ "
	if compact {
		sep = " · "
	}
	switch {
	case f.key == session.FieldAccount || f.key == session.FieldTool:
		what := "account"
		if f.key == session.FieldTool {
			what = "harness"
		}
		if d.switchPending() {
			return strings.Join([]string{"←/→ " + what, "Enter switch (asks first)", "Esc cancel"}, sep)
		}
		return strings.Join([]string{"←/→ " + what, "Enter save", "Tab next", "Esc cancel"}, sep)
	case f.kind == editFieldPills:
		return strings.Join([]string{"←/→ choose", "Enter save", "Tab next", "Esc cancel"}, sep)
	case f.kind == editFieldCheckbox:
		return strings.Join([]string{"Space toggle", "Enter save", "Tab next", "Esc cancel"}, sep)
	default:
		return strings.Join([]string{"Type to edit", "Enter save", "Tab next", "Esc cancel"}, sep)
	}
}

func (d *EditSessionDialog) isPillsFocused() bool {
	return d.focusIndex >= 0 && d.focusIndex < len(d.fields) &&
		d.fields[d.focusIndex].kind == editFieldPills &&
		len(d.fields[d.focusIndex].pillOptions) > 0
}

func (d *EditSessionDialog) selectedPill(key string) string {
	for _, field := range d.fields {
		if field.key == key && field.pillCursor >= 0 && field.pillCursor < len(field.pillOptions) {
			return field.pillOptions[field.pillCursor]
		}
	}
	return ""
}

func (d *EditSessionDialog) hasField(key string) bool {
	for _, field := range d.fields {
		if field.key == key {
			return true
		}
	}
	return false
}

func (d *EditSessionDialog) switchSummary() string {
	targetHarness := d.selectedPill(session.FieldTool)
	if targetHarness == "" {
		targetHarness = d.sourceTool
	}
	targetAccount := d.selectedPill(session.FieldAccount)
	if !d.hasField(session.FieldAccount) && session.CanonicalSwitchHarnessForUI(targetHarness) == session.CanonicalSwitchHarnessForUI(d.sourceTool) {
		targetAccount = d.sourceAccount
	}
	source := fmt.Sprintf("%s/%s", d.sourceTool, displayEditAccount(d.sourceAccount))
	target := fmt.Sprintf("%s/%s", targetHarness, displayEditAccount(targetAccount))
	if session.CanonicalSwitchHarnessForUI(targetHarness) == session.CanonicalSwitchHarnessForUI(d.sourceTool) {
		return "Current: " + source + " → " + target + "\nsame-harness resume"
	}
	return "Current: " + source + " → NEW " + target + "\nsource kept"
}

func displayEditAccount(account string) string {
	if strings.TrimSpace(account) == "" {
		return "default"
	}
	return account
}

func clipEditDialogText(text string, maxRunes int) string {
	if maxRunes < 2 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes-1]) + "…"
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
		dialogWidth = d.width - 4
		if dialogWidth < 24 {
			dialogWidth = 24
		}
	}
	lineWidth := dialogWidth - 8
	if lineWidth < 12 {
		lineWidth = 12
	}
	compact := dialogWidth < 45

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
	content.WriteString(dimStyle.Render("  session: " + clipEditDialogText(d.sessionTitle, lineWidth-11)))
	content.WriteString("\n")
	for n, line := range strings.Split(d.switchSummary(), "\n") {
		if n > 0 {
			content.WriteString("\n")
		}
		content.WriteString(dimStyle.Render("  " + clipEditDialogText(line, lineWidth)))
	}
	content.WriteString("\n\n")

	for i, f := range d.fields {
		focused := i == d.focusIndex

		if f.kind == editFieldCheckbox {
			// renderCheckboxLine emits a single compact "▶ [x] Label\n" row,
			// matching the New Session dialog's options panel.
			content.WriteString(renderCheckboxLine(f.label, f.checked, focused))
			continue
		}

		fieldLabel := clipEditDialogText(f.label, lineWidth-3)
		if focused {
			content.WriteString(activeLabelStyle.Render("▶ " + fieldLabel + ":"))
		} else {
			content.WriteString(labelStyle.Render("  " + fieldLabel + ":"))
		}
		content.WriteString("\n  ")

		switch f.kind {
		case editFieldText:
			content.WriteString(f.input.View())
		case editFieldPills:
			if compact && f.pillCursor >= 0 {
				if f.pillLabels != nil && f.pillCursor < len(f.pillLabels) {
					content.WriteString(renderLabelPills([]string{f.pillLabels[f.pillCursor]}, 0))
				} else if f.pillCursor < len(f.pillOptions) {
					content.WriteString(renderToolPills([]string{f.pillOptions[f.pillCursor]}, 0))
				}
			} else if f.pillLabels != nil {
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
	if session.CanonicalSwitchHarnessForUI(d.selectedPill(session.FieldTool)) == "pi" {
		content.WriteString(dimStyle.Render("  Pi uses its default account only."))
		content.WriteString("\n")
	}
	content.WriteString(helpStyle.Render(clipEditDialogText(d.footerHint(compact), lineWidth)))

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
