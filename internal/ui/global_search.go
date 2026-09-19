package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The Recall search overlay (docs/recall.md, phase 3): the `G` key over
// recall.db. It replaced the in-memory global search index, which opened
// one directory watcher per project and loaded every transcript into
// memory (the OOM the comment at the old init site recorded). Nothing here
// walks a tree or parses a file on a keypress: every keystroke is an FTS
// query against the index as it stands, and the index is refreshed by a
// bounded sweep (150 ms / 32 MB) in a tea.Cmd after the overlay opens,
// continued in further bounded passes through the busy/load gate while
// anything was deferred. The header says how stale the index is.
//
// Everything the overlay can do has a CLI form: typing = `recall search`,
// the preview = `recall show`, Enter = `recall open`.

var (
	globalSearchBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorCyan).
				Padding(0, 1)

	globalResultStyle = lipgloss.NewStyle().
				Padding(0, 2)

	globalSelectedStyle = lipgloss.NewStyle().
				Padding(0, 2).
				Background(ColorCyan).
				Foreground(ColorBg)

	globalSearchHeaderStyle = lipgloss.NewStyle().
				Foreground(ColorCyan).
				Bold(true)

	highlightStyle = lipgloss.NewStyle().
			Background(ColorYellow).
			Foreground(ColorBg).
			Bold(true)
)

// Overlay limits.
const (
	recallResultLimit  = 15
	recallPreviewTurns = 12
	recallDebounce     = 250 * time.Millisecond
	// recallMaxWidth caps the overlay on wide terminals; below it the
	// overlay takes the terminal width less a margin, so it fits 80 columns.
	recallMaxWidth = 160
)

// recallCatchUpDelay paces the background continuation of a deferred
// sweep: one bounded pass, then a pause, then the next through the gate,
// so a large backlog never holds a core while the user types. Tests
// shorten it.
var recallCatchUpDelay = time.Second

// GlobalSearchResult is one ranked session as the overlay shows it and as
// Home receives it on Enter.
type GlobalSearchResult struct {
	SessID    int64
	Harness   string
	Profile   string
	SessionID string // the harness conversation id
	DeckID    string // bound agent-deck session, when any
	Title     string
	Snippet   string
	CWD       string
	EndedAt   time.Time
	BodyHits  int
	CardHit   bool
	Missing   bool
	Sidechain bool
	// InAgentDeck / InstanceID: a registered session owns this conversation.
	InAgentDeck bool
	InstanceID  string
}

// globalSearchResultsMsg delivers async search results back to the UI.
type globalSearchResultsMsg struct {
	query string
	res   query.SearchResult
	err   error
}

// globalSearchDebounceMsg fires after the debounce interval.
type globalSearchDebounceMsg struct {
	query string
}

// recallPreviewMsg delivers the preview of one session.
type recallPreviewMsg struct {
	sessID int64
	detail query.Detail
	err    error
}

// recallRefreshMsg reports one bounded sweep pass.
type recallRefreshMsg struct {
	res   ingest.Result
	err   error
	gated bool
}

// recallStatusMsg delivers the index summary for the header.
type recallStatusMsg struct {
	status query.Status
	err    error
}

// recallCatchUpMsg fires after recallCatchUpDelay: run the next gated pass.
type recallCatchUpMsg struct{}

// GlobalSearch is the Recall search overlay.
type GlobalSearch struct {
	input         textinput.Model
	results       []*GlobalSearchResult
	cursor        int
	width         int
	height        int
	visible       bool
	switchToLocal bool
	previewScroll int
	query         string
	searching     bool
	searchErr     string
	candidates    int
	ceilingHit    bool

	source   RecallSource
	status   query.Status
	hasStat  bool
	refresh  string // staleness line: what the last pass deferred or skipped
	sweeping bool
	preview  map[int64]query.Detail
	previewT map[int64]bool   // preview requested
	previewE map[int64]string // preview failed: what `recall show` answered
}

