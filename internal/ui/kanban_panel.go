package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// KanbanTask holds the display-relevant fields of a Hermes Kanban task.
type KanbanTask struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	BlockReason string `json:"block_reason"`
	Assignee    string `json:"assignee"`
}

// KanbanPanel is a toggleable overlay that shows running and blocked Hermes
// Kanban tasks in a two-column layout (● RUNNING | ▲ BLOCKED).
// Width-responsive:
//   - ≥100 cols → two columns side by side
//   - <100 cols → single unified list with status prefix
type KanbanPanel struct {
	visible bool
	width   int
	height  int

	tasks     []KanbanTask
	fetchedAt time.Time
	fetchErr  string
	loading   bool
}

// NewKanbanPanel creates a hidden kanban panel.
func NewKanbanPanel() *KanbanPanel {
	return &KanbanPanel{}
}

// IsVisible returns whether the panel is currently shown.
func (p *KanbanPanel) IsVisible() bool {
	if p == nil {
		return false
	}
	return p.visible
}

// Show makes the panel visible and triggers a data refresh.
func (p *KanbanPanel) Show() {
	if p == nil {
		return
	}
	p.visible = true
	p.loading = true
	p.fetchErr = ""
}

// Hide dismisses the panel.
func (p *KanbanPanel) Hide() {
	if p == nil {
		return
	}
	p.visible = false
}

// Toggle flips visibility. Returns true if now visible.
func (p *KanbanPanel) Toggle() bool {
	if p == nil {
		return false
	}
	if p.visible {
		p.Hide()
	} else {
		p.Show()
	}
	return p.visible
}

// SetSize updates terminal dimensions used for layout.
func (p *KanbanPanel) SetSize(width, height int) {
	if p == nil {
		return
	}
	p.width = width
	p.height = height
}

// SetTasks updates the displayed task list.
func (p *KanbanPanel) SetTasks(tasks []KanbanTask, err string) {
	if p == nil {
		return
	}
	p.tasks = tasks
	p.fetchErr = err
	p.loading = false
	p.fetchedAt = time.Now()
}

// FetchKanbanTasks fetches running and blocked Hermes Kanban tasks.
// hermes --status only accepts one value, so we fetch all tasks and filter.
func FetchKanbanTasks() ([]KanbanTask, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "hermes", "kanban", "list", "--json").Output()
	if err != nil {
		return nil, err
	}

	var all []KanbanTask
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, err
	}

	var tasks []KanbanTask
	for _, t := range all {
		if t.Status == "running" || t.Status == "claimed" || t.Status == "blocked" {
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}

// View renders the panel as a full overlay string.
func (p *KanbanPanel) View() string {
	if p == nil || !p.visible {
		return ""
	}

	w := p.width
	if w < 30 {
		w = 30
	}
	h := p.height
	if h < 8 {
		h = 8
	}

	// Styles
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("7"))

	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))

	runStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("2")) // green for running

	blockStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("1")) // red for blocked

	// Separate tasks
	var running, blocked []KanbanTask
	for _, t := range p.tasks {
		switch t.Status {
		case "running", "claimed":
			running = append(running, t)
		case "blocked":
			blocked = append(blocked, t)
		}
	}

	var content string
	innerW := w - 4 // subtract border + padding

	if innerW >= 96 {
		content = p.renderTwoColumn(running, blocked, innerW, titleStyle, dimStyle, runStyle, blockStyle)
	} else {
		content = p.renderSingleColumn(running, blocked, innerW, titleStyle, dimStyle, runStyle, blockStyle)
	}

	// Build header line
	staleMark := ""
	if !p.fetchedAt.IsZero() && time.Since(p.fetchedAt) > 30*time.Second {
		staleMark = dimStyle.Render(" (stale)")
	}
	header := titleStyle.Render("Hermes Kanban") + staleMark
	if p.loading {
		header += dimStyle.Render("  loading…")
	} else if p.fetchErr != "" {
		header += lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("  " + p.fetchErr)
	}

	keyhint := dimStyle.Render("  r refresh · B/esc close")

	body := header + "\n" + keyhint + "\n\n" + content

	// Clamp height
	lines := strings.Split(body, "\n")
	maxLines := h - 4
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, dimStyle.Render("  … ("+fmt.Sprintf("%d", len(p.tasks)-maxLines)+" more)"))
	}

	return borderStyle.Width(innerW + 2).Render(strings.Join(lines, "\n"))
}

