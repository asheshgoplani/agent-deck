package ui

import (
	"log/slog"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// TUI heartbeat: <cache>/tui/<pid>.json, rewritten from the tick loop every
// update.TUIHeartbeatEvery and removed at exit, so `agent-deck update
// --check --json` can list the TUIs still running an older image than the
// file on disk, with the reason each one has not restarted. Written from
// the tick on purpose: while the loop is parked in tea.Exec (attached to a
// session) the file's last_tick_at stands still, which is exactly the
// "not ticking, nothing auto-update does runs" signal the watch needs.

// heartbeatStartedAt is when this process came up, for the file.
var heartbeatStartedAt = time.Now()

// tuiHeartbeat is this TUI's current heartbeat.
func (h *Home) tuiHeartbeat(now time.Time) update.TUIHeartbeat {
	state, reason := h.restartStateForHeartbeat()
	return update.TUIHeartbeat{
		PID:              os.Getpid(),
		Version:          Version,
		Exe:              h.restartExecutable(),
		Profile:          h.profile,
		StartedAt:        heartbeatStartedAt,
		UpdatedAt:        now,
		LastTickAt:       now,
		InstalledVersion: h.installedUpdateVersion(),
		InstalledSince:   h.installedUpdateSince(),
		RestartState:     state,
		BlockReason:      reason,
	}
}

// maybeWriteHeartbeat rewrites the heartbeat file when the last write is
// older than update.TUIHeartbeatEvery. No-op without a heartbeat dir.
func (h *Home) maybeWriteHeartbeat(now time.Time) {
	if h.heartbeatDir == "" || now.Sub(h.heartbeatWrittenAt) < update.TUIHeartbeatEvery {
		return
	}
	h.heartbeatWrittenAt = now
	if err := update.WriteTUIHeartbeat(h.heartbeatDir, h.tuiHeartbeat(now)); err != nil {
		uiLog.Debug("tui_heartbeat_write_failed", slog.String("error", err.Error()))
	}
}

// removeHeartbeat deletes this TUI's file; called on the way out.
func (h *Home) removeHeartbeat() {
	if h.heartbeatDir == "" {
		return
	}
	if err := update.RemoveTUIHeartbeat(h.heartbeatDir, os.Getpid()); err != nil {
		uiLog.Debug("tui_heartbeat_remove_failed", slog.String("error", err.Error()))
	}
}
