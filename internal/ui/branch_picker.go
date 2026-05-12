package ui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

var loadBranchCandidates = branchCandidatesForPath

// BranchPickerDialog is an in-TUI branch picker with inline filtering.
type BranchPickerDialog struct {
	visible     bool
	width       int
	height      int
	allBranches []string
	branches    []string
	cursor      int
	offset      int
}

func NewBranchPickerDialog() *BranchPickerDialog {
	return &BranchPickerDialog{}
}

func branchCandidatesForPath(projectPath string) ([]string, error) {
	projectPath = session.ExpandPath(strings.Trim(strings.TrimSpace(projectPath), "'\""))
	if projectPath == "" {
		return nil, errors.New("project path is empty")
	}

	backend, err := git.NewGitBackend(projectPath)
	if err != nil {
		return nil, errors.New("path is not a git repository")
	}

	branches, err := backend.ListBranchCandidates()
	if err != nil {
		return nil, err
	}
	if len(branches) == 0 {
		return nil, errors.New("no branches found in repository")
	}

	return branches, nil
}

func (d *BranchPickerDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
}

func (d *BranchPickerDialog) IsVisible() bool {
	return d.visible
}

func (d *BranchPickerDialog) Hide() {
	d.visible = false
	d.allBranches = nil
	d.branches = nil
	d.cursor = 0
	d.offset = 0
}

func (d *BranchPickerDialog) Show(projectPath, query string) error {
	branches, err := loadBranchCandidates(projectPath)
	if err != nil {
		return err
	}

	d.visible = true
	d.allBranches = branches
	d.cursor = 0
	d.offset = 0
	d.SetQuery(query)
	return nil
}

func (d *BranchPickerDialog) SetQuery(query string) {
	d.filter(query)
}

func (d *BranchPickerDialog) filter(query string) {
	query = strings.ToLower(strings.TrimSpace(query))
	d.branches = d.branches[:0]
	for _, branch := range d.allBranches {
		if query == "" || strings.Contains(strings.ToLower(branch), query) {
			d.branches = append(d.branches, branch)
		}
	}
	if len(d.branches) == 0 {
		d.cursor = 0
		d.offset = 0
		return
	}
	if d.cursor >= len(d.branches) {
		d.cursor = len(d.branches) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
	d.ensureCursorVisible()
}

func (d *BranchPickerDialog) maxVisibleRows() int {
	if d.height <= 0 {
		return 6
	}
	rows := d.height / 4
	if rows < 4 {
		rows = 4
	}
	if rows > 8 {
		rows = 8
	}
	return rows
}

func (d *BranchPickerDialog) ensureCursorVisible() {
	rows := d.maxVisibleRows()
	if d.cursor < d.offset {
		d.offset = d.cursor
	}
	if d.cursor >= d.offset+rows {
		d.offset = d.cursor - rows + 1
	}
	if d.offset < 0 {
		d.offset = 0
	}
	maxOffset := len(d.branches) - rows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if d.offset > maxOffset {
		d.offset = maxOffset
	}
}

// Update handles list navigation keys. Text entry should remain in the parent input.
func (d *BranchPickerDialog) Update(msg tea.KeyMsg) (string, bool) {
	if !d.visible {
		return "", false
	}

	switch msg.String() {
	case "esc":
		d.Hide()
		return "", true
	case "enter":
		if len(d.branches) == 0 {
			return "", true
		}
		selected := d.branches[d.cursor]
		d.Hide()
		return selected, true
	case "up", "ctrl+k", "ctrl+p":
		if len(d.branches) > 0 && d.cursor > 0 {
			d.cursor--
			d.ensureCursorVisible()
		}
		return "", true
	case "down", "ctrl+j", "ctrl+n":
		if len(d.branches) > 0 && d.cursor < len(d.branches)-1 {
			d.cursor++
			d.ensureCursorVisible()
		}
		return "", true
	}

	return "", false
}

func (d *BranchPickerDialog) View() string {
	if !d.visible {
		return ""
	}

	labelStyle := lipgloss.NewStyle().
		Foreground(ColorComment)
	selectedStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)
	itemStyle := lipgloss.NewStyle().
		Foreground(ColorComment)

	var body strings.Builder
	total := len(d.branches)
	rows := d.maxVisibleRows()
	startIdx := d.offset
	endIdx := startIdx + rows
	if endIdx > total {
		endIdx = total
	}

	body.WriteString(labelStyle.Render("─ branches (↑↓/Enter/Esc) ─"))
	body.WriteString("\n")

	if total == 0 {
		body.WriteString(labelStyle.Render("    No matching branches"))
		return body.String()
	}

	if startIdx > 0 {
		body.WriteString(labelStyle.Render(fmt.Sprintf("    ↑ %d more above", startIdx)))
		body.WriteString("\n")
	}

	for i := startIdx; i < endIdx; i++ {
		prefix := "    "
		style := itemStyle
		if i == d.cursor {
			prefix = "  ▶ "
			style = selectedStyle
		}
		body.WriteString(style.Render(prefix + d.branches[i]))
		body.WriteString("\n")
	}

	if endIdx < total {
		body.WriteString(labelStyle.Render(fmt.Sprintf("    ↓ %d more below", total-endIdx)))
	}

	return body.String()
}
