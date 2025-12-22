package ui

import (
	"log"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// MCPScope represents LOCAL or GLOBAL scope
type MCPScope int

const (
	MCPScopeLocal MCPScope = iota
	MCPScopeGlobal
)

// MCPColumn represents Attached or Available column
type MCPColumn int

const (
	MCPColumnAttached MCPColumn = iota
	MCPColumnAvailable
)

// MCPItem represents an MCP in the dialog list
type MCPItem struct {
	Name        string
	Description string
}

// MCPDialog handles MCP management for Claude sessions
type MCPDialog struct {
	visible     bool
	width       int
	height      int
	projectPath string
	sessionID   string // ID of the session being managed (for restart)

	// Current scope and column
	scope  MCPScope
	column MCPColumn

	// Items per scope (attached = enabled, available = pool - attached)
	localAttached   []MCPItem
	localAvailable  []MCPItem
	globalAttached  []MCPItem
	globalAvailable []MCPItem

	// Selection index per scope/column (4 combinations)
	localAttachedIdx   int
	localAvailableIdx  int
	globalAttachedIdx  int
	globalAvailableIdx int

	// Track changes
	localChanged  bool
	globalChanged bool

	err error
}

// NewMCPDialog creates a new MCP management dialog
func NewMCPDialog() *MCPDialog {
	return &MCPDialog{}
}

// Show displays the MCP dialog for a project
func (m *MCPDialog) Show(projectPath string, sessionID string) error {
	// Reload config to pick up any changes to config.toml
	session.ReloadUserConfig()

	// Store session ID for restart
	m.sessionID = sessionID

	// Get all available MCPs from config.toml (the pool)
	availableMCPs := session.GetAvailableMCPs()
	allNames := session.GetAvailableMCPNames()

	// Build items lookup for descriptions
	itemsMap := make(map[string]MCPItem)
	for _, name := range allNames {
		desc := ""
		if def, ok := availableMCPs[name]; ok {
			desc = def.Description
		}
		itemsMap[name] = MCPItem{Name: name, Description: desc}
	}

	// Load LOCAL attached from .mcp.json
	localAttachedNames := make(map[string]bool)
	mcpInfo := session.GetMCPInfo(projectPath)
	for _, name := range mcpInfo.Local {
		localAttachedNames[name] = true
	}

	// Load GLOBAL attached from Claude config (includes both global and project-specific MCPs)
	globalAttachedNames := make(map[string]bool)
	for _, name := range session.GetGlobalMCPNames() {
		globalAttachedNames[name] = true
	}
	// Also include project-specific MCPs from Claude's config (projects[path].mcpServers)
	for _, name := range session.GetProjectMCPNames(projectPath) {
		globalAttachedNames[name] = true
	}

	// Build attached/available lists for LOCAL
	m.localAttached = nil
	m.localAvailable = nil
	for _, name := range allNames {
		item := itemsMap[name]
		if localAttachedNames[name] {
			m.localAttached = append(m.localAttached, item)
		} else if !globalAttachedNames[name] {
			// Only show in LOCAL Available if not already attached globally
			m.localAvailable = append(m.localAvailable, item)
		}
	}

	// Build attached/available lists for GLOBAL
	m.globalAttached = nil
	m.globalAvailable = nil
	for _, name := range allNames {
		item := itemsMap[name]
		if globalAttachedNames[name] {
			m.globalAttached = append(m.globalAttached, item)
		} else {
			m.globalAvailable = append(m.globalAvailable, item)
		}
	}

	m.visible = true
	m.projectPath = projectPath
	m.scope = MCPScopeLocal
	m.column = MCPColumnAttached
	m.localAttachedIdx = 0
	m.localAvailableIdx = 0
	m.globalAttachedIdx = 0
	m.globalAvailableIdx = 0
	m.localChanged = false
	m.globalChanged = false
	m.err = nil

	return nil
}

// Hide hides the dialog
func (m *MCPDialog) Hide() {
	m.visible = false
	m.localAttached = nil
	m.localAvailable = nil
	m.globalAttached = nil
	m.globalAvailable = nil
	m.err = nil
}

// IsVisible returns whether the dialog is visible
func (m *MCPDialog) IsVisible() bool {
	return m.visible
}

// HasItems returns true if there are MCPs to manage
func (m *MCPDialog) HasItems() bool {
	return len(m.localAttached)+len(m.localAvailable)+len(m.globalAttached)+len(m.globalAvailable) > 0
}

// HasChanged returns true if any MCPs were changed (either scope)
func (m *MCPDialog) HasChanged() bool {
	result := m.localChanged || m.globalChanged
	log.Printf("[MCP-DEBUG] HasChanged() called - localChanged=%v, globalChanged=%v, result=%v",
		m.localChanged, m.globalChanged, result)
	return result
}

// GetProjectPath returns the project path being managed
func (m *MCPDialog) GetProjectPath() string {
	return m.projectPath
}

// GetSessionID returns the session ID being managed
func (m *MCPDialog) GetSessionID() string {
	return m.sessionID
}

// GetError returns any error that occurred
func (m *MCPDialog) GetError() error {
	return m.err
}

// SetSize sets the dialog size
func (m *MCPDialog) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// getCurrentList returns the currently focused list and index pointer
func (m *MCPDialog) getCurrentList() (*[]MCPItem, *int) {
	switch {
	case m.scope == MCPScopeLocal && m.column == MCPColumnAttached:
		return &m.localAttached, &m.localAttachedIdx
	case m.scope == MCPScopeLocal && m.column == MCPColumnAvailable:
		return &m.localAvailable, &m.localAvailableIdx
	case m.scope == MCPScopeGlobal && m.column == MCPColumnAttached:
		return &m.globalAttached, &m.globalAttachedIdx
	case m.scope == MCPScopeGlobal && m.column == MCPColumnAvailable:
		return &m.globalAvailable, &m.globalAvailableIdx
	}
	return &m.localAttached, &m.localAttachedIdx
}

// Move moves the selected item between Attached <-> Available
func (m *MCPDialog) Move() {
	log.Printf("[MCP-DEBUG] Move() called - scope=%d, column=%d", m.scope, m.column)
	list, idx := m.getCurrentList()
	if len(*list) == 0 || *idx < 0 || *idx >= len(*list) {
		log.Printf("[MCP-DEBUG] Move() early return - list empty or invalid index")
		return
	}

	item := (*list)[*idx]
	log.Printf("[MCP-DEBUG] Moving item: %q", item.Name)

	// Remove from current list
	*list = append((*list)[:*idx], (*list)[*idx+1:]...)

	// Add to other column
	if m.column == MCPColumnAttached {
		// Moving from Attached -> Available
		if m.scope == MCPScopeLocal {
			m.localAvailable = append(m.localAvailable, item)
			m.localChanged = true
			log.Printf("[MCP-DEBUG] Moved to localAvailable, localChanged=true")
		} else {
			m.globalAvailable = append(m.globalAvailable, item)
			m.globalChanged = true
			log.Printf("[MCP-DEBUG] Moved to globalAvailable, globalChanged=true")
		}
	} else {
		// Moving from Available -> Attached
		if m.scope == MCPScopeLocal {
			m.localAttached = append(m.localAttached, item)
			m.localChanged = true
			log.Printf("[MCP-DEBUG] Moved to localAttached, localChanged=true")
		} else {
			m.globalAttached = append(m.globalAttached, item)
			m.globalChanged = true
			log.Printf("[MCP-DEBUG] Moved to globalAttached, globalChanged=true")
		}
	}

	log.Printf("[MCP-DEBUG] After Move: localChanged=%v, globalChanged=%v", m.localChanged, m.globalChanged)

	// Adjust index if needed
	if *idx >= len(*list) && len(*list) > 0 {
		*idx = len(*list) - 1
	}
}

// Apply saves the changes to LOCAL (.mcp.json) and GLOBAL (Claude config)
func (m *MCPDialog) Apply() error {
	log.Printf("[MCP-DEBUG] Apply() called - localChanged=%v, globalChanged=%v, projectPath=%q",
		m.localChanged, m.globalChanged, m.projectPath)

	// Apply LOCAL changes
	if m.localChanged {
		// Get names of attached MCPs
		enabledNames := make([]string, len(m.localAttached))
		for i, item := range m.localAttached {
			enabledNames[i] = item.Name
		}

		// Write to .mcp.json
		if err := session.WriteMCPJsonFromConfig(m.projectPath, enabledNames); err != nil {
			m.err = err
			return err
		}

		// Clear MCP cache so preview updates
		session.ClearMCPCache(m.projectPath)
	}

	// Apply GLOBAL changes
	if m.globalChanged {
		// Get names of attached MCPs
		enabledNames := make([]string, len(m.globalAttached))
		for i, item := range m.globalAttached {
			enabledNames[i] = item.Name
		}

		// Write to Claude's global config
		if err := session.WriteGlobalMCP(enabledNames); err != nil {
			m.err = err
			return err
		}

		// Also clear project-specific MCPs (they were shown in global view)
		// This ensures removed MCPs are actually removed
		if err := session.ClearProjectMCPs(m.projectPath); err != nil {
			m.err = err
			return err
		}

		// Clear MCP cache so preview updates
		session.ClearMCPCache(m.projectPath)
	}

	return nil
}

// Update handles input
func (m *MCPDialog) Update(msg tea.KeyMsg) (*MCPDialog, tea.Cmd) {
	list, idx := m.getCurrentList()

	switch msg.String() {
	case "tab":
		// Switch scope: LOCAL <-> GLOBAL
		if m.scope == MCPScopeLocal {
			m.scope = MCPScopeGlobal
		} else {
			m.scope = MCPScopeLocal
		}

	case "left", "h":
		// Switch to Attached column
		m.column = MCPColumnAttached

	case "right", "l":
		// Switch to Available column
		m.column = MCPColumnAvailable

	case "up", "k":
		if len(*list) > 0 && *idx > 0 {
			*idx--
		}

	case "down", "j":
		if len(*list) > 0 && *idx < len(*list)-1 {
			*idx++
		}

	case " ":
		m.Move()
	}

	return m, nil
}

// View renders the dialog
func (m *MCPDialog) View() string {
	if !m.visible {
		return ""
	}

	// Title
	title := "MCP Manager"

	// Scope tabs
	localTab := "LOCAL"
	globalTab := "GLOBAL"
	if m.scope == MCPScopeLocal {
		localTab = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("[" + localTab + "]")
		globalTab = lipgloss.NewStyle().Foreground(ColorTextDim).Render(" " + globalTab + " ")
	} else {
		localTab = lipgloss.NewStyle().Foreground(ColorTextDim).Render(" " + localTab + " ")
		globalTab = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("[" + globalTab + "]")
	}
	tabs := localTab + " ───────────────────── " + globalTab

	// Get current scope's lists
	var attached, available []MCPItem
	var attachedIdx, availableIdx int
	if m.scope == MCPScopeLocal {
		attached = m.localAttached
		available = m.localAvailable
		attachedIdx = m.localAttachedIdx
		availableIdx = m.localAvailableIdx
	} else {
		attached = m.globalAttached
		available = m.globalAvailable
		attachedIdx = m.globalAttachedIdx
		availableIdx = m.globalAvailableIdx
	}

	// Render columns
	attachedCol := m.renderColumn("Attached", attached, attachedIdx, m.column == MCPColumnAttached)
	availableCol := m.renderColumn("Available", available, availableIdx, m.column == MCPColumnAvailable)

	columns := lipgloss.JoinHorizontal(lipgloss.Top, attachedCol, "  ", availableCol)

	// Scope description
	var scopeDesc string
	if m.scope == MCPScopeLocal {
		scopeDesc = DimStyle.Render("Writes to: .mcp.json (this project only)")
	} else {
		scopeDesc = DimStyle.Render("Writes to: Claude config (global + project-specific)")
	}

	// Error display
	var errText string
	if m.err != nil {
		errText = lipgloss.NewStyle().Foreground(ColorRed).Render("Error: " + m.err.Error())
	}

	// Hint with consistent styling
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	hint := hintStyle.Render("Tab scope │ ←→ column │ Space move │ Enter apply │ Esc cancel")

	// Assemble dialog
	titleStyle := DialogTitleStyle.Width(60)

	parts := []string{
		titleStyle.Render(title),
		"",
		tabs,
		scopeDesc,
		"",
		columns,
	}
	if errText != "" {
		parts = append(parts, "", errText)
	}
	parts = append(parts, "", hint)

	dialogContent := lipgloss.JoinVertical(lipgloss.Left, parts...)

	dialog := DialogBoxStyle.Width(64).Render(dialogContent)

	// Center the dialog
	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)
}

