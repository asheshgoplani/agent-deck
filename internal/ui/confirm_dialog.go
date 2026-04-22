package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ConfirmType indicates what action is being confirmed
type ConfirmType int

const (
	ConfirmDeleteSession ConfirmType = iota
	ConfirmCloseSession
	ConfirmDeleteGroup
	ConfirmQuitWithPool
	ConfirmCreateDirectory
	ConfirmInstallHooks
	ConfirmDeleteRemoteSession
	ConfirmCloseRemoteSession
	ConfirmRemoveSession     // status-gated registry-only remove (TUI 'X')
	ConfirmBulkRemoveErrored // bulk remove of all errored sessions (TUI Ctrl+X)
)

// ConfirmDialog handles confirmation for destructive actions
type ConfirmDialog struct {
	visible     bool
	confirmType ConfirmType
	targetID    string // Session ID or group path
	targetName  string // Display name
	width       int
	height      int
	mcpCount    int  // Number of running MCPs (for quit confirmation)
	sandboxed   bool // Whether the session uses a Docker sandbox.
	worktree    bool // Whether the session has an associated git worktree.

	remoteName string // Remote name for remote session confirmations.

	// focusedButton tracks which button has arrow-key focus.
	// 0 = confirm (left), 1 = cancel (right).
	// For ConfirmQuitWithPool: 0 = keep, 1 = shutdown.
	focusedButton int
	// buttonCount is the number of selectable buttons for the current dialog.
	buttonCount int

	// Pending session creation data (for ConfirmCreateDirectory)
	pendingSessionName       string
	pendingSessionPath       string
	pendingSessionCommand    string
	pendingSessionGroupPath  string
	pendingToolOptionsJSON   json.RawMessage // Generic tool options (claude, codex, etc.)
	pendingClaudeExtraArgs   []string        // User-supplied claude CLI tokens
	pendingParentSessionID   string
	pendingParentProjectPath string
}

// NewConfirmDialog creates a new confirmation dialog
func NewConfirmDialog() *ConfirmDialog {
	return &ConfirmDialog{}
}

// ShowDeleteSession shows confirmation for session deletion.
func (c *ConfirmDialog) ShowDeleteSession(sessionID string, sessionName string, sandboxed, worktree bool) {
	c.visible = true
	c.confirmType = ConfirmDeleteSession
	c.targetID = sessionID
	c.targetName = sessionName
	c.sandboxed = sandboxed
	c.worktree = worktree
	c.buttonCount = 2
	c.focusedButton = 1 // default to Cancel
}

// ShowCloseSession shows confirmation for non-destructive session close.
func (c *ConfirmDialog) ShowCloseSession(sessionID string, sessionName string, sandboxed bool) {
	c.visible = true
	c.confirmType = ConfirmCloseSession
	c.targetID = sessionID
	c.targetName = sessionName
	c.sandboxed = sandboxed
	c.buttonCount = 2
	c.focusedButton = 1
}

// ShowDeleteRemoteSession shows confirmation for deleting a remote session.
func (c *ConfirmDialog) ShowDeleteRemoteSession(remoteName, sessionID, sessionName string) {
	c.visible = true
	c.confirmType = ConfirmDeleteRemoteSession
	c.targetID = sessionID
	c.targetName = sessionName
	c.remoteName = remoteName
	c.buttonCount = 2
	c.focusedButton = 1
}

// ShowCloseRemoteSession shows confirmation for closing a remote session.
func (c *ConfirmDialog) ShowCloseRemoteSession(remoteName, sessionID, sessionName string) {
	c.visible = true
	c.confirmType = ConfirmCloseRemoteSession
	c.targetID = sessionID
	c.targetName = sessionName
	c.remoteName = remoteName
	c.buttonCount = 2
	c.focusedButton = 1
}

