package ui

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SkillColumn identifies the focused column.
type SkillColumn int

const (
	SkillColumnAttached SkillColumn = iota
	SkillColumnAvailable

	// Keep project attach/detach focused on managed skills.
	skillDialogAvailableSource = "pool"
	skillTypeJumpTimeout       = 1200 * time.Millisecond
)

// SkillDialogItem wraps one discovered skill.
type SkillDialogItem struct {
	Candidate session.SkillCandidate
}

// SkillDialog manages project-scoped Claude skills.
type SkillDialog struct {
	visible     bool
	width       int
	height      int
	projectPath string
	sessionID   string
	tool        string

	column SkillColumn

	attached      []SkillDialogItem
	available     []SkillDialogItem
	attachedIdx   int
	availableIdx  int
	attachedOff   int
	availableOff  int
	hasChanges    bool
	err           error
	emptyHelpText string
	typeJumpBuf   string
	typeJumpUntil time.Time
}

// NewSkillDialog creates a skill manager dialog instance.
func NewSkillDialog() *SkillDialog {
	return &SkillDialog{}
}

// Show opens the dialog for a specific project/session.
func (d *SkillDialog) Show(projectPath, sessionID, tool string) error {
	d.projectPath = projectPath
	d.sessionID = sessionID
	d.tool = tool
	d.err = nil
	d.hasChanges = false
	d.column = SkillColumnAttached
	d.attachedIdx = 0
	d.availableIdx = 0
	d.attachedOff = 0
	d.availableOff = 0
	d.emptyHelpText = ""
	d.typeJumpBuf = ""
	d.typeJumpUntil = time.Time{}

	if tool != "claude" {
		d.visible = true
		d.attached = nil
		d.available = nil
		d.emptyHelpText = "Skills manager is currently available for Claude sessions only."
		return nil
	}

	allDiscoveredSkills, err := session.ListAvailableSkills()
	if err != nil {
		return err
	}
	attachedSkills, err := session.GetAttachedProjectSkills(projectPath)
	if err != nil {
		return err
	}

	discoveredByID := make(map[string]session.SkillCandidate, len(allDiscoveredSkills))
	availableSkills := make([]session.SkillCandidate, 0, len(allDiscoveredSkills))
	for _, skill := range allDiscoveredSkills {
		discoveredByID[skill.ID] = skill
		if strings.EqualFold(skill.Source, skillDialogAvailableSource) {
			availableSkills = append(availableSkills, skill)
		}
	}

	d.attached = make([]SkillDialogItem, 0, len(attachedSkills))
	attachedIDs := make(map[string]bool, len(attachedSkills))
	for _, attachment := range attachedSkills {
		candidate, ok := discoveredByID[attachment.ID]
		if !ok {
			candidate = session.SkillCandidate{
				ID:          attachment.ID,
				Name:        attachment.Name,
				Source:      attachment.Source,
				SourcePath:  attachment.SourcePath,
				EntryName:   attachment.EntryName,
				Description: "(source unavailable)",
				Kind:        "dir",
			}
		}
		d.attached = append(d.attached, SkillDialogItem{Candidate: candidate})
		attachedIDs[candidate.ID] = true
	}

	d.available = make([]SkillDialogItem, 0, len(availableSkills))
	for _, candidate := range availableSkills {
		if attachedIDs[candidate.ID] {
			continue
		}
		d.available = append(d.available, SkillDialogItem{Candidate: candidate})
	}

	sort.Slice(d.attached, func(i, j int) bool {
		return strings.ToLower(d.attached[i].Candidate.Name) < strings.ToLower(d.attached[j].Candidate.Name)
	})
	sort.Slice(d.available, func(i, j int) bool {
		return strings.ToLower(d.available[i].Candidate.Name) < strings.ToLower(d.available[j].Candidate.Name)
	})

	d.visible = true
	return nil
}

