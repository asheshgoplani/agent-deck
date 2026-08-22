package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/agents"
)

// AgentsPanel is the Agents tab: the grouped-by-machine fleet list and one
// agent's detail screen.
//
// It is strictly a reader. No key it handles starts, stops, pauses or fires
// anything — phase 1 renders what already exists and says who owns it.
type AgentsPanel struct {
	visible    bool
	width      int
	height     int
	cursor     int
	scroll     int
	detailMode bool

	view agents.View
	// rows is the flattened, navigable list: machine headers are rendered but
	// not selectable, so the cursor only ever lands on an agent.
	rows []agentPanelRow
	// now is injected by the refresh so relative times in a single frame all
	// agree with each other.
	now time.Time
	// rulesByAgent holds each agent's policy file names, so the detail screen
	// can show the RULES section the mockup and the prompt both call for.
	rulesByAgent map[string][]string
	// rules is the selected agent's rules, resolved at render time.
	rules     []string
	loadError string
}

// agentPanelRow is one rendered line.
type agentPanelRow struct {
	machine  string
	isHeader bool
	link     agents.LinkState
	linkNote string
	agent    agents.AgentRow
}

// NewAgentsPanel creates the panel.
func NewAgentsPanel() *AgentsPanel {
	return &AgentsPanel{now: time.Now()}
}

// Show makes the panel visible and resets navigation.
//
// Every method on this type tolerates a nil receiver. Home values built
// directly by tests (rather than through NewHome) leave optional panels nil,
// and the render/update path must be total over those values rather than
// panicking on a field it did not expect to be set.
func (ap *AgentsPanel) Show() {
	if ap == nil {
		return
	}
	ap.visible = true
	ap.cursor = 0
	ap.scroll = 0
	ap.detailMode = false
}

// Hide hides the panel.
func (ap *AgentsPanel) Hide() {
	if ap == nil {
		return
	}
	ap.visible = false
}

// IsVisible reports whether the panel is shown.
func (ap *AgentsPanel) IsVisible() bool { return ap != nil && ap.visible }

// SetSize records the terminal dimensions.
func (ap *AgentsPanel) SetSize(w, h int) {
	if ap == nil {
		return
	}
	ap.width = w
	ap.height = h
}

// SetView replaces the rendered fleet view.
func (ap *AgentsPanel) SetView(view agents.View, now time.Time) {
	if ap == nil {
		return
	}
	ap.view = view
	ap.loadError = ""
	ap.now = now
	ap.rows = ap.rows[:0]

	for _, machine := range view.Machines {
		ap.rows = append(ap.rows, agentPanelRow{
			machine: machine.Name, isHeader: true,
			link: machine.Link, linkNote: machine.LinkDetail,
		})
		for _, row := range machine.Agents {
			ap.rows = append(ap.rows, agentPanelRow{machine: machine.Name, agent: row})
		}
	}

	// Keep the cursor on a real agent after a refresh changes the list.
	if ap.cursor >= len(ap.rows) {
		ap.cursor = len(ap.rows) - 1
	}
	if ap.cursor < 0 {
		ap.cursor = 0
	}
	if len(ap.rows) > 0 && ap.rows[ap.cursor].isHeader {
		ap.moveCursor(1)
	}
}

// SetLoadError replaces fleet data with an explicit unknown/error state.
func (ap *AgentsPanel) SetLoadError(err string, now time.Time) {
	if ap == nil {
		return
	}
	ap.view = agents.View{}
	ap.rows = nil
	ap.loadError = agents.SanitizeForDisplay(err)
	ap.now = now
}

// SetRules supplies each agent's policy file names, keyed by agent name.
func (ap *AgentsPanel) SetRules(rules map[string][]string) {
	if ap == nil {
		return
	}
	ap.rulesByAgent = rules
}

// HasAgents reports whether anything has been adopted. The Agents surfaces
// are opt-in by presence: a user with no definitions must see no new UI.
func (ap *AgentsPanel) HasAgents() bool { return ap != nil && ap.view.TotalAgents > 0 }

// Selected returns the highlighted agent, or nil.
func (ap *AgentsPanel) Selected() *agents.AgentRow {
	if ap == nil || ap.cursor < 0 || ap.cursor >= len(ap.rows) {
		return nil
	}
	row := ap.rows[ap.cursor]
	if row.isHeader {
		return nil
	}
	return &row.agent
}

