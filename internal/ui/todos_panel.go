package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/todos"
)

// TodosPanel is the TUI view of the owner's to-do list (`agent-deck todo`,
// docs/todos.md): the open items, ticked off with x, added with a, done
// ones shown with tab. It re-reads the list every todosPanelRefresh while
// open, so items added by voice, the phone or the CLI appear on their own.
// Writes and publishes run in tea.Cmds (they may wait for the file lock)
// and come back as todosPanelResultMsg; the re-read is a lock-free read of
// a small file and runs in Update. Bubble Tea does not wait for running
// commands at quit, so a publish still running then is lost (the write
// itself finished first).
type TodosPanel struct {
	visible  bool
	width    int
	height   int
	cursor   int
	showDone bool
	adding   bool
	input    textinput.Model
	items    []todos.Item
	status   string
	loadErr  string
	profile  string

	// open and publish are swapped by tests.
	open    func() (*todos.Store, error)
	publish func(c todos.Change, profile string)
	// tickGen invalidates ticks from an earlier Show.
	tickGen int
}

// todosPanelRefresh paces the re-read while the panel is open.
const todosPanelRefresh = 2 * time.Second

// todosPanelTickMsg asks the open panel to re-read the list.
type todosPanelTickMsg struct{ gen int }

func NewTodosPanel(profile string) *TodosPanel {
	ti := textinput.New()
	ti.Placeholder = "what to do"
	ti.CharLimit = todos.MaxTextRunes
	return &TodosPanel{
		input:   ti,
		profile: profile,
		open:    todos.Open,
		publish: func(c todos.Change, profile string) { todos.Publish(c, profile) },
	}
}

// Show opens the panel on a fresh read and starts the refresh tick.
func (p *TodosPanel) Show() tea.Cmd {
	if p == nil {
		return nil
	}
	p.visible = true
	p.adding = false
	p.status = ""
	p.input.Blur()
	p.refresh()
	p.tickGen++
	return p.tick()
}

func (p *TodosPanel) tick() tea.Cmd {
	gen := p.tickGen
	return tea.Tick(todosPanelRefresh, func(time.Time) tea.Msg { return todosPanelTickMsg{gen: gen} })
}

func (p *TodosPanel) Hide() {
	if p != nil {
		p.visible = false
		p.adding = false
		p.input.Blur()
	}
}

func (p *TodosPanel) IsVisible() bool { return p != nil && p.visible }

func (p *TodosPanel) SetSize(width, height int) {
	if p != nil {
		p.width, p.height = width, height
	}
}

