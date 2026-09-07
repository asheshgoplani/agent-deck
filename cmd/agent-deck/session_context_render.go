package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
)

// contextView is everything the `session context` renderers need.
//
// It is a plain value on purpose: every renderer below is a pure function of
// this struct, so the whole human-readable surface is testable without a live
// session, a harness, or a HOME.
type contextView struct {
	// Ref is how the user named the session.
	Ref string
	// Title is the agent-deck session title.
	Title string
	// Profile is the agent-deck profile the session lives in.
	Profile string
	// Tool is the agent-deck tool name.
	Tool string
	// Report is the inspection result. Never nil in a rendered view.
	Report *ctxinspect.Report
	// Warnings are resolution problems found before inspection ran — an
	// unresolvable transcript, a colliding session id.
	Warnings []string
	// Verbose asks for the adapter's per-category notes inline. They are
	// correct and they are numerous — on a fresh session there are twice as
	// many note lines as data rows — so by default the overview says how many
	// there are and where to read them. A screen whose disclaimers outweigh its
	// figures three to one does not read as careful; it reads as unfinished.
	Verbose bool
}

// contextScopeLine is the sentence that has to be on the first screen.
//
// Everything this command measures is the fixed startup overhead: the same
// bytes on every turn, the part a user can actually delete. It is not the
// conversation, and "my context is full" almost always means the conversation.
// Without this line the reader takes a small percentage as the answer to a
// question the report never asked.
const contextScopeLine = "this measures the FIXED STARTUP OVERHEAD — the instruction files, skills, agents and tool schemas loaded into every turn.\nit does not measure your conversation, which is what usually fills a context window."

// rankedItem is one row of the breakdown: an item plus the category it came
// from, so a flat ranking can still say where each entry lives.
type rankedItem struct {
	Category string
	Item     ctxinspect.Item
}

// defaultBreakdownRows bounds the default breakdown so the interesting rows are
// not pushed off a terminal by a long tail of near-zero items. --all lifts it.
const defaultBreakdownRows = 20

// gaugeWidth is the width of the overview's occupancy bar.
const gaugeWidth = 28

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------

// renderContextOverview renders level 1: the header, the occupancy gauge and
// one row per adapter-declared category.
func renderContextOverview(v contextView) string {
	rep := v.Report
	var b strings.Builder

	writeContextHeader(&b, v)
	b.WriteString("\n")

	fixed, complete := rep.FixedTotal()
	b.WriteString("  " + contextGauge(rep, fixed, complete) + "\n")
	// The reason belongs here, under the gauge whose missing percentage
	// prompted the question — not in the caveat block far below it.
	for _, line := range ctxtext.WindowGaugeSentences(rep.Window) {
		b.WriteString("  " + line + "\n")
	}
	for _, line := range strings.Split(contextScopeLine, "\n") {
		b.WriteString("  " + line + "\n")
	}
	// The payoff, on the screen that asks the question. This report exists so
	// somebody can clean something up, and until this line the overview had no
	// verb on it: the only actionable content was a level down, behind a
	// heading nobody had a reason to open.
	b.WriteString("  " + contextActionableLine(rep) + "\n")
	b.WriteString("\n")

	rows := [][]string{{"TOKENS", "POTENTIAL", "PROVENANCE", "CATEGORY"}}
	for _, cat := range rep.Categories {
		// DisplayTotal, not Total: a category whose contents were never
		// established must not print the same "0" as one established to be
		// empty. "0 MCP instructions" is a claim about the user's setup that an
		// unread transcript cannot support.
		total, catComplete := cat.DisplayTotal()
		potential, hasPotential := cat.PotentialTotal()
		rows = append(rows, []string{
			formatTotalTokens(total, catComplete),
			potentialCell(potential, hasPotential),
			categoryBadgeCell(cat),
			categorySummary(cat),
		})
	}
	if rep.Unaccounted != nil {
		rows = append(rows, []string{
			formatTokenCount(rep.Unaccounted.Load.Actual),
			"—",
			rep.Unaccounted.Badge().Short(),
			rep.Unaccounted.Label + " (unattributed remainder, not configurable)",
		})
	}
	if len(rows) == 1 {
		b.WriteString("  no categories: this adapter could report nothing about this session\n")
	} else {
		writeContextTable(&b, rows, "  ")
	}

	if h := rep.History; h != nil {
		b.WriteString("\n  history: " + formatTokenCount(h.Tokens))
		if h.Turns > 0 {
			b.WriteString(fmt.Sprintf(" over %d turns", h.Turns))
		}
		note := strings.TrimSpace(h.Note)
		if note == "" {
			note = "orientation only; not part of the fixed startup overhead"
		}
		b.WriteString(" (" + note + ")\n")
	}

	b.WriteString("\n")
	b.WriteString(contextLegend("  "))

	b.WriteString("\n")
	notes := 0
	for _, cat := range rep.Categories {
		for _, note := range cat.Notes {
			notes++
			if v.Verbose {
				b.WriteString("  note (" + cat.Name + "): " + note + "\n")
			}
		}
	}
	if notes > 0 && !v.Verbose {
		b.WriteString(fmt.Sprintf("  %d note%s about how these figures were obtained: pass --verbose to read them.\n", notes, plural(notes)))
	}
	b.WriteString("  " + estimatorFooter(rep) + "\n")
	b.WriteString("  self-check: " + reconVerdict(rep.Reconciliation) + "\n")

	// The first screen holds back the caveats that say nothing is wrong. Warns
	// and bugs stay, because they are the ones that change how a figure above
	// should be read.
	writeContextCaveatsRanked(&b, v, true)
	b.WriteString("\nnext: --tab breakdown ranks every item and names what to do about it; --item <id> prints one item's text; --tab verify shows the arithmetic.\n")
	return b.String()
}

