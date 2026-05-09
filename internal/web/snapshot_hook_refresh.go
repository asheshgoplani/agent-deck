package web

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sessionstatus"
)

// refreshSnapshotHookStatuses re-applies the hook fast-path Status mapping to
// a MenuSnapshot in place, delegating the decision to internal/sessionstatus.
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
// as the CLI without touching the TUI publish pipeline. The mapping logic
// itself lives in internal/sessionstatus so all surfaces converge on the same
// rules (V1.9 PRIORITY plan, theme T1).
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

// applyHookStatusToMenuSession bridges the web's MenuSession DTO into the
// shared sessionstatus.Derive helper. The web read-path runs in
// AllowStaleWaiting=true mode (no per-request tmux subprocess budget); the
// MenuSession DTO does not carry the per-instance Acknowledged bit yet, so
// Acknowledged=false is the conservative v1.9.0 default.
func applyHookStatusToMenuSession(sess *MenuSession, hs *session.HookStatus, now time.Time) {
	if sess == nil {
		return
	}
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:              sess.Tool,
		PriorStatus:       sess.Status,
		Hook:              hs,
		Now:               now,
		AllowStaleWaiting: true,
	})
	sess.Status = out.Status
}
