package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// overlayDropdown paints `overlay` on top of `base` starting at the given
// row and column (0-indexed). Lines of the overlay replace the characters
// underneath while preserving the rest of each base line. This gives a
// "z-index" effect for floating dropdowns.
func overlayDropdown(base string, overlay string, row, col int) string {
	baseLines := strings.Split(base, "\n")
	overLines := strings.Split(overlay, "\n")

	for i, ol := range overLines {
		targetRow := row + i
		if targetRow < 0 || targetRow >= len(baseLines) {
			continue
		}
		bl := baseLines[targetRow]
		blWidth := lipgloss.Width(bl)

		// Build: [left padding] [overlay line] [right remainder]
		var result strings.Builder

		if col > 0 {
			if col <= blWidth {
				// Truncate base line to col visible chars
				result.WriteString(truncateVisible(bl, col))
			} else {
				// Base line is shorter than col; pad with spaces
				result.WriteString(bl)
				result.WriteString(strings.Repeat(" ", col-blWidth))
			}
		}

		result.WriteString(ol)

		// Append remaining base chars after the overlay
		olWidth := lipgloss.Width(ol)
		afterCol := col + olWidth
		if afterCol < blWidth {
			result.WriteString(sliceVisibleFrom(bl, afterCol))
		}

		baseLines[targetRow] = result.String()
	}

	return strings.Join(baseLines, "\n")
}

// truncateVisible returns the prefix of s that spans exactly n visible columns.
// ANSI escape sequences are preserved for any characters included.
func truncateVisible(s string, n int) string {
	if n <= 0 {
		return ""
	}
	visible := 0
	inEsc := false
	var buf strings.Builder
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			buf.WriteRune(r)
			continue
		}
		if inEsc {
			buf.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' || r == '\\' {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			break
		}
		buf.WriteRune(r)
		visible++
	}
	return buf.String()
}

// sliceVisibleFrom returns the suffix of s starting from visible column n.
// ANSI sequences attached to skipped characters are dropped.
func sliceVisibleFrom(s string, n int) string {
	if n <= 0 {
		return s
	}
	visible := 0
	inEsc := false
	for i, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' || r == '\\' {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			return s[i:]
		}
		visible++
	}
	return ""
}

// focusTarget identifies a focusable element in the new session dialog.
type focusTarget int

const (
	focusName      focusTarget = iota
	focusPath                  // project path input (hidden when multi-repo enabled).
	focusCommand               // tool/command picker.
	focusWorktree              // worktree checkbox.
	focusSandbox               // sandbox checkbox.
	focusConductor             // conducting parent dropdown (conditional — only when conductors exist).
	focusMultiRepo             // multi-repo toggle (transforms path into list when enabled).
	focusInherited             // inherited Docker settings toggle (conditional).
	focusBranch                // branch input (conditional — only when worktree enabled).
	focusOptions               // tool-specific options panel (conditional).
)

// New session dialog: outer box and textinput widths stay in sync so long
// project paths are not clipped in the path field.
const (
	newDialogPreferredOuterWidth = 84
	newDialogMinOuterWidth       = 44
	newDialogTerminalGutter      = 10 // margin when shrinking to terminal width
	newDialogInputWidthPad       = 12 // outer width minus indent ≈ textinput width
	newDialogInputMinWidth       = 28
	newDialogInputMaxWidth       = 100
)

// settingDisplay pairs a label with a formatted value for read-only display.
type settingDisplay struct {
	label string
	value string
}

// NewDialog represents the new session creation dialog.
type NewDialog struct {
	nameInput            textinput.Model
	pathInput            textinput.Model
	commandInput         textinput.Model
	claudeOptions        *ClaudeOptionsPanel // Claude-specific options (concrete for value extraction).
	geminiOptions        *YoloOptionsPanel   // Gemini YOLO panel (concrete for value extraction).
	codexOptions         *YoloOptionsPanel   // Codex YOLO panel (concrete for value extraction).
	toolOptions          OptionsPanel        // Currently active tool options panel (nil if none).
	focusTargets         []focusTarget       // Ordered list of active focusable elements.
	focusIndex           int                 // Index into focusTargets.
	width                int
	height               int
	visible              bool
	presetCommands       []string
	commandCursor        int
	parentGroupPath      string
	parentGroupName      string
	pathSuggestions      []string // filtered subset of path suggestions shown in dropdown.
	allPathSuggestions   []string // full unfiltered set of path suggestions.
	pathSuggestionCursor int      // tracks selected entry in dropdown (0 = "Type custom", 1.. = suggestions).
	suggestionNavigated  bool     // tracks if user explicitly navigated suggestions.
	pathSoftSelected     bool     // true when path text is "soft selected" (ready to replace on type).
	suggestionsActive    bool     // true when arrow-key focus is inside the suggestions dropdown.
	suggestionsHidden    bool     // true when the dropdown is explicitly dismissed (e.g. after Enter).
	// Worktree support.
	worktreeEnabled bool
	branchInput     textinput.Model
	branchAutoSet   bool   // true if branch was auto-derived from session name.
	branchPrefix    string // configured prefix for auto-generated branch names.
	branchPicker    *BranchPickerDialog
	// Docker sandbox support.
	sandboxEnabled    bool
	inheritedExpanded bool             // whether the inherited settings section is expanded.
	inheritedSettings []settingDisplay // non-default Docker config values to display.
	// Inline validation error displayed inside the dialog.
	validationErr         string
	pathCycler            session.CompletionCycler // Path autocomplete state.
	suggestionsLineOffset int                      // Content line where suggestions overlay should appear.
	// Multi-repo mode.
	multiRepoEnabled    bool
	multiRepoPaths      []string // All paths when multi-repo is active.
	multiRepoPathCursor int      // Selected path index in the stacked list.
	multiRepoEditing    bool     // True when editing a path entry.
	// Recent sessions picker.
	recentSessions      []*statedb.RecentSessionRow
	recentSessionCursor int
	showRecentPicker    bool
	recentSnapshot      *dialogSnapshot // saved state to restore on Esc
	// Conducting parent selector.
	conductorSessions []*session.Instance // nil when no conductors; populated by ShowInGroup
	conductorCursor   int                 // 0 = "None", 1..N index into conductorSessions
}

// dialogSnapshot captures form state so the recent picker can restore on cancel.
type dialogSnapshot struct {
	name             string
	path             string
	commandCursor    int
	commandInput     string
	sandboxEnabled   bool
	worktreeEnabled  bool
	branch           string
	branchAutoSet    bool
	claudeOptions    *session.ClaudeOptions
	geminiYolo       bool
	codexYolo        bool
	multiRepoEnabled bool
	multiRepoPaths   []string
	conductorCursor  int
}

// buildPresetCommands returns the list of commands for the picker,
// including any custom tools from config.toml.
func buildPresetCommands() []string {
	presets := []string{"", "claude", "gemini", "opencode", "codex", "pi", "copilot"}
	if customTools := session.GetCustomToolNames(); len(customTools) > 0 {
		presets = append(presets, customTools...)
	}
	return presets
}

// buildInheritedSettings returns display pairs for non-default Docker config values.
func buildInheritedSettings(docker session.DockerSettings) []settingDisplay {
	var settings []settingDisplay
	if docker.DefaultImage != "" {
		settings = append(settings, settingDisplay{label: "Image", value: docker.DefaultImage})
	}
	if docker.CPULimit != "" {
		settings = append(settings, settingDisplay{label: "CPU Limit", value: docker.CPULimit})
	}
	if docker.MemoryLimit != "" {
		settings = append(settings, settingDisplay{label: "Memory Limit", value: docker.MemoryLimit})
	}
	if docker.MountSSH {
		settings = append(settings, settingDisplay{label: "Mount SSH", value: "yes"})
	}
	if len(docker.VolumeIgnores) > 0 {
		settings = append(
			settings,
			settingDisplay{label: "Volume Ignores", value: fmt.Sprintf("%d items", len(docker.VolumeIgnores))},
		)
	}
	if len(docker.Environment) > 0 {
		settings = append(
			settings,
			settingDisplay{label: "Env Vars", value: fmt.Sprintf("%d items", len(docker.Environment))},
		)
	}
	return settings
}

// NewNewDialog creates a new NewDialog instance
func NewNewDialog() *NewDialog {
	// Create name input
	nameInput := textinput.New()
	nameInput.Placeholder = "session-name"
	nameInput.Focus()
	nameInput.CharLimit = MaxNameLength

	// Create path input
	pathInput := textinput.New()
	pathInput.Placeholder = "~/project/path"
	pathInput.CharLimit = 256
	pathInput.ShowSuggestions = false // we use our own dropdown with filtering

	// Get current working directory for default path
	cwd, err := os.Getwd()
	if err == nil {
		pathInput.SetValue(cwd)
	}

	// Create command input
	commandInput := textinput.New()
	commandInput.Placeholder = "custom command"
	commandInput.CharLimit = 100

	// Create branch input for worktree
	branchInput := textinput.New()
	branchInput.Placeholder = "feature/branch-name"
	branchInput.CharLimit = 100

	dlg := &NewDialog{
		nameInput:       nameInput,
		pathInput:       pathInput,
		commandInput:    commandInput,
		branchInput:     branchInput,
		branchPicker:    NewBranchPickerDialog(),
		claudeOptions:   NewClaudeOptionsPanel(),
		geminiOptions:   NewYoloOptionsPanel("Gemini", "YOLO mode - auto-approve all"),
		codexOptions:    NewYoloOptionsPanel("Codex", "YOLO mode - bypass approvals and sandbox"),
		focusIndex:      0,
		visible:         false,
		presetCommands:  buildPresetCommands(),
		commandCursor:   0,
		parentGroupPath: "default",
		parentGroupName: "default",
		worktreeEnabled: false,
		branchPrefix:    "feature/",
	}
	dlg.syncInputWidths()
	dlg.updateToolOptions() // Also calls rebuildFocusTargets.
	return dlg
}