// contextActionableLine is the overview's one actionable sentence, with the
// flag that lists the items appended when there are any.
func contextActionableLine(rep *ctxinspect.Report) string {
	n, tokens, complete := rep.ActionableTotal()
	line := ctxtext.ActionableSentence(n, tokens, complete, formatTotalTokens(tokens, complete))
	if n == 0 {
		return line
	}
	// Not "largest first": the ranking puts actionable items first, then loaded
	// before deferred, then larger before smaller. Promising a pure cost order
	// here would repeat the exact mistake the breakdown headline just stopped
	// making one screen away.
	return line + " --tab breakdown lists them first, with what to do about each."
}

// contextLegend explains the on-screen shorthand.
//
// Every code below appears on the default screen, and none of them was defined
// anywhere: a reader met CAPT/~est, ABSENT/residual and 🔒 with only "~ =
// estimated" to go on. A vocabulary the screen uses and never explains is not
// rigour, it is a private notation.
func contextLegend(indent string) string {
	var b strings.Builder
	b.WriteString(indent + "reading the columns:\n")
	b.WriteString(indent + "  TOKENS      what it costs in every turn.  ~ estimated · ≥ lower bound · — not knowable (never zero)\n")
	b.WriteString(indent + "  POTENTIAL   what it would cost if fully loaded. Never added to the total.\n")
	b.WriteString(indent + "  PROVENANCE  text/tokens.  text: CAPT verbatim from the harness · RECON rebuilt from its rules · ABSENT not on disk\n")
	b.WriteString(indent + "              tokens: measured by the provider · ~est estimated by agent-deck · residual by subtraction · — unknown\n")
	// A RECON row is as of now on disk. A session that is already running is
	// still sending what it booted with, so editing a file moves the figure here
	// before it moves anything the model receives. Left unsaid, the report is
	// quietly wrong for exactly the reader who acted on it.
	b.WriteString(indent + "              RECON is as of now on disk; a running session keeps its boot-time copy until you restart it.\n")
	b.WriteString(indent + "  🔒          you cannot remove it: harness internals, or content agent-deck did not put there.\n")
	// The legend defines the codes in a table cell. adapter, basis, anchor,
	// residual and reconciliation are on this screen too, and they need a
	// sentence each rather than a column, so they live one flag away.
	b.WriteString(indent + "  adapter, basis, anchor, residual, reconciliation: --glossary defines them.\n")
	return b.String()
}

// renderContextQuiet is the whole report in one line: the gauge figure, the
// denominator when there is one, and how many things the user can act on.
//
// It exists because -q used to print nothing at all and exit 0, which for a
// reporting command is indistinguishable from a silent failure. A quiet mode
// should be terse, not mute.
func renderContextQuiet(v contextView) string {
	rep := v.Report
	fixed, complete := rep.FixedTotal()

	amount := gaugeAmount(rep, fixed, complete)
	head := amount + " fixed startup overhead"
	if pct, ok := rep.Window.Percent(fixed); ok {
		head = fmt.Sprintf("%s / %s (%s) fixed startup overhead",
			amount, formatTokenAmount(rep.Window.Tokens), ctxtext.PercentText(rep.Window, pct))
		if rep.Window.Assumed() {
			head += " (window assumed, not measured)"
		}
	} else {
		// One line is all this mode gets, and it still has to leave the reader
		// somewhere to go.
		head += " (no window size known, so no percentage; " + ctxtext.WindowRemedy() + ")"
	}
	if rep.Anchor.IsUpperBound() {
		// One line is all this mode gets; the qualifier still has to be in it.
		head += " (upper bound)"
	}

	// "Remove" was the wrong verb here for the same reason it was wrong on the
	// breakdown headline: most levers are an edit. And a count with no figure
	// beside it does not answer the question a one-line mode is asked.
	n, tokens, complete := rep.ActionableTotal()
	return fmt.Sprintf("%s; %d item%s you can act on, worth %s (--tab breakdown to see them)",
		head, n, plural(n), formatTotalTokens(tokens, complete))
}