// ShowRemoveSession shows confirmation for status-gated registry removal (TUI 'X').
// Safer than ConfirmDeleteSession: the caller has already verified the
// session is stopped or errored, and the dialog wording reflects the
// registry-only intent (transcripts + worktrees are preserved).
func (c *ConfirmDialog) ShowRemoveSession(sessionID string, sessionName string) {
	c.visible = true
	c.confirmType = ConfirmRemoveSession
	c.targetID = sessionID
	c.targetName = sessionName
	c.buttonCount = 2
	c.focusedButton = 1 // default to Cancel
}

// ShowBulkRemoveErrored shows confirmation for removing all errored sessions
// (TUI Ctrl+X). count is the number of errored sessions that will be removed.
func (c *ConfirmDialog) ShowBulkRemoveErrored(count int) {
	c.visible = true
	c.confirmType = ConfirmBulkRemoveErrored
	c.targetID = ""
	c.targetName = ""
	c.mcpCount = count // reuse mcpCount as a generic integer carrier
	c.buttonCount = 2
	c.focusedButton = 1
}

// ShowDeleteGroup shows confirmation for group deletion
func (c *ConfirmDialog) ShowDeleteGroup(groupPath, groupName string) {
	c.visible = true
	c.confirmType = ConfirmDeleteGroup
	c.targetID = groupPath
	c.targetName = groupName
	c.buttonCount = 2
	c.focusedButton = 1
}

// ShowQuitWithPool shows confirmation for quitting with MCP pool running
func (c *ConfirmDialog) ShowQuitWithPool(mcpCount int) {
	c.visible = true
	c.confirmType = ConfirmQuitWithPool
	c.mcpCount = mcpCount
	c.targetID = ""
	c.targetName = ""
	c.buttonCount = 2
	c.focusedButton = 0 // default to "Keep running" (safe choice)
}

// ShowCreateDirectory shows confirmation for creating a missing directory.
func (c *ConfirmDialog) ShowCreateDirectory(
	path string,
	sessionName string,
	command string,
	groupPath string,
	toolOptionsJSON json.RawMessage,
	claudeExtraArgs []string,
	parentSessionID string,
	parentProjectPath string,
) {
	c.visible = true
	c.confirmType = ConfirmCreateDirectory
	c.targetID = path
	c.targetName = path
	c.pendingSessionName = sessionName
	c.pendingSessionPath = path
	c.pendingSessionCommand = command
	c.pendingSessionGroupPath = groupPath
	c.pendingToolOptionsJSON = toolOptionsJSON
	c.pendingClaudeExtraArgs = claudeExtraArgs
	c.pendingParentSessionID = parentSessionID
	c.pendingParentProjectPath = parentProjectPath
	c.buttonCount = 2
	c.focusedButton = 1
}

// ShowInstallHooks shows confirmation for installing Claude Code hooks
func (c *ConfirmDialog) ShowInstallHooks() {
	c.visible = true
	c.confirmType = ConfirmInstallHooks
	c.targetID = ""
	c.targetName = ""
	c.buttonCount = 2
	c.focusedButton = 1
}

// GetPendingSession returns the pending session creation data
func (c *ConfirmDialog) GetPendingSession() (name, path, command, groupPath string, toolOptionsJSON json.RawMessage, claudeExtraArgs []string, parentSessionID, parentProjectPath string) {
	return c.pendingSessionName, c.pendingSessionPath, c.pendingSessionCommand, c.pendingSessionGroupPath, c.pendingToolOptionsJSON, c.pendingClaudeExtraArgs, c.pendingParentSessionID, c.pendingParentProjectPath
}

// Hide hides the dialog.
func (c *ConfirmDialog) Hide() {
	c.visible = false
	c.targetID = ""
	c.targetName = ""
	c.sandboxed = false
	c.remoteName = ""
}

// IsVisible returns whether the dialog is visible
func (c *ConfirmDialog) IsVisible() bool {
	return c.visible
}

// GetTargetID returns the session ID or group path being confirmed
func (c *ConfirmDialog) GetTargetID() string {
	return c.targetID
}

