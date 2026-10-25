package ui

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/optiming"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteGroupPaths returns the distinct, normalized group paths available as
// move targets on a remote, sorted for the move dialog (M key on a remote
// session). The remote's own group list (fetched on the fleet poll via
// `group list --json`) is the primary source: unlike session-derived buckets
// it includes EMPTY groups, so a folder that currently holds no sessions is
// still offered after every session has been moved out of it. Groups observed
// on the fetched sessions are unioned in as a fallback for remotes whose
// group-list fetch failed or predates the JSON shape.
func (h *Home) remoteGroupPaths(remoteName string) []string {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	seen := make(map[string]bool)
	var paths []string
	add := func(p string) {
		p = normalizeRemoteGroupPath(p)
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for _, p := range h.remoteGroups[remoteName] {
		add(p)
	}
	for _, s := range h.remoteSessions[remoteName] {
		add(s.Group)
	}
	sort.Strings(paths)
	return paths
}

// moveRemoteSessionToGroup routes a TUI "move to group" for a remote session
// over SSH: the session's row lives in the remote's own state DB, so the
// local tree cannot move it. Runs `agent-deck group move <id> <group>` on the
// remote (the same command the remote's own TUI would run) and returns a
// remoteMoveResultMsg so the fleet cache is updated only on remote
// confirmation. Mirrors the remote-rename path in GroupDialogRenameSession.
func (h *Home) moveRemoteSessionToGroup(title, remoteName, sessionID, targetGroupPath string) tea.Cmd {
	h.markRemoteAction(remoteName, "move", sessionID)
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return remoteMoveResultMsg{remoteName: remoteName, sessionID: sessionID, groupPath: targetGroupPath,
				err: fmt.Errorf("cannot move '%s': failed to load remotes config: %v", title, err)}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteMoveResultMsg{remoteName: remoteName, sessionID: sessionID, groupPath: targetGroupPath,
				err: fmt.Errorf("cannot move '%s': remote '%s' is not configured", title, remoteName)}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := runner.RunCommand(ctx, "group", "move", sessionID, targetGroupPath); err != nil {
			return remoteMoveResultMsg{remoteName: remoteName, sessionID: sessionID, groupPath: targetGroupPath,
				err: fmt.Errorf("failed to move '%s' on %s: %w", title, remoteName, err)}
		}
		return remoteMoveResultMsg{remoteName: remoteName, sessionID: sessionID, groupPath: targetGroupPath}
	}
}

// createRemoteGroup routes a TUI "create group" (g key on a remote row) over
// SSH: the group must be created in the remote's own state DB, not the local
// tree. Runs `agent-deck group create <name> [--parent <parent>]` on the
// remote and returns a remoteGroupResultMsg so the local group-path cache is
// updated only on remote confirmation. Mirrors moveRemoteSessionToGroup and
// the remote-rename path in GroupDialogRenameSession.
// remoteGroupCreateArgs builds the `group create` argv sent to a remote. The
// dialog's Default Path is forwarded as --default-path, the way the local
// create persists it, so what the dialog shows is what the server stores; a
// blank field sends nothing. The path is a server path and is not expanded
// locally.
func remoteGroupCreateArgs(name, parentPath, defaultPath string) []string {
	args := []string{"group", "create", name}
	if parentPath != "" {
		args = append(args, "--parent", parentPath)
	}
	if defaultPath != "" {
		args = append(args, "--default-path", defaultPath)
	}
	return args
}

func (h *Home) createRemoteGroup(name, remoteName, parentPath, defaultPath string) tea.Cmd {
	h.markRemoteAction(remoteName, "create-group", name)
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return remoteGroupResultMsg{remoteName: remoteName,
				err: fmt.Errorf("cannot create group '%s': failed to load remotes config: %v", name, err)}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteGroupResultMsg{remoteName: remoteName,
				err: fmt.Errorf("cannot create group '%s': remote '%s' is not configured", name, remoteName)}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		args := remoteGroupCreateArgs(name, parentPath, defaultPath)
		if _, err := runner.RunCommand(ctx, args...); err != nil {
			return remoteGroupResultMsg{remoteName: remoteName,
				err: fmt.Errorf("failed to create group '%s' on %s: %w", name, remoteName, err)}
		}
		full := name
		if parentPath != "" {
			full = parentPath + "/" + name
		}
		return remoteGroupResultMsg{remoteName: remoteName, groupPath: full}
	}
}