func (p *TodosPanel) refresh() {
	store, err := p.open()
	if err == nil {
		p.items, err = store.List(p.showDone)
	}
	if err != nil {
		p.loadErr = err.Error()
		return
	}
	p.loadErr = ""
	if p.cursor >= len(p.items) {
		p.cursor = len(p.items) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func (p *TodosPanel) selected() *todos.Item {
	if p == nil || p.cursor < 0 || p.cursor >= len(p.items) {
		return nil
	}
	return &p.items[p.cursor]
}

// HandleTick re-reads the list and schedules the next tick while open.
func (p *TodosPanel) HandleTick(msg todosPanelTickMsg) tea.Cmd {
	if p == nil || !p.visible || msg.gen != p.tickGen {
		return nil
	}
	if !p.adding {
		keep := ""
		if it := p.selected(); it != nil {
			keep = it.ID
		}
		p.refresh()
		for i, it := range p.items {
			if it.ID == keep {
				p.cursor = i
			}
		}
	}
	return p.tick()
}

func (p *TodosPanel) Update(msg tea.Msg) (*TodosPanel, tea.Cmd) {
	if p == nil || !p.visible {
		return p, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	if p.adding {
		switch key.String() {
		case "esc":
			p.adding = false
			p.input.Blur()
			p.input.SetValue("")
			return p, nil
		case "enter":
			text := p.input.Value()
			p.adding = false
			p.input.Blur()
			p.input.SetValue("")
			return p, p.add(text)
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return p, cmd
	}

	switch key.String() {
	case "esc", "q", "alt+o":
		p.Hide()
	case "j", "down", "ctrl+n":
		if p.cursor+1 < len(p.items) {
			p.cursor++
		}
	case "k", "up", "ctrl+p":
		if p.cursor > 0 {
			p.cursor--
		}
	case "a", "n":
		p.adding = true
		p.status = ""
		p.input.SetValue("")
		return p, p.input.Focus()
	case "x", " ", "enter":
		return p, p.toggle()
	case "tab":
		p.showDone = !p.showDone
		p.refresh()
	case "r":
		p.refresh()
	}
	return p, nil
}

// add saves a new item and publishes the change in a tea.Cmd: the write
// may wait up to 10 s for the file lock and must never block Update.
func (p *TodosPanel) add(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	p.status = "Saving…"
	open, publish, profile := p.open, p.publish, p.profile
	return func() tea.Msg {
		store, err := open()
		if err != nil {
			return todosPanelResultMsg{status: "Could not open the list: " + err.Error()}
		}
		it, err := store.Add(text, todos.AddOptions{Source: todos.SourceTUI})
		if err != nil {
			return todosPanelResultMsg{status: "Not added: " + err.Error()}
		}
		publish(todos.Change{Action: todos.ActionAdd, Todo: it, Open: derefTodoCount(store.OpenCount()), By: todos.SourceTUI}, profile)
		return todosPanelResultMsg{status: "Added " + it.ID, focusID: it.ID}
	}
}

// toggle ticks off the selected open item, or reopens a done one, in a
// tea.Cmd (see add).
func (p *TodosPanel) toggle() tea.Cmd {
	sel := p.selected()
	if sel == nil {
		return nil
	}
	id, wasDone := sel.ID, sel.Done()
	p.status = "Saving…"
	open, publish, profile := p.open, p.publish, p.profile
	return func() tea.Msg {
		store, err := open()
		if err != nil {
			return todosPanelResultMsg{status: "Could not open the list: " + err.Error()}
		}
		var (
			it      todos.Item
			changed bool
			action  = todos.ActionDone
			verb    = "Done "
		)
		if wasDone {
			action, verb = todos.ActionUndo, "Reopened "
			it, changed, err = store.Undo(id)
		} else {
			it, changed, err = store.Done(id)
		}
		if err != nil {
			return todosPanelResultMsg{status: "Not changed: " + err.Error()}
		}
		if changed {
			publish(todos.Change{Action: action, Todo: it, Open: derefTodoCount(store.OpenCount()), By: todos.SourceTUI}, profile)
		}
		return todosPanelResultMsg{status: verb + it.ID, focusID: it.ID}
	}
}

func derefTodoCount(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

func (p *TodosPanel) View() string {
	if p == nil || !p.visible {
		return ""
	}
	width := p.width - 6
	if width > 80 {
		width = 80
	}
	if width < 40 {
		width = 40
	}
	inner := width - 4
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	var b strings.Builder
	title := "To-dos"
	open := 0
	for _, it := range p.items {
		if !it.Done() {
			open++
		}
	}
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render(title))
	b.WriteString(dim.Render(fmt.Sprintf("  %d open", open)))
	b.WriteString("\n\n")
	switch {
	case p.loadErr != "":
		b.WriteString(lipgloss.NewStyle().Foreground(ColorRed).Render(truncateDisplay("Unable to read the list: "+p.loadErr, inner, "…")))
		b.WriteByte('\n')
	case len(p.items) == 0:
		b.WriteString(dim.Render("Nothing on the list."))
		b.WriteByte('\n')
	default:
		for i, it := range p.items {
			cursor := "  "
			if i == p.cursor {
				cursor = "> "
			}
			box := "[ ] "
			if it.Done() {
				box = "[x] "
			}
			line := fmt.Sprintf("%s%s%3s  %s", cursor, box, it.ID, it.Text)
			line = truncateDisplay(line, inner, "…")
			if it.Done() {
				line = dim.Render(line)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if p.adding {
		b.WriteString("\nAdd: ")
		b.WriteString(p.input.View())
		b.WriteByte('\n')
	}
	if p.status != "" {
		b.WriteString("\n")
		b.WriteString(truncateDisplay(p.status, inner, "…"))
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	help := "j/k move  x done  a add  tab show done  esc close"
	if p.showDone {
		help = "j/k move  x done/reopen  a add  tab hide done  esc close"
	}
	if p.adding {
		help = "enter add  esc cancel"
	}
	b.WriteString(dim.Render(help))

	box := lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(1, 2).
		Render(b.String())
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, box)
}

// todosPanelResultMsg is the outcome of a write run off the update loop.
type todosPanelResultMsg struct {
	status  string
	focusID string
}

// HandleResult shows the outcome of a write and re-reads the list.
func (p *TodosPanel) HandleResult(msg todosPanelResultMsg) {
	if p == nil {
		return
	}
	p.status = msg.status
	if !p.visible {
		return
	}
	p.refresh()
	if msg.focusID == "" {
		return
	}
	for i, it := range p.items {
		if it.ID == msg.focusID {
			p.cursor = i
		}
	}
}