// NewGlobalSearch creates the overlay without a source; SetSource wires it.
func NewGlobalSearch() *GlobalSearch {
	ti := textinput.New()
	ti.Placeholder = "Search every conversation on this machine..."
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 60
	return &GlobalSearch{input: ti, results: []*GlobalSearchResult{}, preview: map[int64]query.Detail{}, previewT: map[int64]bool{}, previewE: map[int64]string{}}
}

// SetSource wires the index the overlay reads (nil disables it).
func (gs *GlobalSearch) SetSource(src RecallSource) { gs.source = src }

// HasSource reports whether the overlay can be opened.
func (gs *GlobalSearch) HasSource() bool { return gs.source != nil }

// SetSize sets the dimensions of the overlay.
func (gs *GlobalSearch) SetSize(width, height int) {
	gs.width = width
	gs.height = height
}

// Show opens the overlay and returns the commands that refresh the header
// and run the first bounded sweep pass.
func (gs *GlobalSearch) Show() tea.Cmd {
	gs.visible = true
	gs.input.Focus()
	gs.input.SetValue("")
	gs.results = nil
	gs.cursor = 0
	gs.switchToLocal = false
	gs.previewScroll = 0
	gs.searching = false
	gs.searchErr = ""
	gs.query = ""
	gs.refresh = ""
	gs.preview = map[int64]query.Detail{}
	gs.previewT = map[int64]bool{}
	gs.previewE = map[int64]string{}
	if gs.source == nil {
		return nil
	}
	gs.sweeping = true
	return tea.Batch(gs.statusCmd(), gs.refreshCmd(false))
}

// WantsSwitchToLocal returns true if user pressed Tab to switch to local search.
func (gs *GlobalSearch) WantsSwitchToLocal() bool {
	if gs.switchToLocal {
		gs.switchToLocal = false
		return true
	}
	return false
}

// Hide hides the overlay and ends the catch-up chain: a tick that
// outlives the overlay finds sweeping false and runs nothing.
func (gs *GlobalSearch) Hide() {
	gs.visible = false
	gs.sweeping = false
	gs.input.Blur()
}

// IsVisible returns whether the overlay is visible.
func (gs *GlobalSearch) IsVisible() bool { return gs.visible }

// Selected returns the currently selected result.
func (gs *GlobalSearch) Selected() *GlobalSearchResult {
	if len(gs.results) == 0 {
		return nil
	}
	if gs.cursor >= len(gs.results) {
		gs.cursor = len(gs.results) - 1
	}
	return gs.results[gs.cursor]
}

func (gs *GlobalSearch) statusCmd() tea.Cmd {
	src := gs.source
	return func() tea.Msg {
		st, err := src.Status(context.Background())
		return recallStatusMsg{status: st, err: err}
	}
}

func (gs *GlobalSearch) refreshCmd(gated bool) tea.Cmd {
	src := gs.source
	return func() tea.Msg {
		res, err := src.Refresh(context.Background(), gated)
		return recallRefreshMsg{res: res, err: err, gated: gated}
	}
}

func (gs *GlobalSearch) searchCmd(q string) tea.Cmd {
	src := gs.source
	return func() tea.Msg {
		res, err := src.Search(context.Background(), q, recallResultLimit)
		return globalSearchResultsMsg{query: q, res: res, err: err}
	}
}

func (gs *GlobalSearch) previewCmd(sessID int64) tea.Cmd {
	src := gs.source
	return func() tea.Msg {
		d, err := src.Show(context.Background(), sessID, recallPreviewTurns)
		return recallPreviewMsg{sessID: sessID, detail: d, err: err}
	}
}

// catchUpCmd schedules the next gated pass after recallCatchUpDelay.
func (gs *GlobalSearch) catchUpCmd() tea.Cmd {
	return tea.Tick(recallCatchUpDelay, func(time.Time) tea.Msg { return recallCatchUpMsg{} })
}

// requestPreview asks for the selected session's preview once.
func (gs *GlobalSearch) requestPreview() tea.Cmd {
	sel := gs.Selected()
	if sel == nil || gs.source == nil || gs.previewT[sel.SessID] {
		return nil
	}
	gs.previewT[sel.SessID] = true
	return gs.previewCmd(sel.SessID)
}

