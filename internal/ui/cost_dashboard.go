package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

type costDashboard struct {
	store     *costs.Store
	width     int
	height    int
	today     costs.CostSummary
	week      costs.CostSummary
	month     costs.CostSummary
	top       []costs.SessionCost
	byModel   map[string]int64
	projected int64
}

func newCostDashboard(store *costs.Store, width, height int) costDashboard {
	d := costDashboard{store: store, width: width, height: height}
	d.refresh()
	return d
}

func (d *costDashboard) refresh() {
	if d.store == nil {
		return
	}
	d.today, _ = d.store.TotalToday()
	d.week, _ = d.store.TotalThisWeek()
	d.month, _ = d.store.TotalThisMonth()
	d.top, _ = d.store.TopSessionsByCost(5)
	d.byModel, _ = d.store.CostByModel()
	d.projected, _ = d.store.ProjectedMonthly()
}

func (d costDashboard) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	b.WriteString(titleStyle.Render(" Cost Dashboard"))
	b.WriteString("\n\n")

	// Summary cards
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	valueStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)

	b.WriteString(fmt.Sprintf("  %s %s    %s %s    %s %s    %s %s\n\n",
		labelStyle.Render("Today:"), valueStyle.Render(costs.FormatUSD(d.today.TotalCostMicrodollars)),
		labelStyle.Render("Week:"), valueStyle.Render(costs.FormatUSD(d.week.TotalCostMicrodollars)),
		labelStyle.Render("Month:"), valueStyle.Render(costs.FormatUSD(d.month.TotalCostMicrodollars)),
		labelStyle.Render("Projected:"), valueStyle.Render(costs.FormatUSD(d.projected)+"/mo"),
	))

	// Token totals
	tokenStyle := lipgloss.NewStyle().Foreground(ColorComment)
	b.WriteString(fmt.Sprintf("  %s  Input: %s  Output: %s  Cache R: %s  Cache W: %s\n\n",
		labelStyle.Render("Today tokens:"),
		tokenStyle.Render(formatTokens(d.today.TotalInputTokens)),
		tokenStyle.Render(formatTokens(d.today.TotalOutputTokens)),
		tokenStyle.Render(formatTokens(d.today.TotalCacheReadTokens)),
		tokenStyle.Render(formatTokens(d.today.TotalCacheWriteTokens)),
	))

	// Top sessions
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText).Underline(true)
	b.WriteString("  " + sectionStyle.Render("Top Sessions") + "\n")
	if len(d.top) == 0 {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorComment).Render("(no cost data yet)") + "\n")
	}
	for i, sc := range d.top {
		title := sc.SessionTitle
		if title == "" {
			title = sc.SessionID
		}
		if len(title) > 35 {
			title = title[:32] + "..."
		}
		b.WriteString(fmt.Sprintf("  %d. %-35s %s  (%d events)\n",
			i+1, title,
			valueStyle.Render(costs.FormatUSD(sc.CostMicrodollars)),
			sc.EventCount))
	}
	b.WriteString("\n")

	// Model breakdown
	b.WriteString("  " + sectionStyle.Render("Cost by Model") + "\n")
	if len(d.byModel) == 0 {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorComment).Render("(no cost data yet)") + "\n")
	}
	for model, cost := range d.byModel {
		b.WriteString(fmt.Sprintf("  %-35s %s\n", model, valueStyle.Render(costs.FormatUSD(cost))))
	}
	b.WriteString("\n")

	// Help
	helpStyle := lipgloss.NewStyle().Foreground(ColorComment)
	b.WriteString("  " + helpStyle.Render("Press q or $ to return"))

	return b.String()
}

func formatTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