// moveCursor steps by delta, skipping machine headers.
func (ap *AgentsPanel) moveCursor(delta int) {
	if ap == nil || len(ap.rows) == 0 {
		return
	}
	next := ap.cursor
	for i := 0; i < len(ap.rows); i++ {
		next += delta
		if next < 0 || next >= len(ap.rows) {
			return
		}
		if !ap.rows[next].isHeader {
			ap.cursor = next
			return
		}
	}
}

// Update handles keyboard input.
func (ap *AgentsPanel) Update(msg tea.Msg) (*AgentsPanel, tea.Cmd) {
	if ap == nil || !ap.visible {
		return ap, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return ap, nil
	}

	switch key.String() {
	case "esc", "q", "alt+a":
		if ap.detailMode {
			ap.detailMode = false
		} else {
			ap.Hide()
		}
	case "j", "down", "ctrl+n":
		if !ap.detailMode {
			ap.moveCursor(1)
		}
	case "k", "up", "ctrl+p":
		if !ap.detailMode {
			ap.moveCursor(-1)
		}
	case "enter", "l":
		if !ap.detailMode && ap.Selected() != nil {
			ap.detailMode = true
		}
	case "h", "backspace":
		ap.detailMode = false
	}
	return ap, nil
}

// View renders the panel.
func (ap *AgentsPanel) View() string {
	if ap == nil || !ap.visible {
		return ""
	}
	width := ap.width - 4
	if width < 40 {
		width = 40
	}
	if width > 120 {
		width = 120
	}

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1).
		Width(width)

	// The border style reserves two columns of padding, so the rules drawn
	// inside it must be narrower than the box or they wrap onto a stub line.
	inner := width - 2
	if inner < 20 {
		inner = 20
	}
	if ap.detailMode {
		return borderStyle.Render(ap.renderDetail(inner))
	}
	return borderStyle.Render(ap.renderList(inner))
}

func (ap *AgentsPanel) renderList(width int) string {
	var sb strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	summary := fmt.Sprintf("%d agents", ap.view.TotalAgents)
	if ap.loadError != "" {
		summary = "status unknown"
	}
	if ap.view.NeedAttention > 0 {
		summary += fmt.Sprintf(" · %d need attention", ap.view.NeedAttention)
	}
	sb.WriteString(titleStyle.Render("AGENTS"))
	sb.WriteString("  ")
	sb.WriteString(dimStyle.Render(summary))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", width))
	sb.WriteString("\n")

	if ap.loadError != "" {
		alertStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		sb.WriteString(alertStyle.Render("  ERROR: agents registry could not be loaded"))
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("  " + truncateStr(ap.loadError, width-2)))
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("─", width))
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("[Esc] Close"))
		return sb.String()
	}

	if len(ap.rows) == 0 {
		sb.WriteString(dimStyle.Render("  Nothing adopted yet."))
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("  agent-deck agent adopt <conductor-dir|session|plist|unit>"))
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("─", width))
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("[Esc] Close"))
		return sb.String()
	}

	machineStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	selectedStyle := lipgloss.NewStyle().Background(ColorSurface).Foreground(ColorText).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(ColorText)
	alertStyle := lipgloss.NewStyle().Foreground(ColorRed)

	visible := ap.height - 8
	if visible < 5 {
		visible = 5
	}
	ap.clampScroll(visible)

	end := ap.scroll + visible
	if end > len(ap.rows) {
		end = len(ap.rows)
	}

	for i := ap.scroll; i < end; i++ {
		row := ap.rows[i]
		if row.isHeader {
			header := " " + strings.ToUpper(row.machine)
			sb.WriteString(machineStyle.Render(header))
			if note := ap.linkNoteFor(row); note != "" {
				sb.WriteString(dimStyle.Render("   " + note))
			}
			sb.WriteString("\n")
			continue
		}

		line := ap.renderAgentLine(row.agent, width)
		if i == ap.cursor {
			sb.WriteString(selectedStyle.Render(line))
		} else {
			sb.WriteString(normalStyle.Render(line))
		}
		sb.WriteString("\n")

		if row.agent.Attention != "" {
			sb.WriteString(alertStyle.Render("   ! " + truncateStr(agents.SanitizeForDisplay(row.agent.Attention), width-6)))
			sb.WriteString("\n")
		}
	}

	sb.WriteString(strings.Repeat("─", width))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("[Enter] Details  [j/k] Move  [Esc] Close"))
	return sb.String()
}

