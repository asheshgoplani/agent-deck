package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// GroupDialogMode represents the dialog mode
type GroupDialogMode int

const (
	GroupDialogCreate GroupDialogMode = iota
	GroupDialogRename
	GroupDialogMove
	GroupDialogRenameSession
)

// GroupDialog handles group creation, renaming, and moving sessions
type GroupDialog struct {
	visible       bool
	mode          GroupDialogMode
	nameInput     textinput.Model
	pathInput     textinput.Model // Optional default working directory for new groups (Issue #918)
	focusIndex    int             // 0 = nameInput, 1 = pathInput (Create mode only)
	width         int
	height        int
	groupPath     string   // Current group being edited (for rename) or parent path (for create subgroup)
	parentName    string   // Display name of parent group (for subgroup creation)
	groupPaths    []string // Available target group paths (for move)
	selected      int      // Selected group index (for move)
	sessionID     string   // Session ID being renamed (for rename session)
	validationErr string   // Inline validation error displayed inside the dialog

	// Tab toggle between Root and Subgroup modes (Issue #111)
	contextParentPath string // Original cursor context parent path (for toggling back)
	contextParentName string // Original cursor context parent name (for toggling back)

	// Fzf-like live suggestions for the Default Path field in Create mode.
	// suggestProvider supplies the candidate corpus (recents + group defaults)
	// each time the dialog opens; typing fuzzy-filters it live, and path-shaped
	// queries additionally pull filesystem subdirectory completions.
	suggestProvider        func() []string
	allPathSuggestions     []string // full candidate corpus from the provider
	pathSuggestions        []string // filtered subset shown in the dropdown
	pathSuggestionCursor   int      // highlighted index into pathSuggestions (-1 = none)
	pathSuggestionsHidden  bool     // true after Esc until the user types again
	pathDropdownLineOffset int      // content line of the Default Path row (View-computed)
}

// NewGroupDialog creates a new group dialog
func NewGroupDialog() *GroupDialog {
	ti := textinput.New()
	ti.Placeholder = "Group name"
	ti.CharLimit = 50
	ti.Width = 30

	// Issue #918: optional default working directory for new groups.
	pi := textinput.New()
	pi.Placeholder = "Default path (optional)"
	pi.CharLimit = 1024
	pi.Width = 30

	return &GroupDialog{
		nameInput:  ti,
		pathInput:  pi,
		groupPaths: []string{},
	}
}

// Show shows the dialog in create mode (root level group)
func (g *GroupDialog) Show() {
	g.visible = true
	g.mode = GroupDialogCreate
	g.groupPath = "" // No parent = root level
	g.parentName = ""
	g.validationErr = ""
	g.nameInput.SetValue("")
	g.nameInput.CursorEnd() // Issue #604: reset cursor — SetValue only clamps, it does not reset.
	g.resetPathInput()
	g.focusName()
}

// ShowCreateSubgroup shows the dialog for creating a subgroup under a parent
func (g *GroupDialog) ShowCreateSubgroup(parentPath, parentName string) {
	g.visible = true
	g.mode = GroupDialogCreate
	g.groupPath = parentPath // Parent path for the new subgroup
	g.parentName = parentName
	g.validationErr = ""
	g.nameInput.SetValue("")
	g.nameInput.CursorEnd() // Issue #604
	g.resetPathInput()
	g.focusName()
}

// ShowCreateWithContext opens the create dialog with cursor context for Tab toggling.
// If parentPath is non-empty, defaults to subgroup mode with Tab toggle available.
// If parentPath is empty, opens as root-level group with no toggle.
func (g *GroupDialog) ShowCreateWithContext(parentPath, parentName string) {
	g.visible = true
	g.mode = GroupDialogCreate
	g.contextParentPath = parentPath
	g.contextParentName = parentName
	g.validationErr = ""
	g.nameInput.SetValue("")
	g.nameInput.CursorEnd() // Issue #604
	g.resetPathInput()
	g.focusName()

	if parentPath != "" {
		// Default to subgroup mode
		g.groupPath = parentPath
		g.parentName = parentName
	} else {
		// Root mode, no toggle
		g.groupPath = ""
		g.parentName = ""
	}
}

