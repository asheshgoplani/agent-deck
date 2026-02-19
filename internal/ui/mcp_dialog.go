package ui

import (
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var mcpDialogLog = logging.ForComponent(logging.CompMCP)

// MCPScope represents LOCAL, GLOBAL, or USER scope
type MCPScope int

const (
	MCPScopeLocal MCPScope = iota
	MCPScopeGlobal
	MCPScopeUser // NEW: Manages ~/.claude.json (ROOT)
)

// MCPColumn represents Attached or Available column
type MCPColumn int

const (
	MCPColumnAttached MCPColumn = iota
	MCPColumnAvailable

	mcpTypeJumpTimeout = 1200 * time.Millisecond
)

// MCPItem represents an MCP in the dialog list
type MCPItem struct {
	Name         string
	Description  string
	IsOrphan     bool   // True if MCP is attached but not in config.toml pool
	IsPooled     bool   // True if this MCP uses socket pool
	Transport    string // "stdio", "http", or "sse"
	HTTPStatus   string // For HTTP MCPs: "running", "stopped", "external", etc.
	HasServerCfg bool   // True if HTTP MCP has [mcps.X.server] config
}

// MCPDialog handles MCP management for Claude and Gemini sessions
type MCPDialog struct {
	visible     bool
	width       int
	height      int
	projectPath string
	sessionID   string // ID of the session being managed (for restart)
	tool        string // "claude" or "gemini"

	// Current scope and column
	scope  MCPScope
	column MCPColumn

	// Items per scope (attached = enabled, available = pool - attached)
	localAttached   []MCPItem
	localAvailable  []MCPItem
	globalAttached  []MCPItem
	globalAvailable []MCPItem
	userAttached    []MCPItem // USER scope: ~/.claude.json (ROOT)
	userAvailable   []MCPItem // USER scope: ~/.claude.json (ROOT)

	// Selection index per scope/column (6 combinations)
	localAttachedIdx   int
	localAvailableIdx  int
	globalAttachedIdx  int
	globalAvailableIdx int
	userAttachedIdx    int // USER scope index
	userAvailableIdx   int // USER scope index

	// Track changes
	localChanged  bool
	globalChanged bool
	userChanged   bool // USER scope changed

	err           error
	configError   string // Error message from config parsing
	typeJumpBuf   string
	typeJumpUntil time.Time
}

// NewMCPDialog creates a new MCP management dialog
func NewMCPDialog() *MCPDialog {
	return &MCPDialog{}
}

// Show displays the MCP dialog for a project
func (m *MCPDialog) Show(projectPath string, sessionID string, tool string) error {
	// Reload config to pick up any changes to config.toml
	// Capture any parse errors to display in dialog
	m.configError = ""
	if _, err := session.ReloadUserConfig(); err != nil {
		m.configError = err.Error()
	}

	// Store session ID and tool for restart
	m.sessionID = sessionID
	m.tool = tool

	// Get all available MCPs from config.toml (the pool)
	availableMCPs := session.GetAvailableMCPs()
	allNames := session.GetAvailableMCPNames()

	// Build items lookup for descriptions, transport, and pool status
	pool := session.GetGlobalPool()
	httpPool := session.GetGlobalHTTPPool()
	itemsMap := make(map[string]MCPItem)
	for _, name := range allNames {
		def, ok := availableMCPs[name]
		desc := ""
		if ok {
			desc = def.Description
		}

		// Determine transport type and status
		transport := "stdio"
		httpStatus := ""
		hasServerCfg := false
		isPooled := false

		if ok && def.IsHTTP() {
			transport = def.GetTransport()
			hasServerCfg = def.HasAutoStartServer()

			// Check HTTP server status
			if httpPool != nil && httpPool.IsRunning(name) {
				server := httpPool.GetServer(name)
				if server != nil && server.StartedByUs() {
					httpStatus = "running"
				} else {
					httpStatus = "external"
				}
			} else if hasServerCfg {
				httpStatus = "stopped"
			} else {
				httpStatus = "external"
			}
		} else {
			// stdio MCP - check socket pool
			isPooled = pool != nil && pool.ShouldPool(name) && pool.IsRunning(name)
		}

		itemsMap[name] = MCPItem{
			Name:         name,
			Description:  desc,
			IsPooled:     isPooled,
			Transport:    transport,
			HTTPStatus:   httpStatus,
			HasServerCfg: hasServerCfg,
		}
	}

	// Track which MCPs are in the config.toml pool
	poolNames := make(map[string]bool)
	for _, name := range allNames {
		poolNames[name] = true
	}

	// Reset lists
	m.localAttached = nil
	m.localAvailable = nil
	m.globalAttached = nil
	m.globalAvailable = nil
	m.userAttached = nil
	m.userAvailable = nil

	if tool == "gemini" {
		// Gemini: Only global MCPs from settings.json
		mcpInfo := session.GetGeminiMCPInfo(projectPath)
		globalAttachedNames := make(map[string]bool)
		for _, name := range mcpInfo.Global {
			globalAttachedNames[name] = true
		}

		// Build attached/available lists for GLOBAL only
		for _, name := range allNames {
			item := itemsMap[name]
			if globalAttachedNames[name] {
				m.globalAttached = append(m.globalAttached, item)
			} else {
				m.globalAvailable = append(m.globalAvailable, item)
			}
		}

		// Add orphan GLOBAL MCPs (attached in settings.json but not in config.toml pool)
		for name := range globalAttachedNames {
			if !poolNames[name] {
				m.globalAttached = append(m.globalAttached, MCPItem{
					Name:        name,
					Description: "(not in config.toml)",
					IsOrphan:    true,
				})
			}
		}
	} else {
		// Claude: Load LOCAL attached from .mcp.json
		localAttachedNames := make(map[string]bool)
		mcpInfo := session.GetMCPInfo(projectPath)
		for _, name := range mcpInfo.Local() {
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
		for _, name := range allNames {
			item := itemsMap[name]
			if localAttachedNames[name] {
				m.localAttached = append(m.localAttached, item)
			} else if !globalAttachedNames[name] {
				// Only show in LOCAL Available if not already attached globally
				m.localAvailable = append(m.localAvailable, item)
			}
		}

		// Add orphan LOCAL MCPs (attached in .mcp.json but not in config.toml pool)
		// These are "ghost" MCPs that Claude loads but agent-deck couldn't previously manage
		for name := range localAttachedNames {
			if !poolNames[name] {
				m.localAttached = append(m.localAttached, MCPItem{
					Name:        name,
					Description: "(not in config.toml)",
					IsOrphan:    true,
				})
			}
		}

		// Build attached/available lists for GLOBAL
		for _, name := range allNames {
			item := itemsMap[name]
			if globalAttachedNames[name] {
				m.globalAttached = append(m.globalAttached, item)
			} else {
				m.globalAvailable = append(m.globalAvailable, item)
			}
		}

		// Add orphan GLOBAL MCPs (attached in Claude config but not in config.toml pool)
		for name := range globalAttachedNames {
			if !poolNames[name] {
				m.globalAttached = append(m.globalAttached, MCPItem{
					Name:        name,
					Description: "(not in config.toml)",
					IsOrphan:    true,
				})
			}
		}

		// Load USER attached from ~/.claude.json (ROOT config)
		userAttachedNames := make(map[string]bool)
		for _, name := range session.GetUserMCPNames() {
			userAttachedNames[name] = true
		}

		// Build attached/available lists for USER
		for _, name := range allNames {
			item := itemsMap[name]
			if userAttachedNames[name] {
				m.userAttached = append(m.userAttached, item)
			} else {
				m.userAvailable = append(m.userAvailable, item)
			}
		}

		// Add orphan USER MCPs (attached in ~/.claude.json but not in config.toml pool)
		for name := range userAttachedNames {
			if !poolNames[name] {
				m.userAttached = append(m.userAttached, MCPItem{
					Name:        name,
					Description: "(not in config.toml)",
					IsOrphan:    true,
				})
			}
		}
	}

	m.visible = true
	m.projectPath = projectPath
	// Gemini only has global scope, Claude uses configured default
	if tool == "gemini" {
		m.scope = MCPScopeGlobal
	} else {
		switch session.GetMCPDefaultScope() {
		case "global":
			m.scope = MCPScopeGlobal
		case "user":
			m.scope = MCPScopeUser
		default:
			m.scope = MCPScopeLocal
		}
	}
	m.column = MCPColumnAttached
	m.localAttachedIdx = 0
	m.localAvailableIdx = 0
	m.globalAttachedIdx = 0
	m.globalAvailableIdx = 0
	m.userAttachedIdx = 0
	m.userAvailableIdx = 0
	m.localChanged = false
	m.globalChanged = false
	m.userChanged = false
	m.err = nil
	m.typeJumpBuf = ""
	m.typeJumpUntil = time.Time{}

	return nil
}

// Hide hides the dialog
func (m *MCPDialog) Hide() {
	m.visible = false
	m.localAttached = nil
	m.localAvailable = nil
	m.globalAttached = nil
	m.globalAvailable = nil
	m.userAttached = nil
	m.userAvailable = nil
	m.err = nil
	m.typeJumpBuf = ""
	m.typeJumpUntil = time.Time{}
}

// IsVisible returns whether the dialog is visible
func (m *MCPDialog) IsVisible() bool {
	return m.visible
}

// HasItems returns true if there are MCPs to manage
func (m *MCPDialog) HasItems() bool {
	return len(m.localAttached)+len(m.localAvailable)+len(m.globalAttached)+len(m.globalAvailable) > 0
}

// HasChanged returns true if any MCPs were changed (any scope)
func (m *MCPDialog) HasChanged() bool {
	result := m.localChanged || m.globalChanged || m.userChanged
	mcpDialogLog.Debug("has_changed_check",
		slog.Bool("local_changed", m.localChanged),
		slog.Bool("global_changed", m.globalChanged),
		slog.Bool("user_changed", m.userChanged),
		slog.Bool("result", result))
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
	case m.scope == MCPScopeUser && m.column == MCPColumnAttached:
		return &m.userAttached, &m.userAttachedIdx
	case m.scope == MCPScopeUser && m.column == MCPColumnAvailable:
		return &m.userAvailable, &m.userAvailableIdx
	}
	return &m.localAttached, &m.localAttachedIdx
}

// Move moves the selected item between Attached <-> Available
func (m *MCPDialog) Move() {
	mcpDialogLog.Debug("mcp_move_start",
		slog.Int("scope", int(m.scope)),
		slog.Int("column", int(m.column)))
	list, idx := m.getCurrentList()
	if len(*list) == 0 || *idx < 0 || *idx >= len(*list) {
		mcpDialogLog.Debug("mcp_move_early_return", slog.String("reason", "list_empty_or_invalid_index"))
		return
	}

	item := (*list)[*idx]
	mcpDialogLog.Debug("mcp_move_item", slog.String("name", item.Name))

	// Remove from current list
	*list = append((*list)[:*idx], (*list)[*idx+1:]...)

	// Add to other column
	if m.column == MCPColumnAttached {
		// Moving from Attached -> Available
		switch m.scope {
		case MCPScopeLocal:
			m.localAvailable = append(m.localAvailable, item)
			m.localChanged = true
			mcpDialogLog.Debug("mcp_moved_to_available", slog.String("scope", "local"))
		case MCPScopeGlobal:
			m.globalAvailable = append(m.globalAvailable, item)
			m.globalChanged = true
			mcpDialogLog.Debug("mcp_moved_to_available", slog.String("scope", "global"))
		case MCPScopeUser:
			m.userAvailable = append(m.userAvailable, item)
			m.userChanged = true
			mcpDialogLog.Debug("mcp_moved_to_available", slog.String("scope", "user"))
		}
	} else {
		// Moving from Available -> Attached
		switch m.scope {
		case MCPScopeLocal:
			m.localAttached = append(m.localAttached, item)
			m.localChanged = true
			mcpDialogLog.Debug("mcp_moved_to_attached", slog.String("scope", "local"))
		case MCPScopeGlobal:
			m.globalAttached = append(m.globalAttached, item)
			m.globalChanged = true
			mcpDialogLog.Debug("mcp_moved_to_attached", slog.String("scope", "global"))
		case MCPScopeUser:
			m.userAttached = append(m.userAttached, item)
			m.userChanged = true
			mcpDialogLog.Debug("mcp_moved_to_attached", slog.String("scope", "user"))
		}
	}

	mcpDialogLog.Debug("mcp_move_complete",
		slog.Bool("local_changed", m.localChanged),
		slog.Bool("global_changed", m.globalChanged),
		slog.Bool("user_changed", m.userChanged))

	// Adjust index if needed
	if *idx >= len(*list) && len(*list) > 0 {
		*idx = len(*list) - 1
	}
}

func (m *MCPDialog) resetTypeJump() {
	m.typeJumpBuf = ""
	m.typeJumpUntil = time.Time{}
}

func (m *MCPDialog) typeJump(r rune) {
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
		return
	}

	now := time.Now()
	if now.After(m.typeJumpUntil) {
		m.typeJumpBuf = ""
	}
	m.typeJumpBuf += strings.ToLower(string(r))
	m.typeJumpUntil = now.Add(mcpTypeJumpTimeout)

	list, idx := m.getCurrentList()
	if len(*list) == 0 {
		return
	}

	findFrom := func(prefix string) int {
		start := *idx + 1
		for i := 0; i < len(*list); i++ {
			j := (start + i) % len(*list)
			name := strings.ToLower((*list)[j].Name)
			if strings.HasPrefix(name, prefix) {
				return j
			}
		}
		return -1
	}

	if match := findFrom(m.typeJumpBuf); match >= 0 {
		*idx = match
		return
	}

	// Fallback: if multi-char prefix misses, try latest rune by itself.
	last := strings.ToLower(string(r))
	if match := findFrom(last); match >= 0 {
		m.typeJumpBuf = last
		*idx = match
	}
}

