package ui

import (
	"context"
	"log/slog"
	"os"
	"sync"
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
//
// That alone cannot tell a parked loop from a hung one, and an attach can
// last all night (2026-10-09: attached at 22:08, through a release, read
// "attached or hung" until morning). So a small worker off the event loop
// (runAttachedHeartbeat) keeps rewriting the loop's last heartbeat while
// the loop is parked, with Attached set and LastTickAt left where the
// loop put it. It never decides anything: every auto-update decision
// still waits for the next tick, after the detach.

// attachedHeartbeatPoll is how often the attach worker looks at the
// attach flag; it writes at most every update.TUIHeartbeatEvery.
const attachedHeartbeatPoll = 5 * time.Second

// heartbeatFile serialises the two writers of the heartbeat file (the
// event loop and the attach worker) and holds the loop's last heartbeat
// for the worker to repeat.
type heartbeatFile struct {
	mu        sync.Mutex
	last      update.TUIHeartbeat
	writtenAt time.Time
}

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
	hb := h.tuiHeartbeat(now)
	f := &h.heartbeatFile
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last, f.writtenAt = hb, now
	if err := update.WriteTUIHeartbeat(h.heartbeatDir, hb); err != nil {
		uiLog.Debug("tui_heartbeat_write_failed", slog.String("error", err.Error()))
	}
}

// startAttachedHeartbeat runs the attach worker until h.ctx ends.
func (h *Home) startAttachedHeartbeat() {
	if h.ctx == nil {
		return
	}
	ticker := time.NewTicker(attachedHeartbeatPoll)
	go func() {
		defer ticker.Stop()
		h.runAttachedHeartbeat(h.ctx, ticker.C)
	}()
}

// runAttachedHeartbeat calls attachedHeartbeatTick for every tick until
// ctx ends. It runs on its own goroutine, never on the event loop.
func (h *Home) runAttachedHeartbeat(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			h.attachedHeartbeatTick(now)
		}
	}
}

// attachedHeartbeatTick is one look by the attach worker: while the loop
// is parked in an attach it rewrites the loop's last heartbeat, at most
// every update.TUIHeartbeatEvery, with Attached set and UpdatedAt moved
// on. Otherwise it only forgets the attach it saw.
func (h *Home) attachedHeartbeatTick(now time.Time) {
	if h.heartbeatDir == "" || !h.isAttaching.Load() {
		h.attachedSince = time.Time{}
		return
	}
	if h.attachedSince.IsZero() {
		h.attachedSince = now
	}
	f := &h.heartbeatFile
	f.mu.Lock()
	defer f.mu.Unlock()
	// Re-checked under the lock: once the loop is back it owns the file.
	if f.last.PID == 0 || !h.isAttaching.Load() || now.Sub(f.writtenAt) < update.TUIHeartbeatEvery {
		return
	}
	hb := f.last
	hb.UpdatedAt = now
	hb.Attached = true
	hb.AttachedSince = h.attachedSince
	f.writtenAt = now
	if err := update.WriteTUIHeartbeat(h.heartbeatDir, hb); err != nil {
		uiLog.Debug("tui_heartbeat_write_failed", slog.String("error", err.Error()), slog.Bool("attached", true))
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
