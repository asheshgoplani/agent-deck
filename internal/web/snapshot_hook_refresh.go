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
// Stale waiting remains durable for tools with start+stop hooks. Legacy Codex
// notify has no start edge, so its stale completion must yield to the live
// snapshot instead (#2189). The session's "seen" mark keeps a turn the user
// already looked at idle.
func applyHookStatusToMenuSession(sess *MenuSession, hs *session.HookStatus, now time.Time) {
	if sess == nil {
		return
	}
	// The published seen mark covers only turns that ended before the visit,
	// as on the cold read: if the TUI missed a running edge, a Stop newer
	// than the visit is still unseen.
	acked := sess.Acknowledged && hs != nil &&
		session.HookPredatesVisit(hs.UpdatedAt, hs.ReceivedAt, sess.AcknowledgedAt)
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:              sess.Tool,
		PriorStatus:       sess.Status,
		Hook:              hs,
		Now:               now,
		AllowStaleWaiting: !session.IsCodexCompatible(sess.Tool),
		Acknowledged:      acked,
		InstanceID:        sess.ID,
	})
	// Same call as the Codex fast path in Instance.updateStatus: a completion
	// older than the user's last visit was already seen. The seen mark must
	// be set too: a session marked unread can be published before its visit
	// time is cleared.
	if out.Status == session.StatusWaiting && session.IsCodexCompatible(sess.Tool) && acked {
		out.Status = session.StatusIdle
	}
	sess.Status = out.Status
}