// ShowCreateWithContextDefaultRoot opens the create dialog defaulting to root mode,
// but stores the cursor context so Tab toggle can switch to subgroup mode.
// Used when the cursor is on a session inside a group (not on the group header itself).
func (g *GroupDialog) ShowCreateWithContextDefaultRoot(parentPath, parentName string) {
	g.visible = true
	g.mode = GroupDialogCreate
	g.contextParentPath = parentPath
	g.contextParentName = parentName
	g.validationErr = ""
	g.nameInput.SetValue("")
	g.nameInput.CursorEnd() // Issue #604
	g.resetPathInput()
	g.focusName()

	// Default to root mode, Tab toggles to subgroup
	g.groupPath = ""
	g.parentName = ""
}

// CanToggle returns true when the Tab toggle between Root and Subgroup is available.
// Only applies in Create mode when the cursor was on a group context.
func (g *GroupDialog) CanToggle() bool {
	return g.mode == GroupDialogCreate && g.contextParentPath != ""
}

// ToggleRootSubgroup swaps between root-level and subgroup creation modes.
func (g *GroupDialog) ToggleRootSubgroup() {
	if !g.CanToggle() {
		return
	}
	if g.groupPath == "" {
		// Currently root → switch to subgroup
		g.groupPath = g.contextParentPath
		g.parentName = g.contextParentName
	} else {
		// Currently subgroup → switch to root
		g.groupPath = ""
		g.parentName = ""
	}
	g.validationErr = ""
}

// ShowRename shows the dialog in rename mode
func (g *GroupDialog) ShowRename(currentPath, currentName string) {
	g.visible = true
	g.mode = GroupDialogRename
	g.groupPath = currentPath
	g.validationErr = ""
	g.nameInput.SetValue(currentName)
	g.nameInput.CursorEnd() // Issue #604: place cursor at end of pre-filled name.
	// Issue #1068: must reset focusIndex and blur pathInput, otherwise stale
	// state from a prior Create-dialog Tab routes keys to the invisible path.
	g.resetPathInput() // also drops stale suggestion corpus (rename has no path field)
	g.focusName()
}

// ShowMove shows the dialog for moving a session to a group path.
func (g *GroupDialog) ShowMove(groupPaths []string) {
	g.visible = true
	g.mode = GroupDialogMove
	g.validationErr = ""
	g.groupPaths = groupPaths
	g.selected = 0
}

// ShowRenameSession shows the dialog for renaming a session
func (g *GroupDialog) ShowRenameSession(sessionID, currentName string) {
	g.visible = true
	g.mode = GroupDialogRenameSession
	g.sessionID = sessionID
	g.validationErr = ""
	g.nameInput.SetValue(currentName)
	g.nameInput.CursorEnd() // Issue #604: place cursor at end of pre-filled name.
	// Issue #1068: must reset focusIndex and blur pathInput, otherwise stale
	// state from a prior Create-dialog Tab routes keys to the invisible path.
	g.resetPathInput() // also drops stale suggestion corpus (rename has no path field)
	g.focusName()
}

// GetSessionID returns the session ID being renamed
func (g *GroupDialog) GetSessionID() string {
	return g.sessionID
}

// Hide hides the dialog
func (g *GroupDialog) Hide() {
	g.visible = false
	g.nameInput.Blur()
}

// IsVisible returns whether the dialog is visible
func (g *GroupDialog) IsVisible() bool {
	return g.visible
}

// Mode returns the current dialog mode
func (g *GroupDialog) Mode() GroupDialogMode {
	return g.mode
}

// GetValue returns the input value
func (g *GroupDialog) GetValue() string {
	return strings.TrimSpace(g.nameInput.Value())
}

// GetDefaultPath returns the default-path input value for the group being
// created (Issue #918). Empty when the user left the field blank or when the
// dialog is in a mode that does not expose the field.
func (g *GroupDialog) GetDefaultPath() string {
	return strings.TrimSpace(g.pathInput.Value())
}

// SetSuggestProvider wires the candidate corpus source for the Default Path
// suggestions. It is consulted each time the dialog opens in Create mode, so
// recents captured since the last open are included.
func (g *GroupDialog) SetSuggestProvider(fn func() []string) {
	g.suggestProvider = fn
}

