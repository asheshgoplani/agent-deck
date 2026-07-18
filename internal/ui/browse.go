// Package ui: BrowsePanel is a self-contained full-screen history-browser
// mode. It owns discovery, tree state, cursor, and filter; rendering is done
// via the Task 8 render helpers (browse_render.go / browse_styles.go) in this
// same package. Modeled on SettingsPanel's Show/Hide/IsVisible/SetSize/
// Update/View lifecycle (see settings_panel.go).
package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/history"
	"github.com/asheshgoplani/agent-deck/internal/history/model"
	"github.com/asheshgoplani/agent-deck/internal/history/source"
	"github.com/asheshgoplani/agent-deck/internal/history/tree"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// browsePageSize is how many additional sessions a "load more" row reveals
// per Enter press, and the initial per-project session cap.
const browsePageSize = 15

// browseChromeLines is the vertical chrome consumed by the pane border (top +
// bottom, 2 rows) plus the header and footer lines (1 row each).
const browseChromeLines = 4

// BrowsePanel is a full-screen Bubble Tea component that browses discovered
// tool history (currently Claude Code) as a folder/project/session tree.
type BrowsePanel struct {
	visible bool
	width   int
	height  int

	tool source.Tool

	projects []model.Project // canonical, unfiltered, as last discovered
	root     *tree.Node      // tree built from projects (post-filter)
	rows     []tree.Row      // flattened visible rows built from root

	expanded map[string]bool
	loaded   map[string]int

	cursor int
	offset int

	filter   string
	filterOn bool

	err error
}

// NewBrowsePanel creates a new, hidden BrowsePanel. Discovery happens on
// Refresh, not construction, so building a panel never touches disk.
func NewBrowsePanel() *BrowsePanel {
	return &BrowsePanel{
		tool:     source.NewClaudeCodeTool(),
		expanded: map[string]bool{},
		loaded:   map[string]int{},
	}
}

// Show makes the panel visible. Discovery is the caller's job (via Refresh)
// so opening the panel never blocks on stale data.
func (p *BrowsePanel) Show() {
	p.visible = true
}

// Hide hides the panel. Cursor, filter, and tree state are preserved so
// reopening the panel resumes where the user left off.
func (p *BrowsePanel) Hide() {
	p.visible = false
}

// IsVisible reports whether the panel is currently shown.
func (p *BrowsePanel) IsVisible() bool {
	return p.visible
}

// IsFiltering reports whether the panel is currently in filter-input mode.
func (p *BrowsePanel) IsFiltering() bool {
	return p.filterOn
}

// SetSize sets the panel's rendering dimensions.
func (p *BrowsePanel) SetSize(w, h int) {
	p.width = w
	p.height = h
}

// Refresh re-discovers sessions from disk, overlays live agent-deck instance
// status on top of the ported registry/mtime status, and rebuilds the tree.
// Top-level nodes are (re-)expanded so the browser never opens to a wall of
// collapsed folders.
func (p *BrowsePanel) Refresh(instances []*session.Instance) {
	projects, err := p.tool.Discover()
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	history.OverlayInstanceStatus(projects, instances)
	p.applyProjects(projects)
}

// applyProjects installs freshly-discovered projects, expands the top level,
// and rebuilds the row list. Shared by Refresh and the in-panel "r" refresh.
func (p *BrowsePanel) applyProjects(projects []model.Project) {
	p.projects = projects
	top := tree.BuildTree(projects)
	for _, path := range tree.NodePathsToDepth(top, 1) {
		p.expanded[path] = true
	}
	p.rebuildRows()
}