func (p *KanbanPanel) renderTwoColumn(
	running, blocked []KanbanTask,
	width int,
	titleStyle, dimStyle, runStyle, blockStyle lipgloss.Style,
) string {
	colW := (width - 3) / 2 // 3 for " │ " divider

	leftLines := renderTaskColumn("● RUNNING", running, colW, runStyle, dimStyle, false)
	rightLines := renderTaskColumn("▲ BLOCKED", blocked, colW, blockStyle, dimStyle, true)

	// Pad shorter column
	maxRows := len(leftLines)
	if len(rightLines) > maxRows {
		maxRows = len(rightLines)
	}
	for len(leftLines) < maxRows {
		leftLines = append(leftLines, strings.Repeat(" ", colW))
	}
	for len(rightLines) < maxRows {
		rightLines = append(rightLines, strings.Repeat(" ", colW))
	}

	divider := dimStyle.Render("│")
	var rows []string
	for i := 0; i < maxRows; i++ {
		rows = append(rows, leftLines[i]+" "+divider+" "+rightLines[i])
	}
	return strings.Join(rows, "\n")
}

func (p *KanbanPanel) renderSingleColumn(
	running, blocked []KanbanTask,
	width int,
	titleStyle, dimStyle, runStyle, blockStyle lipgloss.Style,
) string {
	var lines []string

	runHeader := runStyle.Render("● RUNNING")
	lines = append(lines, runHeader)
	if len(running) == 0 {
		lines = append(lines, dimStyle.Render("  (none)"))
	} else {
		for _, t := range running {
			lines = append(lines, "  "+truncate(t.Title, width-3))
			if t.Assignee != "" {
				lines = append(lines, dimStyle.Render("    "+t.Assignee))
			}
		}
	}

	lines = append(lines, "")
	blockHeader := blockStyle.Render("▲ BLOCKED")
	lines = append(lines, blockHeader)
	if len(blocked) == 0 {
		lines = append(lines, dimStyle.Render("  (none)"))
	} else {
		for _, t := range blocked {
			lines = append(lines, "  "+truncate(t.Title, width-3))
			if t.BlockReason != "" {
				lines = append(lines, dimStyle.Render("    ↳ "+truncate(t.BlockReason, width-6)))
			}
		}
	}

	return strings.Join(lines, "\n")
}

// renderTaskColumn renders one side of the two-column view.
func renderTaskColumn(
	header string,
	tasks []KanbanTask,
	colW int,
	headerStyle, dimStyle lipgloss.Style,
	showBlockReason bool,
) []string {
	var lines []string
	lines = append(lines, headerStyle.Render(pad(header, colW)))
	lines = append(lines, strings.Repeat("─", colW))

	if len(tasks) == 0 {
		lines = append(lines, dimStyle.Render(pad("(none)", colW)))
	} else {
		for _, t := range tasks {
			lines = append(lines, truncate(t.Title, colW))
			if t.Assignee != "" {
				lines = append(lines, dimStyle.Render(truncate("  "+t.Assignee, colW)))
			}
			if showBlockReason && t.BlockReason != "" {
				lines = append(lines, dimStyle.Render(truncate("  ↳ "+t.BlockReason, colW)))
			}
		}
	}
	return lines
}

func truncate(s string, maxW int) string {
	if maxW <= 3 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxW {
		return s
	}
	return string(runes[:maxW-1]) + "…"
}

func pad(s string, width int) string {
	runes := []rune(s)
	if len(runes) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(runes))
}

// kanbanFetchDoneMsg carries the result of an async FetchKanbanTasks call.
type kanbanFetchDoneMsg struct {
	tasks []KanbanTask
	err   string
}


func isHermesNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "not found")
}

// fetchKanbanTasksCmd returns a Bubble Tea Cmd that fetches Kanban tasks
// asynchronously and delivers a kanbanFetchDoneMsg.
func fetchKanbanTasksCmd() tea.Cmd {
	return func() tea.Msg {
		tasks, err := FetchKanbanTasks()
		if err != nil {
			if isHermesNotFound(err) {
				return kanbanFetchDoneMsg{}
			}
			return kanbanFetchDoneMsg{err: err.Error()}
		}
		return kanbanFetchDoneMsg{tasks: tasks}
	}
}