// ShowInGroup shows the dialog with a pre-selected parent group and optional default path.
// conductors is the list of active conductor sessions available as parent options.
func (d *NewDialog) ShowInGroup(groupPath, groupName, defaultPath string, conductors []*session.Instance, suggestedParentID string) {
	if groupPath == "" {
		groupPath = "default"
		groupName = "default"
	}
	d.parentGroupPath = groupPath
	d.parentGroupName = groupName
	d.visible = true
	d.focusIndex = 0
	d.validationErr = ""
	d.nameInput.SetValue("")
	d.nameInput.Focus()
	d.suggestionNavigated = false // reset on show
	d.pathSuggestionCursor = 0    // reset cursor too
	d.suggestionsActive = false
	d.suggestionsHidden = false
	d.pathCycler.Reset()       // clear stale autocomplete matches from previous show
	d.showRecentPicker = false // reset recent picker
	d.recentSessionCursor = 0
	d.conductorSessions = conductors
	d.conductorCursor = 0
	for i, c := range conductors {
		if c.ID == suggestedParentID {
			d.conductorCursor = i + 1 // +1 because 0 = "None"
			break
		}
	}
	d.pathInput.Blur()
	d.claudeOptions.Blur()
	d.claudeOptions.ResetStartQuery() // #741: per-session query must not leak across openings
	d.geminiOptions.Blur()
	d.codexOptions.Blur()
	if d.branchPicker != nil {
		d.branchPicker.Hide()
	}
	// Keep commandCursor at previously set default (don't reset to 0)
	d.updateToolOptions()
	// Reset worktree fields from global config defaults.
	d.worktreeEnabled = false
	d.branchInput.SetValue("")
	d.branchAutoSet = false
	d.branchPrefix = "feature/" // default; overridden below if config provides one.
	// Reset multi-repo fields (ephemeral, never pre-filled).
	d.multiRepoEnabled = false
	d.multiRepoPaths = nil
	d.multiRepoPathCursor = 0
	d.multiRepoEditing = false
	// Reset sandbox from global config default.
	d.sandboxEnabled = false
	d.inheritedExpanded = false
	d.inheritedSettings = nil
	// Set path input to group's default path if provided, otherwise use current working directory.
	if defaultPath != "" {
		d.pathInput.SetValue(defaultPath)
	} else {
		cwd, err := os.Getwd()
		if err == nil {
			d.pathInput.SetValue(cwd)
		}
	}
	d.pathSoftSelected = true // activate soft-select for pre-filled path.
	// Initialize tool options from global config.
	d.geminiOptions.SetDefaults(false)
	d.codexOptions.SetDefaults(false)
	if userConfig, err := session.LoadUserConfig(); err == nil && userConfig != nil {
		d.geminiOptions.SetDefaults(userConfig.Gemini.YoloMode)
		d.codexOptions.SetDefaults(userConfig.Codex.YoloMode)
		d.claudeOptions.SetDefaults(userConfig)
		d.sandboxEnabled = userConfig.Docker.DefaultEnabled
		d.worktreeEnabled = userConfig.Worktree.DefaultEnabled
		if d.worktreeEnabled {
			d.branchAutoSet = true
		}
		d.inheritedSettings = buildInheritedSettings(userConfig.Docker)
		d.branchPrefix = userConfig.Worktree.Prefix()
	}
	d.branchInput.Placeholder = d.branchPrefix + "branch-name"
	d.rebuildFocusTargets()
}

// SetDefaultTool sets the pre-selected command based on tool name
// Call this before Show/ShowInGroup to apply user's preferred default
func (d *NewDialog) SetDefaultTool(tool string) {
	if tool == "" {
		d.commandCursor = 0 // Default to shell
		return
	}

	// Find the tool in preset commands
	for i, cmd := range d.presetCommands {
		if cmd == tool {
			d.commandCursor = i
			d.updateToolOptions()
			return
		}
	}

	// Tool not found in presets, default to shell
	d.commandCursor = 0
	d.updateToolOptions()
}

// GetSelectedGroup returns the parent group path
func (d *NewDialog) GetSelectedGroup() string {
	return d.parentGroupPath
}

func (d *NewDialog) effectiveDialogWidth() int {
	w := newDialogPreferredOuterWidth
	if d.width > 0 && d.width < w+newDialogTerminalGutter {
		w = d.width - newDialogTerminalGutter
		if w < newDialogMinOuterWidth {
			w = newDialogMinOuterWidth
		}
	}
	return w
}

func (d *NewDialog) syncInputWidths() {
	iw := d.effectiveDialogWidth() - newDialogInputWidthPad
	if iw < newDialogInputMinWidth {
		iw = newDialogInputMinWidth
	}
	if iw > newDialogInputMaxWidth {
		iw = newDialogInputMaxWidth
	}
	d.nameInput.Width = iw
	d.pathInput.Width = iw
	d.commandInput.Width = iw
	d.branchInput.Width = iw
}

// SetSize sets the dialog dimensions
func (d *NewDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
	d.syncInputWidths()
	if d.branchPicker != nil {
		d.branchPicker.SetSize(width, height)
	}
}

// SetPathSuggestions sets the available path suggestions for autocomplete
func (d *NewDialog) SetPathSuggestions(paths []string) {
	d.allPathSuggestions = paths
	d.pathSuggestions = paths
	d.pathSuggestionCursor = 0
}

// IsRecentPickerOpen returns whether the recent sessions picker is visible.
func (d *NewDialog) IsRecentPickerOpen() bool {
	return d.showRecentPicker && len(d.recentSessions) > 0
}

// IsBranchPickerOpen returns whether the inline branch result list is visible.
func (d *NewDialog) IsBranchPickerOpen() bool {
	return d.branchPicker != nil && d.branchPicker.IsVisible()
}

// IsSuggestionsActive returns whether arrow-key focus is inside the path
// suggestions dropdown. Used by the parent so it can forward keys to the
// dialog before its own Enter/Esc handlers consume them.
func (d *NewDialog) IsSuggestionsActive() bool {
	return d.suggestionsActive
}

// IsTypeCustomHighlighted returns true when the synthetic "Type custom"
// entry is the highlighted item in the active dropdown.
func (d *NewDialog) IsTypeCustomHighlighted() bool {
	return d.suggestionsActive && d.pathSuggestionCursor == 0
}

// ApplyHighlightedSuggestion applies the currently highlighted real
// suggestion to the path input and exits the active dropdown mode (dropdown
// remains visible). Has no effect on the input when "Type custom" is
// highlighted — only the active mode is exited.
func (d *NewDialog) ApplyHighlightedSuggestion() {
	if d.suggestionsActive && d.pathSuggestionCursor > 0 {
		suggestionIdx := d.pathSuggestionCursor - 1
		if suggestionIdx < len(d.pathSuggestions) {
			d.pathInput.SetValue(d.pathSuggestions[suggestionIdx])
			d.pathInput.SetCursor(len(d.pathInput.Value()))
		}
		d.suggestionNavigated = true
	}
	d.suggestionsActive = false
	d.pathSoftSelected = false
	d.pathInput.Focus()
}

// DismissSuggestions hides the dropdown until the user types in the
// input or focus changes. Used after Enter so the dropdown disappears
// even when the form fails to submit due to validation errors.
func (d *NewDialog) DismissSuggestions() {
	d.suggestionsHidden = true
	d.suggestionsActive = false
}

// SetRecentSessions sets the list of recently deleted session configs.
func (d *NewDialog) SetRecentSessions(sessions []*statedb.RecentSessionRow) {
	d.recentSessions = sessions
	d.recentSessionCursor = 0
	d.showRecentPicker = false
}

// saveSnapshot captures current form state so the picker can restore on cancel.
func (d *NewDialog) saveSnapshot() *dialogSnapshot {
	claudeOpts := d.claudeOptions.GetOptions()
	if claudeOpts != nil {
		copy := *claudeOpts
		claudeOpts = &copy
	}

	return &dialogSnapshot{
		name:             d.nameInput.Value(),
		path:             d.pathInput.Value(),
		commandCursor:    d.commandCursor,
		commandInput:     d.commandInput.Value(),
		sandboxEnabled:   d.sandboxEnabled,
		worktreeEnabled:  d.worktreeEnabled,
		branch:           d.branchInput.Value(),
		branchAutoSet:    d.branchAutoSet,
		claudeOptions:    claudeOpts,
		geminiYolo:       d.geminiOptions.GetYoloMode(),
		codexYolo:        d.codexOptions.GetYoloMode(),
		multiRepoEnabled: d.multiRepoEnabled,
		multiRepoPaths:   append([]string{}, d.multiRepoPaths...),
		conductorCursor:  d.conductorCursor,
	}
}