// renderColumn renders a single column (Attached or Available)
func (m *MCPDialog) renderColumn(title string, items []MCPItem, selectedIdx int, focused bool) string {
	// Header
	headerStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	if focused {
		headerStyle = headerStyle.Foreground(ColorAccent)
	}
	header := headerStyle.Render("- " + title + " ")

	// Pad header to column width
	colWidth := 26
	headerLen := len("- " + title + " ")
	headerPad := colWidth - headerLen
	if headerPad > 0 {
		header += headerStyle.Render(repeatStr("-", headerPad))
	}

	// Items
	var lines []string
	lines = append(lines, header)

	if len(items) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)
		lines = append(lines, emptyStyle.Render("  (empty)"))
	} else {
		for i, item := range items {
			name := item.Name
			if len(name) > 20 {
				name = name[:17] + "..."
			}

			var line string
			if i == selectedIdx && focused {
				line = lipgloss.NewStyle().
					Background(ColorAccent).
					Foreground(ColorBg).
					Bold(true).
					Width(colWidth).
					Render(" > " + name)
			} else {
				line = lipgloss.NewStyle().
					Foreground(ColorText).
					Width(colWidth).
					Render("   " + name)
			}
			lines = append(lines, line)
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// repeatStr repeats a string n times
func repeatStr(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}
