package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// previewLayout is the geometry a remote preview field renders into: the
// columns one body line may use and the rows the whole body may use. Every
// field fits itself into it — a long single line wraps, a list caps itself
// with "+N more" — so no field can ever widen the block or push another
// field off the pane (the rc.6 defect, where one 7-slot accounts line shifted
// and cut the entire stats block).
type previewLayout struct {
	width int
	rows  int
}

// previewListIndent is the left indent of a list row under its summary line.
const previewListIndent = "  "

// previewMoreLine is the cap line a list ends with when it has more rows
// than the pane can hold.
func previewMoreLine(hidden int) string {
	return previewListIndent + fmt.Sprintf("+%d more", hidden)
}

// previewClauseSep joins the clauses of a one-line field ("a · b · c").
const previewClauseSep = " · "

// wrapPreviewLine wraps one body line to width, continuation lines indented
// by previewListIndent. Clauses (previewClauseSep-separated) are the unit:
// a break lands between clauses when it can, and the continuation line
// starts with "· " so it reads as the rest of the same field. Only a clause
// wider than the line breaks at spaces, and only a word wider than the line
// is cut mid-word — a hyphenated slot name never splits at its hyphen (which
// ansi.Wrap would do). A width of 0 or less leaves the line alone.
func wrapPreviewLine(line string, width int) []string {
	if width <= 0 || lipgloss.Width(line) <= width {
		return []string{line}
	}
	var out []string
	cur := ""
	limit := func() int {
		if len(out) == 0 {
			return width
		}
		return max(1, width-len(previewListIndent))
	}
	flush := func() {
		if len(out) > 0 {
			cur = previewListIndent + cur
		}
		out = append(out, cur)
		cur = ""
	}
	// add appends a piece to the current line under sep, starting a new line
	// (prefixed with lead) when it does not fit.
	add := func(piece, sep, lead string) {
		if cur == "" {
			cur = piece
			return
		}
		if lipgloss.Width(cur+sep+piece) <= limit() {
			cur += sep + piece
			return
		}
		flush()
		cur = lead + piece
	}
	for _, clause := range strings.Split(line, previewClauseSep) {
		if lipgloss.Width(clause) <= limit() {
			add(clause, previewClauseSep, "· ")
			continue
		}
		for _, word := range strings.Fields(clause) {
			for lipgloss.Width(word) > limit() {
				if cur != "" {
					flush()
				}
				cur = ansi.Truncate(word, limit(), "")
				word = strings.TrimPrefix(word, cur)
				flush()
			}
			add(word, " ", "")
		}
	}
	if cur != "" {
		flush()
	}
	return out
}