// Apply saves the changes to LOCAL (.mcp.json), GLOBAL (Claude/Gemini config), and USER (~/.claude.json)
func (m *MCPDialog) Apply() error {
	mcpDialogLog.Debug("mcp_apply_start",
		slog.String("tool", m.tool),
		slog.Bool("local_changed", m.localChanged),
		slog.Bool("global_changed", m.globalChanged),
		slog.Bool("user_changed", m.userChanged),
		slog.String("project_path", m.projectPath))

	if m.tool == "gemini" {
		// Gemini: Only global scope, write to settings.json
		if m.globalChanged {
			enabledNames := make([]string, len(m.globalAttached))
			for i, item := range m.globalAttached {
				enabledNames[i] = item.Name
			}

			if err := session.WriteGeminiMCPSettings(enabledNames); err != nil {
				m.err = err
				return err
			}

			session.ClearMCPCache(m.projectPath)
		}
		return nil
	}

	// Claude: Apply LOCAL changes
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

	// Claude: Apply GLOBAL changes
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

	// Claude: Apply USER changes (affects ALL sessions!)
	if m.userChanged {
		// Get names of attached MCPs
		enabledNames := make([]string, len(m.userAttached))
		for i, item := range m.userAttached {
			enabledNames[i] = item.Name
		}

		// Write to ~/.claude.json (ROOT config)
		if err := session.WriteUserMCP(enabledNames); err != nil {
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
		// Switch scope: LOCAL -> GLOBAL -> USER -> LOCAL (Claude only)
		// Gemini only has global scope, so Tab does nothing
		if m.tool != "gemini" {
			switch m.scope {
			case MCPScopeLocal:
				m.scope = MCPScopeGlobal
			case MCPScopeGlobal:
				m.scope = MCPScopeUser
			case MCPScopeUser:
				m.scope = MCPScopeLocal
			}
		}
		m.resetTypeJump()

	case "left", "h":
		// Switch to Attached column
		m.column = MCPColumnAttached
		m.resetTypeJump()

	case "right", "l":
		// Switch to Available column
		m.column = MCPColumnAvailable
		m.resetTypeJump()

	case "up", "k":
		m.resetTypeJump()
		if len(*list) > 0 && *idx > 0 {
			*idx--
		}

	case "down", "j":
		m.resetTypeJump()
		if len(*list) > 0 && *idx < len(*list)-1 {
			*idx++
		}

	case " ":
		m.resetTypeJump()
		m.Move()

	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			m.typeJump(msg.Runes[0])
		}
	}

	return m, nil
}

