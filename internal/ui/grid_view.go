package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// GridMode represents the input mode (vi-style modal)
type GridMode int

const (
	GridModeNavigate GridMode = iota // arrows/hjkl/Tab move focus between cells
	GridModeInput                    // keys go to focused cell's textarea
)

const (
	gridMaxCells    = 12
	gridTickRate    = 1 * time.Second
	gridPopupRate   = 250 * time.Millisecond
	gridFetchStale  = 800 * time.Millisecond
	gridMinCellW    = 20
	gridMinCellH    = 6
	gridHeaderLines = 1
	gridInputLines  = 1
	gridSepLines    = 2 // separator above and below content

	gridResizeStep = 0.10 // 10% per keypress
	gridMinProp    = 0.15 // minimum 15% per column/row

	gridSaveDebounce = 500 * time.Millisecond
)

// --- Messages ---

// gridCellCaptureMsg delivers async CapturePane results for a specific cell
type gridCellCaptureMsg struct {
	sessionID string
	content   string
	err       error
}

// gridAttachMsg signals home.go to attach to the focused session via tea.Exec
type gridAttachMsg struct {
	instance *session.Instance
}

// gridTickMsg triggers periodic refresh of all visible cells
type gridTickMsg time.Time

// gridPopupTickMsg triggers faster refresh for the popup overlay
type gridPopupTickMsg time.Time

// gridPopupCaptureMsg delivers CapturePane results for the popup
type gridPopupCaptureMsg struct {
	content string
	err     error
}

// gridSendOutputMsg is returned after async inter-session send completes
type gridSendOutputMsg struct {
	sourceTitle string
	targetTitle string
	lineCount   int
	err         error
}

// --- Types ---

// GridCell represents one session cell in the grid
type GridCell struct {
	instance  *session.Instance
	tmuxSess  *tmux.Session
	content   string
	input     textarea.Model
	lastFetch time.Time
}

// GridPopup is the focused-cell overlay
type GridPopup struct {
	cellIndex int
	instance  *session.Instance
	tmuxSess  *tmux.Session
	content   string
	input     textarea.Model
	lastFetch time.Time
}

// GridView is the fullscreen overlay showing group sessions as a grid
type GridView struct {
	visible    bool
	cells      []GridCell
	groupName  string
	groupPath  string
	totalCount int // total sessions in group (before truncation)
	focusIndex int
	mode       GridMode
	width      int
	height     int

	// Proportional column/row sizes (nil = equal)
	colWidths  []float64
	rowHeights []float64

	// Popup state
	popup *GridPopup

	// Debounced save
	saveTimer *time.Timer

	// standaloneMode is true when running in tmux popup (affects status bar text)
	standaloneMode bool

	// sourceSession is the session the popup was launched from (for send output)
	sourceSession *session.Instance

	// statusMsg is a temporary status message shown in the status bar
	statusMsg     string
	statusMsgTime time.Time
}

// NewGridView creates a new hidden GridView.
func NewGridView() *GridView {
	return &GridView{}
}

// SetStandaloneMode sets whether the grid runs in standalone popup mode.
// In standalone mode, "attach" becomes "switch" and "back" becomes "close".
func (g *GridView) SetStandaloneMode(standalone bool) {
	g.standaloneMode = standalone
}

// SetSourceSession sets the session the popup was launched from (for send output).
func (g *GridView) SetSourceSession(inst *session.Instance) {
	g.sourceSession = inst
}

// IsVisible returns whether the grid view is currently showing.
func (g *GridView) IsVisible() bool {
	return g.visible
}

// Hide closes the grid view and blurs all textareas.
func (g *GridView) Hide() {
	g.visible = false
	for i := range g.cells {
		g.cells[i].input.Blur()
	}
	g.mode = GridModeNavigate
	g.closePopup()
	g.flushSave()
}

// SetSize updates grid dimensions and recalculates textarea widths.
func (g *GridView) SetSize(w, h int) {
	g.width = w
	g.height = h
	g.updateTextareaWidths()
}

// updateTextareaWidths recalculates textarea widths based on current cell sizes.
func (g *GridView) updateTextareaWidths() {
	if len(g.cells) == 0 {
		return
	}
	cols, _ := gridLayout(len(g.cells))
	for i := range g.cells {
		col := i % cols
		cellW := g.colWidthPx(col, cols)
		inputW := cellW - 4
		if inputW < 10 {
			inputW = 10
		}
		g.cells[i].input.SetWidth(inputW)
	}
}

// Show populates the grid with sessions from the given group.
func (g *GridView) Show(group *session.Group) {
	sessions := group.Sessions
	g.totalCount = len(sessions)
	if len(sessions) > gridMaxCells {
		sessions = sessions[:gridMaxCells]
	}

	g.cells = make([]GridCell, len(sessions))
	for i, inst := range sessions {
		ta := textarea.New()
		ta.ShowLineNumbers = false
		ta.Placeholder = "Type message..."
		ta.Prompt = "> "
		ta.SetHeight(1)
		ta.CharLimit = 4096
		ta.Blur()

		g.cells[i] = GridCell{
			instance: inst,
			tmuxSess: inst.GetTmuxSession(),
			input:    ta,
		}
	}

	g.groupName = group.Name
	g.groupPath = group.Path
	g.focusIndex = 0
	g.mode = GridModeNavigate
	g.visible = true
	g.popup = nil
	g.colWidths = nil
	g.rowHeights = nil

	// Load saved preferences
	g.loadPreferences()

	// Set textarea widths after preferences are loaded
	g.updateTextareaWidths()
}