// Update handles messages for the overlay.
func (gs *GlobalSearch) Update(msg tea.Msg) (*GlobalSearch, tea.Cmd) {
	if !gs.visible {
		return gs, nil
	}
	switch msg := msg.(type) {
	case recallStatusMsg:
		if msg.err == nil {
			gs.status, gs.hasStat = msg.status, true
		}
		return gs, nil

	case recallRefreshMsg:
		gs.sweeping = false
		switch {
		case msg.err != nil && recallSkipped(msg.err):
			gs.refresh = "index not refreshed: " + msg.err.Error()
		case msg.err != nil:
			gs.refresh = "index refresh failed: " + msg.err.Error()
		case msg.res.Deferred > 0:
			gs.refresh = fmt.Sprintf("index behind by %d source(s) / %s; catching up in the background", msg.res.Deferred, humanBytes(msg.res.DeferredBytes))
			// Continue in bounded passes, paced and through the gate,
			// while the overlay is open.
			gs.sweeping = true
			return gs, tea.Batch(gs.catchUpCmd(), gs.statusCmd())
		default:
			gs.refresh = ""
		}
		cmds := []tea.Cmd{gs.statusCmd()}
		if gs.query != "" {
			cmds = append(cmds, gs.searchCmd(gs.query)) // re-run on the fresher index
		}
		return gs, tea.Batch(cmds...)

	case recallCatchUpMsg:
		if !gs.sweeping || gs.source == nil {
			return gs, nil // closed and reopened meanwhile: Show restarted the chain
		}
		return gs, gs.refreshCmd(true)

	case globalSearchDebounceMsg:
		if msg.query == gs.input.Value() && msg.query != "" && gs.source != nil {
			gs.searching = true
			return gs, gs.searchCmd(msg.query)
		}
		return gs, nil

	case globalSearchResultsMsg:
		if msg.query != gs.input.Value() {
			return gs, nil
		}
		gs.searching = false
		if msg.err != nil {
			gs.searchErr = msg.err.Error()
			gs.results = nil
			return gs, nil
		}
		gs.searchErr = ""
		gs.applySearchResults(msg.res)
		return gs, gs.requestPreview()

	case recallPreviewMsg:
		if msg.err != nil {
			gs.previewE[msg.sessID] = msg.err.Error()
		} else {
			gs.preview[msg.sessID] = msg.detail
		}
		return gs, nil

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			return gs, gs.move(-1)
		case tea.MouseButtonWheelDown:
			return gs, gs.move(1)
		}
		return gs, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			gs.Hide()
			return gs, nil
		case "enter":
			if len(gs.results) > 0 {
				gs.Hide() // Home handles the selection
			}
			return gs, nil
		case "up":
			return gs, gs.move(-1)
		case "down":
			return gs, gs.move(1)
		case "[", "pgup":
			gs.previewScroll = max(gs.previewScroll-5, 0)
			return gs, nil
		case "]", "pgdown":
			gs.previewScroll += 5
			return gs, nil
		case "tab":
			gs.switchToLocal = true
			gs.Hide()
			return gs, nil
		default:
			var cmd tea.Cmd
			gs.input, cmd = gs.input.Update(msg)
			q := strings.TrimSpace(gs.input.Value())
			if q == gs.query {
				return gs, cmd
			}
			gs.query = q
			if q == "" {
				gs.results = nil
				gs.searching = false
				gs.searchErr = ""
				return gs, cmd
			}
			gs.searching = true
			return gs, tea.Batch(cmd, tea.Tick(recallDebounce, func(time.Time) tea.Msg {
				return globalSearchDebounceMsg{query: gs.input.Value()}
			}))
		}
	}
	return gs, nil
}

// move shifts the cursor and requests the new selection's preview.
func (gs *GlobalSearch) move(delta int) tea.Cmd {
	if len(gs.results) == 0 {
		return nil
	}
	next := gs.cursor + delta
	if next < 0 || next >= len(gs.results) {
		return nil
	}
	gs.cursor = next
	gs.previewScroll = 0
	return gs.requestPreview()
}

