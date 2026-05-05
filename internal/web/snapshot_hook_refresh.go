package web

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// hookFastPathWindow mirrors session.hookFastPathWindow (unexported there).
// Hook events older than this window do not override the snapshot's status —
// matching the freshness guard in Instance.UpdateStatus's hook fast path.
const hookFastPathWindow = 2 * time.Minute

// refreshSnapshotHookStatuses re-applies the hook fast-path Status mapping that
// Instance.UpdateStatus performs (instance.go:2865-2920) to a MenuSnapshot in
// place. This closes a freshness gap in the web read path.
//
// Why this exists: the live web reads from MemoryMenuData, an in-memory cache
// pushed by the TUI's publishWebSessionStates. The TUI's view of hookStatus is
// fed by StatusFileWatcher (inotify); when an inotify event is dropped (queue
// overflow under load) the TUI's hookStatus stays stale, the fast-path window
// expires, UpdateStatus falls through to tmux pane heuristics, and the
// published Status flips to error. The CLI does not have this gap because
// `agent-deck list --json` reads each hook file from disk per call via
// session.RefreshInstancesForCLIStatus (cli_status_refresh.go).
//
// Calling this from the web GET handlers makes the web read path as resilient
// as the CLI without touching the TUI publish pipeline.
//
// Status precedence (matches Instance.UpdateStatus):
//   - StatusStopped is user-intentional and never overridden.
//   - Tools that emit lifecycle hooks (Claude-compatible, codex, gemini) get
//     their snapshot Status replaced when a fresh hook (within
//     hookFastPathWindow) is found.
//   - Hook "waiting" → StatusWaiting, "running" → StatusRunning, "dead" →
//     StatusError. Other hook values fall through (snapshot kept).
//   - Tools without hook signals (shell, custom) are left untouched.
//
// loader is injectable so tests don't depend on ~/.agent-deck/hooks/ contents.
// Production wiring uses defaultLoadHookStatuses via Server.hookStatusLoader.
func refreshSnapshotHookStatuses(snapshot *MenuSnapshot, loader func() map[string]*session.HookStatus) {
	if snapshot == nil || loader == nil {
		return
	}
	hooksByInstance := loader()
	if len(hooksByInstance) == 0 {
		return
	}
	now := time.Now()
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		if item.Type != MenuItemTypeSession || item.Session == nil {
			continue
		}
		applyHookStatusToMenuSession(item.Session, hooksByInstance[item.Session.ID], now)
	}
}

func applyHookStatusToMenuSession(sess *MenuSession, hs *session.HookStatus, now time.Time) {
	if sess == nil || hs == nil {
		return
	}
	if sess.Status == session.StatusStopped {
		// User-intentional state. Never flip stopped sessions.
		return
	}
	if !toolEmitsLifecycleHooks(sess.Tool) {
		return
	}
	if hs.UpdatedAt.IsZero() {
		return
	}
	fresh := now.Sub(hs.UpdatedAt) <= hookFastPathWindow

	switch hs.Status {
	case "waiting":
		// "waiting" is a durable Claude state. After a Stop hook fires, the
		// session does not transition to any other state until the next
		// UserPromptSubmit — which itself writes a fresh "running" hook
		// event. So if the hook file's latest record says "waiting", no
		// subsequent transition has occurred at the hook layer. Override
		// any non-stopped snapshot Status to mirror what `agent-deck list`
		// reports for the same database state. The CLI achieves the same
		// effect via Instance.UpdateStatus's tmux pane-title heuristic; the
		// web cannot afford that subprocess on every request, so we trust
		// the hook record instead.
		sess.Status = session.StatusWaiting
	case "running":
		// Running is transient: only override on fresh hooks. A stale
		// "running" hook can be misleading because the next "Stop" hook
		// (which would flip to waiting) may already have fired and been
		// reflected in the snapshot.
		if fresh {
			sess.Status = session.StatusRunning
		}
	case "dead":
		// Dead is durable but lifecycle-terminal: only override on fresh
		// hooks. Stale "dead" hooks risk overriding a session that has
		// since been restarted (which would have written a new "running"
		// hook, but we conservatively defer to snapshot here).
		if fresh {
			sess.Status = session.StatusError
		}
	}
}

// toolEmitsLifecycleHooks mirrors the tool gate in Instance.UpdateStatus.
func toolEmitsLifecycleHooks(tool string) bool {
	if session.IsClaudeCompatible(tool) {
		return true
	}
	return tool == "codex" || tool == "gemini"
}
