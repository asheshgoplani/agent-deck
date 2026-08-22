package ui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/agents"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// agentsRefreshInterval bounds how often the TUI re-reads the agents registry
// and probes connector freshness. The render loop runs far more often than
// that, and health probing touches the filesystem.
const agentsRefreshInterval = 10 * time.Second

// refreshAgentsPanel rebuilds the Agents view from the registry, the live
// session fleet, and the existing ledgers.
//
// Safe to call when the panel is hidden — the session list needs the same data
// for its ⚙ marker and preview card. It is read-only throughout.
func (h *Home) refreshAgentsPanel() {
	if h == nil {
		return
	}
	now := time.Now()
	if !h.agentsLastRefresh.IsZero() && now.Sub(h.agentsLastRefresh) < agentsRefreshInterval && h.agentsLoaded {
		return
	}
	h.agentsLastRefresh = now

	defs, err := agents.LoadAll()
	if err != nil {
		// A registry that cannot be read is reported, not swallowed: the
		// alternative is a deck that silently claims the user has no agents.
		h.agentsLoadError = err.Error()
		h.agentsLoaded = true
		h.agentsView = agents.View{}
		h.agentBySession = nil
		h.agentsPanel.SetLoadError(err.Error(), now)
		// A registry we could not read is not a registry with agents in it.
		// Leaving the previous value would keep advertising a surface whose
		// data just failed to load.
		h.helpOverlay.SetHasAgents(false)
		return
	}
	h.agentsLoadError = ""
	h.agentsLoaded = true

	if len(defs) == 0 {
		h.agentsView = agents.View{}
		h.agentBySession = nil
		h.agentsPanel.SetView(h.agentsView, now)
		h.helpOverlay.SetHasAgents(false)
		return
	}

	states := make(map[string]agents.SessionState, len(h.instances))
	for _, inst := range h.instances {
		if inst == nil {
			continue
		}
		states[inst.ID] = agents.SessionState{Status: string(inst.Status), Present: true}
	}

	view := agents.BuildView(agents.BuildOptions{
		Definitions:   defs,
		SessionStates: states,
		Ledger:        agentLedgerLookup,
		LocalMachine:  agentsLocalMachineName(),
		Now:           now,
	})

	h.agentsView = view
	h.agentsPanel.SetView(view, now)

	// The RULES section shows the role's policy file names, which is what the
	// role manifest actually asserts. A session-adopted post has no role
	// package and therefore no rules, which the screen shows as absent rather
	// than inventing.
	rules := map[string][]string{}
	for _, def := range defs {
		if def.Role == nil {
			continue
		}
		if len(def.Role.Spec.Policy) > 0 {
			rules[def.Name] = append([]string(nil), def.Role.Spec.Policy...)
		}
	}
	h.agentsPanel.SetRules(rules)
	h.helpOverlay.SetHasAgents(h.agentsPanel.HasAgents())

	// Index by session so the session list and preview pane can answer
	// "does this row belong to an agent?" without rescanning the registry.
	index := map[string]agents.AgentRow{}
	for _, machine := range view.Machines {
		for _, row := range machine.Agents {
			if row.SessionID != "" {
				index[row.SessionID] = row
			}
		}
	}
	h.agentBySession = index
}

// agentRowForSession returns the agent that owns a session, if any.
func (h *Home) agentRowForSession(sessionID string) (agents.AgentRow, bool) {
	if len(h.agentBySession) == 0 || sessionID == "" {
		return agents.AgentRow{}, false
	}
	row, ok := h.agentBySession[sessionID]
	return row, ok
}

// agentsLocalMachineName labels the local machine in the grouped view.
func agentsLocalMachineName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "local"
	}
	if short, _, found := strings.Cut(host, "."); found {
		return short
	}
	return host
}