// categorySummary is the right-hand cell of an overview row: the title plus the
// counts that tell the user whether it is worth opening.
func categorySummary(cat ctxinspect.Category) string {
	actionable := 0
	for _, it := range cat.Items {
		if it.Actionable() {
			actionable++
		}
	}
	title := strings.TrimSpace(cat.Title)
	if title == "" {
		title = cat.Name
	}
	// "(0 items)" is a count, and a count is a measurement. A category whose
	// contents were never established has nothing to count, and printing zero
	// there turns "we did not read this" into "you have none of these".
	n, known := cat.DisplayItemCount()
	if !known {
		return title + " (not observed — see the note)"
	}
	summary := fmt.Sprintf("%s (%d item%s", title, n, plural(n))
	if actionable > 0 {
		summary += fmt.Sprintf(", %d actionable", actionable)
	}
	return summary + ")"
}

// categoryBadgeCell renders the PROVENANCE cell of a summary row, leaving it an
// explicit unknown for a category nobody was able to look inside.
func categoryBadgeCell(cat ctxinspect.Category) string {
	badge, ok := cat.DisplayBadge()
	if !ok {
		return "—"
	}
	return badge.Short()
}

// contextGauge renders the occupancy bar plus the fixed-overhead figure. It
// degrades to a bare figure when no window size was established, because a
// percentage of an unknown denominator is exactly the kind of plausible number
// this feature exists to avoid.
func contextGauge(rep *ctxinspect.Report, fixed int, complete bool) string {
	if !complete && fixed == 0 {
		return "fixed startup overhead: not measurable — no figure in this report is known, so there is no total to show"
	}
	amount := gaugeAmount(rep, fixed, complete)
	pct, ok := rep.Window.Percent(fixed)
	if !ok {
		// The reason and the remedy are the two sentences immediately below
		// this line — see [ctxtext.WindowGaugeSentences]. They are not appended
		// here: this is the widest line on the screen and both surfaces
		// truncate it, so anything hung off its right edge is the first thing
		// to disappear.
		return fmt.Sprintf("%s  %s / ?  fixed startup overhead",
			ctxtext.IndeterminateBar(gaugeWidth), amount)
	}
	return fmt.Sprintf("%s  %s / %s  (%s)  fixed startup overhead%s",
		gaugeBar(pct, gaugeWidth), amount, formatTokenAmount(rep.Window.Tokens),
		ctxtext.PercentText(rep.Window, pct), gaugeBoundSuffix(rep))
}

// gaugeAmount is the gauge's numerator, marked "≤" when the anchor it derives
// from measured more than the fixed prefix.
//
// The gauge is the one figure everybody reads and nobody scrolls past, and it
// inherits the anchor's over-estimate exactly. Publishing it bare while the
// qualifier sits in a caveat block is the same defect as badging the anchor
// itself as an exact measurement.
func gaugeAmount(rep *ctxinspect.Report, fixed int, complete bool) string {
	amount := formatTotalTokens(fixed, complete)
	if rep.Anchor.IsUpperBound() {
		return "≤" + amount
	}
	return amount
}

// gaugeBoundSuffix names the qualifier the "≤" stands for, once, on the same line.
func gaugeBoundSuffix(rep *ctxinspect.Report) string {
	if !rep.Anchor.IsUpperBound() {
		return ""
	}
	return " — at most: the measurement also covers turns recorded before the startup catalogues"
}

// gaugeBar renders a fixed-width occupancy bar. Values outside 0–100 are
// clamped for display only; the numeric figure beside it is never clamped.
func gaugeBar(pct float64, width int) string {
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
	return "[" + strings.Repeat("#", filled) + strings.Repeat(".", width-filled) + "]"
}

// ---------------------------------------------------------------------------
// Breakdown
// ---------------------------------------------------------------------------

