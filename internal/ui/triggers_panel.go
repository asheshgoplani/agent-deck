package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/agents"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// TriggersPanel lists what wakes sessions (#2702): launchd jobs, systemd
// units, watchers, bridges and other senders that tag their sends, grouped
// by host and kind, with a health glyph per row. It is read-only in this
// phase. Enter on a row narrows it to "what wakes <target>"; the same view
// opens from a session row that shows a ⚡ badge.
//
// The panel never reads storage: Home's background poll (triggers_live.go)
// hands it views through SetViews, and every key only filters those.
// Every string in a view came from another process (a unit file, a sender's
// --source, a message line), so it passes through SanitizeForDisplay before
// it is measured or drawn.
type TriggersPanel struct {
	visible      bool
	width        int
	height       int
	cursor       int
	host         string
	views        []triggers.View
	rows         []triggers.View
	loaded       bool
	loadErr      string
	problemsOnly bool
	allKinds     bool
	reverseID    string
	reverseTitle string
	// listCursorID is the row the full list was on when enter opened a
	// reverse view, so esc lands back on it.
	listCursorID string
	status       string
	now          func() time.Time
}

func NewTriggersPanel() *TriggersPanel { return &TriggersPanel{now: time.Now} }

// Show opens the full list.
func (p *TriggersPanel) Show() {
	if p == nil {
		return
	}
	p.visible = true
	p.reverseID, p.reverseTitle, p.listCursorID = "", "", ""
	p.status = ""
	p.cursor = 0
	p.rebuild("")
}

// ShowReverse opens on "what wakes <title>".
func (p *TriggersPanel) ShowReverse(sessionID, title string) {
	if p == nil {
		return
	}
	p.visible = true
	p.status = ""
	p.listCursorID = ""
	p.reverseID, p.reverseTitle = sessionID, title
	p.cursor = 0
	p.rebuild("")
}

func (p *TriggersPanel) Hide() {
	if p != nil {
		p.visible = false
	}
}

func (p *TriggersPanel) IsVisible() bool { return p != nil && p.visible }

func (p *TriggersPanel) SetSize(width, height int) {
	if p != nil {
		p.width, p.height = width, height
	}
}

// SetHost names this machine in the header when no row says otherwise.
func (p *TriggersPanel) SetHost(host string) {
	if p != nil {
		p.host = host
	}
}

// SetViews replaces the loaded triggers, keeping the cursor on the same
// trigger when it is still listed. A load error keeps the last good views.
func (p *TriggersPanel) SetViews(views []triggers.View, err error) {
	if p == nil {
		return
	}
	if err != nil {
		p.loadErr = err.Error()
		return
	}
	p.loadErr = ""
	p.loaded = true
	keep := ""
	if sel := p.Selected(); sel != nil {
		keep = sel.ID
	}
	p.views = views
	p.rebuild(keep)
}

// Selected is the row under the cursor, or nil when the list is empty.
func (p *TriggersPanel) Selected() *triggers.View {
	if p == nil || p.cursor < 0 || p.cursor >= len(p.rows) {
		return nil
	}
	return &p.rows[p.cursor]
}

func (p *TriggersPanel) filter() triggers.Filter {
	f := triggers.Filter{SessionID: p.reverseID, All: p.allKinds}
	if p.problemsOnly {
		f.Health = "problems"
	}
	return f
}

