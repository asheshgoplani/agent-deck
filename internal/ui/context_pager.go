package ui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
)

// ContextPager is the full-screen, read-only overlay over one session's
// context inventory: everything the harness is being sent before the user
// types anything, ranked by what it costs and annotated with what the user can
// do about it.
//
// It is a renderer and nothing else. Every figure, provenance badge, lever and
// verdict it shows comes from a [ctxinspect.Report] produced off the render
// path; the pager never parses a transcript, never touches disk, and never
// recomputes a number. That split is what lets the CLI (`agent-deck session
// context`) and this panel agree by construction rather than by convention.
//
// The overlay is organised as three tabs, each with its own drill stack:
//
//	Overview   L1 categories → L2 that category's ranked items → L3 verbatim text
//	Breakdown  L2 every item ranked across categories          → L3 verbatim text
//	Verify     the arithmetic: anchor, attribution, residual, invariants
//
// Scrolling is hand-rolled (offset/clamp) to match the rest of internal/ui;
// bubbles/viewport is imported nowhere in this repository.
type ContextPager struct {
	visible       bool
	width, height int

	title     string // session display title, shown in the header
	sessionID string // guards the async inspection against a stale session
	tool      string // agent-deck tool name, for the unsupported banner

	loading bool   // inspection in flight
	errText string // non-empty => inspection failed, shown in the body
	status  string // transient footer line (lever copied, refresh queued, …)

	report   *ctxinspect.Report
	warnings []string // request-resolution problems found before inspection ran

	tab    int
	stacks [contextPagerTabCount][]*contextScreen

	// Rendered-body memo. Navigation re-measures the body on every keypress
	// (clamp, cursor-follow, the position label and View each need the line
	// count), and a level-3 frame can hold tens of thousands of lines of
	// verbatim instruction text. Rebuilding it four times per keystroke is
	// exactly the kind of avoidable work the deck's documented lag comes from,
	// so the lines are built once per (frame, cursor) and reused.
	cacheKey   contextRenderKey
	cacheLines []contextLine
	cacheValid bool
}

// contextRenderKey identifies the state a rendered body depends on: which frame
// is on top (pointer identity, so a popped-and-repushed frame misses) and where
// its cursor is (the item list renders a detail block for the selection).
// Scroll offset is deliberately absent — it selects a window over the lines
// rather than changing them.
type contextRenderKey struct {
	tab    int
	frame  *contextScreen
	cursor int
}

// Tab indices. They are also what the 1/2/3 shortcuts select.
const (
	contextPagerTabOverview = iota
	contextPagerTabBreakdown
	contextPagerTabVerify
	contextPagerTabCount
)

// contextPagerTabTitles are the tab labels, in tab order.
var contextPagerTabTitles = [contextPagerTabCount]string{"Overview", "Breakdown", "Verify"}

// contextChromeRows is the number of non-scrolling rows: three header rows
// (identity, basis/breadcrumb, tabs) and two footer rows.
//
// The second footer row carries the reconciliation verdict and the estimator's
// error bound. They are chrome rather than body content on purpose: a figure
// the user reads at level 3 is only as trustworthy as the report's own
// self-check, so the self-check must not be something they can scroll away
// from.
const contextChromeRows = 5

// contextScreenKind identifies what a drill-stack frame renders.
type contextScreenKind uint8

const (
	// contextScreenCategories is level 1: the gauge and one row per
	// adapter-declared category.
	contextScreenCategories contextScreenKind = iota
	// contextScreenItems is level 2: a ranked item list, either one category's
	// items or every item in the report.
	contextScreenItems
	// contextScreenContent is level 3: one item's provenance, lever, segments
	// and verbatim bytes.
	contextScreenContent
	// contextScreenVerify is the Verify tab: the report's arithmetic.
	contextScreenVerify
)

// contextRankedItem is one row of a ranked list: an item plus the category it
// came from, so a flat ranking can still say where each entry lives.
type contextRankedItem struct {
	Category string
	Item     ctxinspect.Item
}

// contextScreen is one frame of a tab's drill stack. Each frame owns its own
// scroll offset and cursor so ascending returns the user exactly where they
// were.
type contextScreen struct {
	kind contextScreenKind
	// crumb is this frame's breadcrumb segment.
	crumb string
	// items are the rows of a contextScreenItems frame.
	items []contextRankedItem
	// item is the subject of a contextScreenContent frame.
	item ctxinspect.Item
	// category names the category a contextScreenContent frame's item came
	// from.
	category string

	offset int
	cursor int
}

// NewContextPager returns a hidden pager.
func NewContextPager() *ContextPager { return &ContextPager{} }

// IsVisible reports whether the pager is open.
func (p *ContextPager) IsVisible() bool { return p != nil && p.visible }

// SessionID returns the session the pager is bound to, for stale-result guards.
func (p *ContextPager) SessionID() string {
	if p == nil {
		return ""
	}
	return p.sessionID
}

// Report returns the report currently displayed, or nil while loading or after
// a failure.
func (p *ContextPager) Report() *ctxinspect.Report {
	if p == nil {
		return nil
	}
	return p.report
}

// SetSize records the terminal dimensions and re-clamps the active frame so a
// resize can never leave the view scrolled past the end.
func (p *ContextPager) SetSize(width, height int) {
	if p == nil {
		return
	}
	p.width, p.height = width, height
	p.clamp()
}

// Show opens the pager in a loading state bound to a session. The report
// arrives asynchronously via SetReport, or SetError on failure.
func (p *ContextPager) Show(title, sessionID, tool string, width, height int) {
	if p == nil {
		return
	}
	p.visible = true
	p.title = title
	p.sessionID = sessionID
	p.tool = tool
	p.width, p.height = width, height
	p.loading = true
	p.errText = ""
	p.status = ""
	p.report = nil
	p.warnings = nil
	p.tab = contextPagerTabOverview
	for i := range p.stacks {
		p.stacks[i] = nil
	}
	p.invalidate()
}

// Hide closes the pager and releases the report.
func (p *ContextPager) Hide() {
	if p == nil {
		return
	}
	p.visible = false
	p.loading = false
	p.errText = ""
	p.status = ""
	p.report = nil
	p.warnings = nil
	p.sessionID = ""
	p.title = ""
	p.tool = ""
	for i := range p.stacks {
		p.stacks[i] = nil
	}
	p.invalidate()
}

// SetReport installs an inspection result and rebuilds every tab's root frame.
//
// A nil report is treated as a failure rather than as an empty inventory: an
// adapter that returns nothing has told us nothing, and rendering that as "no
// context" would be the one lie this feature exists to prevent.
func (p *ContextPager) SetReport(rep *ctxinspect.Report, warnings []string) {
	if p == nil {
		return
	}
	if rep == nil {
		p.SetError("the inspection returned no report")
		return
	}
	p.loading = false
	p.errText = ""
	p.report = rep
	p.warnings = warnings
	p.rebuildRoots()
	p.invalidate()
	p.clamp()
}

// SetError records an inspection failure to render in the body. A failure is
// shown as a failure; it never degrades into a report full of zeroes.
func (p *ContextPager) SetError(msg string) {
	if p == nil {
		return
	}
	p.loading = false
	p.errText = strings.TrimSpace(msg)
	if p.errText == "" {
		p.errText = "context inspection failed for an unreported reason"
	}
	p.report = nil
	for i := range p.stacks {
		p.stacks[i] = nil
	}
	p.invalidate()
}

// SetStatus sets the transient footer line. It is cleared by the next keypress,
// so no timer is needed and View stays pure.
func (p *ContextPager) SetStatus(msg string) {
	if p == nil {
		return
	}
	p.status = strings.TrimSpace(msg)
}

// ClearStatus clears the transient footer line.
func (p *ContextPager) ClearStatus() {
	if p == nil {
		return
	}
	p.status = ""
}

// Loading reports whether an inspection is still in flight.
func (p *ContextPager) Loading() bool { return p != nil && p.loading }

// SetLoading puts the pager back into its loading state, keeping the current
// binding. It is what a manual refresh calls before re-running the inspection.
func (p *ContextPager) SetLoading() {
	if p == nil {
		return
	}
	p.loading = true
	p.errText = ""
	p.report = nil
	for i := range p.stacks {
		p.stacks[i] = nil
	}
	p.invalidate()
}

// Tab returns the active tab index.
func (p *ContextPager) Tab() int {
	if p == nil {
		return 0
	}
	return p.tab
}

// SetTab activates a tab, keeping each tab's drill position independent. An
// out-of-range index is ignored.
func (p *ContextPager) SetTab(tab int) {
	if p == nil || tab < 0 || tab >= contextPagerTabCount {
		return
	}
	p.tab = tab
	p.clamp()
}

// NextTab / PrevTab cycle the tabs.
func (p *ContextPager) NextTab() { p.SetTab((p.Tab() + 1) % contextPagerTabCount) }
func (p *ContextPager) PrevTab() {
	p.SetTab((p.Tab() + contextPagerTabCount - 1) % contextPagerTabCount)
}

// Depth returns how deep the active tab is drilled: 0 at its root.
func (p *ContextPager) Depth() int {
	if p == nil {
		return 0
	}
	return len(p.stacks[p.tab]) - 1
}

