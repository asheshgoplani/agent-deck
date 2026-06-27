package ui

import (
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// shouldNotifyBudgetCrossing reports whether a one-time notification should fire
// for an upward transition into BudgetHigh or BudgetOver. Warn is intentionally
// bar/badge-only; dropping back never notifies.
func shouldNotifyBudgetCrossing(prev, cur session.BudgetLevel) bool {
	if cur <= prev {
		return false
	}
	return cur == session.BudgetHigh || cur == session.BudgetOver
}

// budgetWarnState returns a session's budget level and whether a usable context
// token signal exists (Claude-compatible tool + cached analytics). When ok is
// false, callers must not warn or act.
func (h *Home) budgetWarnState(inst *session.Instance, cfg session.ContextBudgetSettings) (session.BudgetLevel, bool) {
	if !session.IsClaudeCompatible(inst.Tool) {
		return session.BudgetNormal, false
	}
	a := h.getAnalyticsForSession(inst)
	if a == nil {
		return session.BudgetNormal, false
	}
	return a.BudgetLevel(cfg), true
}

// evaluateContextBudgetWarnings runs once per background tick over all sessions,
// firing a debounced one-shot notification on each upward crossing into
// high/over. Visual treatments (bar/badge) are handled in render.
func (h *Home) evaluateContextBudgetWarnings(instances []*session.Instance) {
	cfg := session.GetContextBudgetSettings()
	if !cfg.GetEnabled() {
		return
	}
	for _, inst := range instances {
		level, ok := h.budgetWarnState(inst, cfg)
		if !ok {
			continue
		}
		prev := h.budgetLastLevel[inst.ID] // zero value = BudgetNormal
		if shouldNotifyBudgetCrossing(prev, level) {
			h.notifyBudgetCrossing(inst, level)
		}
		h.budgetLastLevel[inst.ID] = level
	}
}

// notifyBudgetCrossing emits the one-time alert for a high/over crossing. It
// logs at WARN always; visual feedback is provided by the budget bar/badge.
// Debounce is handled by the caller's per-session last-level map.
func (h *Home) notifyBudgetCrossing(inst *session.Instance, level session.BudgetLevel) {
	uiLog.Warn("context_budget_crossing",
		slog.String("session", inst.Title),
		slog.String("id", inst.ID),
		slog.String("level", level.String()))
}