// View renders the dialog
func (m *MCPDialog) View() string {
	if !m.visible {
		return ""
	}

	// Title varies by tool
	title := "MCP Manager"
	if m.tool == "gemini" {
		title = "MCP Manager (Gemini)"
	}

	// Scope tabs - Gemini only has global
	var tabs string
	if m.tool == "gemini" {
		// Gemini: Only show GLOBAL (centered)
		globalTab := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("[GLOBAL]")
		tabs = "──────────────── " + globalTab + " ────────────────"
	} else {
		// Claude: Show LOCAL/GLOBAL/USER tabs
		localTab := "LOCAL"
		globalTab := "GLOBAL"
		userTab := "USER"

		localStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
		globalStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
		userStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

		switch m.scope {
		case MCPScopeLocal:
			localStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
			localTab = "[" + localTab + "]"
			globalTab = " " + globalTab + " "
			userTab = " " + userTab + " "
		case MCPScopeGlobal:
			globalStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
			localTab = " " + localTab + " "
			globalTab = "[" + globalTab + "]"
			userTab = " " + userTab + " "
		case MCPScopeUser:
			userStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorYellow) // Yellow to indicate caution
			localTab = " " + localTab + " "
			globalTab = " " + globalTab + " "
			userTab = "[" + userTab + "]"
		}

		tabs = localStyle.Render(localTab) + " ─── " + globalStyle.Render(globalTab) + " ─── " + userStyle.Render(userTab)
	}

	// Get current scope's lists
	var attached, available []MCPItem
	var attachedIdx, availableIdx int
	switch m.scope {
	case MCPScopeLocal:
		attached = m.localAttached
		available = m.localAvailable
		attachedIdx = m.localAttachedIdx
		availableIdx = m.localAvailableIdx
	case MCPScopeGlobal:
		attached = m.globalAttached
		available = m.globalAvailable
		attachedIdx = m.globalAttachedIdx
		availableIdx = m.globalAvailableIdx
	case MCPScopeUser:
		attached = m.userAttached
		available = m.userAvailable
		attachedIdx = m.userAttachedIdx
		availableIdx = m.userAvailableIdx
	}

	// Render columns
	attachedCol := m.renderColumn("Attached", attached, attachedIdx, m.column == MCPColumnAttached)
	availableCol := m.renderColumn("Available", available, availableIdx, m.column == MCPColumnAvailable)

	columns := lipgloss.JoinHorizontal(lipgloss.Top, attachedCol, "  ", availableCol)

	// Scope description
	var scopeDesc string
	if m.tool == "gemini" {
		scopeDesc = DimStyle.Render("Writes to: ~/.gemini/settings.json")
	} else {
		switch m.scope {
		case MCPScopeLocal:
			if !session.GetManageMCPJson() {
				scopeDesc = lipgloss.NewStyle().Foreground(ColorYellow).Render("⚠ .mcp.json management disabled (manage_mcp_json = false in config.toml)")
			} else {
				scopeDesc = DimStyle.Render("Writes to: .mcp.json (this project only)")
			}
		case MCPScopeGlobal:
			scopeDesc = DimStyle.Render("Writes to: Claude config (profile-specific)")
		case MCPScopeUser:
			scopeDesc = lipgloss.NewStyle().Foreground(ColorYellow).Render("⚠ Writes to: ~/.claude.json (ALL sessions!)")
		}
	}

	// Error display
	var errText string
	if m.err != nil {
		errText = lipgloss.NewStyle().Foreground(ColorRed).Render("Error: " + m.err.Error())
	}

	// Hint with consistent styling
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	var hint string
	if m.tool == "gemini" {
		hint = hintStyle.Render("←→ column │ Type jump │ Space move │ Enter apply │ Esc cancel")
	} else {
		hint = hintStyle.Render("Tab scope │ ←→ column │ Type jump │ Space move │ Enter apply │ Esc cancel")
	}
	if m.typeJumpBuf != "" && time.Now().Before(m.typeJumpUntil) {
		hint += lipgloss.NewStyle().Foreground(ColorTextDim).Render("  (" + m.typeJumpBuf + ")")
	}

	// Legend for orphan MCPs
	orphanLegend := ""
	hasOrphans := false
	for _, item := range m.localAttached {
		if item.IsOrphan {
			hasOrphans = true
			break
		}
	}
	if !hasOrphans {
		for _, item := range m.globalAttached {
			if item.IsOrphan {
				hasOrphans = true
				break
			}
		}
	}
	if hasOrphans {
		orphanLegend = lipgloss.NewStyle().Foreground(ColorYellow).Render("⚠ = not in config.toml (add to manage)")
	}

	// Transport legend
	transportLegend := lipgloss.NewStyle().Foreground(ColorTextDim).Render(
		"[S]=stdio  [H]=http  [E]=sse  ●=running  ○=external  ✗=stopped")

	// Responsive dialog width
	dialogWidth := 64
	if m.width > 0 && m.width < dialogWidth+10 {
		dialogWidth = m.width - 10
		if dialogWidth < 50 {
			dialogWidth = 50
		}
	}
	titleWidth := dialogWidth - 4

	// Assemble dialog
	titleStyle := DialogTitleStyle.Width(titleWidth)

	// Check if we should show empty state help instead of columns
	showEmptyHelp := len(m.localAttached) == 0 && len(m.localAvailable) == 0 &&
		len(m.globalAttached) == 0 && len(m.globalAvailable) == 0 &&
		m.configError == ""

	parts := []string{
		titleStyle.Render(title),
		"",
		tabs,
		scopeDesc,
		"",
	}

	// Show config error prominently if present
	if m.configError != "" {
		errorStyle := lipgloss.NewStyle().Foreground(ColorRed)
		parts = append(parts, errorStyle.Render("⚠ "+m.configError))
		parts = append(parts, "")
	}

	// Show empty state help or columns
	if showEmptyHelp {
		parts = append(parts, m.renderEmptyStateHelp())
	} else {
		parts = append(parts, columns)
	}

	if errText != "" {
		parts = append(parts, "", errText)
	}
	if orphanLegend != "" {
		parts = append(parts, orphanLegend)
	}
	parts = append(parts, transportLegend)
	parts = append(parts, "", hint)

	dialogContent := lipgloss.JoinVertical(lipgloss.Left, parts...)

	dialog := DialogBoxStyle.Width(dialogWidth).Render(dialogContent)

	// Center the dialog
	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)
}