// truncatePreviewCell shortens s to width columns with a trailing "…" when
// it does not fit; a width below 1 returns s unchanged.
func truncatePreviewCell(s string, width int) string {
	if width < 1 || lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// capPreviewList trims rows to fit rows lines under a one-line summary,
// replacing the tail with "+N more". With room for the summary only, the
// rows are dropped outright: "+N more" alone would say less than the summary
// already does.
func capPreviewList(summary string, rows []string, layout previewLayout) []string {
	budget := layout.rows - 1
	if layout.rows <= 0 || budget >= len(rows) {
		return append([]string{summary}, rows...)
	}
	if budget < 2 {
		return []string{summary}
	}
	shown := rows[:budget-1]
	return append(append([]string{summary}, shown...), previewMoreLine(len(rows)-len(shown)))
}

// accountUsageLoad is the sort key for the accounts table: the most-loaded
// slot first, by the binding 5h window then 7d; slots with no known reading
// sort last, by name, so the operator's eye lands on the slot closest to its
// limit.
func accountUsageLoad(u session.AccountUsage) (float64, float64, bool) {
	if !u.Known || (!u.FiveHour.Known && !u.SevenDay.Known) {
		return 0, 0, false
	}
	return u.FiveHour.Percent, u.SevenDay.Percent, true
}

// sortAccountUsage returns usage ordered for the table (accountUsageLoad).
func sortAccountUsage(usage []session.AccountUsage) []session.AccountUsage {
	sorted := append([]session.AccountUsage(nil), usage...)
	sort.SliceStable(sorted, func(i, j int) bool {
		fi, si, ki := accountUsageLoad(sorted[i])
		fj, sj, kj := accountUsageLoad(sorted[j])
		switch {
		case ki != kj:
			return ki
		case !ki:
			return sorted[i].Name < sorted[j].Name
		case fi != fj:
			return fi > fj
		case si != sj:
			return si > sj
		}
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}

// accountUsageUnknownLabel is the clause for a slot with no usable reading,
// naming the reason the remote reported; "usage unknown" only when the
// remote did not say (older than AccountUsage.UnknownReason).
func accountUsageUnknownLabel(u session.AccountUsage) string {
	switch u.UnknownReason {
	case session.AccountUsageNoFeed:
		return "no feed"
	case session.AccountUsageNoData:
		return "no data yet"
	case session.AccountUsageUnreadable:
		return "unreadable"
	}
	return "usage unknown"
}

// accountUsageAgeClause is the last column of an account row: the reading's
// age, prefixed "stale, " past session.AccountUsageStaleAfter.
func accountUsageAgeClause(u session.AccountUsage, now time.Time) string {
	age := "unknown"
	if u.HasUpdatedAt {
		age = accountUsageAgeLabel(now.Sub(u.UpdatedAt))
	}
	if session.AccountUsageStale(u.HasUpdatedAt, u.UpdatedAt, now) {
		return "stale, " + age
	}
	return age
}

// renderAccountsSummaryLine is the one-line head of the accounts table:
// "accounts  7 slots · 5h lowest 8% · 1 stale · 2 unknown". The lowest 5h
// reading is the number an operator picks a slot by; stale and unknown
// counts say how much of the fleet that number does not cover.
func renderAccountsSummaryLine(usage []session.AccountUsage, now time.Time) string {
	lowest, haveLowest := 0.0, false
	stale, unknown := 0, 0
	for _, u := range usage {
		if _, _, known := accountUsageLoad(u); !known {
			unknown++
			continue
		}
		if session.AccountUsageStale(u.HasUpdatedAt, u.UpdatedAt, now) {
			stale++
		}
		if u.FiveHour.Known && (!haveLowest || u.FiveHour.Percent < lowest) {
			lowest, haveLowest = u.FiveHour.Percent, true
		}
	}
	slotWord := "slots"
	if len(usage) == 1 {
		slotWord = "slot"
	}
	parts := []string{fmt.Sprintf("%d %s", len(usage), slotWord)}
	switch {
	case haveLowest && len(usage) == 1:
		parts = append(parts, fmt.Sprintf("5h %.0f%%", lowest))
	case haveLowest:
		parts = append(parts, fmt.Sprintf("5h lowest %.0f%%", lowest))
	}
	if stale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", stale))
	}
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown", unknown))
	}
	return "accounts  " + strings.Join(parts, " · ")
}

// accountRowCells is one table row before column alignment.
type accountRowCells struct {
	name, fiveHour, sevenDay, tail string
}

func accountRow(u session.AccountUsage, now time.Time) accountRowCells {
	row := accountRowCells{name: u.Name, fiveHour: "—", sevenDay: "—"}
	if _, _, known := accountUsageLoad(u); !known {
		row.tail = accountUsageUnknownLabel(u)
		return row
	}
	if u.FiveHour.Known {
		row.fiveHour = fmt.Sprintf("5h %.0f%%", u.FiveHour.Percent)
	}
	if u.SevenDay.Known {
		row.sevenDay = fmt.Sprintf("7d %.0f%%", u.SevenDay.Percent)
	}
	row.tail = accountUsageAgeClause(u, now)
	return row
}

// minAccountNameWidth is the narrowest the name column shrinks to before a
// row is cut at the pane edge instead.
const minAccountNameWidth = 6

// renderAccountsPreviewBlock renders the "accounts" field as a summary line
// plus one aligned row per slot (name, 5h, 7d, age or reason), most-loaded
// first, capped to layout.rows. Columns shrink in a narrow pane by
// truncating the name column (never below minAccountNameWidth); a row that
// still does not fit is cut at the pane edge. No line is ever wider than
// layout.width.
func renderAccountsPreviewBlock(usage []session.AccountUsage, now time.Time, layout previewLayout) []string {
	if len(usage) == 0 {
		return []string{"accounts  none"}
	}
	summary := wrapPreviewLine(renderAccountsSummaryLine(usage, now), layout.width)
	cells := make([]accountRowCells, 0, len(usage))
	nameW, fiveW, sevenW, tailW := 0, 0, 0, 0
	for _, u := range sortAccountUsage(usage) {
		row := accountRow(u, now)
		cells = append(cells, row)
		nameW = max(nameW, lipgloss.Width(row.name))
		fiveW = max(fiveW, lipgloss.Width(row.fiveHour))
		sevenW = max(sevenW, lipgloss.Width(row.sevenDay))
		tailW = max(tailW, lipgloss.Width(row.tail))
	}
	const gap = 2
	if layout.width > 0 {
		fixed := len(previewListIndent) + gap + fiveW + gap + sevenW + gap + tailW
		nameW = max(minAccountNameWidth, min(nameW, layout.width-fixed))
	}
	rows := make([]string, 0, len(cells))
	for _, c := range cells {
		line := fmt.Sprintf("%s%-*s  %-*s  %-*s  %s", previewListIndent,
			nameW, truncatePreviewCell(c.name, nameW), fiveW, c.fiveHour, sevenW, c.sevenDay, c.tail)
		rows = append(rows, strings.TrimRight(truncatePreviewCell(line, layout.width), " "))
	}
	// The summary may itself have wrapped; the rows share what is left
	// under its last line.
	head, last := summary[:len(summary)-1], summary[len(summary)-1]
	rowLayout := previewLayout{width: layout.width, rows: layout.rows - len(head)}
	return append(head, capPreviewList(last, rows, rowLayout)...)
}

// sshSinceLabel renders when a login started: the clock time when it was
// today, the date and time otherwise, "" when the host did not say.
func sshSinceLabel(s session.RemoteSSHSession, now time.Time) string {
	if !s.HasSince {
		return ""
	}
	since := s.Since.In(now.Location())
	if y, m, d := since.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return "since " + since.Format("15:04")
	}
	return "since " + since.Format("Jan 2 15:04")
}