// restoreSnapshot restores form state from a snapshot.
func (d *NewDialog) restoreSnapshot(s *dialogSnapshot) {
	d.nameInput.SetValue(s.name)
	d.pathInput.SetValue(s.path)
	d.commandCursor = s.commandCursor
	d.commandInput.SetValue(s.commandInput)
	d.sandboxEnabled = s.sandboxEnabled
	d.worktreeEnabled = s.worktreeEnabled
	d.branchInput.SetValue(s.branch)
	d.branchAutoSet = s.branchAutoSet
	if s.claudeOptions != nil {
		d.claudeOptions.SetFromOptions(s.claudeOptions)
	}
	d.geminiOptions.SetDefaults(s.geminiYolo)
	d.codexOptions.SetDefaults(s.codexYolo)
	d.multiRepoEnabled = s.multiRepoEnabled
	d.multiRepoPaths = append([]string{}, s.multiRepoPaths...)
	d.multiRepoPathCursor = 0
	d.multiRepoEditing = false
	d.conductorCursor = s.conductorCursor
	d.updateToolOptions()
	d.rebuildFocusTargets()
}

// previewRecentSession pre-fills the dialog from a recent session row (keeps picker open).
func (d *NewDialog) previewRecentSession(rs *statedb.RecentSessionRow) {
	d.nameInput.SetValue(rs.Title)
	d.pathInput.SetValue(rs.ProjectPath)

	// Default to shell/custom command mode.
	d.commandCursor = 0
	d.commandInput.SetValue("")

	// Set command/tool.
	if rs.Tool == "" || rs.Tool == "shell" {
		d.commandInput.SetValue(strings.TrimSpace(rs.Command))
	} else {
		matched := false
		for i, cmd := range d.presetCommands {
			if cmd == rs.Tool {
				d.commandCursor = i
				matched = true
				break
			}
		}
		// If the saved tool no longer exists, fall back to shell/custom command.
		if !matched {
			d.commandCursor = 0
			d.commandInput.SetValue(strings.TrimSpace(rs.Command))
		}
	}
	d.updateToolOptions()

	// Apply tool-specific options
	if len(rs.ToolOptions) > 0 && string(rs.ToolOptions) != "{}" {
		switch {
		case session.IsClaudeCompatible(rs.Tool):
			var wrapper session.ToolOptionsWrapper
			if err := json.Unmarshal(rs.ToolOptions, &wrapper); err == nil && wrapper.Tool == "claude" {
				var opts session.ClaudeOptions
				if err := json.Unmarshal(wrapper.Options, &opts); err == nil {
					d.claudeOptions.SetFromOptions(&opts)
				}
			}
		case rs.Tool == "gemini":
			if rs.GeminiYoloMode != nil {
				d.geminiOptions.SetDefaults(*rs.GeminiYoloMode)
			}
		case rs.Tool == "codex":
			var wrapper session.ToolOptionsWrapper
			if err := json.Unmarshal(rs.ToolOptions, &wrapper); err == nil && wrapper.Tool == "codex" {
				var opts session.CodexOptions
				if err := json.Unmarshal(wrapper.Options, &opts); err == nil && opts.YoloMode != nil {
					d.codexOptions.SetDefaults(*opts.YoloMode)
				}
			}
		}
	}

	d.sandboxEnabled = rs.SandboxEnabled

	// Reset worktree (ephemeral, never pre-filled)
	d.worktreeEnabled = false
	d.branchInput.SetValue("")
	d.branchAutoSet = false

	// Reset multi-repo (ephemeral, never pre-filled)
	d.multiRepoEnabled = false
	d.multiRepoPaths = nil
	d.multiRepoPathCursor = 0
	d.multiRepoEditing = false

	d.rebuildFocusTargets()
}

// filterPathSuggestions filters allPathSuggestions by the current path input value
func (d *NewDialog) filterPathSuggestions() {
	query := strings.ToLower(strings.TrimSpace(d.pathInput.Value()))
	if query == "" {
		d.pathSuggestions = d.allPathSuggestions
	} else {
		filtered := make([]string, 0)
		for _, p := range d.allPathSuggestions {
			if strings.Contains(strings.ToLower(p), query) {
				filtered = append(filtered, p)
			}
		}
		d.pathSuggestions = filtered
	}
	// Cursor space: 0 = "Type custom", 1..N = pathSuggestions[0..N-1]
	if d.pathSuggestionCursor > len(d.pathSuggestions) {
		d.pathSuggestionCursor = 0
	}
}

// Show makes the dialog visible (uses default group)
func (d *NewDialog) Show() {
	d.ShowInGroup("default", "default", "", nil, "")
}

// Hide hides the dialog
func (d *NewDialog) Hide() {
	d.visible = false
	if d.branchPicker != nil {
		d.branchPicker.Hide()
	}
}

// IsVisible returns whether the dialog is visible
func (d *NewDialog) IsVisible() bool {
	return d.visible
}

// GetValues returns the current dialog values with expanded paths
func (d *NewDialog) GetValues() (name, path, command string) {
	name = strings.TrimSpace(d.nameInput.Value())
	// Fix: sanitize input to remove surrounding quotes that cause path issues
	path = strings.Trim(strings.TrimSpace(d.pathInput.Value()), "'\"")

	// Fix malformed paths that have ~ in the middle (e.g., "/some/path~/actual/path")
	// This can happen when textinput suggestion appends instead of replaces
	if idx := strings.Index(path, "~/"); idx > 0 {
		path = path[idx:]
	}

	// Expand environment variables and ~ prefix
	path = session.ExpandPath(path)

	// Get command - either from preset or custom input
	if d.commandCursor < len(d.presetCommands) {
		command = d.presetCommands[d.commandCursor]
	}
	if command == "" && d.commandInput.Value() != "" {
		command = strings.TrimSpace(d.commandInput.Value())
	}

	return name, path, command
}

// ToggleWorktree toggles the worktree checkbox.
// When enabling, auto-populates the branch name from the session name.
func (d *NewDialog) ToggleWorktree() {
	d.worktreeEnabled = !d.worktreeEnabled
	if d.worktreeEnabled {
		d.autoBranchFromName()
	}
	d.rebuildFocusTargets()
}

// autoBranchFromName sets the branch input to "<prefix><session-name>" if the
// name field is non-empty and the branch hasn't been manually edited.
func (d *NewDialog) autoBranchFromName() {
	name := strings.TrimSpace(d.nameInput.Value())
	if name == "" {
		return
	}
	branch := d.branchPrefix + name
	d.branchInput.SetValue(branch)
	d.branchAutoSet = true
}

// IsWorktreeEnabled returns whether worktree mode is enabled
func (d *NewDialog) IsWorktreeEnabled() bool {
	return d.worktreeEnabled
}

// GetValuesWithWorktree returns all values including worktree settings
func (d *NewDialog) GetValuesWithWorktree() (name, path, command, branch string, worktreeEnabled bool) {
	name, path, command = d.GetValues()
	branch = strings.TrimSpace(d.branchInput.Value())
	worktreeEnabled = d.worktreeEnabled
	return
}

// IsGeminiYoloMode returns whether YOLO mode is enabled for Gemini
func (d *NewDialog) IsGeminiYoloMode() bool {
	return d.geminiOptions.GetYoloMode()
}

// GetCodexYoloMode returns the Codex YOLO mode state
func (d *NewDialog) GetCodexYoloMode() bool {
	return d.codexOptions.GetYoloMode()
}

// IsSandboxEnabled returns whether Docker sandbox mode is enabled.
func (d *NewDialog) IsSandboxEnabled() bool {
	return d.sandboxEnabled
}

// ToggleSandbox toggles Docker sandbox mode.
func (d *NewDialog) ToggleSandbox() {
	d.sandboxEnabled = !d.sandboxEnabled
	d.rebuildFocusTargets()
}

// ToggleMultiRepo toggles multi-repo mode.
// When enabling, initializes multiRepoPaths with the current pathInput value.
// When disabling, collapses back to the first path.
func (d *NewDialog) ToggleMultiRepo() {
	d.multiRepoEnabled = !d.multiRepoEnabled
	if d.multiRepoEnabled {
		currentPath := strings.TrimSpace(d.pathInput.Value())
		if currentPath != "" {
			d.multiRepoPaths = []string{currentPath}
		} else {
			d.multiRepoPaths = []string{""}
		}
		d.multiRepoPathCursor = 0
		d.multiRepoEditing = false
	} else {
		// Collapse back to the first non-empty path
		if len(d.multiRepoPaths) > 0 {
			d.pathInput.SetValue(d.multiRepoPaths[0])
		}
		d.multiRepoPaths = nil
		d.multiRepoPathCursor = 0
		d.multiRepoEditing = false
	}
	d.rebuildFocusTargets()
}