// deleteRemoteGroup routes a TUI "delete group" for one of the remote's own
// groups over SSH: `agent-deck group delete <path>` on the remote, without
// --force, so a group that still holds sessions is refused by the remote with
// its own message instead of silently moving sessions around.
func (h *Home) deleteRemoteGroup(groupPath, remoteName string) tea.Cmd {
	h.markRemoteAction(remoteName, "delete-group", groupPath)
	return func() tea.Msg {
		result := func(err error) tea.Msg {
			return remoteGroupDeleteResultMsg{remoteName: remoteName, groupPath: groupPath, err: err}
		}
		runner, err := remoteRunnerFor(remoteName)
		if err != nil {
			return result(fmt.Errorf("cannot delete group '%s': %v", groupPath, err))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := runner.RunCommand(ctx, "group", "delete", groupPath); err != nil {
			return result(fmt.Errorf("failed to delete group '%s' on %s: %w", groupPath, remoteName, err))
		}
		return result(nil)
	}
}

// remoteGroupBaseName is the last segment of a remote group path, the name
// createRemoteGroup was asked for.
func remoteGroupBaseName(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// setRemotePending marks (verb != "") or clears a remote session row's
// in-flight action and redraws.
func (h *Home) setRemotePending(sessionID, verb string) {
	if h.remotePending == nil {
		h.remotePending = make(map[string]string)
	}
	if verb == "" {
		delete(h.remotePending, sessionID)
	} else {
		h.remotePending[sessionID] = verb
	}
	// Read at draw time by renderRemoteSessionItem; no row rebuild needed.
}

// patchRemoteSession applies fn to the cached copy of one remote session and
// redraws, so the screen reflects a confirmed action now rather than at the
// next poll; the next fetch reconciles from the remote's truth.
func (h *Home) patchRemoteSession(remoteName, sessionID string, fn func(*session.RemoteSessionInfo)) {
	h.remoteSessionsMu.Lock()
	if sessions, ok := h.remoteSessions[remoteName]; ok {
		for i := range sessions {
			if sessions[i].ID == sessionID {
				fn(&sessions[i])
				break
			}
		}
	}
	h.remoteSessionsMu.Unlock()
	h.cachedStatusCounts.valid.Store(false)
	h.rebuildFlatItems()
}

// insertRemoteSession adds (or replaces by id) one session in the cache and
// redraws, so a session the remote just confirmed is on screen before any
// fetch. Ungrouped sessions land in the default group like the remote does.
func (h *Home) insertRemoteSession(info session.RemoteSessionInfo) {
	if info.Group == "" {
		info.Group = session.DefaultGroupPath
	}
	h.remoteSessionsMu.Lock()
	if h.remoteSessions == nil {
		h.remoteSessions = make(map[string][]session.RemoteSessionInfo)
	}
	sessions := h.remoteSessions[info.RemoteName]
	replaced := false
	for i := range sessions {
		if sessions[i].ID == info.ID {
			sessions[i] = info
			replaced = true
			break
		}
	}
	if !replaced {
		sessions = append(sessions, info)
	}
	h.remoteSessions[info.RemoteName] = sessions
	h.remoteSessionsMu.Unlock()
	h.cachedStatusCounts.valid.Store(false)
	h.rebuildFlatItems()
}

// seedRemotePreview stores content as the preview for a remote session so
// it renders at once; the regular remote preview fetch replaces it later.
func (h *Home) seedRemotePreview(remoteName, sessionID, content string) {
	key := remotePreviewCacheKey(remoteName, sessionID)
	h.previewCacheMu.Lock()
	defer h.previewCacheMu.Unlock()
	h.previewCache[key] = content
	h.previewCacheTime[key] = time.Now()
}

// dropRemoteSession removes one session from the cache and redraws.
func (h *Home) dropRemoteSession(remoteName, sessionID string) {
	h.remoteSessionsMu.Lock()
	if sessions, ok := h.remoteSessions[remoteName]; ok {
		kept := sessions[:0]
		for _, s := range sessions {
			if s.ID != sessionID {
				kept = append(kept, s)
			}
		}
		h.remoteSessions[remoteName] = kept
	}
	h.remoteSessionsMu.Unlock()
	h.noteRemoteSessionRemoved(remoteName, sessionID)
	h.cachedStatusCounts.valid.Store(false)
	h.rebuildFlatItems()
}

// remoteActionKey identifies one in-flight remote action for timing.
func remoteActionKey(remoteName, verb, target string) string {
	return remoteName + "|" + verb + "|" + target
}

// markRemoteAction records the moment a remote action was requested.
func (h *Home) markRemoteAction(remoteName, verb, target string) {
	if h.remoteActionStarted == nil {
		h.remoteActionStarted = make(map[string]time.Time)
	}
	h.remoteActionStarted[remoteActionKey(remoteName, verb, target)] = time.Now()
}

// remoteActionTook returns " in 0.4s" for an action recorded by
// markRemoteAction, and "" when the start is unknown. The entry is dropped so
// a later action with the same key measures itself afresh. The raw number
// also goes to the debug log as remote_action, so latency can be compared
// across builds without reading the screen.
func (h *Home) remoteActionTook(remoteName, verb, target string) string {
	key := remoteActionKey(remoteName, verb, target)
	start, ok := h.remoteActionStarted[key]
	if !ok {
		return ""
	}
	delete(h.remoteActionStarted, key)
	elapsed := time.Since(start)
	uiLog.Debug("remote_action", slog.String("remote", remoteName), slog.String("verb", verb), slog.String("target", target), slog.Int64("took_ms", elapsed.Milliseconds()))
	return fmt.Sprintf(" in %.1fs", elapsed.Seconds())
}

// remoteGroupPathFromItem extracts the remote-relative group path from a
// remote group header's Item.Path ("remotes/<name>/<group-path>"); returns
// "" for the level-0 host header (Path == "remotes/<name>"). Used by the g
// key handler to decide the parent for a remote subgroup create.
func remoteGroupPathFromItem(item session.Item) string {
	prefix := "remotes/" + item.RemoteName + "/"
	p := strings.TrimPrefix(item.Path, prefix)
	if p == item.Path {
		return "" // level-0 host header
	}
	return p
}

// applyRemoteFetch folds one remote's fetch result (or a pushed change shaped
// like one) into the cache, the on-disk snapshot and the rows.
func (h *Home) applyRemoteFetch(msg remoteSessionsFetchedMsg) (tea.Model, tea.Cmd) {
	if msg.pollName != "" && !h.remotePollConfigMatches(msg.pollName, msg.pollConfig) {
		h.remoteSessionsMu.Lock()
		h.remoteFetchLanded(msg)
		h.remoteSessionsMu.Unlock()
		return h, nil
	}
	if msg.configErr != nil || h.remoteFetchIsStale(msg) {
		// Config unreadable, or a fetch that started before one already
		// applied for the same remote: keep the current rows. Account for
		// the landed fetch so the round can finish, and advance the poll
		// clock for a poll result, or the next tick would start another
		// round at once and a remote whose pushes keep outrunning the poll
		// would be listed every two seconds for nothing (finding 1).
		h.remoteSessionsMu.Lock()
		h.remoteFetchLanded(msg)
		if !msg.pushed {
			h.lastRemoteFetch = time.Now()
		}
		// A superseded poll result still carries what the push did not:
		// the cost summary always, the group list when nothing newer
		// brought one. Without this a remote whose pushes always beat the
		// poll never shows a cost figure.
		staleGroups := make(map[string][]string, len(msg.groups))
		for name, paths := range msg.groups {
			if msg.gen >= h.remoteGroupsGen[name] {
				staleGroups[name] = paths
			}
		}
		h.applyRemoteGroupLists(msg.gen, staleGroups, nil, false)
		h.remoteSessionsMu.Unlock()
		if msg.configErr == nil {
			h.applyRemoteCosts(msg)
			// The version answer is a fact about the remote, not about
			// which listing is newer: keep it (#2164).
			h.recordRemoteVersions(msg.versions)
			h.recordRemoteStats(msg.stats)
		} else {
			h.setError(fmt.Errorf("remote refresh skipped, config could not be read: %v", msg.configErr))
		}
		return h, nil
	}
	h.remoteSessionsMu.Lock()
	prevSessions := h.remoteSessions
	// #1170: merge rather than wholesale-replace so a remote that errored
	// this round keeps its last-good sessions instead of flickering out.
	h.remoteSessions = mergeRemoteSessions(h.remoteSessions, msg.sessions, msg.failed)
	if msg.gen != 0 {
		if h.remoteFetchApplied == nil {
			h.remoteFetchApplied = make(map[string]uint64)
		}
		for name := range msg.sessions {
			h.remoteFetchApplied[name] = msg.gen
		}
		// A remote this message deconfigured (absent from both lists) must
		// not come back when a slower result from an older round lands.
		for name := range prevSessions {
			if _, fetched := msg.sessions[name]; !fetched && !msg.failed[name] {
				h.remoteFetchApplied[name] = msg.gen
			}
		}
	}
	h.applyRemoteGroupLists(msg.gen, msg.groups, msg.groupsFailed, true)
	for name := range msg.sessions {
		if !msg.failed[name] {
			delete(h.remoteFromCache, name)
		}
	}
	// Only a poll advances the poll clock: a push says nothing about the
	// other remotes, and a chatty remote must not keep the fleet poll from
	// ever running for the quiet ones (finding 1).
	if !msg.pushed {
		h.lastRemoteFetch = time.Now()
	}
	roundDone := h.remoteFetchLanded(msg)
	h.remoteSessionsMu.Unlock()
	h.saveRemoteSessionsCache(msg.sessions)
	h.recordRemoteVersions(msg.versions)
	h.recordRemoteStats(msg.stats)
	h.applyRemoteCosts(msg)
	// #1112 bug 1: a remote running→waiting transition wouldn't update
	// the header pill ("[◐ Waiting N]") because countSessionStatuses
	// caches for 500ms. The row icon updated (read from the map
	// directly), but the pill froze on the previous fetch's totals.
	// Invalidate so the next View() recomputes.
	h.cachedStatusCounts.valid.Store(false)
	h.rebuildFlatItems()
	// #2704: a focus request that arrived before its session did.
	focusCmd := h.retryPendingRemoteFocus(time.Now())
	if !roundDone {
		return h, focusCmd
	}
	h.remoteSessionsMu.Lock()
	again := h.remoteRefetchWanted
	h.remoteRefetchWanted = false
	if again {
		h.remotesFetchActive = true
	}
	h.remoteSessionsMu.Unlock()
	if again {
		return h, tea.Batch(focusCmd, h.fetchRemoteSessions)
	}
	return h, focusCmd
}

// remoteFetchIsStale reports whether a newer fetch has already been applied
// for any remote this message carries data for. Remotes listed only as
// failed carry no data, so they never make a message stale.
func (h *Home) remoteFetchIsStale(msg remoteSessionsFetchedMsg) bool {
	if msg.gen == 0 {
		return false
	}
	for name := range msg.sessions {
		if msg.gen < h.remoteFetchApplied[name] {
			return true
		}
	}
	return false
}

// applyRemoteGroupLists stores the group lists a message brought. Remotes
// with a fresh list are replaced wholesale; failed remotes keep their
// last-good cached paths so the move dialog doesn't lose empty-group targets
// on a transient SSH hiccup. With prune set, remotes absent from BOTH lists
// are dropped (deconfigured); a superseded result passes false because it
// knows nothing about who is still configured. Called with remoteSessionsMu
// held.
func (h *Home) applyRemoteGroupLists(gen uint64, groups map[string][]string, groupsFailed map[string]bool, prune bool) {
	if h.remoteGroups == nil {
		h.remoteGroups = make(map[string][]string)
	}
	if h.remoteGroupsGen == nil {
		h.remoteGroupsGen = make(map[string]uint64)
	}
	for name, paths := range groups {
		h.remoteGroups[name] = paths
		h.remoteGroupsGen[name] = gen
	}
	if !prune {
		return
	}
	for name := range h.remoteGroups {
		if _, fresh := groups[name]; fresh || groupsFailed[name] {
			continue
		}
		delete(h.remoteGroups, name)
		delete(h.remoteGroupsGen, name)
	}
}

// applyRemoteCosts folds a message's cost summaries into the per-remote map
// (#1101) so renderCostLine can show them on the next paint.
func (h *Home) applyRemoteCosts(msg remoteSessionsFetchedMsg) {
	if msg.costs == nil {
		return
	}
	h.remoteCostsMu.Lock()
	h.remoteCosts = mergeRemoteCosts(h.remoteCosts, msg)
	h.remoteCostsMu.Unlock()
}

// remoteFetchLanded accounts for one fetch result and reports whether that
// left no fetch outstanding, clearing the in-flight guard when it did. Only
// a round's own per-remote results count down the outstanding total; a
// message from outside a round (config unreadable, no remotes) releases the
// guard only when no round is in flight. Pushed changes are not part of any
// round and never touch the guard. Called with remoteSessionsMu held.
func (h *Home) remoteFetchLanded(msg remoteSessionsFetchedMsg) bool {
	if msg.pushed {
		return false
	}
	if msg.inRound && h.remoteFetchOutstanding > 0 {
		h.remoteFetchOutstanding--
	}
	if h.remoteFetchOutstanding > 0 {
		h.remotesFetchActive = true
		return false
	}
	h.remotesFetchActive = false
	return true
}

// mergeRemoteCosts folds one remote's cost summary into the per-remote map
// without blanking the other remotes' figures: a remote whose result this
// message carries either gets its fresh summary or, when its cost fetch
// failed, contributes zero (as before); remotes still marked failed keep
// their last-good figure; remotes absent from both lists were deconfigured.
func mergeRemoteCosts(prev map[string]*costs.RemoteCostSummary, msg remoteSessionsFetchedMsg) map[string]*costs.RemoteCostSummary {
	merged := make(map[string]*costs.RemoteCostSummary, len(prev)+len(msg.costs))
	for name, summary := range prev {
		if _, fetched := msg.sessions[name]; fetched {
			continue
		}
		if msg.failed[name] {
			merged[name] = summary
		}
	}
	for name, summary := range msg.costs {
		merged[name] = summary
	}
	return merged
}

// remoteChangedMsg says a remote pushed "changed" over its persistent
// channel (#2174): its state DB was written by someone, so refetch now.
type remoteChangedMsg struct {
	remoteName string
	change     session.RemoteChange
}

// waitRemoteChange blocks on the fan-in of remote change pushes and turns the
// next one into a message. It re-arms itself from the handler.
func (h *Home) waitRemoteChange() tea.Msg {
	select {
	case <-h.ctx.Done():
		return nil
	case change, ok := <-session.RemoteChangeEvents():
		if !ok {
			return nil
		}
		return remoteChangedMsg{remoteName: change.Remote, change: change}
	}
}

// remoteIsConfigured reports whether remoteName is in the last config read.
// Before the first read every name passes: the startup cache may hold rows
// for remotes the config has not been consulted about yet.
func (h *Home) remoteIsConfigured(remoteName string) bool {
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	if h.remoteConfigured == nil {
		return true
	}
	_, ok := h.remoteConfigured[remoteName]
	return ok
}

// pushedRemoteFetch shapes a pushed change as a fetch result for that one
// remote: every other remote is marked failed (the merge keeps its rows),
// costs are absent (left untouched), and the sequence number advances so an
// older poll cannot overwrite this newer state. The caller has checked that
// the pusher is still configured (remoteIsConfigured).
func (h *Home) pushedRemoteFetch(ch session.RemoteChange) remoteSessionsFetchedMsg {
	msg := remoteSessionsFetchedMsg{
		gen:          atomic.AddUint64(&h.remoteFetchSeq, 1),
		pushed:       true,
		sessions:     map[string][]session.RemoteSessionInfo{ch.Remote: ch.Sessions},
		failed:       map[string]bool{},
		groups:       map[string][]string{},
		groupsFailed: map[string]bool{},
	}
	if ch.Groups != nil {
		msg.groups[ch.Remote] = ch.Groups
	} else {
		msg.groupsFailed[ch.Remote] = true
	}
	// Every other remote is marked failed: the ones the config lists, and
	// the ones the TUI still knows by rows, a group list or a cost figure
	// (a remote with zero sessions still has a host header and folders to
	// keep, finding 12; the startup cache may know remotes the config has
	// not been read for yet).
	h.remoteSessionsMu.RLock()
	for name := range h.remoteConfigured {
		if name != ch.Remote {
			msg.failed[name] = true
			msg.groupsFailed[name] = true
		}
	}
	for name := range h.remoteSessions {
		if name != ch.Remote {
			msg.failed[name] = true
			msg.groupsFailed[name] = true
		}
	}
	for name := range h.remoteGroups {
		if name != ch.Remote {
			msg.failed[name] = true
			msg.groupsFailed[name] = true
		}
	}
	h.remoteSessionsMu.RUnlock()
	h.remoteCostsMu.RLock()
	for name := range h.remoteCosts {
		if name != ch.Remote {
			msg.failed[name] = true
			msg.groupsFailed[name] = true
		}
	}
	h.remoteCostsMu.RUnlock()
	return msg
}

// remoteActionGrace is how long after a remote confirms a user action its
// pushes are checked against the sessions that action removed. The agent's
// probe lists the state DB on a timer, so a listing snapshotted just before
// the action ran can arrive just after its confirmation.
const remoteActionGrace = 3 * time.Second

// remoteOutcomeUnknown is the handler's answer when the channel dropped
// after a mutating command was written to remoteName and before its reply
// (session.IsRemoteInterrupted): the remote may or may not have run it, so
// nothing is confirmed or reverted on faith, the footer says so, and a
// fetch settles the tree.
func (h *Home) remoteOutcomeUnknown(remoteName string) tea.Cmd {
	h.setError(fmt.Errorf("on %s: connection dropped before the reply, refreshing", remoteName))
	return h.fetchRemoteSessions
}

// noteRemoteActionSettled records that remote just confirmed a user action.
func (h *Home) noteRemoteActionSettled(remoteName string) {
	if h.remoteActionSettled == nil {
		h.remoteActionSettled = make(map[string]time.Time)
	}
	h.remoteActionSettled[remoteName] = time.Now()
}

// remoteActionRecord is what a confirmed action did to one session: took
// it off the remote's rows (delete), or moved it between the active and the
// archived view (archive, unarchive: archived says which way).
type remoteActionRecord struct {
	at       time.Time
	archived *bool
}

// contradicts reports whether a pushed listing still shows the session as
// it was before the action: a removed session listed at all, an archived
// one listed active or the reverse.
func (r remoteActionRecord) contradicts(s *session.RemoteSessionInfo) bool {
	return r.archived == nil || s.Archived != *r.archived
}

// noteRemoteSessionRemoved records that a confirmed action removed sessionID
// from remoteName's rows, so a push still listing it is known to be stale.
func (h *Home) noteRemoteSessionRemoved(remoteName, sessionID string) {
	h.noteRemoteAction(remoteName, sessionID, remoteActionRecord{at: time.Now()})
}

// noteRemoteSessionArchived records that a confirmed action archived (or
// unarchived) sessionID, so a push still listing it the old way is stale.
func (h *Home) noteRemoteSessionArchived(remoteName, sessionID string, archived bool) {
	h.noteRemoteAction(remoteName, sessionID, remoteActionRecord{at: time.Now(), archived: &archived})
}

func (h *Home) noteRemoteAction(remoteName, sessionID string, rec remoteActionRecord) {
	if h.remoteActionRemoved == nil {
		h.remoteActionRemoved = make(map[string]map[string]remoteActionRecord)
	}
	if h.remoteActionRemoved[remoteName] == nil {
		h.remoteActionRemoved[remoteName] = make(map[string]remoteActionRecord)
	}
	h.remoteActionRemoved[remoteName][sessionID] = rec
}

// remoteActionInProgress reports whether a user action on remoteName is
// still waiting for the remote's answer.
func (h *Home) remoteActionInProgress(remoteName string) bool {
	if len(h.remotePending) == 0 {
		return false
	}
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	for _, s := range h.remoteSessions[remoteName] {
		if _, pending := h.remotePending[s.ID]; pending {
			return true
		}
	}
	return false
}

// lastRemoteMutationStamp is the stamp of the last mutating command the
// remote confirmed over its channel, 0 when there is no channel or it does
// not know.
func (h *Home) lastRemoteMutationStamp(remoteName string) int64 {
	if h.remoteMutationStamp != nil {
		return h.remoteMutationStamp(remoteName)
	}
	if ch := session.RemoteChannelFor(remoteName); ch != nil {
		return ch.LastMutationStamp()
	}
	return 0
}

// stalePushedChange reports whether a pushed listing for ch.Remote predates
// a user action and must not be applied (finding 10).
//
// When the listing carries the DB stamp it was taken at and the remote's
// channel knows the stamp of the last mutating command it confirmed, the
// two decide it: a listing stamped before the command is stale, one stamped
// at or after it is the truth (the channel already drops the former before
// it gets here; the check is repeated so the verdict does not depend on
// which side saw the stamps).
//
// Without stamps (an agent that predates them, a command that ran as a
// plain ssh exec while the channel was down), session membership decides:
// while an action on that remote is in flight, or within remoteActionGrace
// of its confirmation, a listing that still contains a session the action
// removed, or shows one it archived as active (or the reverse), is stale:
// the probe read the DB before the action ran. Outside that window, or
// when the listing agrees with the action, the push applies as usual.
// Entries older than the grace are dropped as they are met.
func (h *Home) stalePushedChange(ch session.RemoteChange, now time.Time) bool {
	removed := h.remoteActionRemoved[ch.Remote]
	if len(removed) == 0 {
		return false
	}
	for id, rec := range removed {
		if now.Sub(rec.at) >= remoteActionGrace {
			delete(removed, id)
		}
	}
	if len(removed) == 0 {
		delete(h.remoteActionRemoved, ch.Remote)
		return false
	}
	if ch.Stamp > 0 {
		if last := h.lastRemoteMutationStamp(ch.Remote); last > 0 {
			return ch.Stamp < last
		}
	}
	inGrace := h.remoteActionInProgress(ch.Remote) || now.Sub(h.remoteActionSettled[ch.Remote]) < remoteActionGrace
	if !inGrace {
		return false
	}
	stale := false
	for id, rec := range removed {
		for i := range ch.Sessions {
			if ch.Sessions[i].ID == id && rec.contradicts(&ch.Sessions[i]) {
				stale = true
				break
			}
		}
	}
	return stale
}

// fetchRemoteSessions starts a fleet poll. It reads the config, sweeps stale
// ssh sockets and returns one fetch Cmd per remote; the handler batches
// them, and each answers on its own, so the tree updates host by host as
// answers arrive instead of after the slowest one (#2177).
func (h *Home) fetchRemoteSessions() tea.Msg {
	gen := atomic.AddUint64(&h.remoteFetchSeq, 1)
	config, err := session.LoadUserConfig()
	if err != nil {
		// A hand-edited config.toml that fails to parse must not wipe the
		// remotes from the tree (and the on-disk cache) as if they had been
		// deconfigured; keep everything and say why.
		return remoteSessionsFetchedMsg{gen: gen, configErr: err}
	}
	// The config is the truth about which remotes exist: close the channel
	// of any remote that was removed or re-pointed at another host, so it
	// stops pushing rows into the tree, and remember the names so a push
	// can be checked against them (#8).
	var remotes map[string]session.RemoteConfig
	if config != nil {
		remotes = config.Remotes
	}
	session.ReconcileRemoteChannels(remotes)
	configured := make(map[string]struct{}, len(remotes))
	for name := range remotes {
		configured[name] = struct{}{}
	}
	h.remoteSessionsMu.Lock()
	h.remoteConfigured = configured
	h.remoteSessionsMu.Unlock()
	if len(remotes) == 0 {
		h.seedRemotePolls(remotes)
		return remoteSessionsFetchedMsg{gen: gen, sessions: nil}
	}

	// #1421: sweep orphaned SSH ControlMaster sockets before fetching. A stale
	// socket (master died on a remote update / network drop) makes the next
	// ControlMaster=auto connect hang forever — ConnectTimeout does not bound
	// the Unix-socket dial — which would block this whole periodic fetch and
	// make every remote session vanish until the TUI restarts.
	session.CleanStaleSSHSockets()

	return remoteFetchRoundMsg{gen: gen, fetches: h.remoteFetchCmds(gen, config.Remotes)}
}

// remoteFetchCmds builds one Cmd per configured remote for fetch round gen.
func (h *Home) remoteFetchCmds(gen uint64, remotes map[string]session.RemoteConfig) []tea.Cmd {
	h.seedRemotePolls(remotes)
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	cmds := make([]tea.Cmd, 0, len(remotes))
	for name, rc := range remotes {
		cmds = append(cmds, func() tea.Msg { return h.fetchOneRemote(gen, name, rc, names) })
	}
	return cmds
}

// fetchOneRemote fetches one remote's sessions, cost summary and group list
// and shapes them as a result for that remote alone: every other configured
// remote is marked failed so the merge keeps its rows, and a remote missing
// from the config altogether is absent from both lists and so drops out.
func (h *Home) fetchOneRemote(gen uint64, name string, rc session.RemoteConfig, configured []string) remoteSessionsFetchedMsg {
	msg := remoteSessionsFetchedMsg{
		gen:          gen,
		pollName:     name,
		pollConfig:   rc,
		inRound:      true,
		sessions:     make(map[string][]session.RemoteSessionInfo, 1),
		costs:        make(map[string]*costs.RemoteCostSummary, 1),
		groups:       make(map[string][]string, 1),
		groupsFailed: make(map[string]bool, len(configured)),
		failed:       make(map[string]bool, len(configured)),
	}
	for _, other := range configured {
		if other != name {
			msg.failed[other] = true
			msg.groupsFailed[other] = true
		}
	}

	// Reserve the host across periodic, manual and push-triggered rounds.
	// A skipped host preserves its rows without issuing another SSH command.
	if !h.beginRemotePoll(name, rc) {
		msg.failed[name] = true
		msg.groupsFailed[name] = true
		return msg
	}
	started := h.remoteClock()
	var pollErr error
	defer func() { h.finishRemotePoll(name, rc, started, pollErr) }()

	// Honor the per-remote command_timeout_seconds: hosts with large
	// session fleets legitimately need more than the old flat 15s. Each
	// remote runs under its own bound (#1170) and answers on its own
	// (#2177), so one slow or offline host starves nobody.
	ctx, cancel := context.WithTimeout(h.ctx, rc.GetCommandTimeout())
	defer cancel()

	var runner remoteFetchRunner
	if h.newRemoteFetchRunner != nil {
		runner = h.newRemoteFetchRunner(name, rc)
	} else {
		runner = session.NewSSHRunner(name, rc)
	}
	remoteStarted := time.Now()
	remoteOutcome := "ok"
	var listStats *session.ListStats
	defer func() {
		statusPassMS, tmuxCalls, statCount := int64(0), int64(0), 0
		if listStats != nil {
			statusPassMS, tmuxCalls, statCount = listStats.StatusPassMS, listStats.TmuxCalls, listStats.Sessions
		}
		health.RecordRemote(name, time.Since(remoteStarted), remoteOutcome, statusPassMS, tmuxCalls, statCount)
		optiming.Since("remote.list", remoteStarted)
	}()
	sessions, listStats, err := runner.FetchSessions(ctx)
	pollErr = err
	if err != nil {
		remoteOutcome = "failed"
		// #1170: the handler keeps this remote's last-good sessions; the
		// group list can't be trusted either, so keep that too.
		msg.failed[name] = true
		msg.groupsFailed[name] = true
		return msg
	}
	for i := range sessions {
		sessions[i].RemoteName = name
	}
	// The cost summary and the group list are independent of the session
	// list and of each other, so they run concurrently over the same
	// ControlMaster connection: one round trip per poll instead of three in
	// a row (#2167). Each keeps its own bound, and a failure degrades as
	// before: costs to "contributes zero", groups to the session-derived
	// fallback in remoteGroupPaths.
	var (
		summary    *costs.RemoteCostSummary
		costErr    error
		groupPaths []string
		groupErr   error
		side       sync.WaitGroup
	)
	// #2164: the remote's agent-deck version rides the same round, but only
	// when its cached answer is older than remoteVersionCheckInterval, so
	// the header's drift marker costs one SSH call per hour per remote, not
	// per tick. Test runners that cannot report a version are skipped.
	checker, canCheck := runner.(remoteVersionChecker)
	needVersion := canCheck && h.remoteVersionNeedsCheck(name, time.Now())
	if needVersion {
		side.Add(1)
		go func() {
			defer side.Done()
			versionCtx, versionCancel := context.WithTimeout(h.ctx, rc.GetCommandTimeout())
			defer versionCancel()
			version, found := checker.CheckBinary(versionCtx)
			state := session.RemoteVersionState{Version: version, Found: found, CheckedAt: time.Now()}
			// The remote's own update timer rides the same hourly check
			// (#2472), so the preview says whether it depends on this
			// controller's nudge alone.
			if timerChecker, ok := runner.(remoteTimerChecker); ok && found {
				if st, ok := fetchRemoteTimer(h.ctx, timerChecker, rc.GetCommandTimeout()); ok {
					state.Timer, state.TimerCheckedAt = &st, time.Now()
				}
			}
			msg.versions = map[string]session.RemoteVersionState{name: state}
		}()
	}
	// The remote's live load/memory/disk snapshot rides the same round too,
	// every poll (unlike the version check, which is throttled to once an
	// hour): the preview panel already waits on this poll, so there is no
	// new blocking work on the TUI hot path. An older remote without
	// `system stats` (or an unreachable one) simply fails the call, and the
	// panel renders "stats unknown" instead of guessing.
	statsRunner, canStats := runner.(remoteStatsFetcher)
	if canStats {
		side.Add(1)
		go func() {
			defer side.Done()
			statsCtx, statsCancel := context.WithTimeout(h.ctx, rc.GetCommandTimeout())
			defer statsCancel()
			started := time.Now()
			stats, err := statsRunner.FetchSystemStats(statsCtx)
			if err != nil {
				stats = session.RemoteHostStats{}
			}
			msg.stats = map[string]remoteHostStatsResult{
				name: {Stats: stats, Latency: time.Since(started), FetchedAt: time.Now()},
			}
		}()
	}

	side.Add(2)
	go func() {
		defer side.Done()
		costCtx, costCancel := context.WithTimeout(h.ctx, rc.GetCommandTimeout())
		defer costCancel()
		summary, costErr = runner.FetchCostSummary(costCtx)
	}()
	go func() {
		defer side.Done()
		groupCtx, groupCancel := context.WithTimeout(h.ctx, rc.GetCommandTimeout())
		defer groupCancel()
		groupPaths, groupErr = runner.FetchGroupPaths(groupCtx)
	}()
	side.Wait()
	if costErr != nil || groupErr != nil {
		remoteOutcome = "partial"
	}

	msg.sessions[name] = sessions
	// #1101: a nil summary (older remote without `costs summary --json`)
	// is left out so the renderer treats the remote as contributing zero.
	if costErr == nil && summary != nil {
		msg.costs[name] = summary
	}
	if groupErr == nil {
		if groupPaths == nil {
			groupPaths = []string{}
		}
		msg.groups[name] = groupPaths
	} else {
		msg.groupsFailed[name] = true
	}
	return msg
}

// mergeRemoteSessions reconciles a freshly fetched remote-session map against
// the previously displayed one (issue #1170). The contract:
//
//   - remotes present in fetched → replaced wholesale (new sessions appear,
//     removed sessions drop);
//   - remotes in failed (errored this round) → keep their last-good sessions
//     from prev, so a transient SSH hiccup never wipes a remote; a remote
//     known with ZERO sessions (fresh, or fully archived) is kept too, so its
//     host header and folders survive a result that did not cover it
//     (finding 12);
//   - remotes absent from both fetched and failed → dropped (deconfigured).
//
// A remote's own `list --json` answer is untrusted wire data: nothing on the
// client enforces that it names each session id at most once (a
// misconfigured profile scope, a remote-side listing bug, or two profiles
// whose sessions happen to share an id could all produce a collision), and a
// duplicate id here used to survive straight into the flattened tree as two
// rows for "the same" session with two different Status/Group snapshots —
// the group and session duplication seen in the field (root cause: a
// same-id collision inside one fetch's own slice, never de-duplicated
// before this function's caller renders it). dedupeRemoteSessionsByID closes
// that off at the merge point, which is the only place both this round's
// fetch AND the carried-over previous round meet, so every path into
// h.remoteSessions goes through the same guarantee.
//
// It is a pure function so the reconciliation logic is unit-testable without
// SSH or the Bubble Tea event loop.
func mergeRemoteSessions(prev, fetched map[string][]session.RemoteSessionInfo, failed map[string]bool) map[string][]session.RemoteSessionInfo {
	merged := make(map[string][]session.RemoteSessionInfo, len(fetched)+len(failed))
	for name, sess := range fetched {
		merged[name] = dedupeRemoteSessionsByID(sess)
	}
	for name := range failed {
		if _, ok := merged[name]; ok {
			// A successful result for this remote (if any) always wins.
			continue
		}
		if prevSess, ok := prev[name]; ok {
			merged[name] = prevSess
		}
	}
	return merged
}

// dedupeRemoteSessionsByID collapses same-id entries within one remote's
// session slice to exactly one row per id, keeping the LAST occurrence's
// data (the freshest Status/Group the remote sent this round) while leaving
// it at its FIRST position, so the row order the remote listed stays stable
// across a round that happens to repeat an id. Sessions with an empty ID
// never dedupe against each other — an empty id means "no id was reported",
// not "the same unidentified session" — matching how the rest of the
// pipeline already treats RemoteSessionInfo.ID as the join key (buildRemote-
// FlatItems*, remoteHeaderCounts). A slice with no collisions is returned
// unchanged (same backing array), so the common case allocates nothing.
func dedupeRemoteSessionsByID(sess []session.RemoteSessionInfo) []session.RemoteSessionInfo {
	if len(sess) < 2 {
		return sess
	}
	firstAt := make(map[string]int, len(sess)) // id -> first index seen
	lastData := make(map[string]session.RemoteSessionInfo, len(sess))
	hasDup := false
	for i, s := range sess {
		if s.ID == "" {
			continue
		}
		if _, seen := firstAt[s.ID]; !seen {
			firstAt[s.ID] = i
		} else {
			hasDup = true
		}
		lastData[s.ID] = s
	}
	if !hasDup {
		return sess
	}
	out := make([]session.RemoteSessionInfo, 0, len(sess))
	for i, s := range sess {
		if s.ID == "" {
			out = append(out, s)
			continue
		}
		if firstAt[s.ID] != i {
			continue // a later occurrence's data already replaced this id's kept row
		}
		out = append(out, lastData[s.ID])
	}
	return out
}

// shouldFetchRemoteSessions reports whether the periodic tick should kick off
// a remote-session re-fetch: the configured interval has elapsed since the
// last fetch and no fetch is currently in flight. Issue #1170.
func (h *Home) shouldFetchRemoteSessions(now time.Time) bool {
	interval := h.remoteSessionRefreshSec
	if interval <= 0 {
		interval = session.DefaultRemoteSessionRefreshSecs
	}
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	return !h.remotesFetchActive && now.Sub(h.lastRemoteFetch) >= time.Duration(interval)*time.Second
}

// measureRemoteLatencies measures round-trip latency to every configured
// remote in parallel, returning a map keyed by remote name. Failed
// measurements are recorded as Offline=true so the header can show
// `— offline` instead of a misleading stale ms value. Issue #1103.
func (h *Home) measureRemoteLatencies() tea.Msg {
	config, err := session.LoadUserConfig()
	if err != nil || config == nil || len(config.Remotes) == 0 {
		return remoteLatenciesFetchedMsg{latencies: nil}
	}

	results := make(map[string]session.RemoteLatency, len(config.Remotes))
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Bound total budget: each individual MeasureLatency has its own 5s
	// timeout, but we also cap the outer batch so we never starve the
	// tick that triggered us.
	ctx, cancel := context.WithTimeout(h.ctx, 8*time.Second)
	defer cancel()

	for name, rc := range config.Remotes {
		if h.remoteAuthBlocked(name, rc) {
			continue
		}
		wg.Add(1)
		go func(name string, rc session.RemoteConfig) {
			defer wg.Done()
			runner := session.NewSSHRunner(name, rc)
			d, err := runner.MeasureLatency(ctx)
			lat := session.RemoteLatency{MeasuredAt: time.Now()}
			if err != nil {
				lat.Offline = true
			} else {
				ms := int(d.Milliseconds())
				if ms < 0 {
					ms = 0
				}
				lat.MS = ms
			}
			mu.Lock()
			results[name] = lat
			mu.Unlock()
		}(name, rc)
	}
	wg.Wait()

	return remoteLatenciesFetchedMsg{latencies: results}
}

// stampRemoteFetch marks a remote dialog fetch's answer with the generation
// of the opening that asked, so the apply step can tell a late answer for a
// previous opening from the current one. A nil cmd stays nil.
func stampRemoteFetch(gen uint64, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		switch fetched := cmd().(type) {
		case remoteCreationCatalogFetchedMsg:
			fetched.gen = gen
			return fetched
		default:
			return fetched
		}
	}
}

// remoteRunnerFor builds the SSH runner for a configured remote, or explains
// why it cannot.
func remoteRunnerFor(remoteName string) (*session.SSHRunner, error) {
	config, err := session.LoadUserConfig()
	if err != nil || config == nil || config.Remotes == nil {
		return nil, fmt.Errorf("failed to load remote config")
	}
	rc, ok := config.Remotes[remoteName]
	if !ok {
		return nil, fmt.Errorf("remote '%s' not found", remoteName)
	}
	return session.NewSSHRunner(remoteName, rc), nil
}

func (h *Home) remotePathSuggestions(remoteName string) []string {
	h.remoteSessionsMu.RLock()
	sessions := append([]session.RemoteSessionInfo(nil), h.remoteSessions[remoteName]...)
	h.remoteSessionsMu.RUnlock()

	seen := make(map[string]struct{}, len(sessions))
	paths := make([]string, 0, len(sessions))
	for _, rs := range sessions {
		path := strings.TrimSpace(rs.Path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// moveRemoteItem handles shift+up/down (and the +/-/K/J aliases) on a remote
// row (#1875). delta is -1 for up and +1 for down.
//
// The move rewrites this bucket's entry in the local order overlay and saves
// it; nothing is sent to the remote. Every path through here either changes
// the order or reports why it could not, because a silent no-op is the bug
// being fixed — a remote row that swallows the keystroke is indistinguishable
// from a stuck key.
//
// Remote GROUP headers are different: their order lives in the remote's own
// state DB, so the move is forwarded as `group reorder` and the header only
// moves once the remote confirms (see reorderRemoteGroup); the returned
// command carries that round trip. Session moves return nil.
func (h *Home) moveRemoteItem(item session.Item, delta int) tea.Cmd {
	direction := "up"
	edge := "first"
	if delta > 0 {
		direction = "down"
		edge = "last"
	}

	if item.Type == session.ItemTypeRemoteGroup {
		return h.reorderRemoteGroup(item, delta)
	}
	if item.RemoteSession == nil || item.RemoteName == "" {
		h.setError(fmt.Errorf("cannot move %s: this remote session row is malformed", direction))
		return nil
	}
	moved := item.RemoteSession
	if moved.ID == "" {
		h.setError(fmt.Errorf("cannot move '%s' %s: %s did not report an id for it", moved.Title, direction, item.RemoteName))
		return nil
	}

	// The bucket is the one buildRemoteFlatItems put this row in: same remote,
	// same normalized group path.
	groupPath := normalizeRemoteGroupPath(moved.Group)

	h.remoteSessionsMu.RLock()
	fetched := h.remoteSessions[item.RemoteName]
	natural := make([]string, 0, len(fetched))
	for i := range fetched {
		if normalizeRemoteGroupPath(fetched[i].Group) == groupPath {
			natural = append(natural, fetched[i].ID)
		}
	}
	h.remoteSessionsMu.RUnlock()

	// Empty IDs cannot be represented in the persisted order overlay. A swap
	// across one would be discarded by applyRemoteSessionOrder on the immediate
	// rebuild, making a successful-looking move a silent no-op.
	for _, id := range natural {
		if id == "" {
			h.setError(fmt.Errorf("cannot move '%s' %s: %s lists a session without an id in this group, so its order cannot be tracked", moved.Title, direction, item.RemoteName))
			return nil
		}
	}

	// A bucket whose IDs are not unique cannot be reordered at all:
	// orderRemoteBucket refuses to permute it, because the slot mapping is no
	// longer 1:1 and a row could be dropped or doubled. Storing an order for it
	// would change nothing on screen — a no-op dressed as success, which is the
	// bug this issue is about. It also makes the row itself ambiguous: two rows
	// answering to the same ID cannot be told apart. Report it instead.
	if dup, ok := firstDuplicateID(natural); ok {
		h.setError(fmt.Errorf("cannot move '%s' %s: %s lists more than one session with id %q in this group, so their order cannot be tracked", moved.Title, direction, item.RemoteName, dup))
		return nil
	}

	// Start from what is actually on screen — the fetched list with the
	// current overlay already applied — so a move is always relative to what
	// the user sees, whatever has drifted since the overlay was written.
	current := applyRemoteSessionOrder(natural, h.remoteSessionOrder.forRemote(item.RemoteName)[groupPath])
	pos := -1
	for i, id := range current {
		if id == moved.ID {
			pos = i
			break
		}
	}
	if pos < 0 {
		h.setError(fmt.Errorf("cannot move '%s' %s: it is no longer listed on %s", moved.Title, direction, item.RemoteName))
		return nil
	}

	// #2450: a conductor's children render under it, so a row swaps with its
	// nearest on-screen sibling: a top-level row with the next top-level row
	// (a parent's children travel with it), a child with the next child of
	// the same parent. Swapping with whatever the bucket lists next to it
	// would often leave the screen unchanged.
	targetID, ok := h.adjacentRemoteSibling(item, delta)
	if !ok {
		scope := "in its group"
		if item.IsSubSession {
			scope = "under its parent"
		}
		h.setError(fmt.Errorf("'%s' is already %s %s on %s", moved.Title, edge, scope, item.RemoteName))
		return nil
	}
	target := -1
	for i, id := range current {
		if id == targetID {
			target = i
			break
		}
	}
	if target < 0 {
		h.setError(fmt.Errorf("cannot move '%s' %s: the row next to it is no longer listed on %s", moved.Title, direction, item.RemoteName))
		return nil
	}
	current[pos], current[target] = current[target], current[pos]

	// current lists exactly the sessions present in this bucket right now, so
	// storing it whole keeps the overlay complete and the next move stable.
	if h.remoteSessionOrder == nil {
		h.remoteSessionOrder = make(remoteOrder)
	}
	h.remoteSessionOrder.set(item.RemoteName, groupPath, current)

	h.clearError()
	h.rebuildFlatItemsPreservingSelection(h.captureSelectedItemIdentity())

	// A move that cannot be written is a move that will be gone at the next
	// launch. Say so: the rows visibly moved, so staying quiet here would be
	// the same silent lie this issue is about, one layer down.
	if err := h.saveUIStateErr(); err != nil {
		h.setError(fmt.Errorf("moved '%s' %s, but the order could not be saved and will not survive a restart: %w", moved.Title, direction, err))
	}
	return nil
}

// adjacentRemoteSibling returns the ID of the nearest remote session row on
// screen, in direction delta, that shares the moved row's group bucket and
// nesting level: another top-level row for a top-level row, another child of
// the same parent for a child (#2450). ok is false when there is none, i.e.
// the row is already first or last among its siblings.
func (h *Home) adjacentRemoteSibling(item session.Item, delta int) (id string, ok bool) {
	siblingKey := func(it session.Item) string {
		if it.IsSubSession && it.RemoteSession != nil {
			return it.RemoteSession.ParentSessionID
		}
		return ""
	}
	sameRow := func(it session.Item) bool {
		return it.Type == session.ItemTypeRemoteSession && it.RemoteSession != nil &&
			it.RemoteName == item.RemoteName && it.Path == item.Path
	}
	from := -1
	for i, it := range h.flatItems {
		if sameRow(it) && it.RemoteSession.ID == item.RemoteSession.ID {
			from = i
			break
		}
	}
	if from < 0 {
		return "", false
	}
	key := siblingKey(h.flatItems[from])
	for i := from + delta; i >= 0 && i < len(h.flatItems); i += delta {
		it := h.flatItems[i]
		if !sameRow(it) {
			return "", false // left the bucket: a header or another group
		}
		if siblingKey(it) == key {
			return it.RemoteSession.ID, true
		}
	}
	return "", false
}

// reorderRemoteGroup forwards shift+up/down on a remote group header to the
// remote as `group reorder <path> --up|--down`, the same command the remote's
// own TUI runs for the same keystroke, so the order is persisted in the
// remote's state DB and every viewer of that remote sees it. The level-0 host
// header is not a remote group: remotes are listed in config order.
//
// The edge check runs locally first, against the sibling headers actually on
// screen, so a group that is already first or last gets an immediate answer
// without an SSH round trip. The remote's own verdict still decides the rest
// (see remoteGroupReorderResultMsg).
func (h *Home) reorderRemoteGroup(item session.Item, delta int) tea.Cmd {
	direction := "up"
	edge := "first"
	if delta > 0 {
		direction = "down"
		edge = "last"
	}
	groupPath := remoteGroupPathFromItem(item)
	if groupPath == "" || item.RemoteName == "" {
		h.setError(fmt.Errorf("cannot move %s: remote hosts are listed in the order of [remotes] in config.toml", direction))
		return nil
	}

	siblings := h.visibleRemoteGroupSiblings(item.RemoteName, groupPath)
	pos := -1
	for i, p := range siblings {
		if p == groupPath {
			pos = i
			break
		}
	}
	target := pos + delta
	if pos >= 0 && (target < 0 || target >= len(siblings)) {
		h.setError(fmt.Errorf("group '%s' is already %s among its siblings on %s", groupPath, edge, item.RemoteName))
		return nil
	}

	remoteName := item.RemoteName
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return remoteGroupReorderResultMsg{remoteName: remoteName, groupPath: groupPath, delta: delta,
				err: fmt.Errorf("cannot move '%s' %s: failed to load remotes config: %v", groupPath, direction, err)}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteGroupReorderResultMsg{remoteName: remoteName, groupPath: groupPath, delta: delta,
				err: fmt.Errorf("cannot move '%s' %s: remote '%s' is not configured", groupPath, direction, remoteName)}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		moved, err := runner.ReorderGroup(ctx, groupPath, delta)
		if err != nil {
			return remoteGroupReorderResultMsg{remoteName: remoteName, groupPath: groupPath, delta: delta,
				err: fmt.Errorf("failed to move '%s' %s on %s: %w", groupPath, direction, remoteName, err)}
		}
		return remoteGroupReorderResultMsg{remoteName: remoteName, groupPath: groupPath, delta: delta, moved: moved}
	}
}

// visibleRemoteGroupSiblings lists the group headers currently on screen for
// one remote that share groupPath's parent, in screen order.
func (h *Home) visibleRemoteGroupSiblings(remoteName, groupPath string) []string {
	parent := ""
	if i := strings.LastIndex(groupPath, "/"); i >= 0 {
		parent = groupPath[:i]
	}
	var siblings []string
	for _, it := range h.flatItems {
		if it.Type != session.ItemTypeRemoteGroup || it.RemoteName != remoteName {
			continue
		}
		p := remoteGroupPathFromItem(it)
		if p == "" {
			continue
		}
		pp := ""
		if i := strings.LastIndex(p, "/"); i >= 0 {
			pp = p[:i]
		}
		if pp == parent {
			siblings = append(siblings, p)
		}
	}
	return siblings
}

// swapRemoteGroupSibling returns paths with groupPath exchanged against its
// nearest sibling (same parent) in the given direction, mirroring what
// GroupTree.MoveGroupUp/Down did on the remote. A path that is missing or has
// no sibling in that direction leaves the list unchanged.
func swapRemoteGroupSibling(paths []string, groupPath string, delta int) []string {
	out := append([]string(nil), paths...)
	parentOf := func(p string) string {
		if i := strings.LastIndex(p, "/"); i >= 0 {
			return p[:i]
		}
		return ""
	}
	pos := -1
	for i, p := range out {
		if p == groupPath {
			pos = i
			break
		}
	}
	if pos < 0 {
		return out
	}
	parent := parentOf(groupPath)
	step := 1
	if delta < 0 {
		step = -1
	}
	for j := pos + step; j >= 0 && j < len(out); j += step {
		if parentOf(out[j]) == parent {
			out[pos], out[j] = out[j], out[pos]
			return out
		}
	}
	return out
}

type remoteCreationCatalogFetchedMsg struct {
	remoteName string
	catalog    *session.RemoteCreationCatalog
	err        error
	gen        uint64
}

func (h *Home) fetchRemoteCreationCatalog(remoteName string) tea.Cmd {
	return func() tea.Msg {
		runner, err := remoteRunnerFor(remoteName)
		if err != nil {
			return remoteCreationCatalogFetchedMsg{remoteName: remoteName, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		catalog, err := runner.FetchCreationCatalog(ctx)
		return remoteCreationCatalogFetchedMsg{remoteName: remoteName, catalog: catalog, err: err}
	}
}