// Hide closes the dialog.
func (d *SkillDialog) Hide() {
	d.visible = false
	d.attached = nil
	d.available = nil
	d.err = nil
	d.emptyHelpText = ""
	d.typeJumpBuf = ""
	d.typeJumpUntil = time.Time{}
}

// IsVisible returns whether dialog is shown.
func (d *SkillDialog) IsVisible() bool {
	return d.visible
}

// SetSize updates dialog dimensions.
func (d *SkillDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
}

// HasChanged indicates whether user moved any item.
func (d *SkillDialog) HasChanged() bool {
	return d.hasChanges
}

// GetSessionID returns the managed session ID.
func (d *SkillDialog) GetSessionID() string {
	return d.sessionID
}

// GetError returns the latest apply error.
func (d *SkillDialog) GetError() error {
	return d.err
}

func (d *SkillDialog) currentListAndIndex() (*[]SkillDialogItem, *int) {
	if d.column == SkillColumnAttached {
		return &d.attached, &d.attachedIdx
	}
	return &d.available, &d.availableIdx
}

func (d *SkillDialog) currentOffset() *int {
	if d.column == SkillColumnAttached {
		return &d.attachedOff
	}
	return &d.availableOff
}

func (d *SkillDialog) maxRowsPerColumn() int {
	if d.height <= 0 {
		return 16
	}
	rows := d.height - 18
	if rows < 6 {
		rows = 6
	}
	if rows > 30 {
		rows = 30
	}
	return rows
}

func clampIndex(idx *int, total int) {
	if total <= 0 {
		*idx = 0
		return
	}
	if *idx < 0 {
		*idx = 0
	}
	if *idx >= total {
		*idx = total - 1
	}
}

func clampOffset(off *int, idx, total, rows int) {
	if rows <= 0 || total <= rows {
		*off = 0
		return
	}
	maxOff := total - rows
	if *off < 0 {
		*off = 0
	}
	if *off > maxOff {
		*off = maxOff
	}
	if idx < *off {
		*off = idx
	}
	if idx >= *off+rows {
		*off = idx - rows + 1
	}
	if *off < 0 {
		*off = 0
	}
	if *off > maxOff {
		*off = maxOff
	}
}

func (d *SkillDialog) normalizeSelectionAndScroll() {
	rows := d.maxRowsPerColumn()

	clampIndex(&d.attachedIdx, len(d.attached))
	clampIndex(&d.availableIdx, len(d.available))

	clampOffset(&d.attachedOff, d.attachedIdx, len(d.attached), rows)
	clampOffset(&d.availableOff, d.availableIdx, len(d.available), rows)
}

func (d *SkillDialog) moveBy(delta int) {
	list, idx := d.currentListAndIndex()
	if len(*list) == 0 {
		return
	}
	*idx += delta
	clampIndex(idx, len(*list))
	d.normalizeSelectionAndScroll()
}

func (d *SkillDialog) indexOfSkill(items []SkillDialogItem, id string) int {
	for i := range items {
		if items[i].Candidate.ID == id {
			return i
		}
	}
	return -1
}

func (d *SkillDialog) resetTypeJump() {
	d.typeJumpBuf = ""
	d.typeJumpUntil = time.Time{}
}

func (d *SkillDialog) typeJump(r rune) {
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
		return
	}

	now := time.Now()
	if now.After(d.typeJumpUntil) {
		d.typeJumpBuf = ""
	}
	d.typeJumpBuf += strings.ToLower(string(r))
	d.typeJumpUntil = now.Add(skillTypeJumpTimeout)

	list, idx := d.currentListAndIndex()
	if len(*list) == 0 {
		return
	}

	findFrom := func(prefix string) int {
		start := *idx + 1
		for i := 0; i < len(*list); i++ {
			j := (start + i) % len(*list)
			name := strings.ToLower((*list)[j].Candidate.Name)
			if strings.HasPrefix(name, prefix) {
				return j
			}
		}
		return -1
	}

	if match := findFrom(d.typeJumpBuf); match >= 0 {
		*idx = match
		d.normalizeSelectionAndScroll()
		return
	}

	// If multi-char prefix doesn't match, fall back to latest typed rune.
	last := strings.ToLower(string(r))
	if match := findFrom(last); match >= 0 {
		d.typeJumpBuf = last
		*idx = match
		d.normalizeSelectionAndScroll()
		return
	}
}