// GetMultiRepoPaths returns the multi-repo paths and enabled state.
func (d *NewDialog) GetMultiRepoPaths() ([]string, bool) {
	if !d.multiRepoEnabled {
		return nil, false
	}
	// Return non-empty, expanded paths
	var paths []string
	for _, p := range d.multiRepoPaths {
		p = strings.TrimSpace(p)
		if p != "" {
			p = strings.Trim(p, "'\"")
			if idx := strings.Index(p, "~/"); idx > 0 {
				p = p[idx:]
			}
			p = session.ExpandPath(p)
			paths = append(paths, p)
		}
	}
	return paths, true
}

// IsMultiRepoEditing returns true when the user is editing a path in the multi-repo list.
// Used by the parent to prevent enter from submitting the form.
func (d *NewDialog) IsMultiRepoEditing() bool {
	return d.multiRepoEnabled && d.currentTarget() == focusMultiRepo
}

// GetSelectedCommand returns the currently selected command/tool
func (d *NewDialog) GetSelectedCommand() string {
	if d.commandCursor >= 0 && d.commandCursor < len(d.presetCommands) {
		return d.presetCommands[d.commandCursor]
	}
	return ""
}

// GetClaudeOptions returns the Claude-specific options (only relevant if command is "claude")
func (d *NewDialog) GetClaudeOptions() *session.ClaudeOptions {
	if !d.isClaudeSelected() {
		return nil
	}
	return d.claudeOptions.GetOptions()
}

// GetClaudeExtraArgs returns the user-supplied claude CLI tokens from the
// options panel. Returns nil for non-claude tools. Tokens are whitespace-split;
// for values with embedded spaces, use `ad-fork add --extra-arg`.
func (d *NewDialog) GetClaudeExtraArgs() []string {
	if !d.isClaudeSelected() {
		return nil
	}
	return d.claudeOptions.GetExtraArgs()
}

// GetClaudeStartQuery returns the user-supplied claude startup query from
// the options panel (v1.7.67, #725). Returns "" for non-claude tools. The
// value is a single string — NEVER split on spaces — and is assigned by
// the caller to Instance.StartupQuery for single-shot emission on the
// new-session command line.
func (d *NewDialog) GetClaudeStartQuery() string {
	if !d.isClaudeSelected() {
		return ""
	}
	return d.claudeOptions.GetStartQuery()
}

// isClaudeSelected returns true if the selected command is Claude or a claude-compatible custom tool
func (d *NewDialog) isClaudeSelected() bool {
	if d.commandCursor < 0 || d.commandCursor >= len(d.presetCommands) {
		return false
	}
	return session.IsClaudeCompatible(d.presetCommands[d.commandCursor])
}

// Validate checks if the dialog values are valid and returns an error message if not
func (d *NewDialog) Validate() string {
	name := strings.TrimSpace(d.nameInput.Value())
	// Fix: sanitize input to remove surrounding quotes that cause path issues
	path := strings.Trim(strings.TrimSpace(d.pathInput.Value()), "'\"")

	// Check for empty name
	if name == "" {
		return "Session name cannot be empty"
	}

	// Check name length
	if len(name) > MaxNameLength {
		return fmt.Sprintf("Session name too long (max %d characters)", MaxNameLength)
	}

	// Check for empty path
	if path == "" && !d.multiRepoEnabled {
		return "Project path cannot be empty"
	}

	// Validate multi-repo paths
	if d.multiRepoEnabled {
		nonEmpty := 0
		seen := make(map[string]bool)
		for _, p := range d.multiRepoPaths {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			expanded := session.ExpandPath(strings.Trim(p, "'\""))
			if seen[expanded] {
				return "Duplicate paths in multi-repo mode"
			}
			seen[expanded] = true
			nonEmpty++
		}
		if nonEmpty < 2 {
			return "Multi-repo mode requires at least 2 paths"
		}
	}

	// Validate worktree branch if enabled
	if d.worktreeEnabled {
		branch := strings.TrimSpace(d.branchInput.Value())
		if branch == "" {
			return "Branch name required for worktree"
		}
		if err := git.ValidateBranchName(branch); err != nil {
			return err.Error()
		}
	}

	return "" // Valid
}

// SetError sets an inline validation error displayed inside the dialog
func (d *NewDialog) SetError(msg string) {
	d.validationErr = msg
}

// ClearError clears the inline validation error
func (d *NewDialog) ClearError() {
	d.validationErr = ""
}

// currentTarget returns the focusTarget at the current focusIndex.
func (d *NewDialog) currentTarget() focusTarget {
	if d.focusIndex < 0 || d.focusIndex >= len(d.focusTargets) {
		return focusName
	}
	return d.focusTargets[d.focusIndex]
}

// indexOf returns the index of target in focusTargets, or -1 if absent.
func (d *NewDialog) indexOf(target focusTarget) int {
	for i, t := range d.focusTargets {
		if t == target {
			return i
		}
	}
	return -1
}

// rebuildFocusTargets builds the ordered list of active focusable elements
// based on current dialog state (sandbox, worktree, tool options visibility).
func (d *NewDialog) rebuildFocusTargets() {
	var targets []focusTarget
	if d.multiRepoEnabled {
		// Multi-repo replaces the single path field with a path list under focusMultiRepo
		targets = []focusTarget{focusName, focusMultiRepo, focusCommand, focusWorktree, focusSandbox}
	} else {
		targets = []focusTarget{focusName, focusMultiRepo, focusPath, focusCommand, focusWorktree, focusSandbox}
	}
	if len(d.conductorSessions) > 0 {
		targets = append(targets, focusConductor)
	}
	if d.sandboxEnabled && len(d.inheritedSettings) > 0 {
		targets = append(targets, focusInherited)
	}
	if d.worktreeEnabled {
		targets = append(targets, focusBranch)
	}
	if d.toolOptions != nil {
		targets = append(targets, focusOptions)
	}
	d.focusTargets = targets
	// Clamp focusIndex to valid range.
	if d.focusIndex >= len(d.focusTargets) {
		d.focusIndex = len(d.focusTargets) - 1
	}
	if d.focusIndex < 0 {
		d.focusIndex = 0
	}
}

// updateToolOptions sets d.toolOptions to the panel matching the current tool selection.
func (d *NewDialog) updateToolOptions() {
	cmd := d.GetSelectedCommand()
	switch {
	case session.IsClaudeCompatible(cmd):
		d.toolOptions = d.claudeOptions
	case cmd == "gemini":
		d.toolOptions = d.geminiOptions
	case cmd == "codex":
		d.toolOptions = d.codexOptions
	default:
		d.toolOptions = nil
	}
	d.rebuildFocusTargets()
}

func (d *NewDialog) updateFocus() {
	d.nameInput.Blur()
	d.pathInput.Blur()
	d.commandInput.Blur()
	d.branchInput.Blur()
	d.claudeOptions.Blur()
	d.geminiOptions.Blur()
	d.codexOptions.Blur()

	// Reset dropdown and soft-select state when focus changes.
	d.pathSoftSelected = false
	d.suggestionsActive = false
	d.suggestionsHidden = false
	switch d.currentTarget() {
	case focusName:
		d.nameInput.Focus()
	case focusPath:
		if d.pathInput.Value() != "" {
			d.pathSoftSelected = true
			// Keep pathInput blurred — we render custom reverse-video style.
			// pathInput.Focus() is called when soft-select exits.
		} else {
			d.pathInput.Focus()
		}
	case focusCommand:
		if d.commandCursor == 0 { // shell.
			d.commandInput.Focus()
		}
	case focusWorktree, focusSandbox, focusConductor, focusInherited:
		// Checkbox/toggle rows and conductor dropdown — no text input to focus.
	case focusBranch:
		d.branchInput.Focus()
	case focusOptions:
		if d.toolOptions != nil {
			d.toolOptions.Focus()
		}
	}
}

// Update handles key messages.
// isTextInputFocused returns true when a text input field is actively receiving
// keystrokes. Single-letter shortcuts must be suppressed in this state.
func (d *NewDialog) isTextInputFocused() bool {
	switch d.currentTarget() {
	case focusName, focusPath, focusBranch:
		return true
	case focusCommand:
		return d.commandCursor == 0 // custom command input
	case focusMultiRepo:
		return d.multiRepoEditing
	default:
		return false
	}
}