// --- Proportional Layout ---

// gridLayout returns (cols, rows) for n cells.
func gridLayout(n int) (int, int) {
	if n <= 0 {
		return 1, 1
	}
	var cols int
	switch {
	case n == 1:
		cols = 1
	case n == 2:
		cols = 2
	default:
		cols = 3
		if n <= 4 {
			cols = 2
		}
	}
	rows := (n + cols - 1) / cols
	return cols, rows
}

// availableWidth returns total width for cell content (minus separators).
func (g *GridView) availableWidth(cols int) int {
	return g.width - (cols - 1) // subtract column separators
}

// availableHeight returns total height for cell content (minus header, row separators, and status bar).
func (g *GridView) availableHeight(rows int) int {
	return g.height - 2 - (rows - 1) // -1 header bar, -1 status bar, -(rows-1) row separators
}

// colWidthPx returns the pixel width for a specific column.
func (g *GridView) colWidthPx(col, cols int) int {
	avail := g.availableWidth(cols)
	if g.colWidths == nil || len(g.colWidths) != cols {
		// Equal distribution
		w := avail / cols
		if col == cols-1 {
			w = avail - (avail/cols)*(cols-1) // last col absorbs remainder
		}
		if w < gridMinCellW {
			w = gridMinCellW
		}
		return w
	}
	w := int(g.colWidths[col] * float64(avail))
	if col == cols-1 {
		// Last column absorbs rounding error
		used := 0
		for c := 0; c < cols-1; c++ {
			used += int(g.colWidths[c] * float64(avail))
		}
		w = avail - used
	}
	if w < gridMinCellW {
		w = gridMinCellW
	}
	return w
}

// rowHeightPx returns the pixel height for a specific row.
func (g *GridView) rowHeightPx(row, rows int) int {
	avail := g.availableHeight(rows)
	if g.rowHeights == nil || len(g.rowHeights) != rows {
		h := avail / rows
		if row == rows-1 {
			h = avail - (avail/rows)*(rows-1)
		}
		if h < gridMinCellH {
			h = gridMinCellH
		}
		return h
	}
	h := int(g.rowHeights[row] * float64(avail))
	if row == rows-1 {
		used := 0
		for r := 0; r < rows-1; r++ {
			used += int(g.rowHeights[r] * float64(avail))
		}
		h = avail - used
	}
	if h < gridMinCellH {
		h = gridMinCellH
	}
	return h
}

// cellSize returns uniform width and height (backward compat for terminal-too-small check).
func (g *GridView) cellSize() (int, int) {
	n := len(g.cells)
	if n == 0 {
		n = 1
	}
	cols, rows := gridLayout(n)
	// Return smallest cell dimensions to check minimum
	minW := g.width
	for c := 0; c < cols; c++ {
		if w := g.colWidthPx(c, cols); w < minW {
			minW = w
		}
	}
	minH := g.height
	for r := 0; r < rows; r++ {
		if h := g.rowHeightPx(r, rows); h < minH {
			minH = h
		}
	}
	return minW, minH
}

// ensureProportions initializes colWidths/rowHeights from equal if nil.
func (g *GridView) ensureProportions() {
	cols, rows := gridLayout(len(g.cells))
	if g.colWidths == nil || len(g.colWidths) != cols {
		g.colWidths = make([]float64, cols)
		for i := range g.colWidths {
			g.colWidths[i] = 1.0 / float64(cols)
		}
	}
	if g.rowHeights == nil || len(g.rowHeights) != rows {
		g.rowHeights = make([]float64, rows)
		for i := range g.rowHeights {
			g.rowHeights[i] = 1.0 / float64(rows)
		}
	}
}

// resizeColumn adjusts column proportions by delta for the focused column.
func (g *GridView) resizeColumn(delta float64) {
	cols, _ := gridLayout(len(g.cells))
	if cols <= 1 {
		return // nothing to redistribute
	}
	g.ensureProportions()

	focusCol := g.focusIndex % cols

	// Find the target column to take/give from
	targetCol := -1
	if delta > 0 {
		// Expanding: shrink the widest OTHER column
		for c := 0; c < cols; c++ {
			if c != focusCol && (targetCol == -1 || g.colWidths[c] > g.colWidths[targetCol]) {
				targetCol = c
			}
		}
	} else {
		// Shrinking: expand the narrowest OTHER column
		for c := 0; c < cols; c++ {
			if c != focusCol && (targetCol == -1 || g.colWidths[c] < g.colWidths[targetCol]) {
				targetCol = c
			}
		}
	}
	if targetCol == -1 {
		return
	}

	g.colWidths[focusCol] += delta
	g.colWidths[targetCol] -= delta
	clampAndNormalize(g.colWidths)
	g.updateTextareaWidths()
	g.scheduleSave()
}