// applySearchResults converts ranked hits into rows. The order is the
// query layer's (card hit, body hits, recency); the overlay never re-ranks.
func (gs *GlobalSearch) applySearchResults(res query.SearchResult) {
	gs.results = make([]*GlobalSearchResult, 0, len(res.Hits))
	for _, h := range res.Hits {
		r := &GlobalSearchResult{
			SessID: h.SessID, Harness: h.Harness, Profile: h.Profile, SessionID: h.NativeID, DeckID: h.DeckID,
			Title: h.Title, Snippet: h.Snippet, CWD: h.CWD, BodyHits: h.BodyHits, CardHit: h.CardHit,
			Missing: h.Missing, Sidechain: h.Sidechain,
		}
		r.EndedAt = unixOrZeroTime(firstNonZero(h.EndedAt, h.StartedAt))
		gs.results = append(gs.results, r)
	}
	gs.candidates, gs.ceilingHit = res.Candidates, res.CeilingHit
	gs.cursor = 0
	gs.previewScroll = 0
}

// unixOrZeroTime is time.Unix(ts, 0), with the zero time (not 1970) for a
// missing timestamp.
func unixOrZeroTime(ts int64) time.Time {
	if ts == 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

func firstNonZero(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

// MarkInAgentDeck marks results whose conversation a registered session
// owns: by the bound deck id, or by the Claude session id the instance
// carries.
func (gs *GlobalSearch) MarkInAgentDeck(instances []*session.Instance) {
	byID := map[string]*session.Instance{}
	byClaude := map[string]*session.Instance{}
	for _, inst := range instances {
		byID[inst.ID] = inst
		if inst.ClaudeSessionID != "" {
			byClaude[inst.ClaudeSessionID] = inst
		}
	}
	for _, r := range gs.results {
		if inst := byID[r.DeckID]; inst != nil {
			r.InAgentDeck, r.InstanceID = true, inst.ID
		} else if inst := byClaude[r.SessionID]; inst != nil && r.Harness == reader.HarnessClaude {
			r.InAgentDeck, r.InstanceID = true, inst.ID
		}
	}
}

// View renders the overlay with split-pane layout.
func (gs *GlobalSearch) View() string {
	if !gs.visible {
		return ""
	}
	// Two panes plus four border cells must fit the terminal: the overlay
	// is the terminal width less a margin, capped for wide screens, and
	// never wider than the screen it is drawn on.
	totalWidth := min(max(gs.width-4, 40), recallMaxWidth)
	leftWidth := totalWidth * 38 / 100
	rightWidth := totalWidth - leftWidth - 4
	previewHeight := max(gs.height-12, 10)
	gs.input.Width = max(leftWidth-8, 10)

	var left strings.Builder
	left.WriteString(globalSearchHeaderStyle.Render(gs.headerLine()) + "\n")
	left.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Render(gs.staleLine()) + "\n\n")
	left.WriteString(globalSearchBoxStyle.Width(leftWidth-4).Render(gs.input.View()) + "\n\n")

	switch {
	case gs.searchErr != "":
		left.WriteString(lipgloss.NewStyle().Foreground(ColorRed).Render("  " + gs.searchErr))
	case gs.searching && len(gs.results) == 0:
		left.WriteString(lipgloss.NewStyle().Foreground(ColorYellow).Render("  Searching..."))
	case len(gs.results) == 0 && gs.input.Value() != "":
		left.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Render("  No results"))
	case len(gs.results) == 0:
		left.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Italic(true).Render("  Type to search; titles, hints and tags rank first"))
	default:
		summary := fmt.Sprintf("  %d matching message(s)", gs.candidates)
		if gs.ceilingHit {
			summary += " (capped; narrow the query)"
		}
		left.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Render(summary) + "\n")
		for i, r := range gs.results {
			title := clipCells(firstNonEmpty(r.Title, r.Snippet, r.SessionID), max(leftWidth-14, 20))
			prefix := "  "
			if r.InAgentDeck {
				prefix = "• "
			}
			if i == gs.cursor {
				left.WriteString(globalSelectedStyle.Render("› "+title) + "\n")
				left.WriteString(lipgloss.NewStyle().Foreground(ColorPurple).Render("    "+gs.resultMeta(r)) + "\n")
			} else {
				left.WriteString(globalResultStyle.Render(prefix+title) + "\n")
			}
		}
	}
	left.WriteString("\n")
	left.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Render("[↑↓] Select  [Enter] Open  [Tab] Local\n[ ] scroll preview   [Esc] Cancel"))

	var right strings.Builder
	if sel := gs.Selected(); sel != nil {
		right.WriteString(lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Render("📄 "+firstNonEmpty(sel.Title, "(untitled)")) + "\n")
		meta := sel.Harness
		if sel.Profile != "" {
			meta += "/" + sel.Profile
		}
		meta += "  " + shortNative(sel.SessionID)
		if sel.DeckID != "" {
			meta += "  deck:" + sel.DeckID
		}
		right.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Render(meta) + "\n")
		if sel.CWD != "" {
			right.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Render("📁 "+clipLeft(sel.CWD, rightWidth-5)) + "\n")
		}
		right.WriteString("\n")
		lines := gs.previewLines(sel, rightWidth-2)
		visible := previewHeight - 5
		start := min(gs.previewScroll, max(len(lines)-visible, 0))
		gs.previewScroll = start
		end := min(start+visible, len(lines))
		for _, l := range lines[start:end] {
			right.WriteString(l + "\n")
		}
		if len(lines) > visible {
			right.WriteString("\n" + lipgloss.NewStyle().Foreground(ColorComment).Render(fmt.Sprintf("─── %d/%d lines ───", start+1, len(lines))))
		}
	} else {
		right.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Italic(true).Render("Select a result to preview"))
	}

	leftStyle := lipgloss.NewStyle().Width(leftWidth).Height(previewHeight+6).
		BorderStyle(lipgloss.RoundedBorder()).BorderForeground(ColorAccent).Padding(0, 1)
	rightStyle := lipgloss.NewStyle().Width(rightWidth).Height(previewHeight+6).
		BorderStyle(lipgloss.RoundedBorder()).BorderForeground(ColorCyan).Padding(0, 1)
	combined := lipgloss.JoinHorizontal(lipgloss.Top, leftStyle.Render(left.String()), rightStyle.Render(right.String()))
	return centerInScreen(combined, gs.width, gs.height)
}