// SetPathSuggestions overrides the candidate corpus directly (used by tests
// and by callers that want to pin the list for a single dialog session).
func (g *GroupDialog) SetPathSuggestions(paths []string) {
	g.allPathSuggestions = paths
	g.filterPathSuggestions()
}

// refreshPathSuggestions pulls a fresh corpus from the provider (when wired)
// and refilters. Called on every Create-mode open so stale candidates never
// linger across dialogs.
func (g *GroupDialog) refreshPathSuggestions() {
	if g.suggestProvider != nil {
		g.allPathSuggestions = g.suggestProvider()
	}
	g.pathSuggestionsHidden = false
	g.filterPathSuggestions()
}

// filterPathSuggestions fuzzy-filters the corpus against the current input.
// The first match stays highlighted, fzf-style, so Tab/Enter accept without
// any arrow keys when the top hit is already right.
func (g *GroupDialog) filterPathSuggestions() {
	query := strings.TrimSpace(g.pathInput.Value())
	if query == "" {
		g.pathSuggestions = g.allPathSuggestions
	} else {
		g.pathSuggestions = filterPathSuggestions(g.allPathSuggestions, query)
	}
	switch {
	case len(g.pathSuggestions) == 0:
		g.pathSuggestionCursor = -1
	case g.pathSuggestionCursor < 0 || g.pathSuggestionCursor >= len(g.pathSuggestions):
		g.pathSuggestionCursor = 0
	}
}

// IsPathDropdownVisible reports whether the live suggestion dropdown should be
// rendered: Create mode, path field focused, at least one candidate, and not
// explicitly dismissed with Esc.
func (g *GroupDialog) IsPathDropdownVisible() bool {
	return g.mode == GroupDialogCreate &&
		g.focusIndex == 1 &&
		!g.pathSuggestionsHidden &&
		len(g.pathSuggestions) > 0
}

// navigatePathSuggestions moves the dropdown highlight by delta. Returns
// false (caller falls through to other bindings, e.g. Root/Subgroup toggle)
// when the dropdown has nothing to navigate.
func (g *GroupDialog) navigatePathSuggestions(delta int) bool {
	if !g.IsPathDropdownVisible() {
		return false
	}
	total := len(g.pathSuggestions)
	g.pathSuggestionCursor = ((g.pathSuggestionCursor+delta)%total + total) % total
	return true
}

// ApplyHighlightedPathSuggestion writes the highlighted suggestion into the
// input and hides the dropdown so the next Enter submits instead of
// re-accepting. Returns false when no suggestion was highlighted (caller
// proceeds with its own handling).
func (g *GroupDialog) ApplyHighlightedPathSuggestion() bool {
	if !g.IsPathDropdownVisible() || g.pathSuggestionCursor < 0 ||
		g.pathSuggestionCursor >= len(g.pathSuggestions) {
		return false
	}
	g.pathInput.SetValue(g.pathSuggestions[g.pathSuggestionCursor])
	g.pathInput.SetCursor(len(g.pathInput.Value()))
	g.pathSuggestionsHidden = true
	return true
}

// DismissPathSuggestions hides the dropdown until the user types again.
// Returns true when a visible dropdown was dismissed, so the parent can treat
// Esc as "dismiss picker first, close dialog second".
func (g *GroupDialog) DismissPathSuggestions() bool {
	if !g.IsPathDropdownVisible() {
		return false
	}
	g.pathSuggestionsHidden = true
	return true
}

// resetPathInput clears the path field and blurs it. Called by every Show*
// entry point so a previous Create dialog never leaks its path into a Rename.
// In Create mode it also refreshes the suggestion corpus from the provider.
func (g *GroupDialog) resetPathInput() {
	g.pathInput.SetValue("")
	g.pathInput.CursorEnd()
	g.pathInput.Blur()
	if g.mode == GroupDialogCreate {
		g.refreshPathSuggestions() // also unhides and refilters (cursor clamped)
		return
	}
	// Non-create modes expose no path field: drop any suggestion state so a
	// stale dropdown can never be resurrected by a later Tab traversal.
	g.allPathSuggestions = nil
	g.pathSuggestions = nil
	g.pathSuggestionsHidden = false
	g.pathSuggestionCursor = -1
}

// focusName focuses the name input and updates the focus index accordingly.
func (g *GroupDialog) focusName() {
	g.focusIndex = 0
	g.nameInput.Focus()
	g.pathInput.Blur()
}