// sortSSHSessions orders users by session count (most first), then by the
// oldest login, then by name.
func sortSSHSessions(sessions []session.RemoteSSHSession) []session.RemoteSSHSession {
	sorted := append([]session.RemoteSSHSession(nil), sessions...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch {
		case a.Count != b.Count:
			return a.Count > b.Count
		case a.HasSince != b.HasSince:
			return a.HasSince
		case a.HasSince && !a.Since.Equal(b.Since):
			return a.Since.Before(b.Since)
		}
		return a.User < b.User
	})
	return sorted
}

// renderSSHSummaryLine is "ssh  3 users · 6 sessions".
func renderSSHSummaryLine(sessions []session.RemoteSSHSession) string {
	total := 0
	for _, s := range sessions {
		total += s.Count
	}
	userWord, sessionWord := "users", "sessions"
	if len(sessions) == 1 {
		userWord = "user"
	}
	if total == 1 {
		sessionWord = "session"
	}
	return fmt.Sprintf("ssh  %d %s · %d %s", len(sessions), userWord, total, sessionWord)
}

// renderSSHPreviewBlock renders the "ssh" field: who is connected to the
// host over SSH. One line ("ssh  yasir ×2 since 09:10 · ashesh ×3 since
// 08:54") while it fits layout.width; past that, a summary line plus one
// aligned row per user (name, ×count, since, from), most sessions first,
// capped to layout.rows with "+N more". Nobody connected says so.
func renderSSHPreviewBlock(sessions []session.RemoteSSHSession, now time.Time, layout previewLayout) []string {
	if len(sessions) == 0 {
		return []string{"ssh  nobody connected"}
	}
	sorted := sortSSHSessions(sessions)
	clauses := make([]string, 0, len(sorted))
	for _, s := range sorted {
		clause := fmt.Sprintf("%s ×%d", s.User, s.Count)
		if since := sshSinceLabel(s, now); since != "" {
			clause += " " + since
		}
		clauses = append(clauses, clause)
	}
	line := "ssh  " + strings.Join(clauses, previewClauseSep)
	if layout.width <= 0 || lipgloss.Width(line) <= layout.width {
		return []string{line}
	}
	nameW, countW, sinceW := 0, 0, 0
	type cells struct{ name, count, since, from string }
	rows := make([]cells, 0, len(sorted))
	for _, s := range sorted {
		c := cells{name: s.User, count: fmt.Sprintf("×%d", s.Count), since: sshSinceLabel(s, now)}
		if s.From != "" {
			c.from = "from " + s.From
		}
		rows = append(rows, c)
		nameW = max(nameW, lipgloss.Width(c.name))
		countW = max(countW, lipgloss.Width(c.count))
		sinceW = max(sinceW, lipgloss.Width(c.since))
	}
	lines := make([]string, 0, len(rows))
	for _, c := range rows {
		row := fmt.Sprintf("%s%-*s  %-*s  %-*s  %s", previewListIndent, nameW, c.name, countW, c.count, sinceW, c.since, c.from)
		lines = append(lines, strings.TrimRight(truncatePreviewCell(row, layout.width), " "))
	}
	return capPreviewList(renderSSHSummaryLine(sessions), lines, layout)
}

// remoteSSHUnknownLine is the "ssh" field when the remote did not report
// sessions: it names the reason it can (the remote's own error, or that the
// remote is older than the release that sends the field) and never guesses.
func remoteSSHUnknownLine(result remoteHostStatsResult, hasResult bool, state session.RemoteVersionState, controller string) string {
	switch {
	case !hasResult || !result.Stats.Ok:
		return "ssh  unknown (stats unknown)"
	case result.Stats.SSHError != "":
		return "ssh  unknown (" + result.Stats.SSHError + ")"
	case state.Compare(controller) == session.RemoteVersionOlder:
		return "ssh  unknown (remote older than " + sshFieldSinceVersion + ")"
	}
	return "ssh  unknown (remote does not report ssh sessions)"
}

// sshFieldSinceVersion is the first release whose `system stats` sends
// ssh_sessions.
const sshFieldSinceVersion = "1.16.11"