// headerLine names the overlay and the index size.
func (gs *GlobalSearch) headerLine() string {
	if !gs.hasStat {
		return "🔍 Recall"
	}
	return fmt.Sprintf("🔍 Recall (%d sessions, %d messages)", gs.status.Sessions, gs.status.Messages)
}

// staleLine is the staleness indicator: what the bounded sweep left
// behind, or when the index was last swept.
func (gs *GlobalSearch) staleLine() string {
	switch {
	case gs.sweeping && gs.refresh == "":
		return "refreshing the index (bounded: 150 ms / 32 MB per pass)..."
	case gs.refresh != "":
		return gs.refresh
	case gs.hasStat && gs.status.LastSweep > 0:
		return "index swept " + humanizeSince(time.Since(time.Unix(gs.status.LastSweep, 0)))
	}
	return "index as on disk"
}

// resultMeta is the line under the selected row: date, harness, hits.
func (gs *GlobalSearch) resultMeta(r *GlobalSearchResult) string {
	parts := []string{}
	if !r.EndedAt.IsZero() {
		parts = append(parts, humanizeSince(time.Since(r.EndedAt)))
	}
	parts = append(parts, r.Harness)
	if r.CardHit {
		parts = append(parts, "title/hint")
	}
	if r.BodyHits > 0 {
		parts = append(parts, fmt.Sprintf("%d in body", r.BodyHits))
	}
	if r.Missing {
		parts = append(parts, "file missing")
	}
	return strings.Join(parts, " • ")
}