// resizeRow adjusts row proportions by delta for the focused row.
func (g *GridView) resizeRow(delta float64) {
	_, rows := gridLayout(len(g.cells))
	if rows <= 1 {
		return
	}
	cols, _ := gridLayout(len(g.cells))
	g.ensureProportions()

	focusRow := g.focusIndex / cols

	targetRow := -1
	if delta > 0 {
		for r := 0; r < rows; r++ {
			if r != focusRow && (targetRow == -1 || g.rowHeights[r] > g.rowHeights[targetRow]) {
				targetRow = r
			}
		}
	} else {
		for r := 0; r < rows; r++ {
			if r != focusRow && (targetRow == -1 || g.rowHeights[r] < g.rowHeights[targetRow]) {
				targetRow = r
			}
		}
	}
	if targetRow == -1 {
		return
	}

	g.rowHeights[focusRow] += delta
	g.rowHeights[targetRow] -= delta
	clampAndNormalize(g.rowHeights)
	g.scheduleSave()
}

// clampAndNormalize clamps all values to gridMinProp minimum, then normalizes to sum 1.0.
// Iterates to ensure clamping doesn't push other values below minimum after normalization.
func clampAndNormalize(props []float64) {
	for range 3 { // max 3 iterations is sufficient for convergence
		for i := range props {
			if props[i] < gridMinProp {
				props[i] = gridMinProp
			}
		}
		normalizeProportions(props)

		// Check if all are above minimum
		ok := true
		for _, p := range props {
			if p < gridMinProp-0.001 {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
}

// normalizeProportions ensures proportions sum to 1.0.
func normalizeProportions(props []float64) {
	sum := 0.0
	for _, p := range props {
		sum += p
	}
	if sum <= 0 {
		for i := range props {
			props[i] = 1.0 / float64(len(props))
		}
		return
	}
	for i := range props {
		props[i] /= sum
	}
}

// --- Persistence ---

func (g *GridView) loadPreferences() {
	db := statedb.GetGlobal()
	if db == nil || g.groupPath == "" {
		return
	}
	cols, rows := gridLayout(len(g.cells))
	savedCols, savedRows, err := db.LoadGridPreferences(g.groupPath)
	if err != nil || savedCols == nil {
		return
	}
	if len(savedCols) == cols && len(savedRows) == rows {
		g.colWidths = savedCols
		g.rowHeights = savedRows
	} else {
		// Stale: delete
		_ = db.DeleteGridPreferences(g.groupPath)
	}
}

func (g *GridView) scheduleSave() {
	if g.saveTimer != nil {
		g.saveTimer.Stop()
	}
	groupPath := g.groupPath
	colWidths := make([]float64, len(g.colWidths))
	copy(colWidths, g.colWidths)
	rowHeights := make([]float64, len(g.rowHeights))
	copy(rowHeights, g.rowHeights)

	g.saveTimer = time.AfterFunc(gridSaveDebounce, func() {
		if db := statedb.GetGlobal(); db != nil && groupPath != "" {
			_ = db.SaveGridPreferences(groupPath, colWidths, rowHeights)
		}
	})
}

func (g *GridView) flushSave() {
	if g.saveTimer != nil {
		g.saveTimer.Stop()
		g.saveTimer = nil
	}
	// Save immediately if we have custom proportions
	if g.colWidths != nil && g.groupPath != "" {
		if db := statedb.GetGlobal(); db != nil {
			_ = db.SaveGridPreferences(g.groupPath, g.colWidths, g.rowHeights)
		}
	}
}

func (g *GridView) resetProportions() {
	g.colWidths = nil
	g.rowHeights = nil
	g.updateTextareaWidths()
	if g.saveTimer != nil {
		g.saveTimer.Stop()
		g.saveTimer = nil
	}
	if db := statedb.GetGlobal(); db != nil && g.groupPath != "" {
		_ = db.DeleteGridPreferences(g.groupPath)
	}
}

// --- Popup ---

func (g *GridView) openPopup() tea.Cmd {
	if g.focusIndex >= len(g.cells) {
		return nil
	}
	cell := g.cells[g.focusIndex]
	if cell.instance == nil {
		return nil
	}

	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Placeholder = "Type message..."
	ta.Prompt = "> "
	ta.SetHeight(1)
	popupW := g.width * 80 / 100
	inputW := popupW - 8
	if inputW < 20 {
		inputW = 20
	}
	ta.SetWidth(inputW)
	ta.CharLimit = 4096
	ta.Focus()

	g.popup = &GridPopup{
		cellIndex: g.focusIndex,
		instance:  cell.instance,
		tmuxSess:  cell.tmuxSess,
		content:   cell.content, // seed with existing content
		input:     ta,
	}

	return tea.Batch(g.fetchPopup(), g.startPopupTick())
}

func (g *GridView) closePopup() {
	if g.popup != nil {
		g.popup.input.Blur()
		g.popup = nil
	}
}

func (g *GridView) fetchPopup() tea.Cmd {
	if g.popup == nil || g.popup.tmuxSess == nil {
		return nil
	}
	tmuxSess := g.popup.tmuxSess
	return func() tea.Msg {
		content, err := tmuxSess.CapturePane()
		return gridPopupCaptureMsg{content: content, err: err}
	}
}

func (g *GridView) startPopupTick() tea.Cmd {
	return tea.Tick(gridPopupRate, func(t time.Time) tea.Msg {
		return gridPopupTickMsg(t)
	})
}

// --- View ---

// View renders the grid (and popup overlay if open).
func (g *GridView) View() string {
	if g.width == 0 || g.height == 0 {
		return "Loading..."
	}

	minW, minH := g.cellSize()
	if minH < gridMinCellH || minW < gridMinCellW {
		msg := fmt.Sprintf("Terminal too small for grid view\nNeed at least %dx%d per cell", gridMinCellW, gridMinCellH)
		return lipgloss.Place(g.width, g.height, lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Foreground(ColorYellow).Render(msg))
	}

	// Header bar with group name
	groupIcon := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("▸ ")
	groupLabel := lipgloss.NewStyle().Bold(true).Foreground(ColorCyan).Render(g.groupName)
	countLabel := DimStyle.Render(fmt.Sprintf(" (%d sessions)", len(g.cells)))
	headerBar := lipgloss.NewStyle().
		Background(ColorSurface).
		Width(g.width).
		MaxWidth(g.width).
		Padding(0, 1).
		Render(groupIcon + groupLabel + countLabel)

	n := len(g.cells)
	cols, rows := gridLayout(n)

	var allRows []string
	cellIdx := 0
	for row := 0; row < rows; row++ {
		cellH := g.rowHeightPx(row, rows)
		var rowCells []string
		for col := 0; col < cols; col++ {
			cellW := g.colWidthPx(col, cols)
			if cellIdx < n {
				rendered := g.renderCell(cellIdx, cellW, cellH)
				rowCells = append(rowCells, rendered)
				cellIdx++
			} else {
				empty := ensureExactHeight(ensureExactWidth("", cellW), cellH)
				rowCells = append(rowCells, empty)
			}
		}
		// Join cells horizontally with thin separator
		sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
		sepLines := make([]string, cellH)
		for i := range sepLines {
			sepLines[i] = sepStyle.Render("│")
		}
		sep := strings.Join(sepLines, "\n")

		rowContent := rowCells[0]
		for i := 1; i < len(rowCells); i++ {
			rowContent = lipgloss.JoinHorizontal(lipgloss.Top, rowContent, sep, rowCells[i])
		}
		allRows = append(allRows, rowContent)
	}

	// Join rows vertically with horizontal separator
	rowSepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	rowSep := rowSepStyle.Render(strings.Repeat("─", g.width))

	var mainContent string
	for i, row := range allRows {
		if i > 0 {
			mainContent += rowSep + "\n"
		}
		mainContent += row + "\n"
	}

	// Popup overlay: render popup on top of dimmed grid
	if g.popup != nil {
		statusBar := g.renderPopupStatusBar()

		// Dim the grid: replace each line's content with dimmed version
		contentH := g.height - 2 // minus header bar and status bar
		gridLines := strings.Split(mainContent, "\n")
		// Ensure we have exactly contentH lines for the background
		for len(gridLines) < contentH {
			gridLines = append(gridLines, "")
		}
		if len(gridLines) > contentH {
			gridLines = gridLines[:contentH]
		}
		// Dim all grid lines
		dimStyle := lipgloss.NewStyle().Foreground(ColorBorder)
		for i, line := range gridLines {
			clean := ansi.Strip(line)
			gridLines[i] = dimStyle.Render(clean)
		}

		// Render popup box
		popupView := g.renderPopup()
		popupLines := strings.Split(popupView, "\n")

		// Calculate vertical offset to center popup
		popupH := len(popupLines)
		startRow := (contentH - popupH) / 2
		if startRow < 0 {
			startRow = 0
		}

		// Calculate horizontal offset to center popup
		popupW := g.width * 80 / 100
		startCol := (g.width - popupW) / 2
		if startCol < 0 {
			startCol = 0
		}
		padding := strings.Repeat(" ", startCol)

		// Overlay popup lines on top of dimmed grid
		for i, popupLine := range popupLines {
			row := startRow + i
			if row >= 0 && row < len(gridLines) {
				gridLines[row] = padding + popupLine
			}
		}

		mainContent = headerBar + "\n" + strings.Join(gridLines, "\n") + "\n" + statusBar
		return clampViewToViewport(mainContent, g.width, g.height)
	}

	statusBar := g.renderStatusBar()
	result := headerBar + "\n" + mainContent + statusBar
	return clampViewToViewport(result, g.width, g.height)
}

// renderPopup renders the popup overlay box.
// Uses manual border drawing (not lipgloss.Border) for precise height control.
func (g *GridView) renderPopup() string {
	p := g.popup
	if p == nil {
		return ""
	}

	popupW := g.width * 80 / 100
	popupH := g.height * 70 / 100
	innerW := popupW - 2 // left + right border chars
	contentW := innerW - 2 // 1 char padding each side

	borderStyle := lipgloss.NewStyle().Foreground(ColorGreen)
	sepStyle := lipgloss.NewStyle().Foreground(ColorGreen)

	// Header
	status := p.instance.GetStatusThreadSafe()
	tool := p.instance.GetToolThreadSafe()
	statusIcon := StatusIndicator(string(status))
	toolStyle := GetToolStyle(tool)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

	header := fmt.Sprintf(" %s %s %s",
		statusIcon,
		titleStyle.Render(p.instance.Title),
		toolStyle.Render(tool),
	)

	// Fixed lines: top border + header + sep + sep + input + bottom border = 6
	contentHeight := popupH - 6
	if contentHeight < 1 {
		contentHeight = 1
	}

	// Build content lines
	var contentLines []string
	if p.content == "" {
		dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)
		contentLines = []string{" " + dimStyle.Render("Loading...")}
	} else {
		lines := strings.Split(p.content, "\n")
		if len(lines) > contentHeight {
			lines = lines[len(lines)-contentHeight:]
		}
		for _, line := range lines {
			cleanLine := ansi.Strip(line)
			displayWidth := runewidth.StringWidth(cleanLine)
			if displayWidth > contentW {
				line = runewidth.Truncate(cleanLine, contentW-1, "~")
			}
			contentLines = append(contentLines, " "+line)
		}
	}
	// Pad to exact height
	for len(contentLines) < contentHeight {
		contentLines = append(contentLines, "")
	}
	if len(contentLines) > contentHeight {
		contentLines = contentLines[len(contentLines)-contentHeight:]
	}

	// Input line
	prompt := lipgloss.NewStyle().
		Foreground(ColorGreen).
		Bold(true).
		Render(" ❯ ")
	inputLine := prompt + p.input.View()

	// Assemble with manual borders for exact height
	topBorder := borderStyle.Render("╭" + strings.Repeat("─", innerW) + "╮")
	bottomBorder := borderStyle.Render("╰" + strings.Repeat("─", innerW) + "╯")
	sepLine := borderStyle.Render("│") + sepStyle.Render(strings.Repeat("─", innerW)) + borderStyle.Render("│")

	var lines []string
	lines = append(lines, topBorder)
	lines = append(lines, borderStyle.Render("│")+ensureExactWidth(header, innerW)+borderStyle.Render("│"))
	lines = append(lines, sepLine)
	for _, cl := range contentLines {
		lines = append(lines, borderStyle.Render("│")+ensureExactWidth(cl, innerW)+borderStyle.Render("│"))
	}
	lines = append(lines, sepLine)
	lines = append(lines, borderStyle.Render("│")+ensureExactWidth(inputLine, innerW)+borderStyle.Render("│"))
	lines = append(lines, bottomBorder)

	return strings.Join(lines, "\n")
}

// renderStatusBar renders the bottom status bar for the grid.
func (g *GridView) renderStatusBar() string {
	modeBadgeStyle := lipgloss.NewStyle().Bold(true)
	sep := MenuSeparatorStyle.Render(" • ")

	var parts []string

	if g.mode == GridModeInput {
		modeBadgeStyle = modeBadgeStyle.Foreground(ColorBg).Background(ColorGreen)
		parts = append(parts, modeBadgeStyle.Render(" INPUT "))

		if g.focusIndex < len(g.cells) {
			target := g.cells[g.focusIndex].instance.Title
			parts = append(parts, MenuDescStyle.Render("typing → "+target))
		}
		parts = append(parts, sep)
		parts = append(parts, MenuKeyStyle.Render("Enter")+MenuDescStyle.Render(" send"))
		parts = append(parts, sep)
		parts = append(parts, MenuKeyStyle.Render("Ctrl+C")+MenuDescStyle.Render(" interrupt"))
		parts = append(parts, sep)
		parts = append(parts, MenuKeyStyle.Render("Esc")+MenuDescStyle.Render(" navigate"))
	} else {
		modeBadgeStyle = modeBadgeStyle.Foreground(ColorBg).Background(ColorCyan)
		parts = append(parts, modeBadgeStyle.Render(" NAVIGATE "))
		parts = append(parts, MenuKeyStyle.Render("Tab/hjkl")+MenuDescStyle.Render(" focus"))
		parts = append(parts, sep)
		if g.standaloneMode {
			parts = append(parts, MenuKeyStyle.Render("Enter")+MenuDescStyle.Render(" switch"))
		} else {
			parts = append(parts, MenuKeyStyle.Render("Enter")+MenuDescStyle.Render(" attach"))
		}
		parts = append(parts, sep)
		parts = append(parts, MenuKeyStyle.Render("i")+MenuDescStyle.Render(" input"))
		parts = append(parts, sep)
		parts = append(parts, MenuKeyStyle.Render("f")+MenuDescStyle.Render(" popup"))
		parts = append(parts, sep)
		parts = append(parts, MenuKeyStyle.Render("+/-")+MenuDescStyle.Render(" resize"))
		parts = append(parts, sep)
		parts = append(parts, MenuKeyStyle.Render("=")+MenuDescStyle.Render(" reset"))
		if g.sourceSession != nil {
			parts = append(parts, sep)
			parts = append(parts, MenuKeyStyle.Render("x")+MenuDescStyle.Render(" send output"))
		}
		parts = append(parts, sep)
		if g.standaloneMode {
			parts = append(parts, MenuKeyStyle.Render("Esc/q")+MenuDescStyle.Render(" close"))
		} else {
			parts = append(parts, MenuKeyStyle.Render("Esc/q")+MenuDescStyle.Render(" back"))
		}
	}

	if g.totalCount > gridMaxCells {
		parts = append(parts, sep)
		parts = append(parts, DimStyle.Render(fmt.Sprintf("Showing %d of %d", gridMaxCells, g.totalCount)))
	}

	// Show temporary status message (e.g., send confirmation)
	if g.statusMsg != "" && time.Since(g.statusMsgTime) < 5*time.Second {
		parts = append(parts, sep)
		if strings.HasPrefix(g.statusMsg, "Send failed") {
			parts = append(parts, ErrorStyle.Render(g.statusMsg))
		} else {
			parts = append(parts, SuccessStyle.Render(g.statusMsg))
		}
	}

	bar := strings.Join(parts, "")

	return lipgloss.NewStyle().
		Background(ColorSurface).
		Width(g.width).
		MaxWidth(g.width).
		Padding(0, 1).
		Render(bar)
}

// renderPopupStatusBar renders the status bar when popup is open.
func (g *GridView) renderPopupStatusBar() string {
	modeBadgeStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorGreen)
	sep := MenuSeparatorStyle.Render(" • ")

	var parts []string
	parts = append(parts, modeBadgeStyle.Render(" POPUP "))

	if g.popup != nil {
		parts = append(parts, MenuDescStyle.Render(g.popup.instance.Title))
		parts = append(parts, sep)
	}
	parts = append(parts, MenuKeyStyle.Render("Enter")+MenuDescStyle.Render(" send"))
	parts = append(parts, sep)
	parts = append(parts, MenuKeyStyle.Render("Ctrl+C")+MenuDescStyle.Render(" interrupt"))
	parts = append(parts, sep)
	parts = append(parts, MenuKeyStyle.Render("Esc")+MenuDescStyle.Render(" close"))
	parts = append(parts, sep)
	parts = append(parts, MenuKeyStyle.Render("Ctrl+Q")+MenuDescStyle.Render(" exit grid"))

	bar := strings.Join(parts, "")
	return lipgloss.NewStyle().
		Background(ColorSurface).
		Width(g.width).
		MaxWidth(g.width).
		Padding(0, 1).
		Render(bar)
}

// renderCell renders a single grid cell.
func (g *GridView) renderCell(idx, cellW, cellH int) string {
	cell := g.cells[idx]
	isFocused := idx == g.focusIndex
	contentW := cellW - 2 // 1 char padding each side

	// Determine border color
	borderColor := ColorBorder
	if isFocused {
		if g.mode == GridModeInput {
			borderColor = ColorGreen
		} else {
			borderColor = ColorCyan
		}
	}
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)

	// --- Header line ---
	status := cell.instance.GetStatusThreadSafe()
	tool := cell.instance.GetToolThreadSafe()
	statusIcon := StatusIndicator(string(status))
	toolStyle := GetToolStyle(tool)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	if isFocused {
		titleStyle = titleStyle.Foreground(ColorAccent)
	}

	title := cell.instance.Title
	maxTitleW := contentW - 10
	if maxTitleW < 5 {
		maxTitleW = 5
	}
	if runewidth.StringWidth(title) > maxTitleW {
		title = runewidth.Truncate(title, maxTitleW-3, "...")
	}

	header := fmt.Sprintf(" %s %s %s",
		statusIcon,
		titleStyle.Render(title),
		toolStyle.Render(tool),
	)

	// --- Content area ---
	contentHeight := cellH - gridHeaderLines - gridSepLines - gridInputLines
	if contentHeight < 1 {
		contentHeight = 1
	}

	var contentLines []string
	if cell.content == "" {
		dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)
		contentLines = []string{" " + dimStyle.Render("Loading...")}
	} else {
		lines := strings.Split(cell.content, "\n")
		if len(lines) > contentHeight {
			lines = lines[len(lines)-contentHeight:]
		}
		for _, line := range lines {
			cleanLine := ansi.Strip(line)
			displayWidth := runewidth.StringWidth(cleanLine)
			if displayWidth > contentW {
				line = runewidth.Truncate(cleanLine, contentW-1, "~")
			}
			contentLines = append(contentLines, " "+line)
		}
	}
	content := ensureExactHeight(strings.Join(contentLines, "\n"), contentHeight)

	// --- Input area ---
	var inputBlock string
	if isFocused && g.mode == GridModeInput {
		prompt := lipgloss.NewStyle().
			Foreground(ColorGreen).
			Bold(true).
			Render(" ❯ ")
		inputView := cell.input.View()
		inputBlock = prompt + inputView
	} else if isFocused {
		inputLabel := lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true).
			Render(" ❯ ")
		hint := lipgloss.NewStyle().
			Foreground(ColorComment).
			Italic(true).
			Render("press i to type")
		inputBlock = inputLabel + hint
	} else {
		inputBlock = " " + DimStyle.Render("─── ❯ ───")
	}

	// --- Separator lines ---
	sepLine := borderStyle.Render(strings.Repeat("─", cellW))

	// --- Assemble cell ---
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(sepLine)
	b.WriteString("\n")
	b.WriteString(content)
	b.WriteString("\n")
	b.WriteString(sepLine)
	b.WriteString("\n")
	b.WriteString(inputBlock)

	result := b.String()
	result = ensureExactHeight(result, cellH)
	result = ensureExactWidth(result, cellW)

	return result
}