// linkNoteFor renders a machine's link health. An unreachable remote says so
// loudly: its rows are the last thing we saw, not the current truth.
func (ap *AgentsPanel) linkNoteFor(row agentPanelRow) string {
	switch row.link {
	case agents.LinkUnconfirmed:
		if row.linkNote != "" {
			return "— UNCONFIRMED: " + row.linkNote
		}
		return "— UNCONFIRMED: unreachable"
	case agents.LinkOK:
		if row.linkNote != "" {
			return "— link ok, " + agents.SanitizeForDisplay(row.linkNote)
		}
		return "— link ok"
	case agents.LinkNotContacted:
		return "— not contacted; placement only"
	default:
		return ""
	}
}

func (ap *AgentsPanel) renderAgentLine(row agents.AgentRow, width int) string {
	glyph := agentStateGlyph(row.State)
	role := row.Role
	if row.Class != agents.ClassAgent {
		role = string(row.Class)
	}

	nameWidth := 18
	line := fmt.Sprintf(" %s %-*s %-11s %-10s",
		glyph, nameWidth, truncateStr(agents.SanitizeForDisplay(row.Name), nameWidth),
		truncateStr(agents.SanitizeForDisplay(role), 11), truncateStr(string(row.State), 10))

	if last := agents.FormatLastDid(row, ap.now); last != "" {
		line += "  last: " + truncateStr(last, 30)
	}
	if next := agents.FormatNextDue(row); next != "" {
		line += "  next: " + truncateStr(next, 14)
	}
	// Measure by display cells, not bytes: `line` starts with a lipgloss
	// glyph carrying an SGR escape, so len() counted ~20 invisible bytes and
	// truncated the row that much too early at every width. cellWidth and
	// cellTruncate are the repo's ANSI-aware helpers, already used by the
	// session-row renderer in home.go.
	if cellWidth(line) > width {
		line = cellTruncate(line, width, "…")
	}
	return line
}

func (ap *AgentsPanel) clampScroll(visible int) {
	if ap.cursor < ap.scroll {
		ap.scroll = ap.cursor
	}
	if ap.cursor >= ap.scroll+visible {
		ap.scroll = ap.cursor - visible + 1
	}
	if ap.scroll < 0 {
		ap.scroll = 0
	}
}

// renderDetail renders one agent's desk: triggers, connectors, recent work and
// rules. Every trigger row names who actually fires it.
func (ap *AgentsPanel) renderDetail(width int) string {
	selected := ap.Selected()
	if selected == nil {
		return ap.renderList(width)
	}
	row := *selected
	ap.rules = ap.rulesByAgent[row.Name]

	var sb strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorCyan)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)

	// Every field on this header comes from a definition file, so all of it
	// is untrusted text. An unsanitized post id or role name could erase the
	// panel border and forge a "link ok" line — the exact string this feature
	// exists to make trustworthy.
	roleLine := agents.SanitizeForDisplay(row.Role)
	if row.RoleVersion != "" {
		roleLine += " " + agents.SanitizeForDisplay(row.RoleVersion)
	}
	sb.WriteString(titleStyle.Render(" " + agents.SanitizeForDisplay(row.Name)))
	sb.WriteString(dimStyle.Render(fmt.Sprintf("  ·  role: %s  ·  %s  ·  %s",
		roleLine, agents.SanitizeForDisplay(harnessLabel(row)), agents.SanitizeForDisplay(row.Machine))))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render(fmt.Sprintf(" post %s  ·  reports to %s  ·  %s",
		agents.SanitizeForDisplay(row.PostID), agents.SanitizeForDisplay(row.ReportsTo), row.State)))
	sb.WriteString("\n")
	if row.ReportsToIssue != "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(ColorRed).
			Render(" ! " + truncateStr(agents.SanitizeForDisplay(row.ReportsToIssue), width-4)))
		sb.WriteString("\n")
	}
	sb.WriteString(strings.Repeat("─", width))
	sb.WriteString("\n")

	sb.WriteString(sectionStyle.Render(" TRIGGERS"))
	sb.WriteString("\n")
	if len(row.Triggers) == 0 {
		sb.WriteString(dimStyle.Render("   none declared"))
		sb.WriteString("\n")
	} else {
		for _, t := range row.Triggers {
			owner := "external"
			if !t.External {
				owner = "ARMED HERE"
			}
			sb.WriteString(fmt.Sprintf("   %s %-20s %-14s %s\n",
				triggerGlyph(t), truncateStr(agents.SanitizeForDisplay(t.Name), 20),
				truncateStr(agents.SanitizeForDisplay(t.NextDueText), 14),
				dimStyle.Render("["+owner+"]")))
			if t.Note != "" {
				sb.WriteString(dimStyle.Render("       " + truncateStr(agents.SanitizeForDisplay(t.Note), width-8)))
				sb.WriteString("\n")
			}
		}
	}
	sb.WriteString("\n")

	sb.WriteString(sectionStyle.Render(" CONNECTORS"))
	sb.WriteString("\n")
	if len(row.Connectors) == 0 {
		sb.WriteString(dimStyle.Render("   none bound"))
		sb.WriteString("\n")
	} else {
		for _, c := range row.Connectors {
			sb.WriteString(fmt.Sprintf("   %s %-16s %s\n",
				healthDot(c.State), truncateStr(c.Name, 16),
				truncateStr(agents.SanitizeForDisplay(c.Detail), width-24)))
		}
	}
	sb.WriteString("\n")

	sb.WriteString(sectionStyle.Render(" RECENT WORK"))
	sb.WriteString("\n")
	if len(row.Recent) == 0 {
		sb.WriteString(dimStyle.Render("   nothing recorded"))
		sb.WriteString("\n")
	} else {
		for _, entry := range row.Recent {
			sb.WriteString(fmt.Sprintf("   %s  %s\n",
				entry.At.Format("15:04"), truncateStr(agents.SanitizeForDisplay(entry.Summary), width-12)))
		}
	}

	if len(ap.rules) > 0 {
		sb.WriteString("\n")
		sb.WriteString(sectionStyle.Render(" RULES"))
		sb.WriteString("\n")
		for _, rule := range ap.rules {
			sb.WriteString("   · ")
			sb.WriteString(valueStyle.Render(truncateStr(agents.SanitizeForDisplay(rule), width-8)))
			sb.WriteString("\n")
		}
	}

	if len(row.Unresolved) > 0 {
		sb.WriteString("\n")
		sb.WriteString(sectionStyle.Render(" UNRESOLVED"))
		sb.WriteString("\n")
		for _, item := range row.Unresolved {
			sb.WriteString(dimStyle.Render("   · " + truncateStr(agents.SanitizeForDisplay(item), width-6)))
			sb.WriteString("\n")
		}
	}

	sb.WriteString(strings.Repeat("─", width))
	sb.WriteString("\n")
	footer := "Read-only. agent-deck displays this; it does not run it.  [h/Esc] Back"
	if row.LoadError != "" {
		footer = "This definition could not be read.  [h/Esc] Back"
	} else if len(row.Violations) > 0 || row.Armed() {
		footer = "This record claims to be ARMED, which phase 1 never emits.  [h/Esc] Back"
	}
	sb.WriteString(dimStyle.Render(footer))
	return sb.String()
}