// focusPath focuses the path input and updates the focus index accordingly.
func (g *GroupDialog) focusPath() {
	g.focusIndex = 1
	g.nameInput.Blur()
	g.pathInput.Focus()
}

// Validate checks if the dialog values are valid and returns an error message if not
func (g *GroupDialog) Validate() string {
	if g.mode == GroupDialogMove {
		return "" // Move mode doesn't need validation
	}

	name := strings.TrimSpace(g.nameInput.Value())

	// Check for empty name
	if name == "" {
		if g.mode == GroupDialogRenameSession {
			return "Session name cannot be empty"
		}
		return "Group name cannot be empty"
	}

	// Check name length
	if len(name) > MaxNameLength {
		return fmt.Sprintf("Name too long (max %d characters)", MaxNameLength)
	}

	// Check for "/" in group names (would break path hierarchy)
	if g.mode == GroupDialogCreate || g.mode == GroupDialogRename {
		if strings.Contains(name, "/") {
			return "Group name cannot contain '/' character"
		}
	}

	return "" // Valid
}

// SetError sets an inline validation error displayed inside the dialog
func (g *GroupDialog) SetError(msg string) {
	g.validationErr = msg
}

// ClearError clears the inline validation error
func (g *GroupDialog) ClearError() {
	g.validationErr = ""
}

// GetGroupPath returns the group path being edited (or parent path for subgroup creation)
func (g *GroupDialog) GetGroupPath() string {
	return g.groupPath
}

// GetParentPath returns the parent path for subgroup creation
func (g *GroupDialog) GetParentPath() string {
	return g.groupPath
}

// HasParent returns true if creating a subgroup under a parent
func (g *GroupDialog) HasParent() bool {
	return g.groupPath != "" && g.mode == GroupDialogCreate
}

// GetSelectedGroup returns the selected group for move mode
func (g *GroupDialog) GetSelectedGroup() string {
	if g.selected >= 0 && g.selected < len(g.groupPaths) {
		return g.groupPaths[g.selected]
	}
	return ""
}

// SetSize sets the dialog size
func (g *GroupDialog) SetSize(width, height int) {
	g.width = width
	g.height = height
}

// Update handles input
func (g *GroupDialog) Update(msg tea.KeyMsg) (*GroupDialog, tea.Cmd) {
	if g.mode == GroupDialogMove {
		switch msg.String() {
		case "up", "k":
			if g.selected > 0 {
				g.selected--
			}
		case "down", "j":
			if g.selected < len(g.groupPaths)-1 {
				g.selected++
			}
		}
		return g, nil
	}

	// Issue #918: in Create mode, Tab cycles name ↔ path. Shift+Tab cycles back.
	// Issue #1536: Tab now ALWAYS advances focus so the Default Path field is
	// reachable by normal Tab traversal. Previously, when the #111 Root/Subgroup
	// toggle was available, Tab toggled on the name field and never fell through
	// to focus the path — trapping the user. The toggle is rebound to Up/Down
	// (see below): Space is a legal group-name character, Left/Right move the
	// text cursor, so Up/Down is the only free binding for the Root/Subgroup
	// toggle across both single-line inputs.
	if g.mode == GroupDialogCreate {
		switch msg.String() {
		case "tab":
			// Accept the highlighted path suggestion instead of advancing
			// focus; the dropdown closes so the next Enter submits the form.
			if g.ApplyHighlightedPathSuggestion() {
				return g, nil
			}
			if g.focusIndex == 0 {
				g.focusPath()
			} else {
				g.focusName()
			}
			return g, nil
		case "shift+tab":
			if g.focusIndex == 0 {
				g.focusPath()
			} else {
				g.focusName()
			}
			return g, nil
		case "up", "down":
			// Navigate the suggestion dropdown while it has matches to show;
			// only when it does not (or the field is not focused) does Up/Down
			// keep its Issue #1536 Root/Subgroup toggle role. No-op (falls
			// through to the text input, which ignores Up/Down) when there is
			// neither a dropdown nor a group context to toggle into.
			delta := 1
			if msg.String() == "up" {
				delta = -1
			}
			if g.focusIndex == 1 && g.navigatePathSuggestions(delta) {
				return g, nil
			}
			if g.CanToggle() {
				g.ToggleRootSubgroup()
				return g, nil
			}
		case "ctrl+n", "ctrl+p":
			// Emacs-style navigation that never collides with the Root/Subgroup
			// toggle or text cursor movement.
			delta := 1
			if msg.String() == "ctrl+p" {
				delta = -1
			}
			if g.navigatePathSuggestions(delta) {
				return g, nil
			}
		}
	}

	var cmd tea.Cmd
	prevPath := g.pathInput.Value()
	if g.focusIndex == 1 {
		g.pathInput, cmd = g.pathInput.Update(msg)
	} else {
		g.nameInput, cmd = g.nameInput.Update(msg)
	}
	// Live filtering: any edit to the path field re-runs the fuzzy match and
	// re-opens a dropdown previously dismissed with Esc.
	if g.mode == GroupDialogCreate && g.pathInput.Value() != prevPath {
		g.pathSuggestionsHidden = false
		g.filterPathSuggestions()
	}
	return g, cmd
}