// rebuild reapplies the filters and puts the cursor on keepID when listed.
func (p *TriggersPanel) rebuild(keepID string) {
	p.rows = p.filter().Apply(p.views)
	if keepID != "" {
		for i, v := range p.rows {
			if v.ID == keepID {
				p.cursor = i
				break
			}
		}
	}
	if p.cursor >= len(p.rows) {
		p.cursor = len(p.rows) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func (p *TriggersPanel) leaveReverse() {
	keep := p.listCursorID
	p.reverseID, p.reverseTitle, p.listCursorID = "", "", ""
	p.cursor = 0
	p.rebuild(keep)
}

func (p *TriggersPanel) Update(msg tea.Msg) (*TriggersPanel, tea.Cmd) {
	if p == nil || !p.visible {
		return p, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	p.status = ""
	switch key.String() {
	case "esc":
		if p.reverseID != "" {
			p.leaveReverse()
		} else {
			p.Hide()
		}
	case "q", "alt+t":
		p.Hide()
	case "h", "backspace", "left":
		if p.reverseID != "" {
			p.leaveReverse()
		}
	case "j", "down", "ctrl+n":
		if p.cursor+1 < len(p.rows) {
			p.cursor++
		}
	case "k", "up", "ctrl+p":
		if p.cursor > 0 {
			p.cursor--
		}
	case "g", "home":
		p.cursor = 0
	case "G", "end":
		p.cursor = max(0, len(p.rows)-1)
	case "enter", "l":
		sel := p.Selected()
		if sel == nil {
			return p, nil
		}
		if sel.Target.SessionID == "" {
			p.status = fmt.Sprintf("%q: no session has this name", clean(sel.Target.AsWritten))
			return p, nil
		}
		if p.reverseID == "" {
			p.listCursorID = sel.ID
		}
		p.reverseID = sel.Target.SessionID
		p.reverseTitle = sel.Target.Title
		if p.reverseTitle == "" {
			p.reverseTitle = sel.Target.AsWritten
		}
		p.cursor = 0
		p.rebuild("")
	case "p":
		p.problemsOnly = !p.problemsOnly
		p.rebuild(p.selectedID())
	case "a":
		p.allKinds = !p.allKinds
		p.rebuild(p.selectedID())
	}
	return p, nil
}

func (p *TriggersPanel) selectedID() string {
	if sel := p.Selected(); sel != nil {
		return sel.ID
	}
	return ""
}

// clean makes a foreign string safe to draw: no control characters, no
// escape sequences (#2702 privacy and display rules).
func clean(s string) string { return agents.SanitizeForDisplay(s) }

// triggerCols are the column widths of one row, computed from the inner
// width of the box.
type triggerCols struct{ name, wakes, every int }

const (
	triggerLead    = 4 // "  ● " or "▶ ● "
	triggerLastW   = 6
	triggerNoiseW  = 5
	triggerMaxBoxW = 120
)

func triggerColumns(inner int) triggerCols {
	flex := inner - triggerLead - triggerLastW - triggerNoiseW - 4
	clamp := func(v, lo, hi int) int { return max(lo, min(hi, v)) }
	c := triggerCols{
		name:  clamp(flex*40/100, 6, 32),
		wakes: clamp(flex*36/100, 6, 32),
	}
	c.every = clamp(flex-c.name-c.wakes, 4, 20)
	return c
}

// fitCell truncates by terminal cells (not bytes or runes) and pads to w.
func fitCell(s string, w int) string {
	s = cellTruncate(s, w, "…")
	if pad := w - cellWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func triggerHealthGlyph(health string) (string, lipgloss.Style) {
	switch health {
	case triggers.HealthOK:
		return "●", lipgloss.NewStyle().Foreground(ColorGreen)
	case triggers.HealthFailing:
		return "●", lipgloss.NewStyle().Foreground(ColorYellow)
	case triggers.HealthDeadTarget:
		return "✗", lipgloss.NewStyle().Foreground(ColorRed)
	case triggers.HealthNeverFired:
		return "○", lipgloss.NewStyle().Foreground(ColorTextDim)
	}
	return "?", lipgloss.NewStyle().Foreground(ColorTextDim)
}

// triggerAge is the LAST column: how long ago, compact.
func triggerAge(now time.Time, last *time.Time) string {
	if last == nil {
		return "never"
	}
	d := now.Sub(*last)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

func triggerWakes(v triggers.View) string {
	switch {
	case v.Target.Resolved && v.Target.Title != "":
		return clean(v.Target.Title)
	case v.Target.AsWritten != "":
		return clean(v.Target.AsWritten)
	}
	return "–"
}

func triggerNoise(v triggers.View) (string, bool) {
	if v.NoopShare24h == nil {
		return "–", false
	}
	share := *v.NoopShare24h
	return fmt.Sprintf("%d%%", int(share*100+0.5)), share >= triggers.NoisyShare
}

// renderRow draws one trigger row within inner cells.
func (p *TriggersPanel) renderRow(v triggers.View, selected bool, c triggerCols, now time.Time) string {
	glyph, glyphStyle := triggerHealthGlyph(v.Health)
	name := fitCell(clean(v.Source), c.name)
	wakes := fitCell(triggerWakes(v), c.wakes)
	every := v.Schedule
	if every == "" {
		every = "–"
	}
	everyCell := fitCell(clean(every), c.every)
	last := fitCell(triggerAge(now, v.LastFire), triggerLastW)
	noise, noisy := triggerNoise(v)
	noiseCell := fitCell(noise, triggerNoiseW)

	if selected {
		plain := "▶ " + glyph + " " + name + " " + wakes + " " + everyCell + " " + last + " " + noiseCell
		return lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent).Render(plain)
	}
	wakesStyled := wakes
	if v.Health == triggers.HealthDeadTarget {
		wakesStyled = lipgloss.NewStyle().Foreground(ColorRed).Render(wakes)
	}
	noiseStyled := noiseCell
	if noisy {
		noiseStyled = lipgloss.NewStyle().Foreground(ColorYellow).Render(noiseCell)
	}
	return "  " + glyphStyle.Render(glyph) + " " + name + " " + wakesStyled + " " + everyCell + " " + last + " " + noiseStyled
}

// listLines renders the list body (group headers and rows) and reports the
// line index of the cursor row.
func (p *TriggersPanel) listLines(c triggerCols, now time.Time) ([]string, int) {
	accent := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	var lines []string
	cursorLine := 0
	grouped := p.reverseID == ""
	hostCount := map[string]int{}
	for _, v := range p.rows {
		hostCount[v.Host]++
	}
	lastHost, lastKind := "\x00", "\x00"
	for i, v := range p.rows {
		if grouped {
			if v.Host != lastHost {
				lines = append(lines, accent.Render(fmt.Sprintf("▾ %s (%d)", clean(v.Host), hostCount[v.Host])))
				lastHost, lastKind = v.Host, "\x00"
			}
			if v.Kind != lastKind {
				lines = append(lines, dim.Render("  "+clean(v.Kind)))
				lastKind = v.Kind
			}
		}
		if i == p.cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, p.renderRow(v, i == p.cursor, c, now))
	}
	return lines, cursorLine
}

// header is the title line: "Triggers  <host> · N · N dead · N failing".
func (p *TriggersPanel) header() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	var parts []string
	var head string
	if p.reverseID != "" {
		head = title.Render("What wakes " + clean(p.reverseTitle))
		noun := "triggers"
		if len(p.rows) == 1 {
			noun = "trigger"
		}
		parts = append(parts, fmt.Sprintf("%d %s", len(p.rows), noun))
	} else {
		base := triggers.Filter{All: p.allKinds}.Apply(p.views)
		s := triggers.Summarize(base)
		head = title.Render("Triggers")
		parts = append(parts, p.hostLabel(base), fmt.Sprintf("%d", s.Total),
			fmt.Sprintf("%d dead", s.DeadTargets), fmt.Sprintf("%d failing", s.Failing))
	}
	if p.problemsOnly {
		parts = append(parts, "problems only")
	}
	if p.allKinds {
		parts = append(parts, "all kinds")
	}
	return head + "  " + dim.Render(strings.Join(parts, " · "))
}

// hostLabel names the machines the list covers: the one host every row
// came from, "N machines" when several, else this machine.
func (p *TriggersPanel) hostLabel(views []triggers.View) string {
	hosts := map[string]bool{}
	var one string
	for _, v := range views {
		if !hosts[v.Host] {
			hosts[v.Host] = true
			one = v.Host
		}
	}
	switch len(hosts) {
	case 0:
		if p.host == "" {
			return "local"
		}
		return clean(p.host)
	case 1:
		return clean(one)
	}
	return fmt.Sprintf("%d machines", len(hosts))
}

// detail is the two lines under the list for the selected row.
func (p *TriggersPanel) detail() [2]string {
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	red := lipgloss.NewStyle().Foreground(ColorRed)
	yellow := lipgloss.NewStyle().Foreground(ColorYellow)
	var out [2]string
	sel := p.Selected()
	if sel == nil {
		return out
	}
	name := clean(sel.Source)
	switch sel.Health {
	case triggers.HealthDeadTarget:
		target := clean(sel.Target.AsWritten)
		if target == "" {
			out[0] = red.Render(name) + " sent to a session that no longer exists"
		} else {
			out[0] = red.Render(name) + fmt.Sprintf(" sends to %q: no session has this name", target)
		}
	case triggers.HealthFailing:
		out[0] = yellow.Render(name) + " → " + triggerWakes(*sel) + ": the last send failed"
	case triggers.HealthNeverFired:
		out[0] = name + " → " + triggerWakes(*sel) + ": has not fired yet"
	default:
		out[0] = name + " → " + triggerWakes(*sel)
		if noise, noisy := triggerNoise(*sel); noisy {
			out[0] += yellow.Render(" · " + noise + " no-op in 24h")
		}
		if sel.LastLine != "" {
			out[0] += dim.Render(fmt.Sprintf("  last: %q", clean(sel.LastLine)))
		}
	}
	if sel.ConfigPath != "" {
		out[1] = dim.Render("config " + clean(sel.ConfigPath))
	} else {
		out[1] = dim.Render("no config file known (seen from its tagged sends)")
	}
	return out
}

func (p *TriggersPanel) View() string {
	if p == nil || !p.visible {
		return ""
	}
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	boxW := min(max(p.width-2, 44), triggerMaxBoxW)
	inner := boxW - 4 // border and one cell of padding each side
	c := triggerColumns(inner)
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	rule := dim.Render(strings.Repeat("─", inner))

	var body []string
	body = append(body, p.header(), rule)
	colHead := strings.Repeat(" ", triggerLead) + fitCell("NAME", c.name) + " " + fitCell("WAKES", c.wakes) + " " +
		fitCell("EVERY", c.every) + " " + fitCell("LAST", triggerLastW) + " " + fitCell("NOISE", triggerNoiseW)
	body = append(body, dim.Render(colHead))

	// The detail lines wrap rather than cut: "no session has this name" is
	// the point of a dead-target row, and it must survive 80 columns.
	d := p.detail()
	if p.status != "" {
		d[1] = lipgloss.NewStyle().Foreground(ColorYellow).Render(clean(p.status))
	}
	var detail []string
	for _, line := range d {
		detail = append(detail, strings.Split(ansi.Wordwrap(line, inner, ""), "\n")...)
	}
	if len(detail) > 3 {
		detail = detail[:3]
	}

	// Rows that fit: the frame (borders, header, rule, column header, rule,
	// the detail lines, hint) and one spare line take the rest.
	room := max(3, p.height-8-len(detail))
	var list []string
	switch {
	case p.loadErr != "" && !p.loaded:
		list = []string{lipgloss.NewStyle().Foreground(ColorRed).Render("Unable to read triggers: " + clean(p.loadErr))}
	case !p.loaded:
		list = []string{dim.Render("Loading triggers…")}
	case len(p.rows) == 0 && p.reverseID != "":
		list = []string{"Nothing wakes this session."}
	case len(p.rows) == 0 && p.problemsOnly:
		list = []string{"No problems: every trigger is healthy and quiet."}
	case len(p.rows) == 0:
		list = []string{"No triggers yet.",
			dim.Render("Jobs appear here once they send with --source,"),
			dim.Render("or after `agent-deck triggers scan`.")}
	default:
		lines, cursorLine := p.listLines(c, now)
		start := 0
		if cursorLine >= room {
			start = cursorLine - room + 1
		}
		end := min(len(lines), start+room)
		list = lines[start:end]
	}
	body = append(body, list...)
	body = append(body, rule)
	body = append(body, detail...)
	hint := "j/k move  enter what wakes it  p problems  a all kinds  esc close"
	if p.reverseID != "" {
		hint = "j/k move  enter what wakes it  p problems  a all kinds  esc all triggers"
	}
	body = append(body, dim.Render(hint))

	for i, line := range body {
		body[i] = cellTruncate(line, inner, "…")
	}
	box := lipgloss.NewStyle().
		Width(boxW-2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1).
		Render(strings.Join(body, "\n"))
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, box)
}