func (d *NewDialog) Update(msg tea.Msg) (*NewDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}

	var cmd tea.Cmd
	maxIdx := len(d.focusTargets) - 1
	cur := d.currentTarget()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if d.branchPicker != nil && d.branchPicker.IsVisible() {
			if selected, handled := d.branchPicker.Update(msg); handled {
				if d.branchPicker == nil || !d.branchPicker.IsVisible() {
					d.branchInput.Focus()
				}
				if selected != "" {
					d.branchInput.SetValue(selected)
					d.branchInput.SetCursor(len(selected))
					d.branchAutoSet = false
					d.ClearError()
				}
				return d, nil
			}
		}

		// Recent sessions picker handling
		if d.showRecentPicker && len(d.recentSessions) > 0 {
			switch msg.String() {
			case "ctrl+n", "down":
				d.recentSessionCursor = (d.recentSessionCursor + 1) % len(d.recentSessions)
				d.previewRecentSession(d.recentSessions[d.recentSessionCursor])
				return d, nil
			case "ctrl+p", "up":
				d.recentSessionCursor--
				if d.recentSessionCursor < 0 {
					d.recentSessionCursor = len(d.recentSessions) - 1
				}
				d.previewRecentSession(d.recentSessions[d.recentSessionCursor])
				return d, nil
			case "enter":
				// Fields already applied via preview — just close picker.
				d.showRecentPicker = false
				d.recentSnapshot = nil
				d.pathSoftSelected = true
				return d, nil
			case "esc", "ctrl+r":
				// Cancel — restore original form state.
				if d.recentSnapshot != nil {
					d.restoreSnapshot(d.recentSnapshot)
					d.recentSnapshot = nil
				}
				d.showRecentPicker = false
				return d, nil
			}
			return d, nil // Consume all other keys while picker is open
		}

		// Toggle recent sessions picker
		if msg.String() == "ctrl+r" && len(d.recentSessions) > 0 {
			d.recentSnapshot = d.saveSnapshot()
			d.showRecentPicker = true
			d.recentSessionCursor = 0
			d.previewRecentSession(d.recentSessions[0])
			return d, nil
		}

		// Suggestions dropdown active: arrow keys navigate, space/enter select,
		// left/esc exit. The dropdown shows a synthetic "Type custom path..."
		// entry at index 0, followed by real suggestions at indices 1..N.
		if d.suggestionsActive && d.currentTarget() == focusPath {
			total := len(d.pathSuggestions) + 1 // +1 for the "Type custom" entry
			switch msg.String() {
			case "down", "j":
				d.pathSuggestionCursor = (d.pathSuggestionCursor + 1) % total
				return d, nil
			case "up", "k":
				d.pathSuggestionCursor--
				if d.pathSuggestionCursor < 0 {
					d.pathSuggestionCursor = total - 1
				}
				return d, nil
			case " ":
				// Space: apply highlighted entry + close dropdown (stay in form).
				d.ApplyHighlightedSuggestion()
				return d, nil
			// Note: "enter" is intentionally NOT handled here — the parent
			// (home.go) intercepts it so it can also trigger form submission
			// after applying the selected path.
			case "left", "h", "esc":
				d.suggestionsActive = false
				d.pathInput.Focus()
				return d, nil
			}
			return d, nil // consume all other keys while dropdown is active
		}

		// Soft-select interception for path field
		if d.currentTarget() == focusPath && d.pathSoftSelected {
			// Space or Right enters the suggestions dropdown.
			if msg.String() == " " || msg.Type == tea.KeyRight {
				d.suggestionsActive = true
				d.pathSuggestionCursor = 0 // start on "Type custom" entry
				d.pathSoftSelected = false
				d.pathInput.Blur()
				return d, nil
			}
			switch msg.Type {
			case tea.KeyRunes:
				// Printable char: clear field, focus textinput, let rune fall through
				d.pathSoftSelected = false
				d.pathInput.SetValue("")
				d.pathInput.SetCursor(0)
				d.pathInput.Focus()
				d.pathCycler.Reset()
				// DON'T return — let the rune reach textinput.Update() below
			case tea.KeyBackspace, tea.KeyDelete:
				d.pathSoftSelected = false
				d.pathInput.SetValue("")
				d.pathInput.SetCursor(0)
				d.pathInput.Focus()
				d.pathCycler.Reset()
				d.filterPathSuggestions()
				return d, nil // consume the key
			case tea.KeyLeft:
				d.pathSoftSelected = false
				d.pathInput.Focus() // exit soft-select, allow editing
			}
			// Tab, Enter, Esc, Ctrl+N, Ctrl+P, Up, Down fall through to existing handlers
		}

		switch msg.String() {
		case "tab":
			// On path field (or multi-repo path editing): trigger autocomplete or cycle through matches.
			isPathEditing := cur == focusPath || d.multiRepoEditing
			if isPathEditing {
				path := d.pathInput.Value()
				info, err := os.Stat(path)
				isDir := err == nil && info.IsDir()
				isPartial := !isDir || strings.HasSuffix(path, string(os.PathSeparator))

				if d.pathCycler.IsActive() || isPartial {
					if d.pathCycler.IsActive() {
						d.pathInput.SetValue(d.pathCycler.Next())
						d.pathInput.SetCursor(len(d.pathInput.Value()))
						return d, nil
					}
					matches, err := session.GetDirectoryCompletions(path)
					if err == nil && len(matches) > 0 {
						d.pathCycler.SetMatches(matches)
						d.pathInput.SetValue(d.pathCycler.Next())
						d.pathInput.SetCursor(len(d.pathInput.Value()))
						return d, nil
					}
				}
			}

			// On path field: apply selected suggestion ONLY if user explicitly navigated.
			// Cursor 0 = "Type custom" (no-op); cursor 1..N maps to pathSuggestions[0..N-1].
			if isPathEditing && d.suggestionNavigated && d.pathSuggestionCursor > 0 {
				suggestionIdx := d.pathSuggestionCursor - 1
				if suggestionIdx < len(d.pathSuggestions) {
					d.pathInput.SetValue(d.pathSuggestions[suggestionIdx])
					d.pathInput.SetCursor(len(d.pathInput.Value()))
				}
			}
			// When editing a multi-repo path, Tab is only for autocomplete — don't move focus.
			if d.multiRepoEditing {
				return d, nil
			}
			// Move to next field.
			if d.focusIndex < maxIdx {
				d.focusIndex++
				d.updateFocus()
			} else if cur == focusOptions && d.toolOptions != nil {
				return d, d.toolOptions.Update(msg)
			} else {
				d.focusIndex = 0
				d.updateFocus()
			}
			// Reset navigation flag when leaving path field.
			if d.currentTarget() != focusPath {
				d.suggestionNavigated = false
			}
			return d, cmd

		case "ctrl+n":
			// Next suggestion (cursor space includes synthetic "Type custom" at 0).
			if (cur == focusPath || d.multiRepoEditing) && len(d.pathSuggestions) > 0 {
				d.pathSoftSelected = false
				d.pathInput.Focus() // exit soft-select, focus for future input.
				d.pathSuggestionCursor = (d.pathSuggestionCursor + 1) % (len(d.pathSuggestions) + 1)
				d.suggestionNavigated = true
				return d, nil
			}

		case "ctrl+p":
			// Previous suggestion (cursor space includes synthetic "Type custom" at 0).
			if (cur == focusPath || d.multiRepoEditing) && len(d.pathSuggestions) > 0 {
				d.pathSoftSelected = false
				d.pathInput.Focus() // exit soft-select, focus for future input.
				d.pathSuggestionCursor--
				if d.pathSuggestionCursor < 0 {
					d.pathSuggestionCursor = len(d.pathSuggestions)
				}
				d.suggestionNavigated = true
				return d, nil
			}

		case "ctrl+f":
			if cur == focusBranch {
				if d.branchPicker == nil {
					d.branchPicker = NewBranchPickerDialog()
				}
				d.branchPicker.SetSize(d.width, d.height)
				if err := d.branchPicker.Show(strings.Trim(strings.TrimSpace(d.pathInput.Value()), "'\""), d.branchInput.Value()); err != nil {
					d.SetError(err.Error())
				} else {
					d.ClearError()
					d.branchInput.Focus()
				}
				return d, nil
			}

		case "down":
			if cur == focusConductor {
				total := len(d.conductorSessions) + 1 // +1 for "None"
				if d.conductorCursor < total-1 {
					d.conductorCursor++
					return d, nil
				}
			}
			if cur == focusMultiRepo && d.multiRepoEnabled && !d.multiRepoEditing {
				if d.multiRepoPathCursor < len(d.multiRepoPaths)-1 {
					d.multiRepoPathCursor++
					return d, nil
				}
			}
			if d.focusIndex < maxIdx {
				d.focusIndex++
				d.updateFocus()
			} else if cur == focusOptions && d.toolOptions != nil {
				return d, d.toolOptions.Update(msg)
			}
			return d, nil

		case "shift+tab", "up":
			if cur == focusConductor {
				if d.conductorCursor > 0 {
					d.conductorCursor--
					return d, nil
				}
			}
			if cur == focusMultiRepo && d.multiRepoEnabled && !d.multiRepoEditing {
				if d.multiRepoPathCursor > 0 {
					d.multiRepoPathCursor--
					return d, nil
				}
			}
			if cur == focusOptions && d.toolOptions != nil && !d.toolOptions.AtTop() {
				return d, d.toolOptions.Update(msg)
			}
			d.focusIndex--
			if d.focusIndex < 0 {
				d.focusIndex = maxIdx
			}
			d.updateFocus()
			return d, nil

		case "esc":
			if d.multiRepoEditing {
				// Cancel editing, revert to the stored value
				d.multiRepoEditing = false
				d.pathInput.Blur()
				return d, nil
			}
			d.Hide()
			return d, nil

		case "enter":
			if cur == focusMultiRepo && d.multiRepoEnabled {
				if d.multiRepoEditing {
					// Save the edited path back
					d.multiRepoPaths[d.multiRepoPathCursor] = strings.TrimSpace(d.pathInput.Value())
					d.multiRepoEditing = false
					d.pathInput.Blur()
					d.pathCycler.Reset()
				} else {
					// Start editing: load path into pathInput
					d.multiRepoEditing = true
					d.pathInput.SetValue(d.multiRepoPaths[d.multiRepoPathCursor])
					d.pathInput.SetCursor(len(d.pathInput.Value()))
					d.pathInput.Focus()
					d.pathCycler.Reset()
					d.suggestionNavigated = false
					d.pathSuggestionCursor = 0
					d.filterPathSuggestions()
				}
				return d, nil
			}
			return d, nil

		case "left":
			if cur == focusCommand {
				d.commandCursor--
				if d.commandCursor < 0 {
					d.commandCursor = len(d.presetCommands) - 1
				}
				d.updateToolOptions()
				d.updateFocus()
				return d, nil
			}
			if cur == focusOptions && d.toolOptions != nil {
				return d, d.toolOptions.Update(msg)
			}

		case "right":
			if cur == focusCommand {
				d.commandCursor = (d.commandCursor + 1) % len(d.presetCommands)
				d.updateToolOptions()
				d.updateFocus()
				return d, nil
			}
			if cur == focusOptions && d.toolOptions != nil {
				return d, d.toolOptions.Update(msg)
			}

		case "w":
			if cur == focusCommand && !d.isTextInputFocused() {
				d.ToggleWorktree()
				d.rebuildFocusTargets()
				if d.worktreeEnabled {
					if idx := d.indexOf(focusBranch); idx >= 0 {
						d.focusIndex = idx
					}
					d.updateFocus()
				}
				return d, nil
			}

		case "s":
			if cur == focusCommand && !d.isTextInputFocused() {
				d.ToggleSandbox()
				if !d.sandboxEnabled {
					d.inheritedExpanded = false
				}
				d.rebuildFocusTargets()
				return d, nil
			}

		case "m":
			if cur == focusCommand && !d.isTextInputFocused() {
				d.ToggleMultiRepo()
				d.rebuildFocusTargets()
				return d, nil
			}

		case "a":
			if cur == focusMultiRepo && d.multiRepoEnabled && !d.multiRepoEditing {
				// Pre-fill with parent directory of the last path
				defaultPath := ""
				for i := len(d.multiRepoPaths) - 1; i >= 0; i-- {
					if p := strings.TrimSpace(d.multiRepoPaths[i]); p != "" {
						defaultPath = filepath.Dir(session.ExpandPath(p))
						if defaultPath != "" && defaultPath != "." {
							// Collapse home dir back to ~
							if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(defaultPath, home) {
								defaultPath = "~" + defaultPath[len(home):]
							}
							defaultPath += string(os.PathSeparator)
						} else {
							defaultPath = ""
						}
						break
					}
				}
				d.multiRepoPaths = append(d.multiRepoPaths, defaultPath)
				d.multiRepoPathCursor = len(d.multiRepoPaths) - 1
				// Auto-enter edit mode for the new path
				d.multiRepoEditing = true
				d.pathInput.SetValue(defaultPath)
				d.pathInput.SetCursor(len(defaultPath))
				d.pathInput.Focus()
				d.pathCycler.Reset()
				d.suggestionNavigated = false
				d.pathSuggestionCursor = 0
				d.filterPathSuggestions()
				return d, nil
			}

		case "d":
			if cur == focusMultiRepo && d.multiRepoEnabled && !d.multiRepoEditing && len(d.multiRepoPaths) > 1 {
				d.multiRepoPaths = append(d.multiRepoPaths[:d.multiRepoPathCursor], d.multiRepoPaths[d.multiRepoPathCursor+1:]...)
				if d.multiRepoPathCursor >= len(d.multiRepoPaths) {
					d.multiRepoPathCursor = len(d.multiRepoPaths) - 1
				}
				return d, nil
			}

		case "y":
			if !d.isTextInputFocused() {
				selectedCmd := d.GetSelectedCommand()
				if cur == focusCommand && (selectedCmd == "gemini" || selectedCmd == "codex") && d.toolOptions != nil {
					d.toolOptions.Update(msg)
					return d, nil
				}
				if cur == focusOptions && d.toolOptions != nil {
					d.toolOptions.Update(msg)
					return d, nil
				}
			}

		case " ":
			if cur == focusWorktree {
				d.ToggleWorktree()
				d.rebuildFocusTargets()
				if d.worktreeEnabled {
					if idx := d.indexOf(focusBranch); idx >= 0 {
						d.focusIndex = idx
					}
					d.updateFocus()
				}
				return d, nil
			}
			if cur == focusSandbox {
				d.ToggleSandbox()
				if !d.sandboxEnabled {
					d.inheritedExpanded = false
				}
				d.rebuildFocusTargets()
				return d, nil
			}
			if cur == focusMultiRepo {
				d.ToggleMultiRepo()
				d.rebuildFocusTargets()
				return d, nil
			}
			if cur == focusInherited {
				d.inheritedExpanded = !d.inheritedExpanded
				return d, nil
			}
			if cur == focusOptions && d.toolOptions != nil {
				return d, d.toolOptions.Update(msg)
			}
		}
	}

	// Update focused input.
	switch cur {
	case focusName:
		oldName := d.nameInput.Value()
		d.nameInput, cmd = d.nameInput.Update(msg)
		if d.worktreeEnabled && d.branchAutoSet && d.nameInput.Value() != oldName {
			d.autoBranchFromName()
		}
	case focusPath:
		oldValue := d.pathInput.Value()
		d.pathInput, cmd = d.pathInput.Update(msg)
		if d.pathInput.Value() != oldValue {
			d.suggestionNavigated = false
			d.suggestionsActive = false
			d.suggestionsHidden = false // typing re-opens the dropdown
			d.pathSuggestionCursor = 0
			d.pathCycler.Reset()
			d.filterPathSuggestions()
		}
	case focusCommand:
		if d.commandCursor == 0 {
			d.commandInput, cmd = d.commandInput.Update(msg)
		}
	case focusMultiRepo:
		// When editing a multi-repo path, forward keystrokes to pathInput.
		if d.multiRepoEditing {
			oldValue := d.pathInput.Value()
			d.pathInput, cmd = d.pathInput.Update(msg)
			if d.pathInput.Value() != oldValue {
				d.suggestionNavigated = false
				d.pathSuggestionCursor = 0
				d.pathCycler.Reset()
				d.filterPathSuggestions()
			}
		}
	case focusWorktree, focusSandbox, focusConductor, focusInherited:
		// Checkbox/toggle rows and conductor dropdown — no text input to update.
	case focusBranch:
		oldBranch := d.branchInput.Value()
		d.branchInput, cmd = d.branchInput.Update(msg)
		if d.branchInput.Value() != oldBranch {
			d.branchAutoSet = false
			if d.branchPicker != nil && d.branchPicker.IsVisible() {
				d.branchPicker.SetQuery(d.branchInput.Value())
			}
		}
	case focusOptions:
		if d.toolOptions != nil {
			cmd = d.toolOptions.Update(msg)
		}
	}

	return d, cmd
}