// --- Update ---

// Update handles messages for the grid view.
func (g *GridView) Update(msg tea.Msg) (*GridView, tea.Cmd) {
	switch msg := msg.(type) {
	case gridCellCaptureMsg:
		for i := range g.cells {
			if g.cells[i].instance.ID == msg.sessionID {
				if msg.err == nil {
					g.cells[i].content = msg.content
				} else {
					g.cells[i].content = "Session unavailable"
				}
				g.cells[i].lastFetch = time.Now()
				break
			}
		}
		return g, nil

	case gridTickMsg:
		var cmds []tea.Cmd
		for i := range g.cells {
			if time.Since(g.cells[i].lastFetch) > gridFetchStale {
				cmds = append(cmds, g.fetchCell(i))
			}
		}
		cmds = append(cmds, g.startTick())
		return g, tea.Batch(cmds...)

	case gridPopupCaptureMsg:
		if g.popup != nil {
			if msg.err == nil {
				g.popup.content = msg.content
			}
			g.popup.lastFetch = time.Now()
		}
		return g, nil

	case gridSendOutputMsg:
		if msg.err != nil {
			g.statusMsg = fmt.Sprintf("Send failed: %v", msg.err)
		} else {
			g.statusMsg = fmt.Sprintf("Sent %d lines from %s → %s", msg.lineCount, msg.sourceTitle, msg.targetTitle)
		}
		g.statusMsgTime = time.Now()
		// Refresh the target cell to show the received content
		return g, g.fetchCell(g.focusIndex)

	case gridPopupTickMsg:
		if g.popup != nil {
			var cmds []tea.Cmd
			if time.Since(g.popup.lastFetch) > gridPopupRate/2 {
				cmds = append(cmds, g.fetchPopup())
			}
			cmds = append(cmds, g.startPopupTick())
			return g, tea.Batch(cmds...)
		}
		return g, nil

	case tea.KeyMsg:
		// Popup captures all keys when open
		if g.popup != nil {
			return g.handlePopupKey(msg)
		}
		if g.mode == GridModeNavigate {
			return g.handleNavigateKey(msg)
		}
		return g.handleInputKey(msg)
	}

	return g, nil
}