// View renders the dialog
func (g *GroupDialog) View() string {
	if !g.visible {
		return ""
	}

	var title string
	var content string

	switch g.mode {
	case GroupDialogCreate:
		labelStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
		// Issue #918: show "Name" + optional "Default Path" fields stacked.
		nameRow := labelStyle.Render("Name:         ") + g.nameInput.View()
		pathRow := labelStyle.Render("Default Path: ") + g.pathInput.View()
		fields := nameRow + "\n" + pathRow

		if g.parentName != "" {
			title = "Create Subgroup"
			parentInfo := lipgloss.NewStyle().
				Foreground(ColorCyan).
				Render("Parent: " + g.parentName)
			content = parentInfo + "\n\n" + fields
		} else {
			title = "Create New Group"
			content = fields
		}

		// Add Root/Subgroup toggle indicator when Tab toggle is available
		if g.CanToggle() {
			activeStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
			dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

			rootTab := "Root"
			subTab := "Subgroup"
			var tabs string
			if g.groupPath == "" {
				// Root mode active
				tabs = activeStyle.Render("["+rootTab+"]") + " ─── " + dimStyle.Render(subTab)
			} else {
				// Subgroup mode active
				tabs = dimStyle.Render(rootTab) + " ─── " + activeStyle.Render("["+subTab+"]")
			}
			content = tabs + "\n\n" + content
		}
	case GroupDialogRename:
		title = "Rename Group"
		content = g.nameInput.View()
	case GroupDialogMove:
		title = "Move to Group"
		var items []string
		for i, groupPath := range g.groupPaths {
			if i == g.selected {
				items = append(items, lipgloss.NewStyle().
					Foreground(ColorBg).
					Background(ColorAccent).
					Bold(true).
					Padding(0, 1).
					Render(groupPath))
			} else {
				items = append(items, lipgloss.NewStyle().
					Foreground(ColorText).
					Padding(0, 1).
					Render(groupPath))
			}
		}
		content = strings.Join(items, "\n")
	case GroupDialogRenameSession:
		title = "Rename Session"
		content = g.nameInput.View()
	}

	// Responsive dialog width
	dialogWidth := fitDialogWidth(44, 30, g.width)
	titleWidth := dialogWidth - 4

	// Content line of the Default Path row (for anchoring the suggestion
	// dropdown overlay): title + blank, then the optional toggle tabs block
	// (2 lines) and parent info block (2 lines), then the Name row.
	g.pathDropdownLineOffset = 2
	if g.mode == GroupDialogCreate {
		if g.CanToggle() {
			g.pathDropdownLineOffset += 2
		}
		if g.parentName != "" {
			g.pathDropdownLineOffset += 2
		}
		g.pathDropdownLineOffset += 1 // Name row
	}

	titleStyle := DialogTitleStyle.Width(titleWidth)
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	var hint string
	switch {
	case g.mode == GroupDialogCreate && g.IsPathDropdownVisible():
		hint = hintStyle.Render("↑↓ select │ Tab/Enter accept │ Esc dismiss")
	case g.mode == GroupDialogCreate && g.CanToggle():
		hint = hintStyle.Render("↑↓ Root/Subgroup │ Tab next │ Shift+Tab prev │ Enter confirm │ Esc cancel")
	case g.mode == GroupDialogCreate:
		hint = hintStyle.Render("Tab next │ Shift+Tab prev │ Enter confirm │ Esc cancel")
	default:
		hint = hintStyle.Render("Enter confirm │ Esc cancel")
	}

	errContent := ""
	if g.validationErr != "" {
		errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		errContent = errStyle.Render("⚠ " + g.validationErr)
	}

	dialogContent := lipgloss.JoinVertical(
		lipgloss.Center,
		titleStyle.Render(title),
		"",
		content,
		errContent,
		"",
		hint,
	)

	dialog := DialogBoxStyle.
		Width(dialogWidth).
		Render(dialogContent)

	placed := lipgloss.Place(
		g.width,
		g.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)

	// Overlay the path suggestion dropdown as a floating menu anchored to the
	// Default Path row, so its appearance/disappearance never shifts layout.
	if suggestionsOverlay := g.renderPathDropdown(); suggestionsOverlay != "" {
		topRow, leftCol := dialogOrigin(g.width, g.height, lipgloss.Width(dialog), lipgloss.Height(dialog))
		// Rows: border (1) + vertical padding (1) + content offset. Columns:
		// measure the rendered row's own leading spaces so JoinVertical's
		// centering can never skew the anchor.
		overlayRow := topRow + 1 + 1 + g.pathDropdownLineOffset
		dialogLines := strings.Split(dialog, "\n")
		lineIdx := overlayRow - topRow
		leading := 0
		if lineIdx >= 0 && lineIdx < len(dialogLines) {
			leading = leadingVisibleSpaces(stripAnsi(dialogLines[lineIdx]))
		}
		overlayCol := leftCol + leading

		placed = overlayDropdown(placed, suggestionsOverlay, overlayRow, overlayCol)
	}

	return placed
}