// renderEmptyStateHelp returns a helpful message when no MCPs are configured
func (m *MCPDialog) renderEmptyStateHelp() string {
	helpStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	highlightStyle := lipgloss.NewStyle().Foreground(ColorYellow)
	pathStyle := lipgloss.NewStyle().Foreground(ColorCyan)

	lines := []string{
		"",
		highlightStyle.Render("No MCPs configured"),
		"",
		helpStyle.Render("To add MCPs, edit:"),
		pathStyle.Render("  ~/.agent-deck/config.toml"),
		"",
		helpStyle.Render("Example:"),
		helpStyle.Render("  [mcps.example]"),
		helpStyle.Render("  command = \"npx\""),
		helpStyle.Render("  args = [\"-y\", \"@example/mcp\"]"),
		"",
		helpStyle.Render("Then press M again to see them here."),
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
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
	colWidth := 28
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
			// Build transport/status prefix
			prefix := "[S]" // Default: stdio
			if item.Transport == "http" {
				prefix = "[H]"
				// Add server status indicator for HTTP MCPs
				switch item.HTTPStatus {
				case "running":
					prefix += "●" // Green dot for running
				case "external":
					prefix += "○" // Hollow dot for external
				case "stopped":
					prefix += "✗" // X for stopped
				default:
					prefix += " "
				}
			} else if item.Transport == "sse" {
				prefix = "[E]"
			} else {
				// stdio - add pool indicator
				if item.IsPooled {
					prefix += "●" // Green dot for pooled
				} else {
					prefix += " "
				}
			}

			name := prefix + " " + item.Name

			// Add orphan indicator for MCPs not in config.toml
			if item.IsOrphan {
				name = name + " ⚠"
			}
			if len(name) > 24 {
				name = name[:21] + "..."
			}

			var line string
			if i == selectedIdx && focused {
				line = lipgloss.NewStyle().
					Background(ColorAccent).
					Foreground(ColorBg).
					Bold(true).
					Width(colWidth).
					Render(" > " + name)
			} else if item.IsOrphan {
				// Orphan MCPs shown in yellow/warning color
				line = lipgloss.NewStyle().
					Foreground(ColorYellow).
					Width(colWidth).
					Render("   " + name)
			} else if item.Transport == "http" || item.Transport == "sse" {
				// HTTP/SSE MCPs get a subtle highlight
				line = lipgloss.NewStyle().
					Foreground(ColorPurple).
					Width(colWidth).
					Render("   " + name)
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