func harnessLabel(row agents.AgentRow) string {
	if row.Harness == "" {
		return "no harness"
	}
	if row.Account != "" {
		return row.Harness + " / " + row.Account
	}
	return row.Harness
}

// agentStateGlyph maps a run state to the list glyph.
func agentStateGlyph(state agents.RunState) string {
	switch state {
	case agents.RunWorking:
		return lipgloss.NewStyle().Foreground(ColorGreen).Render("●")
	case agents.RunIdle:
		return lipgloss.NewStyle().Foreground(ColorGreen).Render("●")
	case agents.RunNeedsYou:
		return lipgloss.NewStyle().Foreground(ColorYellow).Render("◐")
	case agents.RunError:
		return lipgloss.NewStyle().Foreground(ColorRed).Render("✕")
	case agents.RunStopped:
		return lipgloss.NewStyle().Foreground(ColorTextDim).Render("○")
	case agents.RunNoRuntime:
		return lipgloss.NewStyle().Foreground(ColorTextDim).Render("◍")
	default:
		return lipgloss.NewStyle().Foreground(ColorTextDim).Render("?")
	}
}

// triggerGlyph marks whether a trigger is armed here. In phase 1 none are, so
// this is always the dim external marker — but it is computed, not hardcoded,
// so the day a trigger really is owned here the glyph changes with it.
func triggerGlyph(t agents.TriggerRow) string {
	if t.Enabled && !t.External {
		return lipgloss.NewStyle().Foreground(ColorGreen).Render("●")
	}
	return lipgloss.NewStyle().Foreground(ColorTextDim).Render("○")
}

func healthDot(state agents.HealthState) string {
	switch state {
	case agents.HealthOK:
		return lipgloss.NewStyle().Foreground(ColorGreen).Render("●")
	case agents.HealthStale:
		return lipgloss.NewStyle().Foreground(ColorYellow).Render("◐")
	case agents.HealthDown:
		return lipgloss.NewStyle().Foreground(ColorRed).Render("●")
	default:
		return lipgloss.NewStyle().Foreground(ColorTextDim).Render("○")
	}
}
