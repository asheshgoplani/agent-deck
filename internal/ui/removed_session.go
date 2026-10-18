package ui

import (
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// logRemovedSessionCleanup logs the best-effort cleanup errors of a session
// the TUI removed (op names the path: delete, worktree_finish).
func logRemovedSessionCleanup(op, id string, r session.RemovedSessionCleanup) {
	if r.InboxErr != nil {
		uiLog.Warn(op+"_inbox_sweep_err", slog.String("id", id), slog.String("err", r.InboxErr.Error()))
	}
	if r.NotifyStateErr != nil {
		uiLog.Warn(op+"_notify_state_sweep_err", slog.String("id", id), slog.String("err", r.NotifyStateErr.Error()))
	}
}