// GetConfirmType returns the type of confirmation
func (c *ConfirmDialog) GetConfirmType() ConfirmType {
	return c.confirmType
}

// GetRemoteName returns the remote name for remote session confirmations.
func (c *ConfirmDialog) GetRemoteName() string {
	return c.remoteName
}

// SetSize updates dialog dimensions
func (c *ConfirmDialog) SetSize(width, height int) {
	c.width = width
	c.height = height
}

// GetFocusedButton returns the currently focused button index.
func (c *ConfirmDialog) GetFocusedButton() int {
	return c.focusedButton
}

// Update handles key events for arrow-key navigation between buttons.
func (c *ConfirmDialog) Update(msg tea.KeyMsg) (*ConfirmDialog, tea.Cmd) {
	switch msg.String() {
	case "left", "h":
		if c.focusedButton > 0 {
			c.focusedButton--
		}
	case "right", "l", "tab":
		if c.focusedButton < c.buttonCount-1 {
			c.focusedButton++
		}
	}
	return c, nil
}

// View renders the confirmation dialog
func (c *ConfirmDialog) View() string {
	if !c.visible {
		return ""
	}

	// Build warning message and buttons based on action type
	var title, warning, details string
	var buttons string
	var borderColor lipgloss.Color

	// Styles (shared)
	detailsStyle := lipgloss.NewStyle().
		Foreground(ColorTextDim).
		MarginBottom(1)

	// Focused buttons get filled background; unfocused get dim outline.
	renderButton := func(label string, bg lipgloss.Color, focused bool) string {
		if focused {
			return lipgloss.NewStyle().
				Foreground(ColorBg).
				Background(bg).
				Padding(0, 2).
				Bold(true).
				Render("▸ " + label)
		}
		return lipgloss.NewStyle().
			Foreground(bg).
			Padding(0, 2).
			Bold(true).
			Render("  " + label)
	}

	hintStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	switch c.confirmType {
	case ConfirmDeleteSession:
		title = "⚠  Delete Session?"
		warning = fmt.Sprintf("This will permanently delete the session:\n\n  \"%s\"", c.targetName)
		details = "• The tmux session will be terminated\n• Any running processes will be killed\n• Terminal history will be lost"
		if c.worktree {
			details += "\n• The git worktree directory will be removed"
		}
		if c.sandboxed {
			details += "\n• The Docker container will be removed"
		}
		details += "\n• Undo is available from the session list"
		borderColor = ColorRed
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Delete", ColorRed, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y delete · n cancel · ←/→ navigate · Enter select · Esc"))

	case ConfirmCloseSession:
		title = "Close Session?"
		warning = fmt.Sprintf("This will close the running process for:\n\n  \"%s\"", c.targetName)
		details = "• The tmux session will be terminated\n• Session metadata will be kept in the list\n• You can restart later from the session list"
		if c.sandboxed {
			details += "\n• The Docker container will be removed"
		}
		borderColor = ColorYellow
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Close", ColorYellow, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y close · n cancel · ←/→ navigate · Enter select · Esc"))

	case ConfirmDeleteRemoteSession:
		title = "⚠  Delete Remote Session?"
		warning = fmt.Sprintf("This will permanently delete the remote session:\n\n  \"%s\" on %s", c.targetName, c.remoteName)
		details = "• The remote tmux session will be terminated\n• Any running processes on the remote will be killed\n• Terminal history will be lost"
		borderColor = ColorRed
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Delete", ColorRed, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y delete · n cancel · ←/→ navigate · Enter select · Esc"))

	case ConfirmCloseRemoteSession:
		title = "Close Remote Session?"
		warning = fmt.Sprintf("This will close the running process for:\n\n  \"%s\" on %s", c.targetName, c.remoteName)
		details = "• The remote tmux session will be terminated\n• Session metadata will be kept on the remote\n• You can restart later"
		borderColor = ColorYellow
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Close", ColorYellow, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y close · n cancel · ←/→ navigate · Enter select · Esc"))

	case ConfirmRemoveSession:
		title = "Remove Session?"
		warning = fmt.Sprintf("Remove this session from the registry:\n\n  \"%s\"", c.targetName)
		details = "• The session record will be deleted from agent-deck\n• Claude transcripts (~/.claude/projects/) are preserved\n• Git worktrees are preserved (use 'd' to destroy them)"
		borderColor = ColorYellow
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Remove", ColorYellow, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y remove · n cancel · ←/→ navigate · Enter select · Esc"))

	case ConfirmBulkRemoveErrored:
		title = "Remove All Errored Sessions?"
		warning = fmt.Sprintf("Remove %d errored session(s) from the registry.", c.mcpCount)
		details = "• Only sessions currently in the 'error' state are affected\n• Claude transcripts are preserved\n• Git worktrees are preserved"
		borderColor = ColorYellow
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Remove All", ColorYellow, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y remove · n cancel · ←/→ navigate · Enter select · Esc"))

	case ConfirmDeleteGroup:
		title = "⚠  Delete Group?"
		warning = fmt.Sprintf("This will delete the group:\n\n  \"%s\"", c.targetName)
		details = "• All sessions will be MOVED to 'default' group\n• Sessions will NOT be killed\n• The group structure will be lost"
		borderColor = ColorRed
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Delete", ColorRed, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y delete · n cancel · ←/→ navigate · Enter select · Esc"))

	case ConfirmQuitWithPool:
		title = "MCP Pool Running"
		warning = fmt.Sprintf("%d MCP servers are running in the pool.", c.mcpCount)
		details = "Keep them running for faster startup next time,\nor shut down to free resources."
		borderColor = ColorAccent
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Keep running", ColorGreen, c.focusedButton == 0), "  ",
			renderButton("Shut down", ColorRed, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("k keep · s shut down · ←/→ navigate · Enter select · Esc"))

	case ConfirmCreateDirectory:
		title = "📁  Directory Not Found"
		warning = fmt.Sprintf("The path does not exist:\n\n  %s", c.targetName)
		details = "Create this directory and start the session?"
		borderColor = ColorAccent
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Create", ColorGreen, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorRed, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y create · n cancel · ←/→ navigate · Enter select · Esc"))

	case ConfirmInstallHooks:
		title = "Claude Code Hooks"
		warning = "Agent-deck can install Claude Code lifecycle hooks\nfor real-time status detection (instant green/yellow/gray)."
		details = "This writes to your Claude settings.json (preserves existing settings).\nNew/restarted sessions will use hooks; existing sessions continue unchanged.\nYou can disable later with: hooks_enabled = false in config.toml"
		borderColor = ColorAccent
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Install", ColorGreen, c.focusedButton == 0), "  ",
			renderButton("Skip", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y install · n skip · ←/→ navigate · Enter select · Esc"))
	}

	// Title style
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(borderColor).
		MarginBottom(1)

	// Warning style
	warningStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		MarginBottom(1)

	// Build content
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render(title),
		warningStyle.Render(warning),
		detailsStyle.Render(details),
		"",
		buttons,
	)

	// Dialog box
	dialogWidth := 50
	if c.width > 0 && c.width < dialogWidth+10 {
		dialogWidth = c.width - 10
	}

	dialogBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(1, 2).
		Width(dialogWidth).
		Render(content)

	// Center in screen
	if c.width > 0 && c.height > 0 {
		// Create full-screen overlay with centered dialog
		dialogHeight := lipgloss.Height(dialogBox)
		dialogWidth := lipgloss.Width(dialogBox)

		padLeft := (c.width - dialogWidth) / 2
		if padLeft < 0 {
			padLeft = 0
		}
		padTop := (c.height - dialogHeight) / 2
		if padTop < 0 {
			padTop = 0
		}

		// Build centered dialog
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