// handleNavigateKey processes keys in NAVIGATE mode.
func (g *GridView) handleNavigateKey(msg tea.KeyMsg) (*GridView, tea.Cmd) {
	n := len(g.cells)
	if n == 0 {
		return g, nil
	}

	cols, _ := gridLayout(n)

	switch msg.String() {
	case "tab", "l", "right":
		g.focusIndex = (g.focusIndex + 1) % n
	case "shift+tab", "h", "left":
		g.focusIndex = (g.focusIndex - 1 + n) % n
	case "j", "down":
		next := g.focusIndex + cols
		if next < n {
			g.focusIndex = next
		}
	case "k", "up":
		next := g.focusIndex - cols
		if next >= 0 {
			g.focusIndex = next
		}
	case "enter":
		cell := g.cells[g.focusIndex]
		if cell.instance != nil {
			return g, func() tea.Msg {
				return gridAttachMsg{instance: cell.instance}
			}
		}
		return g, nil
	case "i":
		g.mode = GridModeInput
		g.cells[g.focusIndex].input.Focus()
		return g, nil
	case "f":
		return g, g.openPopup()
	case "+", "=+": // + key (shift+= on US keyboards)
		g.resizeColumn(gridResizeStep)
		return g, nil
	case "-":
		g.resizeColumn(-gridResizeStep)
		return g, nil
	case "=":
		g.resetProportions()
		return g, nil
	case "ctrl+up":
		g.resizeRow(gridResizeStep)
		return g, nil
	case "ctrl+down":
		g.resizeRow(-gridResizeStep)
		return g, nil
	case "x":
		// Send source session's output to focused cell
		if g.sourceSession != nil && g.focusIndex < len(g.cells) {
			return g, g.sendOutputToCell(g.focusIndex)
		}
		return g, nil
	case "esc", "q", "ctrl+q":
		g.Hide()
		return g, nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.String()[0]-'0') - 1
		if idx < n {
			g.focusIndex = idx
		}
	}

	return g, nil
}

