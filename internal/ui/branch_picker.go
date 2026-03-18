package ui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

var loadBranchCandidates = branchCandidatesForPath

// branchPickerResultMsg is kept for compatibility with existing dialog message handling.
type branchPickerResultMsg struct {
	branch   string
	canceled bool
	err      error
}

// BranchPickerDialog is an in-TUI branch picker with inline filtering.
type BranchPickerDialog struct {
	visible     bool
	width       int
	height      int
	input       textinput.Model
	allBranches []string
	branches    []string
	cursor      int
	offset      int
}

func NewBranchPickerDialog() *BranchPickerDialog {
	input := textinput.New()
	input.Placeholder = "Search branches..."
	input.CharLimit = 200
	input.Width = 40

	return &BranchPickerDialog{
		input: input,
	}
}

func branchCandidatesForPath(projectPath string) ([]string, error) {
	projectPath = session.ExpandPath(strings.Trim(strings.TrimSpace(projectPath), "'\""))
	if projectPath == "" {
		return nil, errors.New("project path is empty")
	}

	repoRoot, err := git.GetWorktreeBaseRoot(projectPath)
	if err != nil {
		return nil, errors.New("path is not a git repository")
	}

	branches, err := git.ListBranchCandidates(repoRoot)
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
	d.input.Blur()
	d.input.SetValue("")
	d.allBranches = nil
	d.branches = nil
	d.cursor = 0
	d.offset = 0
}

func (d *BranchPickerDialog) Show(projectPath string) error {
	branches, err := loadBranchCandidates(projectPath)
	if err != nil {
		return err
	}

	d.visible = true
	d.allBranches = branches
	d.cursor = 0
	d.offset = 0
	d.input.SetValue("")
	d.input.Focus()
	d.filter()
	return nil
}

func (d *BranchPickerDialog) filter() {
	query := strings.ToLower(strings.TrimSpace(d.input.Value()))
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

// Update returns the selected branch and whether the key was consumed.
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

	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	_ = cmd
	d.cursor = 0
	d.offset = 0
	d.filter()
	return "", true
}

func (d *BranchPickerDialog) View() string {
	if !d.visible {
		return ""
	}

	titleStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)
	labelStyle := lipgloss.NewStyle().
		Foreground(ColorComment)
	selectedStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)
	itemStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	var body strings.Builder
	body.WriteString(titleStyle.Render("Branch Search"))
	body.WriteString("\n")
	body.WriteString("  ")
	body.WriteString(d.input.View())
	body.WriteString("\n")

	if len(d.branches) == 0 {
		body.WriteString("\n")
		body.WriteString(labelStyle.Render("  No matching branches"))
	} else {
		body.WriteString("\n")
		rows := d.maxVisibleRows()
		end := d.offset + rows
		if end > len(d.branches) {
			end = len(d.branches)
		}
		for i := d.offset; i < end; i++ {
			prefix := "  "
			style := itemStyle
			if i == d.cursor {
				prefix = "▶ "
				style = selectedStyle
			}
			body.WriteString(style.Render(prefix + d.branches[i]))
			body.WriteString("\n")
		}
		if end < len(d.branches) {
			body.WriteString(labelStyle.Render("  …"))
		} else {
			body.WriteString(labelStyle.Render("  "))
		}
	}

	help := labelStyle.Render("Enter select | Esc close | Up/Down navigate | Type filter")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1).
		MarginTop(1).
		Width(48).
		Render(body.String() + "\n" + help)
}
