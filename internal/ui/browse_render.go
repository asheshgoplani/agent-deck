package ui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
	"github.com/asheshgoplani/agent-deck/internal/history/tree"
)

func browseCaretGlyph(expanded bool) string {
	if expanded {
		return "▾"
	}
	return "▸"
}

// browseClip truncates a plain (unstyled) string to display width w, adding
// an ellipsis. Safe only on strings without ANSI escapes.
func browseClip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// browseRelTime formats how long ago t was, compactly (now / 3m / 2h / 5d / 3w).
func browseRelTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	default:
		return strconv.Itoa(int(d.Hours()/(24*7))) + "w"
	}
}

// browsePadRight pads a (possibly styled) string with spaces to display
// width w.
func browsePadRight(s string, w int) string {
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// browseRowLabel renders one tree row as exactly `width` display columns: a
// 1-col gutter (accent bar when selected, else blank) followed by `width-1`
// cols of content (a full-width highlight bar when selected). expanded and
// now are passed explicitly instead of being read off a stateful model.
func browseRowLabel(r tree.Row, width int, selected bool, expanded map[string]bool, now time.Time) string {
	gutter := " "
	if selected {
		gutter = brGutterStyle.Render("▏")
	}
	avail := width - 1
	if avail < 1 {
		avail = 1
	}
	indent := strings.Repeat("  ", r.Depth)

	if r.Kind == tree.SessionRow {
		st := r.Session.Status
		// Layout across `avail` cols: indent + dot(1) + space(1) + title …gap…
		// + time (flush right), so times form a clean right-hand column.
		age := browseRelTime(r.Session.ModTime, now)
		prefixW := lipgloss.Width(indent) + 2 // glyph(1) + space(1)
		titleBudget := avail - prefixW - lipgloss.Width(age) - 1
		if titleBudget < 0 {
			titleBudget = 0
		}
		title := browseClip(r.Session.Label(), titleBudget)
		left := indent + st.Glyph() + " " + title
		gap := avail - lipgloss.Width(left) - lipgloss.Width(age)
		if gap < 1 {
			gap = 1
		}
		pad := strings.Repeat(" ", gap)
		if selected {
			// One string so the bar fills cleanly and the dot stays aligned with
			// non-selected rows (dot is plain on the bar; the bar marks selection).
			return gutter + brSelectedBar.Render(browsePadRight(left+pad+age, avail))
		}
		ts := brTitleStyle
		if st == model.StatusClosed {
			ts = brClosedTitle
		}
		line := indent + brStatusStyles[st].Render(st.Glyph()) + " " + ts.Render(title) + pad + brTimeStyle.Render(age)
		return gutter + browsePadRight(line, avail)
	}

	var plain, styled string
	switch r.Kind {
	case tree.FolderRow:
		caret := browseCaretGlyph(expanded[r.Path])
		name := browseClip(r.Node.Name+"/", avail-2-lipgloss.Width(indent))
		plain = indent + caret + " " + name
		styled = indent + brFolderStyle.Render(caret+" "+name)
	case tree.ProjectRow:
		caret := browseCaretGlyph(expanded[r.Path])
		cnt := "  (" + strconv.Itoa(len(r.Node.Project.Sessions)) + ")"
		name := browseClip(r.Node.Name, avail-2-lipgloss.Width(indent)-lipgloss.Width(cnt))
		plain = indent + caret + " " + name + cnt
		styled = indent + brProjectStyle.Render(caret+" "+name) + brCountStyle.Render(cnt)
	case tree.LoadMoreRow:
		txt := browseClip("▾ load more ("+strconv.Itoa(r.Remaining)+" more)", avail-lipgloss.Width(indent))
		plain = indent + txt
		styled = indent + brLoadMoreStyle.Render(txt)
	default:
		return gutter + strings.Repeat(" ", avail)
	}
	if selected {
		return gutter + brSelectedBar.Render(browsePadRight(plain, avail))
	}
	return gutter + browsePadRight(styled, avail)
}