// View renders the dialog.
func (d *NewDialog) View() string {
	if !d.visible {
		return ""
	}

	cur := d.currentTarget()

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorCyan).
		MarginBottom(1)

	labelStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	dialogWidth := d.effectiveDialogWidth()

	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorCyan).
		Background(ColorSurface).
		Padding(2, 4).
		Width(dialogWidth)

	// Active field indicator style
	activeLabelStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)

	// Build content
	var content strings.Builder

	// Title with parent group info
	content.WriteString(titleStyle.Render("New Session"))
	content.WriteString("\n")
	groupInfoStyle := lipgloss.NewStyle().Foreground(ColorPurple) // Purple for group context
	content.WriteString(groupInfoStyle.Render("  in group: " + d.parentGroupName))
	content.WriteString("\n")

	// Recent sessions picker
	if d.showRecentPicker && len(d.recentSessions) > 0 {
		pickerHeaderStyle := lipgloss.NewStyle().Foreground(ColorComment)
		pickerSelectedStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
		pickerItemStyle := lipgloss.NewStyle().Foreground(ColorComment)

		content.WriteString("\n")
		content.WriteString(pickerHeaderStyle.Render(
			fmt.Sprintf("─ Recent Sessions (%d) ─ ↑↓ navigate │ Enter apply │ Esc close ─", len(d.recentSessions)),
		))
		content.WriteString("\n")

		maxShow := 5
		total := len(d.recentSessions)
		startIdx := 0
		endIdx := total
		if total > maxShow {
			startIdx = d.recentSessionCursor - maxShow/2
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
			content.WriteString(pickerItemStyle.Render(fmt.Sprintf("    ↑ %d more above", startIdx)))
			content.WriteString("\n")
		}

		for i := startIdx; i < endIdx; i++ {
			rs := d.recentSessions[i]
			// Format: Name  (tool @ ~/shortened/path)
			shortPath := rs.ProjectPath
			if home, err := os.UserHomeDir(); err == nil {
				shortPath = strings.Replace(shortPath, home, "~", 1)
			}
			toolLabel := rs.Tool
			if toolLabel == "" {
				toolLabel = "shell"
			}
			entry := fmt.Sprintf("%s  (%s @ %s)", rs.Title, toolLabel, shortPath)

			if i == d.recentSessionCursor {
				content.WriteString(pickerSelectedStyle.Render("  ▶ " + entry))
			} else {
				content.WriteString(pickerItemStyle.Render("    " + entry))
			}
			content.WriteString("\n")
		}

		if endIdx < total {
			content.WriteString(pickerItemStyle.Render(fmt.Sprintf("    ↓ %d more below", total-endIdx)))
			content.WriteString("\n")
		}
	}
	content.WriteString("\n")

	// Name input
	if cur == focusName {
		content.WriteString(activeLabelStyle.Render("▶ Name:"))
	} else {
		content.WriteString(labelStyle.Render("  Name:"))
	}
	content.WriteString("\n")
	content.WriteString("  ")
	content.WriteString(d.nameInput.View())
	content.WriteString("\n\n")

	// Multi-repo checkbox — rendered above path, toggles between single path and path list.
	multiRepoLabel := "Multi-repo mode"
	if cur == focusCommand {
		multiRepoLabel = "Multi-repo mode (m)"
	}
	content.WriteString(renderCheckboxLine(multiRepoLabel, d.multiRepoEnabled, cur == focusMultiRepo))

	if d.multiRepoEnabled {
		// Multi-repo path list replaces the single path field.
		dimStyle := lipgloss.NewStyle().Foreground(ColorComment)
		pathFocused := cur == focusMultiRepo
		if pathFocused {
			content.WriteString(activeLabelStyle.Render("▶ Paths:"))
		} else {
			content.WriteString(labelStyle.Render("  Paths:"))
		}
		content.WriteString("\n")
		if pathFocused {
			for i, p := range d.multiRepoPaths {
				isSelected := i == d.multiRepoPathCursor
				prefix := "    "
				if isSelected {
					prefix = "  ▸ "
				}
				if isSelected && d.multiRepoEditing {
					content.WriteString(fmt.Sprintf("%s%d. ", prefix, i+1))
					content.WriteString(d.pathInput.View())
					content.WriteString("\n")
				} else {
					display := p
					if display == "" {
						display = "(empty)"
					}
					if isSelected {
						content.WriteString(lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Render(
							fmt.Sprintf("%s%d. %s", prefix, i+1, display)))
					} else {
						content.WriteString(dimStyle.Render(
							fmt.Sprintf("%s%d. %s", prefix, i+1, display)))
					}
					content.WriteString("\n")
				}
			}
			content.WriteString(dimStyle.Render("    [a: add, d: remove, enter: edit, ↑↓: navigate]"))
			content.WriteString("\n")
			// Record line offset for suggestions overlay (rendered after dialog is placed).
			d.suggestionsLineOffset = strings.Count(content.String(), "\n")
		} else {
			for i, p := range d.multiRepoPaths {
				display := p
				if display == "" {
					display = "(empty)"
				}
				content.WriteString(dimStyle.Render(fmt.Sprintf("    %d. %s", i+1, display)))
				content.WriteString("\n")
			}
		}
	} else {
		// Single path input (original behavior).
		if cur == focusPath {
			content.WriteString(activeLabelStyle.Render("▶ Path:"))
		} else {
			content.WriteString(labelStyle.Render("  Path:"))
		}
		content.WriteString("\n")
		content.WriteString("  ")
		if cur == focusPath && d.pathSoftSelected && d.pathInput.Value() != "" {
			// Soft-select highlight: render the textinput's own View() (which
			// includes Bubble Tea's blinking cursor) with TextStyle set to
			// reverse colors. The previous static-string render dropped the
			// cursor entirely, leaving users editing blind (#765). Saving and
			// restoring the prior TextStyle keeps this branch's mutation local
			// to the soft-select case.
			savedTextStyle := d.pathInput.TextStyle
			d.pathInput.TextStyle = lipgloss.NewStyle().
				Background(ColorAccent).
				Foreground(ColorBg)
			content.WriteString(d.pathInput.View())
			d.pathInput.TextStyle = savedTextStyle
		} else {
			content.WriteString(d.pathInput.View())
		}
		content.WriteString("\n")

		// Record line offset for suggestions overlay (rendered after dialog is placed).
		d.suggestionsLineOffset = strings.Count(content.String(), "\n")
	}
	content.WriteString("\n")

	// Command selection
	if cur == focusCommand {
		content.WriteString(activeLabelStyle.Render("▶ Command:"))
	} else {
		content.WriteString(labelStyle.Render("  Command:"))
	}
	content.WriteString("\n  ")

	// Render command options as consistent pill buttons
	var cmdButtons []string
	for i, cmd := range d.presetCommands {
		displayName := cmd
		if displayName == "" {
			displayName = "shell"
		}
		// Prepend icon for custom tools
		if icon := session.GetToolIcon(cmd); cmd != "" && icon != "" {
			// Only prepend for custom tools (not built-ins which are recognizable by name)
			if toolDef := session.GetToolDef(cmd); toolDef != nil && toolDef.Icon != "" {
				displayName = icon + " " + displayName
			}
		}

		var btnStyle lipgloss.Style
		if i == d.commandCursor {
			// Selected: bright background, bold (active pill)
			btnStyle = lipgloss.NewStyle().
				Foreground(ColorBg).
				Background(ColorAccent).
				Bold(true).
				Padding(0, 2)
		} else {
			// Unselected: subtle background pill (consistent style)
			btnStyle = lipgloss.NewStyle().
				Foreground(ColorTextDim).
				Background(ColorSurface).
				Padding(0, 2)
		}

		cmdButtons = append(cmdButtons, btnStyle.Render(displayName))
	}
	content.WriteString(lipgloss.JoinHorizontal(lipgloss.Left, cmdButtons...))
	content.WriteString("\n\n")

	// Custom command input (only if shell is selected)
	if d.commandCursor == 0 {
		// Show active indicator when command field is focused
		if cur == focusCommand {
			content.WriteString(activeLabelStyle.Render("  ▸ Custom:"))
		} else {
			content.WriteString(labelStyle.Render("    Custom:"))
		}
		content.WriteString("\n    ")
		content.WriteString(d.commandInput.View())
		content.WriteString("\n\n")
	}

	// Worktree checkbox — individually focusable.
	worktreeLabel := "Create in worktree"
	if cur == focusCommand {
		worktreeLabel = "Create in worktree (w)"
	}
	content.WriteString(renderCheckboxLine(worktreeLabel, d.worktreeEnabled, cur == focusWorktree))

	// Docker sandbox checkbox — individually focusable.
	sandboxLabel := "Run in Docker sandbox"
	if cur == focusCommand {
		sandboxLabel = "Run in Docker sandbox (s)"
	}
	content.WriteString(renderCheckboxLine(sandboxLabel, d.sandboxEnabled, cur == focusSandbox))

	// Inherited Docker settings (only visible when sandbox is enabled).
	if d.sandboxEnabled && len(d.inheritedSettings) > 0 {
		focused := cur == focusInherited
		dimStyle := lipgloss.NewStyle().Foreground(ColorComment)
		settingStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

		// Render toggle line.
		arrow := "▸"
		if d.inheritedExpanded {
			arrow = "▾"
		}
		summary := fmt.Sprintf("%d active", len(d.inheritedSettings))
		toggleLine := fmt.Sprintf("%s Docker Settings (%s)", arrow, summary)
		if focused {
			content.WriteString(activeLabelStyle.Render("▶ " + toggleLine))
		} else {
			content.WriteString("  " + dimStyle.Render(toggleLine))
		}
		content.WriteString("\n")

		// Render expanded settings.
		if d.inheritedExpanded {
			for _, s := range d.inheritedSettings {
				content.WriteString(settingStyle.Render(fmt.Sprintf("    %s: %s", s.label, s.value)))
				content.WriteString("\n")
			}
		}
	} else if d.sandboxEnabled {
		// Sandbox enabled but all defaults — show informational line.
		dimStyle := lipgloss.NewStyle().Foreground(ColorComment)
		content.WriteString("  " + dimStyle.Render("Docker Settings (all defaults)"))
		content.WriteString("\n")
	}

	// Conducting parent selector (only visible when conductor sessions exist).
	if len(d.conductorSessions) > 0 {
		focused := cur == focusConductor
		if focused {
			content.WriteString(activeLabelStyle.Render("▶ Conducting parent:"))
		} else {
			content.WriteString(labelStyle.Render("  Conducting parent:"))
		}
		content.WriteString("\n")

		selectedStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
		itemStyle := lipgloss.NewStyle().Foreground(ColorComment)

		// Build item list: "None" + one entry per conductor session.
		type conductorItem struct {
			label string
			idx   int // 0 = None, 1..N = session index
		}
		items := make([]conductorItem, 0, len(d.conductorSessions)+1)
		items = append(items, conductorItem{label: "None", idx: 0})
		for i, inst := range d.conductorSessions {
			name := strings.TrimPrefix(inst.Title, "conductor-")
			shortPath := inst.ProjectPath
			if home, err := os.UserHomeDir(); err == nil {
				shortPath = strings.Replace(shortPath, home, "~", 1)
			}
			label := name
			if shortPath != "" {
				label = fmt.Sprintf("%s  (%s)", name, shortPath)
			}
			items = append(items, conductorItem{label: label, idx: i + 1})
		}

		for _, item := range items {
			if item.idx == d.conductorCursor {
				content.WriteString(selectedStyle.Render("  ▶ " + item.label))
			} else {
				content.WriteString(itemStyle.Render("    " + item.label))
			}
			content.WriteString("\n")
		}
	}

	// Branch input (only visible when worktree is enabled).
	if d.worktreeEnabled {
		content.WriteString("\n")
		if cur == focusBranch {
			content.WriteString(activeLabelStyle.Render("▶ Branch:"))
		} else {
			content.WriteString(labelStyle.Render("  Branch:"))
		}
		content.WriteString("\n")
		content.WriteString("  ")
		content.WriteString(d.branchInput.View())
		content.WriteString("\n")
		if d.branchPicker != nil && d.branchPicker.IsVisible() {
			content.WriteString("  ")
			content.WriteString(strings.ReplaceAll(d.branchPicker.View(), "\n", "\n  "))
			content.WriteString("\n")
		}
	}

	// Tool options panel
	if d.toolOptions != nil {
		content.WriteString("\n")
		content.WriteString(d.toolOptions.View())
	}

	// Inline validation error
	if d.validationErr != "" {
		errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		content.WriteString("\n")
		content.WriteString(errStyle.Render("  ⚠ " + d.validationErr))
	}

	content.WriteString("\n")

	// Help text with better contrast
	helpStyle := lipgloss.NewStyle().
		Foreground(ColorComment). // Use consistent theme color
		MarginTop(1)
	recentPrefix := ""
	if len(d.recentSessions) > 0 {
		recentPrefix = "^R recent │ "
	}
	helpText := recentPrefix + "Tab next/accept │ ↑↓ navigate │ Enter create │ Esc cancel"
	if cur == focusPath {
		if d.suggestionsActive {
			helpText = "↑/↓ navigate │ Space select │ Enter select+create │ Esc back"
		} else if d.pathSoftSelected {
			helpText = "Type to replace │ →/Space browse list │ ← edit │ Tab next │ Esc cancel"
		} else {
			helpText = "Tab autocomplete │ →/Space browse list │ Enter create │ Esc cancel"
		}
	} else if cur == focusBranch {
		if d.branchPicker != nil && d.branchPicker.IsVisible() {
			helpText = "Type filter │ ↑↓ navigate │ Enter select │ Esc close"
		} else {
			helpText = "^F branch search │ Tab next │ Enter create │ Esc cancel"
		}
	} else if cur == focusCommand {
		selectedCmd := d.GetSelectedCommand()
		if selectedCmd == "gemini" || selectedCmd == "codex" {
			helpText = "←→ command │ w worktree │ s sandbox │ y yolo │ Tab next │ Enter create │ Esc cancel"
		} else {
			helpText = "←→ command │ w worktree │ s sandbox │ Tab next │ Enter create │ Esc cancel"
		}
	} else if cur == focusConductor {
		helpText = "↑↓ select parent │ Tab next │ Enter create │ Esc cancel"
	} else if cur == focusWorktree || cur == focusSandbox {
		helpText = "Space toggle │ ↑↓ navigate │ Enter create │ Esc cancel"
	} else if cur == focusInherited {
		helpText = "Space expand/collapse │ ↑↓ navigate │ Enter create │ Esc cancel"
	} else if cur == focusOptions && d.toolOptions != nil {
		helpText = "Space/y toggle │ ↑↓ navigate │ Enter create │ Esc cancel"
	}
	content.WriteString(helpStyle.Render(helpText))

	// Wrap in dialog box
	dialog := dialogStyle.Render(content.String())

	// Center the dialog
	placed := lipgloss.Place(
		d.width,
		d.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)

	// Overlay path suggestions dropdown if visible.
	// Rendered as a floating bordered menu over the placed dialog so it
	// doesn't shift the layout when it appears/disappears.
	if suggestionsOverlay := d.renderSuggestionsDropdown(); suggestionsOverlay != "" {
		// Find where to place the overlay:
		// The dialog is centered, so we need the dialog's top-left position
		// within the placed output, plus the line offset to the path input.
		dialogHeight := lipgloss.Height(dialog)
		dialogWidth := lipgloss.Width(dialog)
		topRow := (d.height - dialogHeight) / 2
		leftCol := (d.width - dialogWidth) / 2

		// suggestionsLineOffset is the content line where the dropdown should appear.
		// Add border (1) + top padding (2) to get the actual row within the dialog box.
		overlayRow := topRow + 1 + 2 + d.suggestionsLineOffset
		// Align with the path input: border (1) + padding (4)
		overlayCol := leftCol + 1 + 4

		placed = overlayDropdown(placed, suggestionsOverlay, overlayRow, overlayCol)
	}

	return placed
}

