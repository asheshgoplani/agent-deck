package ui

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/charmbracelet/x/ansi"
)

// The status worker queues its latest verdict (possibly "" to signal recovery);
// only the UI goroutine mutates footer state.
func (h *Home) queueHealthWarning(elapsed time.Duration, sessions int, calls int64) {
	warning := health.BudgetWarning(elapsed, sessions, calls)
	h.healthWarningPending.Store(&warning)
}

func (h *Home) consumeHealthWarning() {
	if warning := h.healthWarningPending.Swap(nil); warning != nil {
		h.healthWarningText = *warning
		if *warning != "" {
			h.healthWarningAt = time.Now()
		}
	}
	if h.healthWarningText == "" {
		if warning := health.CurrentWarning(); warning != "" {
			h.healthWarningText = warning
			h.healthWarningAt = time.Now()
		}
	}
}

func (h *Home) renderHealthWarning() string {
	if h.healthWarningText == "" || time.Since(h.healthWarningAt) > 5*time.Second {
		return ""
	}
	return ansi.Truncate(ErrorStyle.Render("⚠ "+h.healthWarningText), h.width, "…")
}