// renderContextBreakdown renders level 2: every item across every category,
// ranked by what it costs the user now, actionable entries first.
func renderContextBreakdown(v contextView, all bool) string {
	rep := v.Report
	var b strings.Builder

	writeContextHeader(&b, v)
	// The original headline ("ranked by current cost, actionable items first —
	// the top of this list is what you can remove for the most gain") was false
	// in three ways at once: the largest figure in the report is the residual,
	// pinned last because nobody can act on it; the top row's lever is usually
	// an edit, not a removal; and the ranking is not by cost alone. The order is
	// right, and it is the promise about it that has to change.
	b.WriteString("\nwhat you can act on, first — then what is merely costing you, then what is fixed.\n")
	b.WriteString("within each group: loaded before deferred, and larger before smaller. \"Act on\" is edit,\n")
	b.WriteString("delete or run a command, per row — the ITEM column's 🔒 marks the ones you cannot.\n\n")

	items := contextRankedItems(rep)
	if len(items) == 0 {
		b.WriteString("  no items: this adapter could attribute nothing about this session\n")
		writeContextCaveats(&b, v)
		return b.String()
	}

	shown := items
	truncated := 0
	if !all && len(items) > defaultBreakdownRows {
		shown = items[:defaultBreakdownRows]
		truncated = len(items) - defaultBreakdownRows
	}

	// The id is the last column and therefore unpadded: ids are derived from
	// absolute paths and are routinely longer than a terminal is wide, so
	// padding to the widest one would push every other column off the screen.
	rows := [][]string{{"TOKENS", "POTENTIAL", "PROVENANCE", "ORIGIN", "LOAD", "CATEGORY", "ITEM", "ID"}}
	for _, ri := range shown {
		potential, hasPotential := ri.Item.PotentialTokens()
		rows = append(rows, []string{
			formatTokenCount(ri.Item.Load.Actual),
			potentialCell(potential, hasPotential),
			ri.Item.Badge().Short(),
			ri.Item.Origin.String(),
			ri.Item.Load.State.String(),
			ri.Category,
			contextLockGlyphCLI(ri.Item) + truncateCell(itemLabel(ri.Item), maxItemCellWidth),
			ri.Item.ID,
		})
	}
	writeContextTable(&b, rows, "  ")

	if truncated > 0 {
		b.WriteString(fmt.Sprintf("\n  … %d more item%s hidden; pass --all to see them.\n", truncated, plural(truncated)))
	}

	// Name the elephant. The residual is routinely ~98% of the measured total
	// and it sits at the bottom of an "actionable first" list, which reads as a
	// ranking error unless the screen says out loud why it is there.
	if rep.Unaccounted != nil {
		if residual, ok := rep.Unaccounted.Load.Actual.Value(); ok && residual > 0 {
			b.WriteString(fmt.Sprintf("\n  the largest single figure in this report is the last row (%s, %s): it is the harness's own\n  system prompt and built-in tool schemas, and nothing you can do removes it.\n",
				formatTokenCount(rep.Unaccounted.Load.Actual), rep.Unaccounted.Label))
		}
	}

	b.WriteString("\nwhat to do about each:\n")
	levers := 0
	for _, ri := range shown {
		if !ri.Item.Actionable() {
			continue
		}
		levers++
		b.WriteString("  " + ri.Item.ID + "\n      " + leverLine(ri.Item.Lever) + "\n")
	}
	if levers == 0 {
		b.WriteString("  nothing listed above is under your control.\n")
	}

	b.WriteString("\n")
	b.WriteString(contextLegend("  "))
	writeContextCaveats(&b, v)
	b.WriteString("\nnext: --item <id> prints an item's text, its provenance and what to do about it.\n")
	return b.String()
}

// contextLockGlyphCLI marks an item the user cannot act on, matching the TUI.
func contextLockGlyphCLI(it ctxinspect.Item) string {
	if it.Actionable() {
		return ""
	}
	return "🔒 "
}

// maxItemCellWidth bounds the item column. An item label can be an absolute
// path; the full text is always available from --item.
const maxItemCellWidth = 44

// itemLabel returns the item's display name, falling back to its id.
func itemLabel(it ctxinspect.Item) string {
	if label := strings.TrimSpace(it.Label); label != "" {
		return label
	}
	return it.ID
}

// truncateCell shortens a cell to max runes, marking that it did so. It never
// truncates below a useful length.
func truncateCell(s string, max int) string {
	if max < 4 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}

// contextRankedItems flattens every category into one ranking.
//
// Rollup parents are replaced by their children so a group header and its
// members cannot both occupy a row (and cannot both be read as a cost). The
// order matches [ctxinspect.Category.Sorted] so the flat list and the per
// category lists never disagree, with the residual pinned last because it is
// the one row nobody can act on.
func contextRankedItems(rep *ctxinspect.Report) []rankedItem {
	var out []rankedItem
	var collect func(categoryName string, items []ctxinspect.Item)
	collect = func(categoryName string, items []ctxinspect.Item) {
		for _, it := range items {
			if it.Rollup {
				collect(categoryName, it.Children)
				continue
			}
			out = append(out, rankedItem{Category: categoryName, Item: it})
		}
	}
	for _, cat := range rep.Categories {
		collect(cat.Name, cat.Items)
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
			return oka
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
		out = append(out, rankedItem{Category: "unattributed", Item: *rep.Unaccounted})
	}
	return out
}

// ---------------------------------------------------------------------------
// Item detail
// ---------------------------------------------------------------------------

// errContextItemNotFound reports an --item id that matched nothing.
type errContextItemNotFound struct {
	ID string
}

func (e errContextItemNotFound) Error() string {
	return fmt.Sprintf("no context item with id %q in this report; run --tab breakdown to list the ids", e.ID)
}

// errContextItemAmbiguous reports an --item prefix that matched several items.
type errContextItemAmbiguous struct {
	ID      string
	Matches []string
}

func (e errContextItemAmbiguous) Error() string {
	return fmt.Sprintf("item id %q matches %d items (%s); use the full id",
		e.ID, len(e.Matches), strings.Join(e.Matches, ", "))
}

// findContextItem resolves an item by exact id, then by unique prefix.
//
// Rollup parents remain addressable — the caller may legitimately want the
// group view — so this walks the tree rather than the flattened ranking.
func findContextItem(rep *ctxinspect.Report, id string) (rankedItem, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return rankedItem{}, errContextItemNotFound{ID: id}
	}

	var all []rankedItem
	var walk func(categoryName string, items []ctxinspect.Item)
	walk = func(categoryName string, items []ctxinspect.Item) {
		for _, it := range items {
			all = append(all, rankedItem{Category: categoryName, Item: it})
			walk(categoryName, it.Children)
		}
	}
	for _, cat := range rep.Categories {
		walk(cat.Name, cat.Items)
	}
	if rep.Unaccounted != nil {
		all = append(all, rankedItem{Category: "unattributed", Item: *rep.Unaccounted})
	}

	for _, ri := range all {
		if ri.Item.ID == id {
			return ri, nil
		}
	}

	var matches []rankedItem
	var names []string
	for _, ri := range all {
		if strings.HasPrefix(ri.Item.ID, id) {
			matches = append(matches, ri)
			names = append(names, ri.Item.ID)
		}
	}
	switch len(matches) {
	case 0:
		return rankedItem{}, errContextItemNotFound{ID: id}
	case 1:
		return matches[0], nil
	default:
		return rankedItem{}, errContextItemAmbiguous{ID: id, Matches: names}
	}
}