// rebuildRows recomputes p.root and p.rows from p.projects, applying the
// active filter (if any), and clamps the cursor/offset into the new bounds.
func (p *BrowsePanel) rebuildRows() {
	projects := p.projects
	if strings.TrimSpace(p.filter) != "" {
		projects = tree.Filter(projects, p.filter)
	}
	p.root = tree.BuildTree(projects)
	p.rows = tree.FlattenVisible(p.root, p.expanded, p.loaded, browsePageSize)

	if p.cursor >= len(p.rows) {
		p.cursor = len(p.rows) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	p.clampOffset()
}

// viewportHeight is how many data rows fit in the current pane height.
func (p *BrowsePanel) viewportHeight() int {
	vh := p.height - browseChromeLines
	if vh < 1 {
		vh = 1
	}
	return vh
}

// clampOffset keeps the cursor within the visible scroll window and the
// window within the bounds of the row list.
func (p *BrowsePanel) clampOffset() {
	vh := p.viewportHeight()

	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+vh {
		p.offset = p.cursor - vh + 1
	}

	maxOffset := len(p.rows) - vh
	if maxOffset < 0 {
		maxOffset = 0
	}
	if p.offset > maxOffset {
		p.offset = maxOffset
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

// Update handles one key press. The third return value is non-nil only when
// the user pressed Enter on a SessionRow, meaning "resume this session" —
// the caller (Home) performs the actual resume.
func (p *BrowsePanel) Update(msg tea.KeyMsg) (*BrowsePanel, tea.Cmd, *model.Session) {
	if !p.visible {
		return p, nil, nil
	}

	if p.filterOn {
		return p.handleFilterKey(msg)
	}

	switch msg.String() {
	case "esc", "q", "B":
		p.Hide()

	case "up", "k":
		p.moveCursor(-1)

	case "down", "j":
		p.moveCursor(1)

	case "right", "l", " ":
		p.toggleCurrent()

	case "left", "h":
		p.collapseCurrent()

	case "/":
		p.filterOn = true
		p.filter = ""

	case "r":
		p.refreshFromTool()

	case "enter":
		return p.handleEnter()
	}

	return p, nil, nil
}

// moveCursor shifts the cursor by delta rows, clamped to the row list, and
// keeps the scroll offset in sync.
func (p *BrowsePanel) moveCursor(delta int) {
	if len(p.rows) == 0 {
		return
	}
	p.cursor += delta
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.rows) {
		p.cursor = len(p.rows) - 1
	}
	p.clampOffset()
}

// currentRow returns the row under the cursor, or false if there are none.
func (p *BrowsePanel) currentRow() (tree.Row, bool) {
	if len(p.rows) == 0 || p.cursor < 0 || p.cursor >= len(p.rows) {
		return tree.Row{}, false
	}
	return p.rows[p.cursor], true
}

// toggleCurrent expands/collapses the folder or project row under the
// cursor. No-op on session/load-more rows.
func (p *BrowsePanel) toggleCurrent() {
	row, ok := p.currentRow()
	if !ok {
		return
	}
	if row.Kind == tree.FolderRow || row.Kind == tree.ProjectRow {
		p.expanded[row.Path] = !p.expanded[row.Path]
		p.rebuildRows()
	}
}

// collapseCurrent collapses the folder/project row under the cursor if it is
// expanded; otherwise it walks up to the nearest ancestor row and collapses
// that instead (classic tree-view "left arrow" behavior).
func (p *BrowsePanel) collapseCurrent() {
	row, ok := p.currentRow()
	if !ok {
		return
	}
	if (row.Kind == tree.FolderRow || row.Kind == tree.ProjectRow) && p.expanded[row.Path] {
		p.expanded[row.Path] = false
		p.rebuildRows()
		return
	}
	for i := p.cursor - 1; i >= 0; i-- {
		if p.rows[i].Depth < row.Depth {
			p.cursor = i
			p.clampOffset()
			parent := p.rows[i]
			if parent.Kind == tree.FolderRow || parent.Kind == tree.ProjectRow {
				p.expanded[parent.Path] = false
				p.rebuildRows()
			}
			return
		}
	}
}

// handleEnter dispatches Enter based on the row kind under the cursor:
// resume a session, page in more sessions, or toggle a folder/project.
func (p *BrowsePanel) handleEnter() (*BrowsePanel, tea.Cmd, *model.Session) {
	row, ok := p.currentRow()
	if !ok {
		return p, nil, nil
	}
	switch row.Kind {
	case tree.SessionRow:
		return p, nil, row.Session
	case tree.LoadMoreRow:
		p.loaded[row.Path] += browsePageSize
		p.rebuildRows()
	case tree.FolderRow, tree.ProjectRow:
		p.expanded[row.Path] = !p.expanded[row.Path]
		p.rebuildRows()
	}
	return p, nil, nil
}

// refreshFromTool re-discovers and refreshes status without a live-instance
// overlay (Update has no access to the instance list; Home's own Refresh call
// supplies that overlay when it opens/reopens the panel). Bound to "r".
func (p *BrowsePanel) refreshFromTool() {
	projects, err := p.tool.Discover()
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	p.tool.RefreshStatus(projects)
	p.applyProjects(projects)
}

// handleFilterKey routes key presses while in "/" filter-edit mode: enter/esc
// exit the mode, backspace edits, everything else that's a single printable
// rune is appended to the query and the tree is rebuilt.
func (p *BrowsePanel) handleFilterKey(msg tea.KeyMsg) (*BrowsePanel, tea.Cmd, *model.Session) {
	key := msg.String()
	switch key {
	case "enter", "esc":
		p.filterOn = false
	case "backspace":
		if len(p.filter) > 0 {
			p.filter = p.filter[:len(p.filter)-1]
			p.rebuildRows()
		}
	default:
		if len(key) == 1 {
			p.filter += key
			p.rebuildRows()
		}
	}
	return p, nil, nil
}

// View renders the panel: a header, the visible window of tree rows, and a
// footer of key hints, inside the shared brPaneBorder chrome.
func (p *BrowsePanel) View() string {
	if !p.visible {
		return ""
	}

	width := p.width
	if width <= 0 {
		width = 80
	}

	dialogWidth := width - 2 // account for the border's own 2 columns
	if dialogWidth < 10 {
		dialogWidth = 10
	}
	innerWidth := dialogWidth - 2 // brPaneBorder has Padding(0, 1)
	if innerWidth < 4 {
		innerWidth = 4
	}

	var b strings.Builder

	header := "agent-hopdeck · Browse"
	switch {
	case p.filterOn:
		header = "Filter: " + p.filter + "█"
	case p.filter != "":
		header += "  (filter: " + p.filter + ")"
	}
	if p.err != nil {
		header += "  [error: " + p.err.Error() + "]"
	}
	b.WriteString(browsePadRight(browseClip(header, innerWidth), innerWidth))
	b.WriteString("\n")

	vh := p.viewportHeight()
	now := time.Now()

	if len(p.rows) == 0 {
		b.WriteString(browsePadRight(browseClip("  no sessions found — press r to refresh", innerWidth), innerWidth))
		b.WriteString("\n")
		for i := 1; i < vh; i++ {
			b.WriteString(browsePadRight("", innerWidth))
			b.WriteString("\n")
		}
	} else {
		end := p.offset + vh
		if end > len(p.rows) {
			end = len(p.rows)
		}
		for i := p.offset; i < end; i++ {
			selected := i == p.cursor
			b.WriteString(browseRowLabel(p.rows[i], innerWidth, selected, p.expanded, now))
			b.WriteString("\n")
		}
		for i := end - p.offset; i < vh; i++ {
			b.WriteString(browsePadRight("", innerWidth))
			b.WriteString("\n")
		}
	}

	footer := "↑/↓ nav  →/l/space toggle  ←/h collapse  / filter  enter open  r refresh  esc/q close"
	b.WriteString(browsePadRight(browseClip(footer, innerWidth), innerWidth))

	return brPaneBorder.Width(dialogWidth).Render(b.String())
}