// rebuildRoots installs each tab's root frame from the current report.
func (p *ContextPager) rebuildRoots() {
	rep := p.report
	p.stacks[contextPagerTabOverview] = []*contextScreen{{
		kind:  contextScreenCategories,
		crumb: "overview",
	}}
	p.stacks[contextPagerTabBreakdown] = []*contextScreen{{
		kind:  contextScreenItems,
		crumb: "all items",
		items: contextRankAll(rep),
	}}
	p.stacks[contextPagerTabVerify] = []*contextScreen{{
		kind:  contextScreenVerify,
		crumb: "verify",
	}}
}

// current returns the active frame, or nil when no report is loaded.
func (p *ContextPager) current() *contextScreen {
	if p == nil || p.report == nil {
		return nil
	}
	stack := p.stacks[p.tab]
	if len(stack) == 0 {
		return nil
	}
	return stack[len(stack)-1]
}

// rowCount returns how many selectable rows the active frame has.
func (p *ContextPager) rowCount() int {
	s := p.current()
	if s == nil {
		return 0
	}
	switch s.kind {
	case contextScreenCategories:
		n := len(p.report.Categories)
		if p.report.Unaccounted != nil {
			n++
		}
		return n
	case contextScreenItems:
		return len(s.items)
	case contextScreenContent:
		return len(s.item.Children)
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

// bodyHeight is the number of scrolling rows between the header and the footer.
func (p *ContextPager) bodyHeight() int {
	if p == nil {
		return 1
	}
	h := p.height - contextChromeRows
	if h < 1 {
		h = 1
	}
	return h
}

// body returns the active frame's rendered lines, building them at most once
// per (frame, cursor). Every consumer — clamp, cursor-follow, the position
// label and View — goes through here, so the line count a clamp uses and the
// lines View prints can never disagree.
func (p *ContextPager) body() []contextLine {
	s := p.current()
	if s == nil {
		return nil
	}
	// Only the ranked-item frame renders anything that depends on the
	// selection, so a content frame holding tens of thousands of lines of
	// verbatim text is not rebuilt merely because the cursor moved.
	cursor := 0
	if s.kind == contextScreenItems {
		cursor = s.cursor
	}
	key := contextRenderKey{tab: p.tab, frame: s, cursor: cursor}
	if p.cacheValid && p.cacheKey == key {
		return p.cacheLines
	}
	p.cacheLines = p.renderBody()
	p.cacheKey = key
	p.cacheValid = true
	return p.cacheLines
}

// invalidate drops the rendered-body memo. It is called wherever the report or
// the frame set changes underneath it.
func (p *ContextPager) invalidate() {
	p.cacheValid = false
	p.cacheLines = nil
	p.cacheKey = contextRenderKey{}
}

// lineCount reports how many lines the active frame renders to. It is the
// denominator of every clamp.
func (p *ContextPager) lineCount() int { return len(p.body()) }

// maxOffset is the largest valid top-line index.
func (p *ContextPager) maxOffset() int {
	m := p.lineCount() - p.bodyHeight()
	if m < 0 {
		return 0
	}
	return m
}

// clamp keeps the cursor within the row set and the offset within the body,
// then scrolls the minimum distance needed to bring the cursor into view.
func (p *ContextPager) clamp() {
	s := p.current()
	if s == nil {
		return
	}
	rows := p.rowCount()
	if s.cursor < 0 {
		s.cursor = 0
	}
	if rows > 0 && s.cursor >= rows {
		s.cursor = rows - 1
	}
	if rows == 0 {
		s.cursor = 0
	}

	if s.offset < 0 {
		s.offset = 0
	}
	if m := p.maxOffset(); s.offset > m {
		s.offset = m
	}
	p.followCursor()
}

// followCursor scrolls just enough to keep the selected row on screen.
func (p *ContextPager) followCursor() {
	s := p.current()
	if s == nil || p.rowCount() == 0 {
		return
	}
	lines := p.renderBody()
	idx := -1
	for i, l := range lines {
		if l.row == s.cursor {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	body := p.bodyHeight()
	if idx < s.offset {
		s.offset = idx
	}
	if idx >= s.offset+body {
		s.offset = idx - body + 1
	}
	if s.offset < 0 {
		s.offset = 0
	}
}

// MoveCursor moves the selection by delta rows, or scrolls when the frame has
// no selectable rows (the Verify tab and childless content frames).
func (p *ContextPager) MoveCursor(delta int) {
	s := p.current()
	if s == nil {
		return
	}
	if p.rowCount() == 0 {
		if delta < 0 {
			p.ScrollUp(-delta)
		} else {
			p.ScrollDown(delta)
		}
		return
	}
	s.cursor += delta
	p.clamp()
}

// ScrollUp / ScrollDown move the viewport without moving the selection.
func (p *ContextPager) ScrollUp(n int) {
	s := p.current()
	if s == nil || n < 1 {
		return
	}
	s.offset -= n
	if s.offset < 0 {
		s.offset = 0
	}
	if m := p.maxOffset(); s.offset > m {
		s.offset = m
	}
}

func (p *ContextPager) ScrollDown(n int) {
	s := p.current()
	if s == nil || n < 1 {
		return
	}
	s.offset += n
	if m := p.maxOffset(); s.offset > m {
		s.offset = m
	}
	if s.offset < 0 {
		s.offset = 0
	}
}

// pageStep is a near-full body height, keeping one line of overlap.
func (p *ContextPager) pageStep() int {
	step := p.bodyHeight() - 1
	if step < 1 {
		step = 1
	}
	return step
}

// PageUp / PageDown scroll by a page, carrying the selection with them so the
// cursor never falls off screen.
func (p *ContextPager) PageUp()   { p.pageBy(-p.pageStep()) }
func (p *ContextPager) PageDown() { p.pageBy(p.pageStep()) }

func (p *ContextPager) pageBy(delta int) {
	s := p.current()
	if s == nil {
		return
	}
	if p.rowCount() == 0 {
		if delta < 0 {
			p.ScrollUp(-delta)
		} else {
			p.ScrollDown(delta)
		}
		return
	}
	if delta < 0 {
		p.ScrollUp(-delta)
	} else {
		p.ScrollDown(delta)
	}
	p.selectRowNearestViewport()
}

// selectRowNearestViewport pulls the cursor to the first selectable row inside
// the viewport after a page scroll, so paging and selection stay coherent.
func (p *ContextPager) selectRowNearestViewport() {
	s := p.current()
	if s == nil {
		return
	}
	lines := p.renderBody()
	body := p.bodyHeight()
	end := s.offset + body
	if end > len(lines) {
		end = len(lines)
	}
	for i := s.offset; i < end; i++ {
		if lines[i].row >= 0 {
			s.cursor = lines[i].row
			return
		}
	}
}

// Top jumps to the first row; Bottom to the last.
func (p *ContextPager) Top() {
	s := p.current()
	if s == nil {
		return
	}
	s.cursor = 0
	s.offset = 0
	p.clamp()
}

func (p *ContextPager) Bottom() {
	s := p.current()
	if s == nil {
		return
	}
	if rows := p.rowCount(); rows > 0 {
		s.cursor = rows - 1
	}
	s.offset = p.maxOffset()
	p.clamp()
}

// Descend opens the selected row one level deeper and reports whether it moved.
func (p *ContextPager) Descend() bool {
	s := p.current()
	if s == nil {
		return false
	}
	switch s.kind {
	case contextScreenCategories:
		return p.descendCategory(s.cursor)
	case contextScreenItems:
		if s.cursor < 0 || s.cursor >= len(s.items) {
			return false
		}
		ri := s.items[s.cursor]
		p.push(&contextScreen{
			kind:     contextScreenContent,
			crumb:    contextItemLabel(ri.Item),
			item:     ri.Item,
			category: ri.Category,
		})
		return true
	case contextScreenContent:
		if s.cursor < 0 || s.cursor >= len(s.item.Children) {
			return false
		}
		child := s.item.Children[s.cursor]
		p.push(&contextScreen{
			kind:     contextScreenContent,
			crumb:    contextItemLabel(child),
			item:     child,
			category: s.category,
		})
		return true
	default:
		return false
	}
}

// descendCategory opens the category (or the residual row) at index i.
func (p *ContextPager) descendCategory(i int) bool {
	rep := p.report
	if rep == nil || i < 0 {
		return false
	}
	if i < len(rep.Categories) {
		cat := rep.Categories[i]
		items := make([]contextRankedItem, 0, len(cat.Items))
		contextCollectItems(cat.Name, cat.Sorted(), &items)
		p.push(&contextScreen{
			kind:  contextScreenItems,
			crumb: contextCategoryTitle(cat),
			items: items,
		})
		return true
	}
	if i == len(rep.Categories) && rep.Unaccounted != nil {
		p.push(&contextScreen{
			kind:     contextScreenContent,
			crumb:    contextItemLabel(*rep.Unaccounted),
			item:     *rep.Unaccounted,
			category: "unattributed",
		})
		return true
	}
	return false
}

// push adds a frame to the active tab's drill stack.
func (p *ContextPager) push(s *contextScreen) {
	p.stacks[p.tab] = append(p.stacks[p.tab], s)
	p.clamp()
}

// Ascend pops one drill level and reports whether it moved. At a tab's root it
// returns false, which is the caller's signal to close the overlay.
func (p *ContextPager) Ascend() bool {
	if p == nil {
		return false
	}
	stack := p.stacks[p.tab]
	if len(stack) <= 1 {
		return false
	}
	p.stacks[p.tab] = stack[:len(stack)-1]
	p.clamp()
	return true
}

// SelectedItem returns the item the cursor is on, when the active frame has
// one. A category row is not an item and yields false.
func (p *ContextPager) SelectedItem() (ctxinspect.Item, bool) {
	s := p.current()
	if s == nil {
		return ctxinspect.Item{}, false
	}
	switch s.kind {
	case contextScreenItems:
		if s.cursor < 0 || s.cursor >= len(s.items) {
			return ctxinspect.Item{}, false
		}
		return s.items[s.cursor].Item, true
	case contextScreenContent:
		if s.cursor >= 0 && s.cursor < len(s.item.Children) {
			return s.item.Children[s.cursor], true
		}
		return s.item, true
	case contextScreenCategories:
		if p.report != nil && s.cursor == len(p.report.Categories) && p.report.Unaccounted != nil {
			return *p.report.Unaccounted, true
		}
		return ctxinspect.Item{}, false
	default:
		return ctxinspect.Item{}, false
	}
}

// SelectedLever returns the lever of the selected item and whether it is one
// the user can act on. An immovable item reports false: copying an empty
// payload would look like a successful copy of nothing.
func (p *ContextPager) SelectedLever() (ctxinspect.Lever, bool) {
	it, ok := p.SelectedItem()
	if !ok {
		return ctxinspect.Lever{}, false
	}
	if !it.Lever.Kind.Actionable() {
		return it.Lever, false
	}
	if strings.TrimSpace(it.Lever.Payload()) == "" {
		return it.Lever, false
	}
	return it.Lever, true
}

// ---------------------------------------------------------------------------
// Ranking
// ---------------------------------------------------------------------------

// contextRankAll flattens every category into one ranking.
//
// It mirrors [ctxinspect.Category.Sorted]: actionable first, then loaded before
// deferred before merely available, then cost descending. Rollup parents are
// replaced by their children so a group header and its members cannot both be
// read as a cost, and the residual is pinned last because it is the one row
// nobody can act on.
func contextRankAll(rep *ctxinspect.Report) []contextRankedItem {
	if rep == nil {
		return nil
	}
	var out []contextRankedItem
	for _, cat := range rep.Categories {
		contextCollectItems(cat.Name, cat.Items, &out)
	}
	sort.SliceStable(out, func(a, b int) bool {
		ia, ib := out[a].Item, out[b].Item
		if ia.Actionable() != ib.Actionable() {
			return ia.Actionable()
		}
		if ia.Load.State != ib.Load.State {
			return ia.Load.State < ib.Load.State
		}
		va, oka := ia.Load.Actual.Value()
		vb, okb := ib.Load.Actual.Value()
		if oka != okb {
			return oka // known costs rank above unknowns
		}
		if oka && va != vb {
			return va > vb
		}
		if ia.Label != ib.Label {
			return ia.Label < ib.Label
		}
		return ia.ID < ib.ID
	})
	if rep.Unaccounted != nil {
		out = append(out, contextRankedItem{Category: "unattributed", Item: *rep.Unaccounted})
	}
	return out
}

// contextCollectItems appends leaves, descending through rollup parents.
func contextCollectItems(category string, items []ctxinspect.Item, out *[]contextRankedItem) {
	for _, it := range items {
		if it.Rollup {
			contextCollectItems(category, it.Children, out)
			continue
		}
		*out = append(*out, contextRankedItem{Category: category, Item: it})
	}
}

// ---------------------------------------------------------------------------
// Body rendering
// ---------------------------------------------------------------------------

// contextLine is one rendered body line. row is the index of the selectable row
// it represents, or -1 when the line is not selectable. Styling is decided in
// View so that the same line can render selected, dimmed or plain without the
// builder knowing which.
type contextLine struct {
	raw string
	row int
	// dim marks a line the user cannot act on — harness internals, immovable
	// items — so the top of the screen belongs to what they can change.
	dim bool
	// head marks a column header or section heading.
	head bool
	// bad marks a line reporting a contradiction: a failed reconciliation, an
	// invariant violation, a bug-severity caveat.
	bad bool
	// keepWithNext marks a section heading that announces content below it.
	// The viewport must never leave such a line as the last visible row of a
	// frame while its body sits below the fold: a label promising rows the
	// reader cannot see reads as a value that failed to render, and at 80x24
	// the legend heading landed on exactly that fold (G3, 2026-07-29). The
	// heading moves with its content instead.
	keepWithNext bool
}

func contextPlain(s string) contextLine { return contextLine{raw: s, row: -1} }
func contextDim(s string) contextLine   { return contextLine{raw: s, row: -1, dim: true} }
func contextHead(s string) contextLine  { return contextLine{raw: s, row: -1, head: true} }
func contextBad(s string) contextLine   { return contextLine{raw: s, row: -1, bad: true} }

// renderBody renders the active frame to lines. It is pure: no IO, no mutation.
func (p *ContextPager) renderBody() []contextLine {
	s := p.current()
	if s == nil {
		return nil
	}
	switch s.kind {
	case contextScreenCategories:
		return p.renderCategories()
	case contextScreenItems:
		return p.renderItems(s)
	case contextScreenContent:
		return p.renderContent(s)
	case contextScreenVerify:
		return p.renderVerify()
	default:
		return nil
	}
}

// renderCategories renders level 1.
func (p *ContextPager) renderCategories() []contextLine {
	rep := p.report
	var out []contextLine

	out = append(out, contextPlain(""))
	fixed, complete := rep.FixedTotal()
	out = append(out, contextPlain("  "+contextGaugeLine(rep, fixed, complete)))
	for _, l := range contextWindowUnknownLines(rep.Window, p.width) {
		out = append(out, contextDim("  "+l))
	}
	// Line two, every time: what this screen measures and what it does not.
	// Someone who presses C because "my context is full" means their
	// conversation, and this report has never measured a conversation.
	for _, l := range contextScopeLines(p.width) {
		out = append(out, contextDim("  "+l))
	}
	// The payoff, on the screen that asks the question. This report exists so
	// somebody can clean something up, and until this line the first screen had
	// no verb on it: the only actionable content was a level down, behind a
	// heading nobody had a reason to open.
	for _, l := range contextWrap(contextActionableLine(rep), p.width) {
		out = append(out, contextPlain("  "+l))
	}
	out = append(out, contextPlain(""))

	if banner, ok := contextUnsupportedBanner(rep, p.tool); ok {
		for _, l := range banner {
			out = append(out, contextDim("  "+l))
		}
		out = append(out, contextPlain(""))
	}

	rows := [][]string{{"TOKENS", "POTENTIAL", "PROVENANCE", "CATEGORY"}}
	meta := []contextLine{{row: -1, head: true}}
	for _, cat := range rep.Categories {
		// DisplayTotal, not Total: a category whose contents were never
		// established must not print the same "0" as one established to be
		// empty. "0 MCP instructions" is a claim about the user's setup that an
		// unread transcript cannot support.
		total, catComplete := cat.DisplayTotal()
		potential, hasPotential := cat.PotentialTotal()
		rows = append(rows, []string{
			contextTotalTokens(total, catComplete),
			contextPotentialCell(potential, hasPotential),
			contextCategoryBadgeCell(cat),
			contextCategorySummary(cat),
		})
		meta = append(meta, contextLine{row: len(meta) - 1})
	}
	if rep.Unaccounted != nil {
		u := *rep.Unaccounted
		rows = append(rows, []string{
			contextTokenCount(u.Load.Actual),
			"—",
			u.Badge().Short(),
			contextLockGlyph(u) + u.Label + " (unattributed remainder)",
		})
		meta = append(meta, contextLine{row: len(meta) - 1, dim: true})
	}

	if len(rows) == 1 {
		out = append(out, contextDim("  no categories: this adapter could report nothing about this session"))
	} else {
		for i, text := range contextTable(rows, "  ") {
			line := meta[i]
			line.raw = text
			out = append(out, line)
		}
	}

	out = append(out, contextPlain(""))
	if h := rep.History; h != nil {
		out = append(out, contextDim("  "+contextHistoryLine(h)))
	}
	if potential, any := rep.PotentialTotal(); any {
		out = append(out, contextDim(fmt.Sprintf(
			"  potential: ~%s more if every deferred item were fully loaded (never added to the gauge)",
			contextTokenAmount(potential))))
	}

	out = append(out, contextPlain(""))
	for i, l := range contextLegendLines() {
		line := contextDim("  " + l)
		// The first line is the heading that announces the rest; it stays
		// glued to its first body line rather than sitting alone on a fold.
		line.keepWithNext = i == 0
		out = append(out, line)
	}

	// The adapter's per-category notes are correct and there are a lot of them:
	// on a fresh session they outnumber the data rows two to one, and a screen
	// that is mostly disclaimer reads as unfinished rather than as careful.
	// They live one keystroke away on Verify instead.
	notes := 0
	for _, cat := range rep.Categories {
		notes += len(cat.Notes)
	}
	if notes > 0 {
		out = append(out, contextPlain(""))
		out = append(out, contextDim(fmt.Sprintf("  %d note%s about how these figures were obtained — press 3 for Verify.", notes, contextPlural(notes))))
	}

	// The first screen holds back the caveats that say nothing is wrong. Warns
	// and bugs stay, because they are the ones that change how a figure above
	// should be read.
	out = append(out, p.renderCaveatsRanked(true)...)
	return out
}

// contextActionableLine is the first screen's one actionable sentence, with the
// key that lists the items appended when there are any.
func contextActionableLine(rep *ctxinspect.Report) string {
	n, tokens, complete := rep.ActionableTotal()
	line := ctxtext.ActionableSentence(n, tokens, complete, contextTotalTokens(tokens, complete))
	if n == 0 {
		return line
	}
	return line + " Press 2, or Enter on a category, to see them with what to do about each."
}

// contextScopeLines is the sentence that has to be on the first screen.
func contextScopeLines(width int) []string {
	return append(
		contextWrap("this measures the FIXED STARTUP OVERHEAD — the instruction files, skills, agents and tool schemas loaded into every turn.", width),
		contextWrap("it does not measure your conversation, which is what usually fills a context window.", width)...,
	)
}

// contextLegendLines explains the on-screen shorthand.
//
// Every code below appears on the default screen, and none of them was defined
// anywhere on it: a reader met CAPT/~est, ABSENT/residual and 🔒 with only "~ =
// estimated" in the footer to go on. A vocabulary the screen uses and never
// explains is not rigour, it is a private notation.
func contextLegendLines() []string {
	return []string{
		"reading the columns:",
		"  TOKENS      what it costs every turn.  ~ estimated · ≥ lower bound · — not knowable (never zero)",
		"  POTENTIAL   what it would cost if fully loaded. Never added to the gauge.",
		"  PROVENANCE  text/tokens.  text: CAPT verbatim · RECON rebuilt from the harness's rules · ABSENT not on disk",
		"              tokens: measured by the provider · ~est estimated · residual by subtraction · — unknown",
		// A RECON row is as of now on disk. A session that is already running is
		// still sending what it booted with, so editing a file moves the figure
		// here before it moves anything the model receives. Leaving that unsaid
		// makes the screen quietly wrong for exactly the reader who acted on it.
		"              RECON is as of now on disk; a running session keeps its boot-time copy until you restart it.",
		"  🔒          you cannot remove it: harness internals, or content agent-deck did not put there.",
		// The legend defines the codes in a table cell. adapter, basis, anchor,
		// residual and reconciliation are on this screen too, and they need a
		// sentence each rather than a column, so they live at the foot of Verify.
		"  adapter, basis, anchor, residual, reconciliation: press 3 for the glossary.",
	}
}

// renderItems renders level 2: a ranked item list.
func (p *ContextPager) renderItems(s *contextScreen) []contextLine {
	var out []contextLine
	out = append(out, contextPlain(""))
	// The original headline ("ranked by current cost, actionable items first —
	// the top of this list is what you can remove for the most gain") was false
	// in three ways at once: the largest figure in the report is the residual,
	// pinned last because nobody can act on it; the top row's lever is usually
	// an edit, not a removal; and the ranking is not by cost alone. The order is
	// right, and it is the promise about it that has to change.
	out = append(out, contextDim("  what you can act on, first — then what is merely costing you, then what is fixed."))
	out = append(out, contextDim("  within each group: loaded before deferred, and larger before smaller."))
	out = append(out, contextDim("  \"act on\" is edit, delete or run a command, per row — 🔒 marks the ones you cannot."))
	out = append(out, contextPlain(""))

	if len(s.items) == 0 {
		out = append(out, contextDim("  no items: this adapter attributed nothing here."))
		out = append(out, p.renderCaveats()...)
		return out
	}

	rows := [][]string{{"TOKENS", "POTENTIAL", "PROVENANCE", "ORIGIN", "LOAD", "CATEGORY", "ITEM"}}
	meta := []contextLine{{row: -1, head: true}}
	for i, ri := range s.items {
		potential, hasPotential := ri.Item.PotentialTokens()
		rows = append(rows, []string{
			contextTokenCount(ri.Item.Load.Actual),
			contextPotentialCell(potential, hasPotential),
			ri.Item.Badge().Short(),
			ri.Item.Origin.String(),
			ri.Item.Load.State.String(),
			ri.Category,
			contextLockGlyph(ri.Item) + contextItemLabel(ri.Item),
		})
		meta = append(meta, contextLine{row: i, dim: !ri.Item.Actionable()})
	}
	for i, text := range contextTable(rows, "  ") {
		line := meta[i]
		line.raw = text
		out = append(out, line)
	}

	if s.cursor >= 0 && s.cursor < len(s.items) {
		sel := s.items[s.cursor].Item
		out = append(out, contextPlain(""))
		out = append(out, contextPlain("  selected:  "+contextItemLabel(sel)))
		// "lever" is a word this UI never defined and no user reaches for.
		// What they came here to read is the instruction.
		out = append(out, contextDim("  to remove: "+contextLeverLine(sel.Lever)))
		out = append(out, contextDim("  id:        "+sel.ID))
	}

	out = append(out, p.renderCaveats()...)
	return out
}

// renderContent renders level 3: one item's provenance, lever, per-segment
// costs and verbatim bytes.
func (p *ContextPager) renderContent(s *contextScreen) []contextLine {
	it := s.item
	var out []contextLine
	out = append(out, contextPlain(""))

	rows := [][]string{
		{"item", contextItemLabel(it)},
		{"id", it.ID},
		{"category", s.category},
	}
	if d := strings.TrimSpace(it.Detail); d != "" {
		rows = append(rows, []string{"detail", d})
	}
	rows = append(rows,
		[]string{"origin", it.Origin.String()},
		[]string{"load", contextLoadLine(it.Load)},
		[]string{"text", contextTextProvLine(it.Content)},
		[]string{"tokens", contextTokenProvLine(it.Load.Actual)},
		[]string{"to remove", contextLeverLine(it.Lever)},
	)
	if it.Informational {
		rows = append(rows, []string{"counted", "no — its cost is already inside another row above, so counting it here would count the same tokens twice"})
	}
	for _, text := range contextTable(rows, "  ") {
		out = append(out, contextPlain(text))
	}

	for _, c := range it.Caveats {
		line := "  caveat (" + c.Severity.String() + "): " + c.Message
		if c.Severity == ctxinspect.SeverityBug {
			out = append(out, contextBad(line))
		} else {
			out = append(out, contextDim(line))
		}
	}

	if len(it.Children) > 0 {
		out = append(out, contextPlain(""))
		out = append(out, contextHead("  segments — what each part of this item costs"))
		childRows := [][]string{{"TOKENS", "POTENTIAL", "PROVENANCE", "SEGMENT"}}
		meta := []contextLine{{row: -1, head: true}}
		for i, c := range it.Children {
			potential, hasPotential := c.PotentialTokens()
			childRows = append(childRows, []string{
				contextTokenCount(c.Load.Actual),
				contextPotentialCell(potential, hasPotential),
				c.Badge().Short(),
				contextLockGlyph(c) + strings.TrimSpace(contextItemLabel(c)+"  "+c.Detail),
			})
			meta = append(meta, contextLine{row: i, dim: !c.Actionable()})
		}
		for i, text := range contextTable(childRows, "  ") {
			line := meta[i]
			line.raw = text
			out = append(out, line)
		}
	}

	out = append(out, contextPlain(""))
	out = append(out, p.renderContentBytes(it.Content)...)
	return out
}

// renderContentBytes renders the bytes behind an item, or states plainly why
// there are none. An absence is never rendered as an empty body.
func (p *ContextPager) renderContentBytes(c ctxinspect.Content) []contextLine {
	var out []contextLine
	if c.Prov == ctxinspect.TextAbsent {
		note := strings.TrimSpace(c.Note)
		if note == "" {
			note = "the harness does not write this content anywhere agent-deck can read."
		}
		out = append(out, contextHead("  --- content unavailable ---"))
		out = append(out, contextDim("  "+note))
		return out
	}

	header := fmt.Sprintf("  --- content (%d chars, %s) ---", c.Chars, c.Prov.String())
	if c.Truncated {
		header = fmt.Sprintf("  --- content (showing %d of %d chars, %s, TRUNCATED) ---",
			len([]rune(c.Text)), c.Chars, c.Prov.String())
	}
	out = append(out, contextHead(header))
	if note := strings.TrimSpace(c.Note); note != "" {
		out = append(out, contextDim("  ("+note+")"))
	}
	if strings.TrimSpace(c.Text) == "" {
		out = append(out, contextDim("  (the harness recorded this item with no text)"))
	} else {
		for _, l := range strings.Split(strings.ReplaceAll(c.Text, "\r\n", "\n"), "\n") {
			out = append(out, contextPlain("  "+l))
		}
	}
	out = append(out, contextHead("  --- end ---"))
	return out
}

// renderVerify renders the Verify tab: every figure with its provenance, the
// arithmetic that links them, what the adapter said it could achieve, and the
// invariants the report checks against itself.
func (p *ContextPager) renderVerify() []contextLine {
	rep := p.report
	rec := rep.Reconciliation
	var out []contextLine

	out = append(out, contextPlain(""))
	out = append(out, contextDim("  which of these numbers were measured, and what is left unexplained"))
	out = append(out, contextPlain(""))

	attributed, attributedComplete := rep.AttributedTotal()
	fixed, fixedComplete := rep.FixedTotal()
	// Every figure derived from the anchor inherits its over-estimate: mark all
	// three, not just the anchor row. A reader who sees one "≤" and three bare
	// numbers concludes the other three are exact.
	bound := ""
	if rep.Anchor.IsUpperBound() {
		bound = " (≤)"
	}
	rows := [][]string{
		{"FIGURE", "TOKENS", "PROVENANCE", "HOW"},
		{"anchor" + bound, contextTokenCount(rec.Anchor), rec.Anchor.Prov().String(), contextAnchorHow(rep)},
		{"attributed", contextTotalTokens(attributed, attributedComplete), rec.Attributed.Prov().String(), "sum of every category's actual cost"},
		{"unaccounted" + bound, contextTokenCount(rec.Unaccounted), rec.Unaccounted.Prov().String(), "anchor − Σ(attributed); never clamped to zero"},
		{"fixed total" + bound, contextTotalTokens(fixed, fixedComplete), "derived", "attributed + unaccounted; equals the anchor by construction when it reconciles"},
	}
	if potential, any := rep.PotentialTotal(); any {
		rows = append(rows, []string{"potential", "~" + contextTokenAmount(potential), "estimated", "what deferred content would cost if fully loaded; never added to the total"})
	}
	for _, text := range contextTable(rows, "  ") {
		out = append(out, contextPlain(text))
	}

	out = append(out, p.renderAnchorMeasurement()...)

	out = append(out, contextPlain(""))
	verdict := "  verdict:  " + contextReconVerdict(rec)
	if rec.Status == ctxinspect.ReconFailed {
		out = append(out, contextBad(verdict))
	} else {
		out = append(out, contextPlain(verdict))
	}
	if rec.Status == ctxinspect.ReconOK {
		out = append(out, contextDim(fmt.Sprintf("  attributed to a named item: %.1f%% of the measured total (the rest is the harness's own prompt and tool schemas)", rec.Coverage)))
	}
	out = append(out, contextDim("  "+contextEstimatorFooter(rep)))

	out = append(out, contextPlain(""))
	out = append(out, contextHead(fmt.Sprintf("  what the %q adapter can report for %q", contextOrDefault(rep.Capabilities.Adapter, "unknown"), rep.Harness)))
	out = append(out, contextDim("    measured fixed-prefix anchor: "+contextYesNo(rep.Capabilities.CanAnchor)))
	out = append(out, contextDim("    verbatim base system prompt:  "+contextYesNo(rep.Capabilities.CanVerbatimSystem)))
	if len(rep.Capabilities.Categories) > 0 {
		capRows := [][]string{{"CATEGORY", "TEXT", "TOKENS", "MECHANISM"}}
		for _, cc := range rep.Capabilities.Categories {
			capRows = append(capRows, []string{cc.Name, cc.Text.String(), cc.Token.String(), cc.Note})
		}
		for i, text := range contextTable(capRows, "    ") {
			if i == 0 {
				out = append(out, contextHead(text))
				continue
			}
			out = append(out, contextDim(text))
		}
	}
	for _, note := range rep.Capabilities.Notes {
		out = append(out, contextDim("    note: "+note))
	}

	// The overview demotes the per-category notes to a count and sends the
	// reader here for them. It was sending them to a tab that did not print
	// them: the count was true, the destination was not, and a promise the
	// screen does not keep is the same defect as a figure it cannot support.
	out = append(out, p.renderCategoryNotes()...)

	out = append(out, contextPlain(""))
	out = append(out, contextHead("  invariants"))
	if len(rep.Violations) == 0 {
		out = append(out, contextDim("    no violations: the report is internally consistent."))
	} else {
		for _, v := range rep.Violations {
			out = append(out, contextBad("    VIOLATION: "+v))
		}
	}

	out = append(out, p.renderCaveats()...)

	out = append(out, contextPlain(""))
	out = append(out, contextHead("  ground truth"))
	out = append(out, contextDim("    This report is derived from what the harness itself wrote to disk. To diff it against the"))
	out = append(out, contextDim("    harness's own accounting, run `agent-deck session context <ref> --verify`: it sends the"))
	out = append(out, contextDim("    harness's context command (Claude Code: /context, Codex: /status) to the live session and"))
	out = append(out, contextDim("    prints a per-group comparison. That types into your session, so it asks first — and this"))
	out = append(out, contextDim("    panel never does it: opening the inspector sends nothing to the agent."))
	out = append(out, contextDim("    `agent-deck session context <ref> --json` emits every number above with its provenance."))

	out = append(out, p.renderGlossary()...)
	return out
}

// renderAnchorMeasurement states the one measured figure in full, with the
// arithmetic that links it to everything derived from it.
//
// The table above is a summary and behaves like one: it rounds the anchor to
// 27.0k and puts the provider field path in a last column that the pane clips at
// the terminal's width. Both are right for a dense row and wrong for the single
// figure this report labels MEASURED. "27.0k" matches 26,951 and 27,013 equally
// well, so it cannot be checked against Anthropic's own accounting at all, and
// an arithmetic statement that ends in an ellipsis is not a statement. The one
// number a reader is invited to verify has to be verifiable on a frame they can
// reach — so it gets a block of its own here, at full precision, wrapped instead
// of clipped.
func (p *ContextPager) renderAnchorMeasurement() []contextLine {
	rep := p.report
	out := []contextLine{contextPlain(""), contextHead("  the measured figure, in full")}

	if rep.Anchor == nil {
		return append(out, p.contextField("no measured figure",
			"nothing this session recorded measured its fixed prefix, so every total above is an estimate, a lower bound or an unknown. The caveats below say which of those and why.")...)
	}
	total, ok := rep.Anchor.Tokens.Value()
	if !ok {
		return append(out, p.contextField("no measured figure",
			"an anchor was published without a value, which is a bug in this report rather than a fact about the session.")...)
	}

	out = append(out, p.contextField("measured total",
		contextExactAmount(total)+" tokens — the provider's own count for the request that carried this session's startup injections.")...)
	out = append(out, p.contextField("read from",
		contextOrDefault(rep.Anchor.Source, "the harness's own record of that request")+".")...)
	if !rep.Anchor.At.IsZero() {
		out = append(out, p.contextField("recorded at", rep.Anchor.At.Format("2006-01-02 15:04:05 MST")+".")...)
	}

	// The subtraction spelled out. Every reader who wants to check the residual
	// has to do exactly this sum, and doing it from the rounded column gives an
	// answer that is wrong by up to a hundred tokens in each term.
	rec := rep.Reconciliation
	attributed, attributedComplete := rep.AttributedTotal()
	attrText := contextExactAmount(attributed) + " attributed"
	if !attributedComplete {
		attrText = "≥" + attrText
	}
	remainder := "— (not knowable: a contributing count is unknown)"
	if unacc, unaccOK := rec.Unaccounted.Value(); unaccOK {
		remainder = contextExactAmount(unacc) + " unattributed remainder"
		if !attributedComplete {
			remainder = "≤" + remainder
		}
	}
	out = append(out, p.contextField("the arithmetic",
		contextExactAmount(total)+" measured − "+attrText+" = "+remainder+".")...)

	if rep.Anchor.IsUpperBound() {
		out = append(out, p.contextField("UPPER BOUND (≤)",
			contextOrDefault(rep.Anchor.UpperBoundReason, "the measurement covers more than the fixed prefix")+
				". The real fixed prefix is at most the figure above, and every total derived from it inherits the same qualifier.")...)
	}
	return out
}

// contextField prints one labelled fact: the label on its own line, the value
// wrapped under it. A two-column layout would put the value in a cell the pane
// clips, which is the defect this block exists to undo.
func (p *ContextPager) contextField(label, value string) []contextLine {
	out := []contextLine{contextHead("    " + label)}
	for _, l := range contextWrap(value, max0(p.width-8)) {
		out = append(out, contextDim("      "+l))
	}
	return out
}

// contextExactAmount renders a token figure to the digit, grouped in threes.
// contextTokenAmount rounds, which is what a dense column needs and the
// opposite of what a figure offered for verification needs. The formatter is
// shared with the CLI so both frames state the one MEASURED figure identically.
func contextExactAmount(n int) string { return ctxtext.ExactAmount(n) }

// renderCategoryNotes prints the adapter's per-category notes, which the
// overview replaced with a count and a pointer to this tab.
func (p *ContextPager) renderCategoryNotes() []contextLine {
	rep := p.report
	var body []contextLine
	for _, cat := range rep.Categories {
		for _, note := range cat.Notes {
			for i, l := range contextWrap(cat.Name+": "+note, max0(p.width-6)) {
				indent := "    "
				if i > 0 {
					indent = "      "
				}
				body = append(body, contextDim(indent+l))
			}
		}
	}
	if len(body) == 0 {
		return nil
	}
	return append([]contextLine{
		contextPlain(""),
		contextHead("  how these figures were obtained"),
	}, body...)
}

// renderGlossary defines the words these screens use.
//
// The column legend on the overview covers the codes that fit in a table cell.
// adapter, basis, anchor, residual and reconciliation are also on that screen
// and need a sentence each, so they live at the foot of the tab a reader
// already reaches for when they want to know how a number was made. The CLI
// prints the same list behind `--glossary`; the words come from one place so
// the two surfaces cannot answer differently.
func (p *ContextPager) renderGlossary() []contextLine {
	out := []contextLine{contextPlain(""), contextHead("  glossary — the words on these screens")}
	for _, t := range ctxtext.Glossary() {
		out = append(out, contextHead("    "+t.Term))
		for _, l := range contextWrap(t.Def, max0(p.width-8)) {
			out = append(out, contextDim("      "+l))
		}
	}
	return out
}

// renderCaveats renders the resolution warnings and the report's own caveats.
func (p *ContextPager) renderCaveats() []contextLine {
	return p.renderCaveatsRanked(false)
}

// renderCaveatsRanked renders the caveat footer, optionally holding back the
// merely-informative ones behind a count.
//
// The first screen had six data rows and, under them, ten note lines and a
// caveat block. A screen that is mostly disclaimer does not read as careful, it
// reads as unfinished. The notes moved to the Verify tab; the caveats that say
// "worth knowing, nothing is wrong" follow them.
//
// A warn means a number is less trustworthy than it looks and a bug means the
// report contradicts itself. Neither is ever collapsed, on any screen. Hiding
// one to tidy a screen would be the exact trade this feature exists to refuse.
func (p *ContextPager) renderCaveatsRanked(demoteInfo bool) []contextLine {
	rep := p.report
	if rep == nil {
		return nil
	}

	shown := make([]ctxinspect.Caveat, 0, len(rep.Caveats))
	held := 0
	for _, c := range rep.Caveats {
		if demoteInfo && c.Severity == ctxinspect.SeverityInfo {
			held++
			continue
		}
		shown = append(shown, c)
	}
	if len(p.warnings) == 0 && len(shown) == 0 && held == 0 {
		return nil
	}

	heading := contextHead("  caveats")
	// Same widow rule as the legend heading: never the last visible line of a
	// frame while the caveats it announces sit below the fold.
	heading.keepWithNext = true
	out := []contextLine{contextPlain(""), heading}
	for _, w := range p.warnings {
		out = append(out, contextDim("    resolution: "+w))
	}
	for _, c := range shown {
		scope := ""
		if c.Category != "" {
			scope = " [" + c.Category + "]"
		}
		// Wrapped, not clipped. A caveat is a whole sentence explaining why a
		// figure above should be read differently, and every one of them ran
		// past the pane's width and lost its ending — including the one that
		// carries the anchor's cache-read split, whose numbers sat in the part
		// that was cut off.
		body := contextWrap(c.Severity.String()+scope+" ("+c.Code+"): "+c.Message, max0(p.width-6))
		for i, l := range body {
			indent := "    "
			if i > 0 {
				indent = "      "
			}
			if c.Severity == ctxinspect.SeverityBug {
				out = append(out, contextBad(indent+l))
				continue
			}
			out = append(out, contextDim(indent+l))
		}
	}
	if held > 0 {
		out = append(out, contextDim(fmt.Sprintf(
			"    %d further caveat%s worth knowing, none of them saying a figure is wrong — press 3 for Verify.",
			held, contextPlural(held))))
	}
	return out
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

// View renders the overlay: three header rows, the scrolling body, and two
// footer rows. Every line is truncated to the terminal width (ANSI-aware) and
// gets a trailing SGR reset so a colour inside captured content cannot bleed
// into the next row or the chrome.
func (p *ContextPager) View() string {
	if p == nil || !p.visible {
		return ""
	}
	width := p.width
	if width < 1 {
		width = 1
	}

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("24"))
	subStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	tabActive := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	tabIdle := lipgloss.NewStyle().Foreground(ColorTextDim)
	footerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("236"))
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	headStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	badStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorError)
	selStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238"))

	var b strings.Builder

	// Row 1: identity + position.
	pos := p.positionLabel()
	header := cellTruncate(" ctx · "+p.headerIdentity()+" ", width-lipgloss.Width(pos)-1, "…")
	header += strings.Repeat(" ", max0(width-lipgloss.Width(header)-lipgloss.Width(pos)-1)) + pos + " "
	// Truncate again: on a terminal narrower than the position label alone,
	// the assembled row can still overflow, and a header wider than the
	// terminal wraps and shifts every row below it.
	b.WriteString(headerStyle.Width(width).Render(cellTruncate(header, width, "…")))
	b.WriteString("\n")

	// Row 2: basis, anchor and breadcrumb.
	b.WriteString(subStyle.Render(cellTruncate(" "+p.basisLine(), width, "…")))
	b.WriteString("\x1b[0m\n")

	// Row 3: tabs.
	var tabs strings.Builder
	tabs.WriteString(" ")
	for i, title := range contextPagerTabTitles {
		if i == p.tab {
			tabs.WriteString(tabActive.Render("[ " + title + " ]"))
		} else {
			tabs.WriteString(tabIdle.Render("  " + title + "  "))
		}
		tabs.WriteString(" ")
	}
	b.WriteString(cellTruncate(tabs.String(), width, "…"))
	b.WriteString("\x1b[0m\n")

	// Body.
	body := p.bodyHeight()
	rows := 0
	switch {
	case p.loading:
		b.WriteString(p.renderMessage(dimStyle, "Reading what this session is being sent…", body, width))
		rows = body
	case p.errText != "":
		b.WriteString(p.renderMessage(badStyle, "Could not inspect this context: "+p.errText, body, width))
		rows = body
	case p.report == nil:
		b.WriteString(p.renderMessage(dimStyle, "No report.", body, width))
		rows = body
	default:
		lines := p.renderBody()
		s := p.current()
		offset := 0
		cursor := -1
		if s != nil {
			offset = s.offset
			cursor = s.cursor
		}
		if offset > len(lines) {
			offset = max0(len(lines) - 1)
		}
		end := offset + body
		if end > len(lines) {
			end = len(lines)
		}
		// Widow control: never end a frame on a heading whose body continues
		// below the fold. The heading is withheld (its row pads blank) until a
		// scroll brings it in together with its first body line. When the
		// buffer's true end is visible there is nothing below the fold and
		// nothing to withhold.
		for end < len(lines) && end-offset > 1 && lines[end-1].keepWithNext {
			end--
		}
		selectable := p.rowCount() > 0
		for i := offset; i < end; i++ {
			line := lines[i]
			text := cellTruncate(line.raw, width, "…")
			switch {
			case selectable && line.row >= 0 && line.row == cursor:
				b.WriteString(selStyle.Width(width).Render(text))
			case line.bad:
				b.WriteString(badStyle.Render(text))
			case line.head:
				b.WriteString(headStyle.Render(text))
			case line.dim:
				b.WriteString(dimStyle.Render(text))
			default:
				b.WriteString(text)
			}
			b.WriteString("\x1b[0m\n")
			rows++
		}
	}
	for rows < body { // pad short buffers so the footer stays pinned
		b.WriteString("\n")
		rows++
	}

	// Footer row 1: the report's own self-check, never scrollable away.
	verdict := p.verdictLine()
	b.WriteString(footerStyle.Width(width).Render(cellTruncate(" "+verdict+" ", width, "…")))
	b.WriteString("\n")

	// Footer row 2: transient status, else key hints.
	hint := p.status
	if hint == "" {
		hint = p.keyHints()
	}
	b.WriteString(footerStyle.Width(width).Render(cellTruncate(" "+hint+" ", width, "…")))
	return b.String()
}

// headerIdentity is the first header row's subject: session, tool, model and
// window, with the window's source, because a denominator with no provenance
// turns an honest numerator into a misleading percentage.
func (p *ContextPager) headerIdentity() string {
	name := contextOrDefault(p.title, contextOrDefault(p.sessionID, "session"))
	parts := []string{name}
	if p.report != nil {
		rep := p.report
		parts = append(parts, contextOrDefault(rep.Harness, p.tool))
		if adapter := strings.TrimSpace(rep.Adapter); adapter != "" && adapter != rep.Harness {
			parts = append(parts, "adapter "+adapter)
		}
		parts = append(parts, contextOrDefault(rep.Model, "model not recorded"))
		parts = append(parts, "window "+contextWindowLine(rep.Window))
	} else if strings.TrimSpace(p.tool) != "" {
		parts = append(parts, p.tool)
	}
	return strings.Join(parts, " · ")
}

// basisLine is the second header row: whether the report was observed or
// projected, what measured the anchor, and where in the drill the user is.
func (p *ContextPager) basisLine() string {
	if p.report == nil {
		if p.loading {
			return "inspecting…"
		}
		return "no report"
	}
	rep := p.report
	out := strings.ToUpper(rep.Basis.String()) + " — " + rep.Basis.Describe()
	if rep.Anchor != nil {
		out += " · anchor " + contextTokenCount(rep.Anchor.Tokens) + " measured"
	} else {
		out += " · no measured anchor"
	}
	if crumbs := p.breadcrumb(); crumbs != "" {
		out += " · " + crumbs
	}
	return out
}

// breadcrumb joins the active tab's drill stack.
//
// Crumbs are shortened from the middle, because an item's crumb is usually an
// absolute path and the end is the part that identifies it. Tail truncation cut
// exactly the distinguishing bytes: in a tree holding three sibling CLAUDE.md
// files, the screen that says what you drilled into could not say which one.
func (p *ContextPager) breadcrumb() string {
	stack := p.stacks[p.tab]
	if len(stack) <= 1 {
		return ""
	}
	parts := make([]string, 0, len(stack))
	for _, s := range stack {
		parts = append(parts, contextShortenMiddle(s.crumb, contextCrumbWidth))
	}
	return strings.Join(parts, " › ")
}

// contextCrumbWidth bounds one breadcrumb segment.
const contextCrumbWidth = 40

// contextShortenMiddle drops the middle of an over-long string, keeping both
// ends. For a path that means keeping the root hint and the filename — the two
// parts a reader actually needs to tell one entry from another.
func contextShortenMiddle(s string, width int) string {
	if width < 8 || cellWidth(s) <= width {
		return s
	}
	r := []rune(s)
	keep := width - 1 // room for the ellipsis
	head := keep / 3
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// positionLabel is the right-hand side of the header row.
func (p *ContextPager) positionLabel() string {
	switch {
	case p.loading:
		return "loading…"
	case p.errText != "":
		return "error"
	case p.report == nil:
		return "empty"
	}
	total := p.lineCount()
	if total == 0 {
		return "empty"
	}
	s := p.current()
	offset := 0
	if s != nil {
		offset = s.offset
	}
	last := offset + p.bodyHeight()
	if last > total {
		last = total
	}
	if rows := p.rowCount(); rows > 0 && s != nil {
		return fmt.Sprintf("row %d/%d · lines %d-%d/%d", s.cursor+1, rows, offset+1, last, total)
	}
	return fmt.Sprintf("lines %d-%d/%d", offset+1, last, total)
}

// verdictLine is the always-visible footer row: the report's own arithmetic
// self-check and the error bound on its estimates.
//
// It is deliberately compact — a label, the coverage figure and the estimator's
// method — because it has one row on every screen and a truncated self-check is
// worse than a terse one. The full sentences behind each part are on the Verify
// tab, which is one keystroke away.
func (p *ContextPager) verdictLine() string {
	if p.report == nil {
		return "no report yet — nothing has been measured or estimated"
	}
	rec := p.report.Reconciliation
	// "RECON" here is the arithmetic verdict; "RECON" in the PROVENANCE column
	// is text that was reconstructed. One screen, one abbreviation, two
	// unrelated meanings. This one spells itself out.
	out := "self-check: " + contextReconLabel(rec)
	if rec.Status == ctxinspect.ReconOK {
		// A bare "coverage 1.8%" next to an OK verdict reads as "this report is
		// 1.8% reliable". It is the share of measured tokens that carry a name,
		// and the rest is the harness's own prompt — which is the interesting
		// fact, not a confidence score.
		out += fmt.Sprintf(" · %.1f%% of the measured total has a name here", rec.Coverage)
	}
	return out + " · " + contextCalibrationShort(p.report)
}

// contextReconLabel is the short form of the reconciliation verdict.
func contextReconLabel(rec ctxinspect.Reconciliation) string {
	if rec.Status == ctxinspect.ReconFailed {
		return "FAILED — attributed more than the provider measured; this report is not trustworthy"
	}
	return strings.ToUpper(rec.Status.String())
}

// contextCalibrationShort is the one-clause form of the estimator's error
// bound. An unbounded estimate says so rather than staying silent.
func contextCalibrationShort(rep *ctxinspect.Report) string {
	if c := rep.Calibration; c != nil {
		return fmt.Sprintf("estimates within %+.1f%% of the measured total (%s)", c.ErrorPct, c.Method)
	}
	method := rep.EstimatorMethod()
	if method == "" {
		return "no estimated figures"
	}
	// The divisors are calibrated against a recorded /context capture, so a
	// session with no anchor of its own still has a band. Calling that
	// "unbounded" undersold the figures and invited the reader to distrust
	// numbers that are in fact checked.
	if band, ok := rep.EstimatorBandPct(); ok {
		return fmt.Sprintf("~ = estimated (%s), each within ±%s%% of its calibration", method, contextTrimPct(band))
	}
	return "~ = estimated (" + method + "), error unbounded on this session"
}

// contextTrimPct renders a percentage with at most one decimal and no trailing
// ".0", so a band reads "±8%" and not "±8.0%".
func contextTrimPct(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}

// keyHints is the second footer row when no transient status is set.
func (p *ContextPager) keyHints() string {
	base := "↑/↓ move · PgUp/PgDn · g/G top/end · Tab or 1/2/3 switch tab"
	if p.report == nil {
		return base + " · r retry · Esc close"
	}
	if p.Depth() > 0 {
		return base + " · Enter/→ open · Esc/← back · y copy lever · r refresh"
	}
	return base + " · Enter/→ open · y copy lever · r refresh · Esc close"
}

// renderMessage centres a one-line message within the body rows.
func (p *ContextPager) renderMessage(style lipgloss.Style, msg string, body, width int) string {
	var b strings.Builder
	top := body / 2
	for i := 0; i < body; i++ {
		if i == top {
			b.WriteString(style.Render(cellTruncate("  "+msg, width, "…")))
			b.WriteString("\x1b[0m")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Formatting helpers
//
// These mirror the `agent-deck session context` renderer so the two surfaces
// cannot disagree about what a figure means.
//
// "Mirror" was once the whole mechanism — the helpers were duplicated on the
// grounds that cmd/agent-deck is package main and cannot be imported, and that
// pushing presentation into the engine would let a renderer's formatting choice
// leak into the JSON schema. Both grounds are real; the conclusion was not.
// Duplication is not a mechanism, it is a hope, and it failed exactly where it
// mattered: on an unknown context window the CLI printed a sentence and this
// pager printed the bare word "unknown".
//
// The sentences that must not drift now live in
// [github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext], which is
// importable by both and imported by neither the engine nor the JSON schema.
// What stays here is layout: wrapping to this pane's width, colour, truncation
// — the part that legitimately differs from a fixed-width transcript.
// ---------------------------------------------------------------------------

// contextGaugeLine renders the occupancy bar and the fixed-overhead figure.
//
// When no window size was established the percentage is withheld — a percentage
// of an unknown denominator is exactly the kind of plausible number this feature
// exists to avoid — but the bar keeps its shape, and the reason and the remedy
// follow immediately underneath in [contextWindowUnknownLines], where they wrap
// instead of truncating. The bar used to be drawn with the same empty glyph a
// working gauge uses at 0%, so an unknown window and an empty one were
// pixel-identical.
func contextGaugeLine(rep *ctxinspect.Report, fixed int, complete bool) string {
	if !complete && fixed == 0 {
		return "fixed startup overhead: not measurable — no figure in this report is known, so there is no total to show"
	}
	amount := contextGaugeAmount(rep, fixed, complete)
	pct, ok := rep.Window.Percent(fixed)
	if !ok {
		// The reason and the remedy are the two wrapped sentences immediately
		// below — see [ctxtext.WindowGaugeSentences]. Hanging them off this
		// line's right edge would put them exactly where this pane truncates.
		return fmt.Sprintf("%s  %s / ?  fixed startup overhead",
			ctxtext.IndeterminateBar(contextGaugeWidth), amount)
	}
	return fmt.Sprintf("%s  %s / %s  (%s)  fixed startup overhead%s",
		contextGaugeBar(pct, contextGaugeWidth), amount, contextTokenAmount(rep.Window.Tokens),
		ctxtext.PercentText(rep.Window, pct), contextGaugeBoundSuffix(rep))
}

// contextGaugeAmount is the gauge's numerator, marked "≤" when the anchor it
// derives from measured more than the fixed prefix.
//
// The gauge is the one figure everybody reads and nobody scrolls past, and it
// inherits the anchor's over-estimate exactly. Publishing it bare while the
// qualifier sits on the Verify tab is the same defect as badging the anchor
// itself as an exact measurement.
func contextGaugeAmount(rep *ctxinspect.Report, fixed int, complete bool) string {
	amount := contextTotalTokens(fixed, complete)
	if rep.Anchor.IsUpperBound() {
		return "≤" + amount
	}
	return amount
}

// contextGaugeBoundSuffix names the qualifier the "≤" stands for, once, on the
// same line.
func contextGaugeBoundSuffix(rep *ctxinspect.Report) string {
	if !rep.Anchor.IsUpperBound() {
		return ""
	}
	return " — at most: it also covers turns recorded before the startup catalogues"
}

// contextWindowUnknownLines explains an untrustworthy denominator where the
// percentage it produced (or failed to produce) is read, wrapped to the pane so
// the remedy is never the part that falls off the right edge.
//
// The sentences come from [ctxtext.WindowGaugeSentences] — the same ones the
// CLI prints — and are wrapped here, at this pane's width, because wrapping is
// the part that legitimately differs between the two surfaces.
func contextWindowUnknownLines(w ctxinspect.WindowInfo, width int) []string {
	var out []string
	for _, sentence := range ctxtext.WindowGaugeSentences(w) {
		out = append(out, contextWrap(sentence, width)...)
	}
	return out
}

// contextWrap breaks a sentence into lines that fit the given width, on word
// boundaries. Width includes the caller's indent.
func contextWrap(s string, width int) []string {
	limit := width - 4
	if limit < 20 {
		limit = 20
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case cellWidth(line)+1+cellWidth(word) <= limit:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// contextGaugeWidth is the width of the overview's occupancy bar.
const contextGaugeWidth = 28

// contextGaugeBar renders a fixed-width occupancy bar. Values outside 0–100 are
// clamped for display only; the figure beside it is never clamped.
func contextGaugeBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	filled := int(math.Round(pct / 100 * float64(width)))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// contextCategorySummary is the right-hand cell of an overview row.
func contextCategorySummary(cat ctxinspect.Category) string {
	actionable := 0
	for _, it := range cat.Items {
		if it.Actionable() {
			actionable++
		}
	}
	// "(0 items)" is a count, and a count is a measurement. A category whose
	// contents were never established has nothing to count, and printing zero
	// there turns "we did not read this" into "you have none of these".
	n, known := cat.DisplayItemCount()
	if !known {
		return contextCategoryTitle(cat) + " (not observed — press 3 for Verify)"
	}
	summary := fmt.Sprintf("%s (%d item%s", contextCategoryTitle(cat), n, contextPlural(n))
	if actionable > 0 {
		summary += fmt.Sprintf(", %d actionable", actionable)
	}
	return summary + ")"
}

// contextCategoryBadgeCell renders the PROVENANCE cell of a summary row,
// leaving it an explicit unknown for a category nobody was able to look inside.
func contextCategoryBadgeCell(cat ctxinspect.Category) string {
	badge, ok := cat.DisplayBadge()
	if !ok {
		return "—"
	}
	return badge.Short()
}

// contextCategoryTitle returns a category's display heading.
func contextCategoryTitle(cat ctxinspect.Category) string {
	return contextOrDefault(cat.Title, cat.Name)
}

// contextHistoryLine renders the single conversation orientation line.
func contextHistoryLine(h *ctxinspect.HistoryLine) string {
	out := "history: " + contextTokenCount(h.Tokens)
	if h.Turns > 0 {
		out += fmt.Sprintf(" over %d turns", h.Turns)
	}
	note := contextOrDefault(h.Note, "orientation only; not part of the fixed startup overhead")
	return out + " (" + note + ")"
}

// contextLockGlyph prefixes items the user cannot act on.
func contextLockGlyph(it ctxinspect.Item) string {
	if it.Actionable() {
		return ""
	}
	return "🔒 "
}

// contextItemLabel returns an item's display name, falling back to its id.
func contextItemLabel(it ctxinspect.Item) string {
	return contextOrDefault(it.Label, it.ID)
}

// contextUnsupportedBanner returns the lines shown when the harness exposes no
// token accounting at all. It reads what the adapter *declared* rather than
// what this run happened to produce: an empty report and an unmeasurable
// harness are different things and must not print the same banner.
func contextUnsupportedBanner(rep *ctxinspect.Report, tool string) ([]string, bool) {
	caps := rep.Capabilities
	if caps.CanAnchor {
		return nil, false
	}
	for _, cc := range caps.Categories {
		if cc.Token != ctxinspect.TokenUnknown {
			return nil, false
		}
	}
	reason := "this harness exposes no token accounting agent-deck can read, and agent-deck will not guess one"
	if len(caps.Categories) == 0 {
		reason = "the adapter declared no reportable categories"
	}
	name := contextOrDefault(tool, contextOrDefault(rep.Harness, "this tool"))
	return []string{
		"token accounting unsupported for " + name + ": " + reason + ".",
		"what follows is an inventory of what is configured, not a measurement of what is loaded.",
	}, true
}

// contextAnchorHow explains where the anchor came from, or why there is none.
func contextAnchorHow(rep *ctxinspect.Report) string {
	if rep.Anchor == nil {
		return "no cold-start turn was recorded for this session, so nothing measured the fixed prefix"
	}
	// The provider field path and the component sums live in the block below,
	// where they are wrapped. Repeating them in a table cell only put them
	// where this pane clips, and a truncated arithmetic statement is worse than
	// a pointer to an untruncated one.
	out := "provider-reported — exact figures and arithmetic below"
	if rep.Anchor.IsUpperBound() {
		// This is the figure every other number reconciles against, and
		// "measured" reads as "exact" unless the screen says otherwise.
		out = "UPPER BOUND — " + out
	}
	return out
}

// contextReconVerdict renders the reconciliation status and its explanation.
func contextReconVerdict(rec ctxinspect.Reconciliation) string {
	label := strings.ToUpper(rec.Status.String())
	if rec.Status == ctxinspect.ReconFailed {
		label = "RECONCILIATION FAILED"
	}
	return label + " — " + rec.Message
}

// contextEstimatorFooter renders the estimator's self-reported error bound, or
// says plainly that there is none.
func contextEstimatorFooter(rep *ctxinspect.Report) string {
	if rep.Calibration != nil {
		return rep.Calibration.Summary()
	}
	method := rep.EstimatorMethod()
	if method == "" {
		return "this report contains no estimated figures"
	}
	if band, ok := rep.EstimatorBandPct(); ok {
		return fmt.Sprintf("figures marked ~ are estimates (method: %s), each within ±%s%% of its calibration; this session had no measured counterpart to re-check them against",
			method, contextTrimPct(band))
	}
	return "figures marked ~ are estimates (method: " + method + ") with no measured counterpart to bound their error on this session"
}

// contextWindowLine renders the context-window size with how it was
// established. The source is never omitted.
//
// It is [ctxtext.WindowLine], the same function the CLI header calls. The two
// used to be separate implementations of the same sentence and had drifted to
// the point where the CLI explained a missing window and this pager printed the
// bare word "unknown" — a dead end that cannot tell a reader whether the
// feature is broken, the model unsupported, or the fix one environment variable
// away. Shared, they cannot drift again.
func contextWindowLine(w ctxinspect.WindowInfo) string { return ctxtext.WindowLine(w) }

// contextLoadLine renders an item's current and potential cost together.
func contextLoadLine(l ctxinspect.Load) string {
	out := l.State.String() + " — actual " + contextTokenCount(l.Actual)
	if l.Potential != nil {
		out += ", potential " + contextTokenCount(*l.Potential) + " if fully loaded (never added to any total)"
	}
	return out
}

// contextTextProvLine explains the text axis in a sentence.
func contextTextProvLine(c ctxinspect.Content) string {
	out := c.Prov.String()
	switch c.Prov {
	case ctxinspect.TextCaptured:
		out += " — verbatim, read back from a record the harness itself wrote"
	case ctxinspect.TextReconstructed:
		out += " — rebuilt by re-running the harness's own discovery rules; it should match, but a harness change could make it differ"
	default:
		out += " — known to be in the context window and not readable from disk"
	}
	if note := strings.TrimSpace(c.Note); note != "" {
		out += " (" + note + ")"
	}
	return out
}

// contextTokenProvLine explains the token axis in a sentence, naming the method
// for an estimate and the reason for an unknown.
func contextTokenProvLine(t ctxinspect.TokenCount) string {
	out := t.Prov().String()
	if reason := strings.TrimSpace(t.Reason()); reason != "" {
		return out + " — " + reason
	}
	if method := strings.TrimSpace(t.Method()); method != "" {
		return out + " — " + method
	}
	return out
}

// contextLeverLine renders what the user can do about an item.
func contextLeverLine(l ctxinspect.Lever) string {
	var out string
	switch l.Kind {
	case ctxinspect.LeverEditFile:
		out = "edit " + l.Path
		if l.LineRange[0] > 0 && l.LineRange[1] >= l.LineRange[0] {
			out += fmt.Sprintf(" (lines %d–%d)", l.LineRange[0], l.LineRange[1])
		}
	case ctxinspect.LeverDeleteDir:
		out = "delete directory " + l.Path
	case ctxinspect.LeverRunCommand:
		out = "run: " + l.Command
	default:
		out = "immovable"
	}
	if why := strings.TrimSpace(l.Why); why != "" {
		out += " — " + why
	}
	return out
}

// contextPotentialCell renders a potential cost, or an em dash when there is
// none.
func contextPotentialCell(tokens int, ok bool) string {
	if !ok {
		return "—"
	}
	return "~" + contextTokenAmount(tokens)
}

// contextTokenCount renders a count with the marker its provenance earns:
// nothing for a measured or computed figure, "~" for anything derived, and an
// em dash for an unknown, which must never read as zero.
func contextTokenCount(t ctxinspect.TokenCount) string {
	v, ok := t.Value()
	if !ok {
		return "—"
	}
	if v < 0 {
		// A negative figure is an arithmetic contradiction, not an estimate of
		// anything. Marking it "~" would suggest it is approximately right.
		return contextTokenAmount(v)
	}
	switch t.Prov() {
	case ctxinspect.TokenEstimated, ctxinspect.TokenResidual:
		return "~" + contextTokenAmount(v)
	default:
		return contextTokenAmount(v)
	}
}

// contextTotalTokens renders a total, prefixing "≥" when a contributing count
// was unknown so a lower bound cannot be misread as a total. A lower bound of
// zero is not a lower bound: it renders as an em dash like any other unknown.
func contextTotalTokens(tokens int, complete bool) string {
	if complete {
		return contextTokenAmount(tokens)
	}
	if tokens == 0 {
		return "—"
	}
	return "≥" + contextTokenAmount(tokens)
}

// contextTokenAmount renders a token figure compactly. Negative values keep
// their sign: a negative residual is a reported bug, not something to hide.
// Shared with the CLI renderer so "1.0M" cannot come to mean two things.
func contextTokenAmount(n int) string { return ctxtext.TokenAmount(n) }

// contextTable lays rows out in left-aligned columns padded to the widest cell.
// The final column is never padded, so a long path cannot introduce trailing
// whitespace that a background style would then paint across the screen.
//
// Widths are measured in terminal cells, not runes: an em dash, a lock glyph
// and a CJK path component all occupy a different number of columns from their
// rune count, and padding by rune count visibly skews every column after them.
func contextTable(rows [][]string, indent string) []string {
	if len(rows) == 0 {
		return nil
	}
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			w := cellWidth(cell)
			if i >= len(widths) {
				widths = append(widths, w)
				continue
			}
			if w > widths[i] {
				widths[i] = w
			}
		}
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		var b strings.Builder
		b.WriteString(indent)
		for i, cell := range row {
			if i == len(row)-1 {
				b.WriteString(cell)
				break
			}
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", max0(widths[i]-cellWidth(cell))+2))
		}
		out = append(out, b.String())
	}
	return out
}

// contextYesNo renders a capability flag.
func contextYesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// contextPlural returns the plural suffix for a count.
func contextPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// contextOrDefault returns s trimmed, or fallback when it is empty.
func contextOrDefault(s, fallback string) string {
	if v := strings.TrimSpace(s); v != "" {
		return v
	}
	return fallback
}