// renderSuggestionsDropdown renders the path suggestions as a standalone block
// for overlay positioning. Returns empty string if no suggestions to show.
// dropdownMenuBg returns a slightly elevated background color for floating menus.
// Dark theme: one step brighter than Surface. Light theme: one step darker.
func dropdownMenuBg() lipgloss.Color {
	if currentTheme == ThemeLight {
		return lipgloss.Color("#dcdde2")
	}
	return lipgloss.Color("#292e42")
}

func (d *NewDialog) renderSuggestionsDropdown() string {
	cur := d.currentTarget()

	// The dropdown shows whenever the path field is focused — even with no
	// real suggestions, the synthetic "✎ Type custom path…" entry is always
	// available at the top. Hidden after explicit dismiss (e.g. Enter).
	showSingle := !d.multiRepoEnabled && cur == focusPath
	showMulti := d.multiRepoEnabled && cur == focusMultiRepo && d.multiRepoEditing

	if (!showSingle && !showMulti) || d.suggestionsHidden {
		return ""
	}

	menuBg := dropdownMenuBg()
	suggestionStyle := lipgloss.NewStyle().Foreground(ColorComment).Background(menuBg)
	customStyle := lipgloss.NewStyle().Foreground(ColorPurple).Italic(true).Background(menuBg)
	customSelectedStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Italic(true).Background(menuBg)
	selectedStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Background(menuBg)

	var b strings.Builder

	// Synthetic "Type custom path" entry — always pinned at the top.
	{
		label := "✎ Type custom path…"
		prefix := "  "
		style := customStyle
		if d.pathSuggestionCursor == 0 {
			prefix = "▶ "
			style = customSelectedStyle
		}
		b.WriteString(style.Render(prefix + label))
	}

	// Real suggestions below, with paginated scrolling around the selected one.
	maxShow := 5
	total := len(d.pathSuggestions)
	if total > 0 {
		// Cursor 1..N maps to suggestions 0..N-1; -1 means "Type custom" is selected.
		suggCursor := d.pathSuggestionCursor - 1
		startIdx := 0
		endIdx := total
		if total > maxShow {
			anchor := suggCursor
			if anchor < 0 {
				anchor = 0
			}
			startIdx = anchor - maxShow/2
			if startIdx < 0 {
				startIdx = 0
			}
			endIdx = startIdx + maxShow
			if endIdx > total {
				endIdx = total
				startIdx = endIdx - maxShow
			}
		}

		b.WriteString("\n")
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
			if i+1 == d.pathSuggestionCursor {
				style = selectedStyle
				prefix = "▶ "
			}
			b.WriteString(style.Render(prefix + d.pathSuggestions[i]))
		}

		if endIdx < total {
			b.WriteString("\n")
			b.WriteString(suggestionStyle.Render(fmt.Sprintf("  ↓ %d more below", total-endIdx)))
		}
	}

	// Footer with keybinding hints — different text when actively browsing.
	var footerText string
	if d.suggestionsActive {
		footerText = " ↑/↓ navigate │ Space select │ Enter select & close "
	} else {
		footerText = " →/Space browse "
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Background(menuBg).Render(footerText))

	// Wrap in a bordered menu box — accent border when actively browsing.
	borderColor := ColorBorder
	if d.suggestionsActive {
		borderColor = ColorCyan
	}
	menuStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(menuBg).
		Padding(0, 1)

	return menuStyle.Render(b.String())
}

// GetParentSessionID returns the selected conducting parent session ID, or "" for None.
func (d *NewDialog) GetParentSessionID() string {
	if d.conductorCursor == 0 || len(d.conductorSessions) == 0 {
		return ""
	}
	return d.conductorSessions[d.conductorCursor-1].ID
}

// GetParentProjectPath returns the selected conductor's project path, or "".
func (d *NewDialog) GetParentProjectPath() string {
	if d.conductorCursor == 0 || len(d.conductorSessions) == 0 {
		return ""
	}
	return d.conductorSessions[d.conductorCursor-1].ProjectPath
}