// Move toggles one item between attached and available lists.
func (d *SkillDialog) Move() {
	list, idx := d.currentListAndIndex()
	if len(*list) == 0 || *idx < 0 || *idx >= len(*list) {
		return
	}

	item := (*list)[*idx]
	*list = append((*list)[:*idx], (*list)[*idx+1:]...)

	if d.column == SkillColumnAttached {
		d.available = append(d.available, item)
		sort.Slice(d.available, func(i, j int) bool {
			return strings.ToLower(d.available[i].Candidate.Name) < strings.ToLower(d.available[j].Candidate.Name)
		})
		if movedIdx := d.indexOfSkill(d.available, item.Candidate.ID); movedIdx >= 0 {
			d.availableIdx = movedIdx
		}
	} else {
		d.attached = append(d.attached, item)
		sort.Slice(d.attached, func(i, j int) bool {
			return strings.ToLower(d.attached[i].Candidate.Name) < strings.ToLower(d.attached[j].Candidate.Name)
		})
		if movedIdx := d.indexOfSkill(d.attached, item.Candidate.ID); movedIdx >= 0 {
			d.attachedIdx = movedIdx
		}
	}

	d.hasChanges = true
	d.normalizeSelectionAndScroll()
}

// Apply saves project skills according to attached column state.
func (d *SkillDialog) Apply() error {
	d.err = nil
	if d.tool != "claude" {
		return nil
	}

	desired := make([]session.SkillCandidate, 0, len(d.attached))
	for _, item := range d.attached {
		desired = append(desired, item.Candidate)
	}

	if err := session.ApplyProjectSkills(d.projectPath, desired); err != nil {
		d.err = err
		return err
	}
	return nil
}

// Update handles keyboard input while dialog is visible.
func (d *SkillDialog) Update(msg tea.KeyMsg) (*SkillDialog, tea.Cmd) {
	switch msg.String() {
	case "left", "h":
		d.column = SkillColumnAttached
		d.resetTypeJump()
		d.normalizeSelectionAndScroll()
	case "right", "l":
		d.column = SkillColumnAvailable
		d.resetTypeJump()
		d.normalizeSelectionAndScroll()
	case "up", "k":
		d.resetTypeJump()
		d.moveBy(-1)
	case "down", "j":
		d.resetTypeJump()
		d.moveBy(1)
	case "pgup", "ctrl+b":
		d.resetTypeJump()
		d.moveBy(-d.maxRowsPerColumn())
	case "pgdown", "ctrl+f":
		d.resetTypeJump()
		d.moveBy(d.maxRowsPerColumn())
	case " ":
		d.resetTypeJump()
		d.Move()
	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			d.typeJump(msg.Runes[0])
		}
	}

	return d, nil
}