// renderPathDropdown renders the Default Path suggestions as a standalone
// bordered block for overlay positioning. Empty when nothing should show.
func (g *GroupDialog) renderPathDropdown() string {
	if !g.IsPathDropdownVisible() {
		return ""
	}

	menuBg := dropdownMenuBg()
	suggestionStyle := lipgloss.NewStyle().Foreground(ColorComment).Background(menuBg)
	selectedStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Background(menuBg)

	var b strings.Builder

	maxShow := 5
	if g.height > 0 && g.height <= 30 {
		maxShow = 3
	}
	total := len(g.pathSuggestions)
	startIdx := 0
	endIdx := total
	if total > maxShow {
		startIdx = g.pathSuggestionCursor - maxShow/2
		if startIdx < 0 {
			startIdx = 0
		}
		endIdx = startIdx + maxShow
		if endIdx > total {
			endIdx = total
			startIdx = endIdx - maxShow
		}
	}

	if startIdx > 0 {
		b.WriteString(suggestionStyle.Render(fmt.Sprintf("  ↑ %d more above", startIdx)))
		b.WriteString("\n")
	}
	for i := startIdx; i < endIdx; i++ {
		if i > startIdx {
			b.WriteString("\n")
		}
		style := suggestionStyle
		prefix := "  "
		if i == g.pathSuggestionCursor {
			style = selectedStyle
			prefix = "▶ "
		}
		b.WriteString(style.Render(prefix + g.pathSuggestions[i]))
	}
	if endIdx < total {
		b.WriteString("\n")
		b.WriteString(suggestionStyle.Render(fmt.Sprintf("  ↓ %d more below", total-endIdx)))
	}

	// Footer: match count when filtered, plus the key hints.
	var footerText string
	if len(g.pathSuggestions) < len(g.allPathSuggestions) {
		footerText = fmt.Sprintf(" %d/%d matches │ ↑↓ select │ Tab/Enter accept │ Esc dismiss ",
			len(g.pathSuggestions), len(g.allPathSuggestions))
	} else {
		footerText = " ↑↓ select │ Tab/Enter accept │ Esc dismiss "
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Background(menuBg).Render(footerText))

	menuStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Background(menuBg).
		Padding(0, 1)

	return menuStyle.Render(b.String())
}

// leadingVisibleSpaces counts the space characters before the first
// non-space character of an ANSI-stripped line.
func leadingVisibleSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}