// previewLines renders the selected session's preview: the snippet first,
// then the card and the first turns once `recall show` answered.
func (gs *GlobalSearch) previewLines(sel *GlobalSearchResult, width int) []string {
	var lines []string
	if sel.Snippet != "" {
		for _, l := range gs.wrapText(sel.Snippet, width) {
			lines = append(lines, gs.highlightMatches(l, gs.query))
		}
		lines = append(lines, "")
	}
	d, ok := gs.preview[sel.SessID]
	if !ok {
		if why, failed := gs.previewE[sel.SessID]; failed {
			return append(lines, lipgloss.NewStyle().Foreground(ColorRed).Render("preview failed: "+why))
		}
		return append(lines, lipgloss.NewStyle().Foreground(ColorComment).Italic(true).Render("loading turns..."))
	}
	s := d.Session
	lines = append(lines, lipgloss.NewStyle().Foreground(ColorComment).Render(
		fmt.Sprintf("%d turns • %d tool calls • %d errors • model %s", s.Turns, s.ToolCalls, s.Errors, firstNonEmpty(s.Model, "-"))))
	if s.Hints != "" || s.Tags != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorComment).Render("hints: "+firstNonEmpty(s.Hints, "-")+"  tags: "+firstNonEmpty(s.Tags, "-")))
	}
	lines = append(lines, "")
	for _, m := range d.Messages {
		prefix, color := "🤖 ", ColorCyan
		if m.Role == "user" {
			prefix, color = "👤 ", ColorGreen
		}
		text := strings.Join(strings.Fields(m.Text), " ")
		for i, w := range gs.wrapText(text, width-len(prefix)) {
			if i == 0 {
				lines = append(lines, lipgloss.NewStyle().Foreground(color).Render(prefix)+gs.highlightMatches(w, gs.query))
			} else {
				lines = append(lines, "   "+gs.highlightMatches(w, gs.query))
			}
		}
	}
	if d.Truncated > 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorComment).Render(fmt.Sprintf("… %d more; agent-deck recall show %s", d.Truncated, query.Ref(sel.SessID))))
	}
	return lines
}

// wrapText wraps text at word boundaries to fit within maxWidth.
func (gs *GlobalSearch) wrapText(text string, maxWidth int) []string {
	if maxWidth < 10 {
		maxWidth = 10
	}
	if len(text) <= maxWidth {
		return []string{text}
	}
	var lines []string
	var cur strings.Builder
	for _, word := range strings.Fields(text) {
		switch {
		case cur.Len() == 0:
			cur.WriteString(word)
		case cur.Len()+1+len(word) <= maxWidth:
			cur.WriteString(" ")
			cur.WriteString(word)
		default:
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(word)
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// highlightMatches highlights every query term in text (case-insensitive).
func (gs *GlobalSearch) highlightMatches(text, q string) string {
	if q == "" || text == "" {
		return text
	}
	out := text
	for _, term := range strings.Fields(q) {
		term = strings.Trim(term, `"*`)
		if term == "" || term == "AND" || term == "OR" || term == "NOT" {
			continue
		}
		out = highlightTerm(out, term)
	}
	return out
}

func highlightTerm(text, term string) string {
	lower := strings.ToLower(text)
	needle := strings.ToLower(term)
	var b strings.Builder
	last := 0
	for {
		i := strings.Index(lower[last:], needle)
		if i < 0 {
			b.WriteString(text[last:])
			return b.String()
		}
		at := last + i
		b.WriteString(text[last:at])
		b.WriteString(highlightStyle.Render(text[at : at+len(term)]))
		last = at + len(term)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func clipCells(s string, n int) string {
	rs := []rune(strings.Join(strings.Fields(s), " "))
	if len(rs) <= n {
		return string(rs)
	}
	return string(rs[:n-1]) + "…"
}

func clipLeft(s string, n int) string {
	if n < 8 || len(s) <= n {
		return s
	}
	return "..." + s[len(s)-(n-3):]
}

// shortNative is the conversation id as the preview shows it.
func shortNative(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