func (d *SkillDialog) renderColumn(title string, items []SkillDialogItem, selectedIdx, offset, rows int, focused bool) string {
	headerStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	if focused {
		headerStyle = headerStyle.Foreground(ColorAccent)
	}
	header := headerStyle.Render("- " + title + " ")

	colWidth := 38
	headerLen := len("- " + title + " ")
	headerPad := colWidth - headerLen
	if headerPad > 0 {
		header += headerStyle.Render(repeatStr("-", headerPad))
	}

	lines := []string{header}
	if len(items) == 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true).Render("  (empty)"))
		return lipgloss.JoinVertical(lipgloss.Left, lines...)
	}

	start := offset
	end := offset + rows
	if start < 0 {
		start = 0
	}
	if end > len(items) {
		end = len(items)
	}
	for i := start; i < end; i++ {
		item := items[i]
		label := item.Candidate.Name
		if item.Candidate.Source != "" {
			label += " [" + item.Candidate.Source + "]"
		}
		if len(label) > colWidth-4 {
			label = label[:colWidth-7] + "..."
		}

		if i == selectedIdx && focused {
			lines = append(lines, lipgloss.NewStyle().
				Background(ColorAccent).
				Foreground(ColorBg).
				Bold(true).
				Width(colWidth).
				Render(" > "+label))
		} else {
			lines = append(lines, lipgloss.NewStyle().
				Foreground(ColorText).
				Width(colWidth).
				Render("   "+label))
		}
	}

	rangeText := lipgloss.NewStyle().Foreground(ColorTextDim).Width(colWidth).
		Render("  " + strconv.Itoa(start+1) + "-" + strconv.Itoa(end) + " of " + strconv.Itoa(len(items)))
	lines = append(lines, rangeText)

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (d *SkillDialog) renderEmptyStateHelp() string {
	helpStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	highlightStyle := lipgloss.NewStyle().Foreground(ColorYellow)
	pathStyle := lipgloss.NewStyle().Foreground(ColorCyan)

	lines := []string{
		"",
		highlightStyle.Render("No pool skills available"),
		"",
		helpStyle.Render("Place reusable skills in:"),
		pathStyle.Render("  ~/.agent-deck/skills/pool"),
		"",
		helpStyle.Render("Only pool skills appear in Available."),
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// View renders the dialog body.
func (d *SkillDialog) View() string {
	if !d.visible {
		return ""
	}

	title := "Skills Manager"
	scopeDesc := DimStyle.Render("Writes to: .agent-deck/skills.toml + .claude/skills (project)")
	sourceTab := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("[POOL]")
	sourceLine := "──────────────── " + sourceTab + " ────────────────"
	if d.tool != "claude" && d.emptyHelpText != "" {
		scopeDesc = lipgloss.NewStyle().Foreground(ColorYellow).Render(d.emptyHelpText)
		sourceLine = ""
	}

	d.normalizeSelectionAndScroll()
	rows := d.maxRowsPerColumn()
	attachedCol := d.renderColumn("Attached ("+strconv.Itoa(len(d.attached))+")", d.attached, d.attachedIdx, d.attachedOff, rows, d.column == SkillColumnAttached)
	availableCol := d.renderColumn("Available ("+strconv.Itoa(len(d.available))+")", d.available, d.availableIdx, d.availableOff, rows, d.column == SkillColumnAvailable)
	columns := lipgloss.JoinHorizontal(lipgloss.Top, attachedCol, "  ", availableCol)

	hint := lipgloss.NewStyle().Foreground(ColorComment).Render("←→ column │ ↑↓ scroll │ Type jump │ Space move │ Enter apply │ Esc cancel")
	if d.typeJumpBuf != "" && time.Now().Before(d.typeJumpUntil) {
		hint += lipgloss.NewStyle().Foreground(ColorTextDim).Render("  (" + d.typeJumpBuf + ")")
	}

	dialogWidth := 86
	if d.width > 0 && d.width < dialogWidth+10 {
		dialogWidth = d.width - 10
		if dialogWidth < 56 {
			dialogWidth = 56
		}
	}
	titleWidth := dialogWidth - 4

	parts := []string{
		DialogTitleStyle.Width(titleWidth).Render(title),
		"",
		sourceLine,
		scopeDesc,
		"",
	}

	if len(d.attached) == 0 && len(d.available) == 0 {
		parts = append(parts, d.renderEmptyStateHelp())
	} else {
		parts = append(parts, columns)
	}

	if d.err != nil {
		parts = append(parts, "", lipgloss.NewStyle().Foreground(ColorRed).Render("Error: "+d.err.Error()))
	}
	parts = append(parts, "", hint)

	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	dialog := DialogBoxStyle.Width(dialogWidth).Render(content)

	// Match MCP manager behavior: center modal in terminal viewport.
	if d.width <= 0 || d.height <= 0 {
		return dialog
	}
	return lipgloss.Place(
		d.width,
		d.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)
}