// handleInputKey processes keys in INPUT mode.
func (g *GridView) handleInputKey(msg tea.KeyMsg) (*GridView, tea.Cmd) {
	if g.focusIndex >= len(g.cells) {
		return g, nil
	}
	cell := &g.cells[g.focusIndex]

	switch msg.Type {
	case tea.KeyEsc:
		cell.input.Blur()
		g.mode = GridModeNavigate
		return g, nil

	case tea.KeyEnter:
		text := strings.TrimSpace(cell.input.Value())
		if text != "" && cell.tmuxSess != nil {
			_ = cell.tmuxSess.SendKeysAndEnter(text)
			cell.input.SetValue("")
			return g, g.fetchCell(g.focusIndex)
		}
		return g, nil

	case tea.KeyCtrlC:
		if cell.tmuxSess != nil {
			_ = cell.tmuxSess.SendCtrlC()
			return g, g.fetchCell(g.focusIndex)
		}
		return g, nil
	}

	var cmd tea.Cmd
	cell.input, cmd = cell.input.Update(msg)
	return g, cmd
}

// handlePopupKey processes keys when popup is open.
func (g *GridView) handlePopupKey(msg tea.KeyMsg) (*GridView, tea.Cmd) {
	if g.popup == nil {
		return g, nil
	}

	switch msg.Type {
	case tea.KeyEsc:
		g.closePopup()
		return g, nil

	case tea.KeyCtrlQ:
		g.closePopup()
		g.Hide()
		return g, nil

	case tea.KeyEnter:
		text := strings.TrimSpace(g.popup.input.Value())
		if text != "" && g.popup.tmuxSess != nil {
			_ = g.popup.tmuxSess.SendKeysAndEnter(text)
			g.popup.input.SetValue("")
			return g, g.fetchPopup()
		}
		return g, nil

	case tea.KeyCtrlC:
		if g.popup.tmuxSess != nil {
			_ = g.popup.tmuxSess.SendCtrlC()
			return g, g.fetchPopup()
		}
		return g, nil
	}

	// Forward to popup textarea
	var cmd tea.Cmd
	g.popup.input, cmd = g.popup.input.Update(msg)
	return g, cmd
}