// agentLedgerLookup reads recent work from the completion and talkback
// ledgers. Both already exist; nothing new is written.
func agentLedgerLookup(sessionID string) []agents.LedgerEntry {
	var entries []agents.LedgerEntry

	if entry, ok := session.ReadLedgerEntry(sessionID); ok {
		summary := entry.Summary
		if summary == "" {
			summary = "reported " + entry.Status
		}
		entries = append(entries, agents.LedgerEntry{
			At: entry.FinishedAt, Summary: summary, Status: entry.Status,
		})
	}

	if events, err := session.ReadInboxEventsForDisplay(sessionID); err == nil {
		for _, event := range events {
			title := event.ChildTitle
			if title == "" {
				title = event.ChildSessionID
			}
			entries = append(entries, agents.LedgerEntry{
				At:      event.Timestamp,
				Summary: fmt.Sprintf("%s → %s", title, event.ToStatus),
				Status:  event.ToStatus,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })
	return entries
}

// renderAgentCard renders the agent card shown in the preview pane for a
// session an adopted agent owns: role and version, its triggers with when each
// is next due, connector health, and the last few ledger entries.
//
// Everything here is read-only and evidence-backed. Trigger rows say who
// actually fires them, because in phase 1 that is never agent-deck.
func (h *Home) renderAgentCard(row agents.AgentRow, width int) string {
	var b strings.Builder

	contentWidth := width - 4
	if contentWidth < 20 {
		contentWidth = 20
	}

	b.WriteString(renderSectionDivider("Agent", contentWidth))
	b.WriteString("\n")

	labelStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)
	roleStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)

	// Amendment 01 asks the card for role AND version. A session-adopted post
	// has no role package, so there is genuinely no version to show; say that
	// rather than rendering a bare role name that looks like the version was
	// simply forgotten.
	roleText := agents.SanitizeForDisplay(row.Role)
	if row.RoleVersion != "" {
		roleText += " " + agents.SanitizeForDisplay(row.RoleVersion)
	} else {
		roleText += " (no role package)"
	}
	b.WriteString(labelStyle.Render("Role:      "))
	b.WriteString(roleStyle.Render(roleText))
	b.WriteString("\n")

	if row.ReportsTo != "" {
		b.WriteString(labelStyle.Render("Reports:   "))
		b.WriteString(valueStyle.Render(agents.SanitizeForDisplay(row.ReportsTo)))
		b.WriteString("\n")
	}

	// Triggers, each with its next due time and its real owner.
	if len(row.Triggers) > 0 {
		b.WriteString(labelStyle.Render("Triggers:  "))
		b.WriteString("\n")
		for _, t := range row.Triggers {
			owner := "external"
			if !t.External {
				owner = "ARMED HERE"
			}
			line := fmt.Sprintf("  %s %s", truncateStr(agents.SanitizeForDisplay(t.Name), 18), t.NextDueText)
			b.WriteString(valueStyle.Render(truncateStr(line, contentWidth-12)))
			b.WriteString(" ")
			b.WriteString(labelStyle.Render("[" + owner + "]"))
			b.WriteString("\n")
		}
	}

	// Connector health, stated from the evidence that produced it.
	if len(row.Connectors) > 0 {
		b.WriteString(labelStyle.Render("Connectors:"))
		b.WriteString("\n")
		for _, c := range row.Connectors {
			b.WriteString("  ")
			b.WriteString(healthDot(c.State))
			b.WriteString(" ")
			b.WriteString(valueStyle.Render(truncateStr(agents.SanitizeForDisplay(c.Name), 16)))
			b.WriteString(" ")
			b.WriteString(labelStyle.Render(truncateStr(agents.SanitizeForDisplay(c.Detail), contentWidth-22)))
			b.WriteString("\n")
		}
	}

	// The last few things it actually did.
	if len(row.Recent) > 0 {
		b.WriteString(labelStyle.Render("Recent:    "))
		b.WriteString("\n")
		limit := 3
		if len(row.Recent) < limit {
			limit = len(row.Recent)
		}
		for _, entry := range row.Recent[:limit] {
			b.WriteString(labelStyle.Render("  " + entry.At.Format("15:04") + "  "))
			b.WriteString(valueStyle.Render(truncateStr(agents.SanitizeForDisplay(entry.Summary), contentWidth-10)))
			b.WriteString("\n")
		}
	}

	if row.Attention != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorRed).
			Render("  ! " + truncateStr(agents.SanitizeForDisplay(row.Attention), contentWidth-4)))
		b.WriteString("\n")
	}

	return b.String()
}
