package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// pendingRemoteFocus is a `session focus --remote` request (#2704) whose
// session was not in the remote list yet: a new session, or the first
// refresh after start. It lives in memory only and is retried after each
// remote refresh until deadline (the request's own TTL); it is never written
// back to the state db.
type pendingRemoteFocus struct {
	remote   string
	id       string
	attach   bool
	deadline time.Time
}

// remoteFlatItemIndex returns the flatItems index of the remote session row
// remote/id, or -1 if it is not in the current flat view.
func (h *Home) remoteFlatItemIndex(remote, id string) int {
	for i, fi := range h.flatItems {
		if fi.Type == session.ItemTypeRemoteSession && fi.RemoteSession != nil && fi.RemoteName == remote && fi.RemoteSession.ID == id {
			return i
		}
	}
	return -1
}

// lookupRemoteSession reports whether remote's last fetched list holds id and
// whether that session is archived.
func (h *Home) lookupRemoteSession(remote, id string) (group string, found, archived bool) {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	for _, rs := range h.remoteSessions[remote] {
		if rs.ID == id {
			return rs.Group, true, rs.Archived
		}
	}
	return "", false, false
}

// SelectRemoteSession reveals and selects the session id of the remote named
// remote, the remote counterpart of SelectSessionByID: it unfolds the remote
// header and the group headers above the row, clears a status filter or
// group scope that hides it, resets the time filter when only that still
// hides it, and moves the cursor. Returns true if the row was selected.
// Archived and unknown sessions return false and leave the cursor unchanged.
func (h *Home) SelectRemoteSession(remote, id string) bool {
	if remote == "" || id == "" {
		return false
	}
	group, found, archived := h.lookupRemoteSession(remote, id)
	if !found || archived {
		return false
	}
	if idx := h.remoteFlatItemIndex(remote, id); idx >= 0 {
		h.cursor = idx
		h.syncViewport()
		return true
	}

	// Open the remote header and every group header above the row.
	unfolded := false
	paths := []string{"remotes/" + remote}
	prefix := ""
	for _, seg := range strings.Split(normalizeRemoteGroupPath(group), "/") {
		if prefix == "" {
			prefix = seg
		} else {
			prefix += "/" + seg
		}
		paths = append(paths, "remotes/"+remote+"/"+prefix)
	}
	for _, p := range paths {
		if h.remoteGroupsCollapsed[p] {
			delete(h.remoteGroupsCollapsed, p)
			unfolded = true
		}
	}
	prevStatus, prevTime := h.statusFilter, h.timeFilter
	h.groupScopeMu.Lock()
	prevScope := h.groupScope
	h.groupScope = ""
	h.groupScopeMu.Unlock()
	h.statusFilter = ""
	h.rebuildFlatItems()

	idx := h.remoteFlatItemIndex(remote, id)
	if idx < 0 && h.timeFilter != session.TimeFilterAll {
		h.timeFilter = session.TimeFilterAll
		h.rebuildFlatItems()
		idx = h.remoteFlatItemIndex(remote, id)
	}
	if unfolded {
		h.saveUIState()
	}
	if idx < 0 {
		// Still not shown: give the user their filters back.
		h.statusFilter, h.timeFilter = prevStatus, prevTime
		h.groupScopeMu.Lock()
		h.groupScope = prevScope
		h.groupScopeMu.Unlock()
		h.rebuildFlatItems()
		return false
	}
	h.cursor = idx
	h.syncViewport()
	return true
}

// openRemoteSessionRow is Enter on a remote session row, shared by the Enter
// key and `session focus --remote --attach`. The classic layout attaches over
// SSH. The embedded layout restarts a stopped session first (the row
// refreshes when the remote confirms), then opens it in the embedded pane,
// falling back to the SSH attach.
func (h *Home) openRemoteSessionRow(remoteName string, rs *session.RemoteSessionInfo) tea.Cmd {
	if rs == nil {
		return nil
	}
	if !h.embeddedLayout {
		return h.attachRemoteSession(remoteName, rs.ID)
	}
	if strings.EqualFold(rs.Status, string(session.StatusStopped)) {
		return h.restartRemoteSession(remoteName, rs.ID, rs.Title)
	}
	if h.enterEmbeddedMode() {
		return h.startEmbeddedModeCmd()
	}
	return h.attachRemoteSession(remoteName, rs.ID)
}

// consumeRemoteFocus honors a fresh remote focus request. A session not in
// the list yet stays pending until the request's TTL ends; an archived one,
// or one on a remote that is not configured, is dropped.
func (h *Home) consumeRemoteFocus(fr session.FocusRequest) tea.Cmd {
	h.pendingRemoteFocus = nil
	if !h.remoteIsConfigured(fr.Remote) {
		return nil
	}
	p := &pendingRemoteFocus{
		remote:   fr.Remote,
		id:       fr.ID,
		attach:   fr.Attach,
		deadline: time.Unix(0, fr.TS).Add(session.FocusRequestTTL),
	}
	if _, found, archived := h.lookupRemoteSession(fr.Remote, fr.ID); archived {
		return nil
	} else if !found {
		// The tick fetches for it (remoteFocusWantsFetch). A host held by
		// poll backoff (30s and up) would outlive the request's 10s TTL,
		// so the request clears that host's hold.
		h.clearRemotePollBackoff(fr.Remote)
		h.pendingRemoteFocus = p
		return nil
	}
	return h.applyRemoteFocus(p)
}

// remoteFocusRetryInterval paces the extra remote fetches a waiting focus
// request asks for: the regular poll (15 s by default) is slower than the
// request's 10 s lifetime, and one fetch can land before the remote lists a
// session it is still creating.
const remoteFocusRetryInterval = 2 * time.Second

// remoteFocusWantsFetch reports whether the tick should start a remote fetch
// for a waiting request: one is pending and alive, no fetch is in flight, and
// the last fetch is at least remoteFocusRetryInterval old. The request's TTL
// bounds the retries.
func (h *Home) remoteFocusWantsFetch(now time.Time) bool {
	p := h.pendingRemoteFocus
	if p == nil || now.After(p.deadline) {
		return false
	}
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	return !h.remotesFetchActive && now.Sub(h.lastRemoteFetch) >= remoteFocusRetryInterval
}

// retryPendingRemoteFocus runs after each remote refresh: it selects (and
// with --attach opens) a pending request's session once the list has it.
func (h *Home) retryPendingRemoteFocus(now time.Time) tea.Cmd {
	p := h.pendingRemoteFocus
	if p == nil {
		return nil
	}
	if now.After(p.deadline) {
		h.pendingRemoteFocus = nil
		return nil
	}
	_, found, archived := h.lookupRemoteSession(p.remote, p.id)
	if !found {
		return nil
	}
	h.pendingRemoteFocus = nil
	if archived {
		return nil
	}
	return h.applyRemoteFocus(p)
}

// applyRemoteFocus selects the row and, for --attach, opens it like Enter.
func (h *Home) applyRemoteFocus(p *pendingRemoteFocus) tea.Cmd {
	if !h.SelectRemoteSession(p.remote, p.id) || !p.attach {
		return nil
	}
	item := h.flatItems[h.cursor]
	return h.openRemoteSessionRow(item.RemoteName, item.RemoteSession)
}