// --- Fetch helpers ---

// fetchAllCells returns a command that fetches CapturePane for all cells.
func (g *GridView) fetchAllCells() tea.Cmd {
	cmds := make([]tea.Cmd, len(g.cells))
	for i := range g.cells {
		cmds[i] = g.fetchCell(i)
	}
	return tea.Batch(cmds...)
}

// fetchCell returns an async command to capture pane content for a single cell.
func (g *GridView) fetchCell(idx int) tea.Cmd {
	if idx >= len(g.cells) {
		return nil
	}
	cell := g.cells[idx]
	if cell.tmuxSess == nil {
		return nil
	}
	sessionID := cell.instance.ID
	tmuxSess := cell.tmuxSess
	return func() tea.Msg {
		content, err := tmuxSess.CapturePane()
		return gridCellCaptureMsg{
			sessionID: sessionID,
			content:   content,
			err:       err,
		}
	}
}

// startTick returns a command that emits gridTickMsg after the tick interval.
func (g *GridView) startTick() tea.Cmd {
	return tea.Tick(gridTickRate, func(t time.Time) tea.Msg {
		return gridTickMsg(t)
	})
}

// --- Send output ---

const gridMaxTransferSize = 500 * 1024 // 500KB max

// sendOutputToCell sends the source session's output to the target cell's tmux session.
func (g *GridView) sendOutputToCell(cellIdx int) tea.Cmd {
	if g.sourceSession == nil || cellIdx >= len(g.cells) {
		return nil
	}
	cell := g.cells[cellIdx]
	if cell.tmuxSess == nil {
		return nil
	}
	source := g.sourceSession
	target := cell.instance
	targetTmux := cell.tmuxSess
	return func() tea.Msg {
		// Get source content (try AI response first, fallback to CapturePane)
		content, err := getSessionContent(source)
		if err != nil {
			return gridSendOutputMsg{err: err}
		}
		if len(content) > gridMaxTransferSize {
			content = content[:gridMaxTransferSize] + "\n[Truncated at 500KB]"
		}
		wrapped := fmt.Sprintf("--- Output from [%s] ---\n%s\n--- End output from [%s] ---\n",
			source.Title, content, source.Title)
		if err := targetTmux.SendKeysChunked(wrapped); err != nil {
			return gridSendOutputMsg{
				targetTitle: target.Title,
				err:         fmt.Errorf("send failed: %w", err),
			}
		}
		lineCount := strings.Count(content, "\n")
		return gridSendOutputMsg{
			sourceTitle: source.Title,
			targetTitle: target.Title,
			lineCount:   lineCount,
		}
	}
}

// --- JSON helpers for persistence ---

// MarshalGridProportions encodes proportions to JSON string.
func MarshalGridProportions(props []float64) string {
	if props == nil {
		return ""
	}
	data, err := json.Marshal(props)
	if err != nil {
		return ""
	}
	return string(data)
}

// UnmarshalGridProportions decodes proportions from JSON string.
func UnmarshalGridProportions(s string) []float64 {
	if s == "" {
		return nil
	}
	var props []float64
	if err := json.Unmarshal([]byte(s), &props); err != nil {
		return nil
	}
	return props
}