// renderContextItem renders level 3: one item's provenance, lever and the
// actual bytes behind it.
func renderContextItem(v contextView, ri rankedItem) string {
	it := ri.Item
	var b strings.Builder

	writeContextHeader(&b, v)
	b.WriteString("\n")

	rows := [][]string{
		{"item", it.Label},
		{"id", it.ID},
		{"category", ri.Category},
	}
	if d := strings.TrimSpace(it.Detail); d != "" {
		rows = append(rows, []string{"detail", d})
	}
	rows = append(rows,
		[]string{"origin", it.Origin.String()},
		[]string{"load", loadLine(it.Load)},
		[]string{"text", textProvLine(it.Content)},
		[]string{"tokens", tokenProvLine(it.Load.Actual)},
		[]string{"lever", leverLine(it.Lever)},
	)
	writeContextTable(&b, rows, "  ")

	for _, c := range it.Caveats {
		b.WriteString("  caveat (" + c.Severity.String() + "): " + c.Message + "\n")
	}

	if len(it.Children) > 0 {
		b.WriteString("\n  segments:\n")
		child := [][]string{{"TOKENS", "PROVENANCE", "ID", "SEGMENT"}}
		for _, c := range it.Children {
			child = append(child, []string{
				formatTokenCount(c.Load.Actual),
				c.Badge().Short(),
				c.ID,
				strings.TrimSpace(c.Label + "  " + c.Detail),
			})
		}
		writeContextTable(&b, child, "    ")
	}

	b.WriteString("\n")
	b.WriteString(renderContextContent(it.Content))
	return b.String()
}

// renderContextContent prints the bytes behind an item, or states plainly why
// there are none. An absence is never rendered as an empty body.
func renderContextContent(c ctxinspect.Content) string {
	var b strings.Builder
	if c.Prov == ctxinspect.TextAbsent {
		note := strings.TrimSpace(c.Note)
		if note == "" {
			note = "the harness does not write this content anywhere agent-deck can read."
		}
		b.WriteString("--- content unavailable ---\n")
		b.WriteString(note + "\n")
		return b.String()
	}

	header := fmt.Sprintf("--- content (%d chars, %s) ---", c.Chars, c.Prov.String())
	if c.Truncated {
		header = fmt.Sprintf("--- content (showing %d of %d chars, %s, TRUNCATED) ---",
			utf8.RuneCountInString(c.Text), c.Chars, c.Prov.String())
	}
	b.WriteString(header + "\n")
	if note := strings.TrimSpace(c.Note); note != "" {
		b.WriteString("(" + note + ")\n")
	}
	b.WriteString(c.Text)
	if !strings.HasSuffix(c.Text, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("--- end ---\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Verify
// ---------------------------------------------------------------------------

// renderContextVerify renders the arithmetic behind every figure in the report:
// what was measured, what was attributed, what is left over, and what the
// adapter said it could achieve before it ran.
func renderContextVerify(v contextView) string {
	rep := v.Report
	rec := rep.Reconciliation
	var b strings.Builder

	writeContextHeader(&b, v)
	b.WriteString("\nverification — which of these numbers are measured, and what is left unexplained\n\n")

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
		{"anchor" + bound, formatTokenCount(rec.Anchor), rec.Anchor.Prov().String(), anchorHow(rep)},
		{"attributed", formatTotalTokens(attributed, attributedComplete), rec.Attributed.Prov().String(), "sum of every category's actual cost"},
		{"unaccounted" + bound, formatTokenCount(rec.Unaccounted), rec.Unaccounted.Prov().String(), "anchor − Σ(attributed); never clamped to zero"},
		{"fixed total" + bound, formatTotalTokens(fixed, fixedComplete), "derived", "attributed + unaccounted; equals the anchor by construction when it reconciles"},
	}
	if potential, any := rep.PotentialTotal(); any {
		rows = append(rows, []string{"potential", formatTokenAmount(potential), "estimated", "what deferred content would cost if fully loaded; never added to the total"})
	}
	writeContextTable(&b, rows, "  ")

	writeAnchorMeasurement(&b, rep)

	b.WriteString("\n  verdict:  " + reconVerdict(rec) + "\n")
	if rec.Status == ctxinspect.ReconOK {
		b.WriteString(fmt.Sprintf("  attributed to a named item: %.1f%% of the measured total (the rest is the harness's own prompt and tool schemas)\n", rec.Coverage))
	}
	b.WriteString("  " + estimatorFooter(rep) + "\n")

	b.WriteString("\n" + renderContextCapabilities(rep.Capabilities, rep.Harness))

	b.WriteString("\ninvariants:\n")
	if len(rep.Violations) == 0 {
		b.WriteString("  no violations: the report is internally consistent.\n")
	} else {
		for _, viol := range rep.Violations {
			b.WriteString("  VIOLATION: " + viol + "\n")
		}
	}

	writeContextCaveats(&b, v)

	b.WriteString("\nground truth: this report is derived from what the harness wrote to disk. `--verify` sends the\n")
	b.WriteString("harness's own context command to the live session (Claude Code: /context, Codex: /status) and\n")
	b.WriteString("prints a per-group diff against these figures; it types into the session, so it asks first.\n")
	b.WriteString("--json emits every number with its provenance attached.\n")
	return b.String()
}

// anchorHow explains where the anchor came from, or why there is none.
func anchorHow(rep *ctxinspect.Report) string {
	if rep.Anchor == nil {
		return "no cold-start turn was recorded for this session, so nothing measured the fixed prefix"
	}
	source := strings.TrimSpace(rep.Anchor.Source)
	if source == "" {
		source = "harness record"
	}
	out := "provider-reported, read from " + source
	if rep.Anchor.IsUpperBound() {
		// The qualifier belongs on the figure, not twenty lines below it in a
		// caveat block: this is the number every other figure reconciles
		// against, and "measured" reads as "exact" unless told otherwise.
		reason := strings.TrimSpace(rep.Anchor.UpperBoundReason)
		if reason == "" {
			reason = "it covers more than the fixed prefix"
		}
		out = "UPPER BOUND — " + out + "; " + reason
	}
	return out
}

// writeAnchorMeasurement states the one measured figure in full, with the
// arithmetic that links it to everything derived from it.
//
// The table above rounds the anchor ("27.0k" matches 26,951 and 27,013 equally
// well), which is right for a dense row and wrong for the single figure this
// report labels MEASURED. A number offered for verification against the
// provider's own accounting has to be checkable to the digit, so it gets a
// block of its own, mirrored on the pager's Verify tab.
func writeAnchorMeasurement(b *strings.Builder, rep *ctxinspect.Report) {
	b.WriteString("\n  the measured figure, in full:\n")

	if rep.Anchor == nil {
		b.WriteString("    none: nothing this session recorded measured its fixed prefix, so every total above\n")
		b.WriteString("    is an estimate, a lower bound or an unknown. The caveats below say which and why.\n")
		return
	}
	total, ok := rep.Anchor.Tokens.Value()
	if !ok {
		b.WriteString("    none: an anchor was published without a value, which is a bug in this report rather\n")
		b.WriteString("    than a fact about the session.\n")
		return
	}

	exact := ctxtext.ExactAmount(total)
	b.WriteString("    " + exact + " tokens — the provider's own count for the request that carried this session's startup injections.\n")
	b.WriteString("    read from: " + contextValueOr(rep.Anchor.Source, "the harness's own record of that request") + "\n")
	if !rep.Anchor.At.IsZero() {
		b.WriteString("    recorded at: " + rep.Anchor.At.Format("2006-01-02 15:04:05 MST") + "\n")
	}

	// The subtraction spelled out. Every reader who wants to check the residual
	// has to do exactly this sum, and doing it from the rounded column gives an
	// answer that is wrong by up to a hundred tokens in each term.
	attributed, attributedComplete := rep.AttributedTotal()
	attrText := ctxtext.ExactAmount(attributed) + " attributed"
	if !attributedComplete {
		attrText = "≥" + attrText
	}
	remainder := "— (not knowable: a contributing count is unknown)"
	if unacc, unaccOK := rep.Reconciliation.Unaccounted.Value(); unaccOK {
		remainder = ctxtext.ExactAmount(unacc) + " unattributed remainder"
		if !attributedComplete {
			remainder = "≤" + remainder
		}
	}
	b.WriteString("    arithmetic: " + exact + " measured − " + attrText + " = " + remainder + "\n")

	if rep.Anchor.IsUpperBound() {
		b.WriteString("    UPPER BOUND (≤): " + contextValueOr(rep.Anchor.UpperBoundReason, "the measurement covers more than the fixed prefix") +
			". The real fixed prefix is at most the figure above, and every total derived from it inherits the same qualifier.\n")
	}
}

// contextValueOr returns the trimmed value, or the fallback when it is empty.
func contextValueOr(value, fallback string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}
	return fallback
}

// renderContextCapabilities renders what an adapter declares it can achieve.
// It is answerable without a successful inspection, which is what makes it the
// honest screen for a harness agent-deck cannot measure.
func renderContextCapabilities(caps ctxinspect.Capabilities, tool string) string {
	var b strings.Builder
	adapter := strings.TrimSpace(caps.Adapter)
	if adapter == "" {
		adapter = "unknown"
	}
	b.WriteString(fmt.Sprintf("what the %q adapter can report for %q:\n", adapter, tool))
	b.WriteString("  measured fixed-prefix anchor: " + yesNo(caps.CanAnchor) + "\n")
	b.WriteString("  verbatim base system prompt:  " + yesNo(caps.CanVerbatimSystem) + "\n")

	if len(caps.Categories) > 0 {
		rows := [][]string{{"CATEGORY", "TEXT", "TOKENS", "MECHANISM"}}
		for _, cc := range caps.Categories {
			rows = append(rows, []string{cc.Name, cc.Text.String(), cc.Token.String(), cc.Note})
		}
		writeContextTable(&b, rows, "  ")
	}
	for _, note := range caps.Notes {
		b.WriteString("  note: " + note + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Shared pieces
// ---------------------------------------------------------------------------

// writeContextHeader writes the identity block every view starts with.
func writeContextHeader(b *strings.Builder, v contextView) {
	rep := v.Report
	name := firstNonEmpty(v.Title, v.Ref, rep.SessionID, "(unnamed session)")
	b.WriteString(fmt.Sprintf("context · %s · tool %s · adapter %s\n", name, v.Tool, rep.Adapter))

	model := strings.TrimSpace(rep.Model)
	if model == "" {
		model = "(tool default, not recorded)"
	}
	b.WriteString("  model:   " + model + "\n")
	b.WriteString("  window:  " + windowLine(rep.Window) + "\n")
	b.WriteString("  basis:   " + rep.Basis.String() + " — " + rep.Basis.Describe() + "\n")
	if rep.SessionID != "" {
		b.WriteString("  session: " + rep.SessionID + "\n")
	}
	if rep.ProjectPath != "" {
		b.WriteString("  path:    " + rep.ProjectPath + "\n")
	}
	if v.Profile != "" {
		b.WriteString("  profile: " + v.Profile + "\n")
	}
	if reason, unsupported := contextTokenAccountingUnsupported(rep); unsupported {
		b.WriteString("\n  token accounting unsupported for " + v.Tool + ": " + reason + "\n")
		b.WriteString("  what follows is an inventory of what is configured, not a measurement of what is loaded.\n")
	}
}

// writeContextCaveats writes the resolution warnings and the report's own
// caveats as a footer.
//
// Everything that qualifies a figure is printed here, always. It is the right
// behaviour for the tab a reader opens to audit the arithmetic, and the wrong
// one for the screen they open first — see [writeContextCaveatsRanked], which
// is what the overview calls.
func writeContextCaveats(b *strings.Builder, v contextView) {
	writeContextCaveatsRanked(b, v, false)
}

// writeContextCaveatsRanked writes the caveat footer, optionally holding back
// the merely-informative ones behind a count.
//
// A fresh session produced six data rows and, beneath them, twelve note lines
// and a caveat block: the disclaimer outweighed the report three to one, which
// does not read as care, it reads as an unfinished screen nobody trusts. The
// notes went behind --verbose. This does the same for the caveats that say
// "worth knowing, nothing is wrong" — and only those.
//
// A warn means a number is less trustworthy than it looks and a bug means the
// report contradicts itself. Neither is ever collapsed, on any screen, at any
// verbosity. Hiding one to tidy a screen would be the exact trade this feature
// exists to refuse.
func writeContextCaveatsRanked(b *strings.Builder, v contextView, demoteInfo bool) {
	demoteInfo = demoteInfo && !v.Verbose

	shown := make([]ctxinspect.Caveat, 0, len(v.Report.Caveats))
	held := 0
	for _, c := range v.Report.Caveats {
		if demoteInfo && c.Severity == ctxinspect.SeverityInfo {
			held++
			continue
		}
		shown = append(shown, c)
	}

	if len(v.Warnings) == 0 && len(shown) == 0 && held == 0 {
		return
	}
	b.WriteString("\ncaveats:\n")
	for _, w := range v.Warnings {
		b.WriteString("  resolution: " + w + "\n")
	}
	for _, c := range shown {
		scope := ""
		if c.Category != "" {
			scope = " [" + c.Category + "]"
		}
		b.WriteString("  " + c.Severity.String() + scope + " (" + c.Code + "): " + c.Message + "\n")
	}
	if held > 0 {
		b.WriteString(fmt.Sprintf("  %d further caveat%s worth knowing, none of them saying a figure is wrong: pass --verbose to read them.\n",
			held, plural(held)))
	}
}

// contextTokenAccountingUnsupported reports whether the report can carry any
// token figure at all, using what the adapter declared rather than what this
// run happened to produce — an empty report and an unmeasurable harness are
// different things and must not print the same line.
func contextTokenAccountingUnsupported(rep *ctxinspect.Report) (string, bool) {
	caps := rep.Capabilities
	if caps.CanAnchor {
		return "", false
	}
	for _, cc := range caps.Categories {
		if cc.Token != ctxinspect.TokenUnknown {
			return "", false
		}
	}
	if len(caps.Categories) == 0 {
		return "the adapter declared no reportable categories", true
	}
	return "this harness exposes no token accounting agent-deck can read, and agent-deck will not guess one", true
}

// reconVerdict renders the reconciliation status and its explanation.
func reconVerdict(rec ctxinspect.Reconciliation) string {
	label := strings.ToUpper(rec.Status.String())
	if rec.Status == ctxinspect.ReconFailed {
		label = "FAILED"
	}
	return label + " — " + rec.Message
}

// estimatorFooter renders the estimator's self-reported error bound, or says
// plainly that there is none.
func estimatorFooter(rep *ctxinspect.Report) string {
	if rep.Calibration != nil {
		return rep.Calibration.Summary()
	}
	method := rep.EstimatorMethod()
	if method == "" {
		return "this report contains no estimated figures"
	}
	// "No measured counterpart on this session" is not the same as "unbounded".
	// The estimator's divisors are calibrated against a recorded /context
	// capture, so each figure carries a band even when this session offers
	// nothing to check it against — and saying so is the difference between an
	// honest approximation and a bare guess.
	if band, ok := rep.EstimatorBandPct(); ok {
		return fmt.Sprintf("per-item figures marked ~ are estimates (method: %s), each within ±%s%% of its calibration; this session had no measured counterpart to re-check them against",
			method, trimPct(band))
	}
	return fmt.Sprintf("per-item figures marked ~ are estimates (method: %s) with no measured counterpart to bound their error on this session", method)
}

// trimPct renders a percentage with at most one decimal and no trailing ".0".
func trimPct(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}

// windowLine renders the context-window size together with how it was
// established. The source is never omitted: a denominator with no provenance
// turns an honest numerator into a misleading percentage.
//
// Both surfaces render it from [ctxtext.WindowLine]. They used to each write
// their own, and produced different answers to the same question — a sentence
// here, the bare word "unknown" in the pager behind the C key.
func windowLine(w ctxinspect.WindowInfo) string { return ctxtext.WindowLine(w) }

// loadLine renders an item's current and potential cost together.
func loadLine(l ctxinspect.Load) string {
	out := l.State.String() + " — actual " + formatTokenCount(l.Actual)
	if l.Potential != nil {
		out += ", potential " + formatTokenCount(*l.Potential) + " if fully loaded (never added to any total)"
	}
	return out
}

// textProvLine explains the text axis in a sentence.
func textProvLine(c ctxinspect.Content) string {
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

// tokenProvLine explains the token axis in a sentence, naming the method for an
// estimate and the reason for an unknown.
func tokenProvLine(t ctxinspect.TokenCount) string {
	out := t.Prov().String()
	if reason := strings.TrimSpace(t.Reason()); reason != "" {
		return out + " — " + reason
	}
	if method := strings.TrimSpace(t.Method()); method != "" {
		return out + " — " + method
	}
	return out
}

// leverLine renders what the user can do about an item.
func leverLine(l ctxinspect.Lever) string {
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

// potentialCell renders a potential cost, or an em dash when there is none.
func potentialCell(tokens int, ok bool) string {
	if !ok {
		return "—"
	}
	return "~" + formatTokenAmount(tokens)
}

// formatTokenCount renders a count with the marker its provenance earns:
// nothing for a measured or computed figure, "~" for anything derived, and an
// em dash for an unknown, which must never read as zero.
func formatTokenCount(t ctxinspect.TokenCount) string {
	v, ok := t.Value()
	if !ok {
		return "—"
	}
	if v < 0 {
		// A negative figure is an arithmetic contradiction, not an estimate of
		// anything. Marking it "~" would suggest it is approximately right.
		return formatTokenAmount(v)
	}
	switch t.Prov() {
	case ctxinspect.TokenEstimated, ctxinspect.TokenResidual:
		return "~" + formatTokenAmount(v)
	default:
		return formatTokenAmount(v)
	}
}

// formatTotalTokens renders a total, prefixing "≥" when a contributing count
// was unknown so a lower bound cannot be misread as a total.
//
// A lower bound of zero is not a lower bound: it means nothing in the total is
// known, and "≥0" would dress that up as a figure. It renders as an em dash
// like any other unknown.
func formatTotalTokens(tokens int, complete bool) string {
	if complete {
		return formatTokenAmount(tokens)
	}
	if tokens == 0 {
		return "—"
	}
	return "≥" + formatTokenAmount(tokens)
}

// formatTokenAmount renders a token figure compactly. Negative values keep
// their sign: a negative residual is a reported bug, not something to hide.
// Shared with the pager so "1.0M" cannot come to mean two things.
func formatTokenAmount(n int) string { return ctxtext.TokenAmount(n) }

// writeContextTable writes left-aligned columns padded to the widest cell. The
// final column is never padded, so a long path or sentence cannot introduce
// trailing whitespace into golden output.
func writeContextTable(b *strings.Builder, rows [][]string, indent string) {
	if len(rows) == 0 {
		return
	}
	widths := make([]int, 0, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			w := utf8.RuneCountInString(cell)
			if i >= len(widths) {
				widths = append(widths, w)
				continue
			}
			if w > widths[i] {
				widths[i] = w
			}
		}
	}
	for _, row := range rows {
		b.WriteString(indent)
		for i, cell := range row {
			if i == len(row)-1 {
				b.WriteString(cell)
				break
			}
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+2))
		}
		b.WriteString("\n")
	}
}

// yesNo renders a capability flag.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// plural returns the plural suffix for a count.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
