package ui

import (
	"log/slog"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/menudata"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// refreshCostTotals updates cached cost totals from the store.
// Throttled to run at most every 10 seconds.
func (h *Home) refreshCostTotals() {
	if h.costStore == nil {
		return
	}
	if time.Since(h.costRefreshTime) < 10*time.Second {
		return
	}
	h.costRefreshTime = time.Now()
	today, _ := h.costStore.TotalToday()
	yesterday, _ := h.costStore.TotalYesterday()
	week, _ := h.costStore.TotalThisWeek()
	lastWeek, _ := h.costStore.TotalLastWeek()
	thisMonth, _ := h.costStore.TotalThisMonth()
	lastMonth, _ := h.costStore.TotalLastMonth()
	projected, _ := h.costStore.ProjectedMonthly()
	h.costToday.Store(today.TotalCostMicrodollars)
	h.costYesterday.Store(yesterday.TotalCostMicrodollars)
	h.costWeek.Store(week.TotalCostMicrodollars)
	h.costLastWeek.Store(lastWeek.TotalCostMicrodollars)
	h.costThisMonth.Store(thisMonth.TotalCostMicrodollars)
	h.costLastMonth.Store(lastMonth.TotalCostMicrodollars)
	h.costProjected.Store(projected)
}

func (h *Home) publishWebMenuSnapshot() {
	menuData := h.getWebMenuData()
	if menuData == nil || h.groupTree == nil {
		return
	}

	// Archived sessions live in h.instances — the TUI drops them at render time
	// in rebuildFlatItems, not at load time. The web snapshot is built from
	// h.instances instead of h.flatItems, so it must apply the archive filter
	// itself or archived sessions surface as ordinary sidebar rows. This mirrors
	// SessionDataService.LoadMenuSnapshot, the storage-backed loader that serves
	// headless mode. The archived view is fed separately by
	// LoadArchivedMenuSnapshot, so nothing here needs to preserve them.
	//
	// FilterInstancesByArchive allocates the copy this function needs anyway, so
	// it replaces the previous make+copy rather than adding to it — same one
	// allocation per publish, and the archive read happens under the same lock
	// that guards the slice.
	h.instancesMu.RLock()
	instancesCopy := session.FilterInstancesByArchive(h.instances, false)
	h.instancesMu.RUnlock()

	groupTreeCopy := h.groupTree.ShallowCopyForSave()
	groupsData := make([]*session.GroupData, 0, len(groupTreeCopy.GroupList))
	for _, g := range groupTreeCopy.GroupList {
		if g == nil {
			continue
		}
		groupsData = append(groupsData, &session.GroupData{
			Name:        g.Name,
			Path:        g.Path,
			Expanded:    g.Expanded,
			Order:       g.Order,
			DefaultPath: g.DefaultPath,
		})
	}

	menuData.SetSnapshot(menudata.BuildMenuSnapshot(h.profile, instancesCopy, groupsData, time.Now()))
}

func (h *Home) publishWebSessionStates(instances []*session.Instance) {
	menuData := h.getWebMenuData()
	if menuData == nil || len(instances) == 0 {
		return
	}

	states := make(map[string]menudata.MenuSessionState, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		states[inst.ID] = menudata.MenuSessionState{
			Status: inst.GetStatusThreadSafe(),
			Tool:   inst.GetToolThreadSafe(),
		}
	}
	menuData.UpdateSessionStates(states, time.Now())
}

// recordFocusedSession snapshots the cursor-selected session name for the
// reconciler goroutine. Must run on the main (Update) goroutine — getSelected-
// Session reads h.cursor/h.flatItems without a lock.
func (h *Home) recordFocusedSession() {
	name := ""
	if s := h.getSelectedSession(); s != nil {
		if ts := s.GetTmuxSession(); ts != nil {
			name = ts.TmuxName()
		}
	}
	h.focusMu.Lock()
	h.focusedSessionName = name
	h.focusMu.Unlock()
}

// reconcileLivePipes refreshes the live set from the current focus + the set of
// attached sessions, then syncs the PipeManager's pipes to match.
//
// The desired set is resolved against the current instances (name -> socket),
// which makes three things correct at once:
//   - each session connects on its REAL socket, not a guessed default;
//   - names with no live instance (deleted/restarted sessions) are dropped
//     instead of being retried on every tick;
//   - attached sessions are pinned across every socket passed in, so an
//     attached session on an isolated socket keeps its live pipe rather than
//     being evicted to the 2s status poll. GetAttachedSessionsOnSockets
//     (tmux.go) always scans the default socket in addition to whatever is
//     passed here; the claim-polling filter above only limits which EXTRA
//     (non-default) sockets get scanned, not the default one.
func (h *Home) reconcileLivePipes() {
	pm := tmux.GetPipeManager()
	if pm == nil {
		return
	}

	// Snapshot live instances: session name -> socket (the source of truth).
	// nameToID feeds only the claim-polling ownership filter below; don't
	// build it on every 500ms tick when the flag is off.
	h.instancesMu.RLock()
	socketByName := make(map[string]string, len(h.instances))
	var nameToID map[string]string
	if h.claimPolling {
		nameToID = make(map[string]string, len(h.instances))
	}
	sockets := make([]string, 0, len(h.instances))
	socketSeen := make(map[string]bool, len(h.instances))
	for _, inst := range h.instances {
		if ts := inst.GetTmuxSession(); ts != nil {
			socketByName[ts.TmuxName()] = inst.TmuxSocketName
			if nameToID != nil {
				nameToID[ts.TmuxName()] = inst.ID
			}
			if !socketSeen[inst.TmuxSocketName] {
				socketSeen[inst.TmuxSocketName] = true
				sockets = append(sockets, inst.TmuxSocketName)
			}
		}
	}
	h.instancesMu.RUnlock()

	h.focusMu.Lock()
	focused := h.focusedSessionName
	h.focusMu.Unlock()

	h.ownedMu.RLock()
	ownedSnapshot := h.ownedSessions
	h.ownedMu.RUnlock()
	socketByName = filterPipeCandidates(socketByName, nameToID, ownedSnapshot, focused, h.claimPolling)
	if h.claimPolling {
		// Rebuild the socket list from the filtered candidates so the attached
		// scan does not touch other instances' sockets. With claim polling off,
		// filterPipeCandidates is a no-op and sockets already matches
		// socketByName, so skip the redundant O(n) rebuild every tick.
		sockets = sockets[:0]
		socketSeen = map[string]bool{}
		for _, s := range socketByName {
			if !socketSeen[s] {
				socketSeen[s] = true
				sockets = append(sockets, s)
			}
		}
	}

	attached := tmux.GetAttachedSessionsOnSockets(sockets...)
	desired := desiredLivePipes(h.liveSet, focused, attached, socketByName)
	reconcilePipes(pm, desired, func(name string) string { return socketByName[name] })
}

// livePipeReconciler periodically reconciles live pipes. The tick interval
// doubles as the focus debounce. Runs until h.ctx is cancelled (TUI exit).
func (h *Home) livePipeReconciler() {
	ticker := time.NewTicker(livePipeReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.reconcileLivePipes()
		}
	}
}

// NOTE: updateTmuxNotifications (foreground) was removed in v0.9.2 as a CPU optimization.
// Status bar updates and key binding updates are handled by syncNotificationsBackground().

// cleanupNotifications removes all notification bar state on exit
func (h *Home) cleanupNotifications() {
	// Always unbind status-right mouse click (bound unconditionally at init)
	tmux.UnbindMouseStatusClicks()

	if !h.manageTmuxNotifications || !h.notificationsEnabled || h.notificationManager == nil {
		return
	}

	// Clear global status bar (ONE call instead of per-session)
	_ = tmux.ClearStatusLeftGlobal()

	// Unbind all keys (with mutex protection)
	h.boundKeysMu.Lock()
	for key := range h.boundKeys {
		_ = tmux.UnbindKey(key)
	}
	h.boundKeys = make(map[string]string)
	h.boundKeysMu.Unlock()
}

type sessionRenderState struct {
	status    session.Status
	substate  session.Substate // Honest Status v2: additive refinement (model-unavailable, auth-401, ...)
	tool      string
	paneTitle string // Current task description from tmux pane title (stripped of spinner/done markers)
	// Row-label fields (#1753): the per-row render path used to read these
	// through Instance getters, each an Instance.mu RLock. A background
	// UpdateStatus holds that mutex as a WRITER across tmux subprocess calls
	// (Exists probe 2s cap, DetectTool capture 3s cap), and Go's RWMutex
	// queues new readers behind a waiting writer — so one mid-sweep session
	// could stall View() for seconds, scaling with visible row count. That is
	// the residual "Ctrl+Q → black screen" on large decks with a big group
	// expanded. Carrying the labels in the snapshot makes row rendering
	// lock-free.
	account        string // Stored slot; empty means inherited, not a login identity.
	accountDisplay accountPresentation
	title          string // Instance.Title at snapshot time
	autoName       bool   // session may show a captured/live task description as a suffix
	autoNameDesc   string // last persisted auto-name description (fallback when paneTitle empty)
	// archived is true for any archived session, including an archived
	// cross-harness source superseded by a "Restart with new session ID"
	// target. The header pill skips these so it matches `agent-deck status`
	// and the web menu, which both drop archived sessions. Zero value (false)
	// means "counted": callers that build a fake snapshot without touching
	// this field (many tests do) count everything.
	archived bool
}

// shouldPersistAutoNameDesc decides whether the background status loop should
// write a new task description for an auto-named session, given the live
// (already-cleaned) pane title and the value last persisted for it. It returns
// the description to write and whether to write at all. Kept as a pure function
// so the capture branching is testable without a live DB/tmux: the loop itself
// only runs on a background tick. Rules: only auto-named sessions; never write
// an empty pane (idle/just-started must not clobber a saved description); skip
// when unchanged to avoid SQLite write pressure (mirrors the status loop).
func shouldPersistAutoNameDesc(autoName bool, paneTitle, lastPersisted string) (string, bool) {
	if !autoName || paneTitle == "" || paneTitle == lastPersisted {
		return "", false
	}
	return paneTitle, true
}

// displaySessionTitle keeps the session name as the primary label.
func displaySessionTitle(inst *session.Instance, _ string) string {
	return inst.Title
}

// displaySessionTitleFromState is displaySessionTitle computed purely from a
// sessionRenderState — no Instance.mu access, so it can never block behind a
// background UpdateStatus writer (#1753; see the field comments on
// sessionRenderState). The overview row renderer must use this form.
func displaySessionTitleFromState(state sessionRenderState) string {
	return state.title
}

// sessionDisplayLabels keeps the session name first, with a useful pane or
// saved task description as an optional dim suffix. Both overview and switcher
// use this ordering.
func sessionDisplayLabels(inst *session.Instance, paneTitle string) (title, subtitle string) {
	title = displaySessionTitle(inst, paneTitle)
	subtitle = paneTitle
	if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil && subtitle == tmuxSess.TmuxName() {
		subtitle = ""
	}
	if inst.GetAutoName() && subtitle == "" {
		subtitle = cleanPaneTitle(inst.GetAutoNameDescription())
	}
	return title, subtitle
}

// sessionDisplayLabelsFromState is the lock-free form of sessionDisplayLabels,
// reading everything from the render snapshot (#1753). Auto-named sessions
// show a live or saved task description after the name.
func sessionDisplayLabelsFromState(state sessionRenderState) (title, subtitle string) {
	title = displaySessionTitleFromState(state)
	subtitle = state.paneTitle
	if state.autoName && subtitle == "" {
		subtitle = cleanPaneTitle(state.autoNameDesc)
	}
	return title, subtitle
}

// cleanPaneTitle strips spinner/done marker characters from a tmux pane title
// and returns the task description. Returns "" for default/generic titles.
func cleanPaneTitle(title string) string {
	if title == "" {
		return ""
	}
	// Strip known spinner/done markers, plus any Braille chars (U+2800-28FF)
	// that Claude Code may use as spinner frames beyond the canonical set.
	cleaned := tmux.StripSpinnerRunes(title)
	cleaned = strings.TrimLeftFunc(cleaned, func(r rune) bool {
		return r >= 0x2800 && r <= 0x28FF
	})
	cleaned = strings.TrimSpace(cleaned)
	switch cleaned {
	case "", "Claude Code", "Gemini CLI", "Codex CLI", "bash", "zsh", "fish", "sh":
		return ""
	}
	if host, err := os.Hostname(); err == nil && strings.EqualFold(cleaned, host) {
		return ""
	}
	return cleaned
}

func (h *Home) getSessionRenderSnapshot() map[string]sessionRenderState {
	if snap := h.sessionRenderSnapshot.Load(); snap != nil {
		if typed, ok := snap.(map[string]sessionRenderState); ok {
			return typed
		}
	}
	return nil
}

// setAccountSlotsConfigured updates only inherited account presentations when
// settings cross the configured/unconfigured boundary. Copy the snapshot so
// active renderers keep an immutable view, without rereading session state.
func (h *Home) setAccountSlotsConfigured(configured bool) {
	h.sessionRenderSnapshotMu.Lock()
	defer h.sessionRenderSnapshotMu.Unlock()
	if h.accountSlotsConfigured.Swap(configured) == configured {
		return
	}
	previous := h.getSessionRenderSnapshot()
	if len(previous) == 0 {
		return
	}
	snap := make(map[string]sessionRenderState, len(previous))
	display := newAccountPresentation("", configured)
	for id, state := range previous {
		if state.account == "" {
			state.accountDisplay = display
		}
		snap[id] = state
	}
	h.sessionRenderSnapshot.Store(snap)
}

// publishSessionRenderSnapshot takes ownership of snap. Resolve presentations
// at publication so an in-flight refresh cannot restore an old settings gate.
// Instance reads happen before this lock, keeping settings updates bounded to
// cached render data even when a background status writer holds Instance.mu.
func (h *Home) publishSessionRenderSnapshot(snap map[string]sessionRenderState) {
	h.sessionRenderSnapshotMu.Lock()
	defer h.sessionRenderSnapshotMu.Unlock()
	accounts := make(map[string]accountPresentation)
	slotsConfigured := h.accountSlotsConfigured.Load()
	for id, state := range snap {
		display, ok := accounts[state.account]
		if !ok {
			display = newAccountPresentation(state.account, slotsConfigured)
			accounts[state.account] = display
		}
		state.accountDisplay = display
		snap[id] = state
	}
	h.sessionRenderSnapshot.Store(snap)
}

func (h *Home) refreshSessionRenderSnapshot(instances []*session.Instance) {
	if instances == nil {
		h.instancesMu.RLock()
		instances = make([]*session.Instance, len(h.instances))
		copy(instances, h.instances)
		h.instancesMu.RUnlock()
	}

	snap := make(map[string]sessionRenderState, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		state := sessionRenderState{
			status:   inst.GetStatusThreadSafe(),
			substate: inst.CachedSubstate(),
			tool:     inst.DisplayToolThreadSafe(),
			archived: inst.IsArchived(),
			// Label fields: read here, on the refresher's goroutine, so the
			// render path never takes Instance.mu per row (#1753). Title goes
			// through GetTitleThreadSafe because SetField/ReconcileTitleFromClaude/
			// pending-title reapply can mutate it concurrently from the Bubble
			// Tea event-loop goroutine.
			account:      inst.GetAccountThreadSafe(),
			title:        inst.GetTitleThreadSafe(),
			autoName:     inst.GetAutoName(),
			autoNameDesc: inst.GetAutoNameDescription(),
		}
		// Look up pane title from the already-refreshed tmux cache.
		// Only RefreshPaneInfoCache (called from backgroundStatusUpdate) keeps
		// the cache fresh; processStatusUpdate and other rebuild paths run on
		// their own cadence. When that cache crosses the 4-second freshness
		// threshold (GetCachedPaneInfo returns ok=false), keep the previous
		// snapshot's paneTitle so the inline suffix in renderSessionItem does
		// not blink to empty between successful refreshes — the user would
		// otherwise read the disappearance as "title only updated once."
		// Reading the latest snapshot inside the per-instance branch (rather
		// than once before the loop) narrows the read-store race window: if a
		// concurrent rebuild lands a fresher value while we're walking the
		// instances slice, the fallback uses that value instead of stamping
		// an even-older one back into the snapshot.
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
			if paneInfo, ok := tmux.GetCachedPaneInfo(tmuxSess.TmuxName()); ok {
				state.paneTitle = cleanPaneTitle(paneInfo.Title)
				if state.paneTitle == tmuxSess.TmuxName() {
					state.paneTitle = ""
				}
			} else if prev := h.getSessionRenderSnapshot(); prev != nil {
				if prevState, hadPrev := prev[inst.ID]; hadPrev {
					state.paneTitle = prevState.paneTitle
				}
			}
		}
		snap[inst.ID] = state
	}
	h.publishSessionRenderSnapshot(snap)
}

func (h *Home) getSessionRenderState(inst *session.Instance) sessionRenderState {
	if inst == nil {
		return sessionRenderState{}
	}
	if snap := h.getSessionRenderSnapshot(); snap != nil {
		if state, ok := snap[inst.ID]; ok {
			return state
		}
	}
	// Fallback for newly-added sessions before snapshot refresh. This path DOES
	// take Instance.mu (briefly, as a reader); it is bounded to sessions that a
	// snapshot refresh has not seen yet, never the steady-state whole list.
	account := inst.GetAccountThreadSafe()
	return sessionRenderState{
		status:         inst.GetStatusThreadSafe(),
		tool:           inst.DisplayToolThreadSafe(),
		account:        account,
		accountDisplay: newAccountPresentation(account, h.accountSlotsConfigured.Load()),
		title:          inst.GetTitleThreadSafe(),
		autoName:       inst.GetAutoName(),
		autoNameDesc:   inst.GetAutoNameDescription(),
	}
}

// markNavigationActivity records a short "hot" window where background workers
// should avoid heavy refresh work to keep key navigation responsive.
func (h *Home) markNavigationActivity() {
	now := time.Now()
	h.lastNavigationTime = now
	h.isNavigating = true
	h.navigationHotUntil.Store(now.Add(900 * time.Millisecond).UnixNano())
}

func (h *Home) shouldSuppressPreviewRefresh(now time.Time) bool {
	return !h.lastAttachReturn.IsZero() && now.Sub(h.lastAttachReturn) < attachReturnPreviewGrace
}

// getInstanceByID returns the instance with the given ID using O(1) map lookup
// Returns nil if not found. Caller must hold instancesMu if accessing from background goroutine.
func (h *Home) getInstanceByID(id string) *session.Instance {
	return h.instanceByID[id]
}

// pushUndoStack adds a deleted session to the undo stack (LIFO, capped at 10).
// An entry pushed off the stack can no longer be undone: its delete is final,
// so the deferred inbox and notify-state sweep (#910) runs for it then, in
// the returned command (nil when nothing was evicted).
func (h *Home) pushUndoStack(inst *session.Instance) tea.Cmd {
	// The row is deleted: an undo must re-insert it, not update it.
	inst.ForgetStorageSnapshot()
	entry := deletedSessionEntry{
		instance:  inst,
		deletedAt: time.Now(),
	}
	h.undoMu.Lock()
	h.undoStack = append(h.undoStack, entry)
	var evicted []deletedSessionEntry
	if len(h.undoStack) > 10 {
		evicted = append(evicted, h.undoStack[:len(h.undoStack)-10]...)
		h.undoStack = append([]deletedSessionEntry(nil), h.undoStack[len(h.undoStack)-10:]...)
	}
	h.undoMu.Unlock()
	return h.finalizeDeletesCmd(undoEntryIDs(evicted))
}

// undoEntryIDs returns the session ids of undo entries.
func undoEntryIDs(entries []deletedSessionEntry) []string {
	var ids []string
	for _, e := range entries {
		if e.instance != nil && e.instance.ID != "" {
			ids = append(ids, e.instance.ID)
		}
	}
	return ids
}

// finalizeDeletesCmd runs the deferred inbox and notify-state sweep (#910)
// for TUI deletes that can no longer be undone, off the Update loop. Nil
// when ids is empty.
func (h *Home) finalizeDeletesCmd(ids []string) tea.Cmd {
	if len(ids) == 0 {
		return nil
	}
	storage := h.storage
	return func() tea.Msg {
		finalizeRemovedSessions(storage, "delete", ids)
		return nil
	}
}

// clearPendingSweepCmd drops id's pending delete-sweep record after a
// successful undo.
func (h *Home) clearPendingSweepCmd(id string) tea.Cmd {
	storage := h.storage
	return func() tea.Msg {
		if err := storage.ClearPendingDeleteSweep(id); err != nil {
			uiLog.Warn("clear_pending_sweep_err", slog.String("id", id), slog.String("err", err.Error()))
		}
		return nil
	}
}

// finalizeRemovedSessionsFn is finalizeRemovedSessions (a seam for tests).
var finalizeRemovedSessionsFn = finalizeRemovedSessions

// finalizeRemovedSessions sweeps ids (see session.FinalizeRemovedSessions)
// and logs the outcome.
func finalizeRemovedSessions(storage *session.Storage, op string, ids []string) {
	results, err := storage.FinalizeRemovedSessions(ids)
	if err != nil {
		uiLog.Warn(op+"_finalize_sweep_skipped", slog.Int("count", len(ids)), slog.String("err", err.Error()))
	}
	for id, r := range results {
		logRemovedSessionCleanup(op, id, r)
	}
}

// takeUndoStack empties the undo stack and returns its entries.
func (h *Home) takeUndoStack() []deletedSessionEntry {
	h.undoMu.Lock()
	defer h.undoMu.Unlock()
	entries := h.undoStack
	h.undoStack = nil
	return entries
}

// snapshotUndoStack copies the undo stack without changing it, so a reader
// off the Update goroutine never writes state that goroutine reads unlocked.
func (h *Home) snapshotUndoStack() []deletedSessionEntry {
	h.undoMu.Lock()
	defer h.undoMu.Unlock()
	return append([]deletedSessionEntry(nil), h.undoStack...)
}

// FinalizeUndoableDeletes ends every delete still on the TUI undo stack when
// a signal ends the process: they can no longer be undone, so their deferred
// sweep runs now. Bounded by timeout; whatever is not swept by then keeps its
// pending record for the next start's reconcile. Safe from any goroutine
// (the stack is only read; the sweep is idempotent).
func (h *Home) FinalizeUndoableDeletes(timeout time.Duration) {
	ids := undoEntryIDs(h.snapshotUndoStack())
	if len(ids) == 0 {
		return
	}
	runBounded(timeout, func() { finalizeRemovedSessions(h.storage, "delete", ids) })
}

// runBounded runs fn and waits at most timeout for it.
func runBounded(timeout time.Duration, fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		uiLog.Warn("finalize_sweep_timeout", slog.Duration("timeout", timeout))
	}
}

// ReconcileRemovedSessionSweeps sweeps the inbox and notify-state records of
// deletes an earlier process left unswept (it crashed or was killed while
// they could still be undone). Run once per process, after storage loaded;
// a no-op when storage is unavailable.
func (h *Home) ReconcileRemovedSessionSweeps() {
	if h.storage == nil {
		return
	}
	results, err := h.storage.ReconcileRemovedSessionSweeps()
	if err != nil {
		uiLog.Warn("reconcile_removed_sweep_skipped", slog.String("err", err.Error()))
	}
	for id, r := range results {
		logRemovedSessionCleanup("reconcile", id, r)
	}
	if len(results) > 0 {
		uiLog.Info("reconcile_removed_sweep", slog.Int("swept", len(results)))
	}
}

// nextStatusInterval returns how long to wait before the next status sweep,
// given how long the last sweep took. It returns base when the sweep finished
// within base, otherwise 2×lastSweep capped at max.
func nextStatusInterval(lastSweep, base, ceiling time.Duration) time.Duration {
	if lastSweep <= base {
		return base
	}
	next := 2 * lastSweep
	if next > ceiling {
		return ceiling
	}
	return next
}

// statusWorker runs in a background goroutine with its own timer
// This ensures status updates continue even when TUI is paused (tea.Exec)
func (h *Home) statusWorker() {
	defer close(h.statusWorkerDone)

	// Internal timer - independent of Bubble Tea event loop
	// This is the key insight: when tea.Exec suspends the TUI (user attaches to session),
	// the Bubble Tea tick messages stop firing, but this goroutine keeps running.
	// A timer (reset after each sweep) rather than a fixed ticker lets the cadence
	// adapt when a sweep overruns the interval (#1366).
	base := statusWorkerInterval(session.LoadUserConfig)
	h.statusWorkerLoop(base, h.backgroundStatusUpdate, h.processStatusUpdate)
}

func statusWorkerInterval(load func() (*session.UserConfig, error)) time.Duration {
	if cfg, err := load(); err == nil {
		return cfg.StatusInterval()
	}
	return baseStatusInterval
}

// Separate callbacks let tests exercise the real worker cadence without touching tmux.
func (h *Home) statusWorkerLoop(base time.Duration, sweep func(), process func(statusUpdateRequest)) {
	timer := time.NewTimer(base)
	defer timer.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return

		case <-timer.C:
			// Self-triggered update - runs even when TUI is paused
			sweepStart := time.Now()
			sweep()
			timer.Reset(nextStatusInterval(time.Since(sweepStart), base, maxStatusInterval))
			// Coalesce a queued immediate request after full sweep.
			select {
			case <-h.statusTrigger:
			default:
			}

		case req := <-h.statusTrigger:
			// Explicit trigger from TUI (for immediate updates)
			// Panic recovery to prevent worker death from killing status updates
			func() {
				defer func() {
					if r := recover(); r != nil {
						statusLog.Error("worker_panic", slog.Any("panic", r))
					}
				}()
				process(req)
			}()
		}
	}
}

// startLogWorkers initializes the log worker pool
func (h *Home) startLogWorkers() {
	// Start 2 workers to handle log-triggered status updates concurrently
	// This is enough to handle bursts without overwhelming the system
	for i := 0; i < 2; i++ {
		h.logWorkerWg.Add(1)
		go h.logWorker()
	}
}

// logWorker processes per-session status updates triggered by PipeManager %output events
func (h *Home) logWorker() {
	defer h.logWorkerWg.Done()
	for {
		select {
		case <-h.ctx.Done():
			return
		case inst := <-h.logUpdateChan:
			if inst == nil {
				continue
			}
			// Panic recovery for worker stability
			func() {
				defer func() {
					if r := recover(); r != nil {
						uiLog.Error("log_worker_panic", slog.Any("panic", r))
					}
				}()
				_ = inst.UpdateStatus()
			}()
		}
	}
}

// shouldPollStatusInLoop reports whether the per-tick background status sweep
// should run UpdateStatus() on inst. Archived sessions are skipped: their tmux
// pane is torn down and their row status is display-frozen, so a poll can only
// spend a serialized tmux subprocess without changing anything the UI renders.
//
// Exception: an archived session that still claims a live status. Archiving a
// session whose tmux is already gone skips Kill(), so it keeps its last stored
// status; skipping it forever would count it as running in the header pills.
// It is polled until UpdateStatus settles it on error/stopped, then skipped.
func shouldPollStatusInLoop(inst *session.Instance) bool {
	if inst == nil {
		return false
	}
	if !inst.IsArchived() {
		return true
	}
	switch inst.GetStatusThreadSafe() {
	case session.StatusRunning, session.StatusWaiting, session.StatusIdle, session.StatusStarting:
		return true
	}
	return false
}

const fullStatusBatchSize = 32

// Why a row is polled ahead of the rotation budget.
type sweepRowReason int

const (
	sweepRowVisible  sweepRowReason = iota + 1 // on screen or focused
	sweepRowEvidence                           // hook/SSE evidence moved since last applied
)

// noteSweepVisibleRows records the on-screen session rows for the periodic
// sweep. Called on the UI goroutine with each status tick request.
func (h *Home) noteSweepVisibleRows(req statusUpdateRequest) {
	visible := make(map[string]bool, req.visibleHeight)
	for i := req.viewOffset; i < len(req.flatItemIDs) && i < req.viewOffset+req.visibleHeight; i++ {
		visible[req.flatItemIDs[i]] = true
	}
	h.sweepVisibleMu.Lock()
	h.sweepVisibleIDs = visible
	h.sweepVisibleMu.Unlock()
}

// sweepPriorityRows selects the rows the periodic sweep must poll on this
// pass regardless of the rotation budget, keyed by index into instances, and
// returns the evidence snapshot the sweep records for every row it polls.
// A row seen for the first time is only recorded: a cold start with hook
// files on disk for hundreds of stopped rows must not turn into one unbounded
// pass. Rows that stop being tracked are forgotten.
func (h *Home) sweepPriorityRows(instances []*session.Instance) (map[int]sweepRowReason, map[string]session.StatusEvidence) {
	h.sweepVisibleMu.Lock()
	visible := h.sweepVisibleIDs
	h.sweepVisibleMu.Unlock()
	h.focusMu.Lock()
	focused := h.focusedSessionName
	h.focusMu.Unlock()
	if h.sweepEvidence == nil {
		h.sweepEvidence = make(map[string]session.StatusEvidence, len(instances))
	}

	forced := make(map[int]sweepRowReason)
	evidence := make(map[string]session.StatusEvidence, len(instances))
	tracked := make(map[string]bool, len(instances))
	for idx, inst := range instances {
		if !h.shouldSweepInstance(inst) {
			continue
		}
		tracked[inst.ID] = true
		ev := inst.StatusEvidence()
		evidence[inst.ID] = ev
		prev, seen := h.sweepEvidence[inst.ID]
		if !seen {
			h.sweepEvidence[inst.ID] = ev
		} else if ev != prev {
			forced[idx] = sweepRowEvidence
			continue
		}
		if visible[inst.ID] {
			forced[idx] = sweepRowVisible
			continue
		}
		if focused != "" {
			if ts := inst.GetTmuxSession(); ts != nil && ts.TmuxName() == focused {
				forced[idx] = sweepRowVisible
			}
		}
	}
	for id := range h.sweepEvidence {
		if !tracked[id] {
			delete(h.sweepEvidence, id)
		}
	}
	return forced, evidence
}

// feedHookStatuses pushes the watcher's latest sample into every instance
// whose tool is hook-driven, enabling the hook fast path in
// Instance.UpdateStatus. The gate is session.HookStatusTool, the single
// registry: #2222 was a hand-written allowlist here that missed pi, so a pi
// instance's hookStatus was set once at cold load and never refreshed.
func (h *Home) feedHookStatuses(instances []*session.Instance) {
	if h.hookWatcher == nil {
		return
	}
	for _, inst := range instances {
		if !session.HookStatusTool(inst.Tool) {
			continue
		}
		if hs := h.hookWatcher.GetHookStatus(inst.ID); hs != nil {
			inst.UpdateHookStatus(hs)
		}
	}
}

// syncNotificationsBackground updates the tmux notification bar directly
// Called from background worker - does NOT depend on Bubble Tea
func (h *Home) syncNotificationsBackground() {
	defer func() {
		if r := recover(); r != nil {
			notifLog.Error("sync_notifications_panic", slog.Any("panic", r))
		}
	}()

	if !h.manageTmuxNotifications || !h.notificationsEnabled || h.notificationManager == nil {
		return
	}

	// Phase 1: Check for signal file from Ctrl+b 1-6 shortcuts
	// CRITICAL: This must be done in background sync too, because the foreground
	// sync might not run when user is attached to a session (tea.Exec pauses TUI)
	var sessionToAcknowledgeID string
	if signalSessionID := tmux.ReadAndClearAckSignal(); signalSessionID != "" {
		sessionToAcknowledgeID = signalSessionID
		notifLog.Debug("signal_found", slog.String("session_id", signalSessionID))

		// Track notification switch during attach for cursor sync on detach
		if h.isAttaching.Load() {
			h.lastNotifSwitchMu.Lock()
			h.lastNotifSwitchID = signalSessionID
			h.lastNotifSwitchMu.Unlock()
			notifLog.Debug("attach_switch_recorded", slog.String("session_id", signalSessionID))
		}
	}

	// Get current instances (copy to avoid race with main goroutine)
	h.instancesMu.RLock()
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)

	// Phase 2: Acknowledge the session if signal was received
	if sessionToAcknowledgeID != "" {
		if inst, ok := h.instanceByID[sessionToAcknowledgeID]; ok {
			if ts := inst.GetTmuxSession(); ts != nil {
				ts.Acknowledge()
				// Persist ack to SQLite so other instances see it
				if db := statedb.GetGlobal(); db != nil {
					_ = db.SetAcknowledged(inst.ID, true)
				}
				_ = inst.UpdateStatus()
				notifLog.Debug(
					"session_acknowledged",
					slog.String("title", inst.Title),
					slog.String("status", string(inst.Status)),
				)
			}
		}
	}
	h.instancesMu.RUnlock()

	// Detect currently attached session (may be the user's session during tea.Exec)
	currentSessionID := h.getAttachedSessionID()

	// Signal file takes priority for determining "current" session
	if sessionToAcknowledgeID != "" {
		currentSessionID = sessionToAcknowledgeID
	}

	notifLog.Debug(
		"sync_state",
		slog.String("current_session_id", currentSessionID),
		slog.Int("instances", len(instances)),
	)

	// Sync notification manager with current states
	h.notificationManager.SyncFromInstances(instances, currentSessionID)

	// Update tmux status bar directly
	barText := h.notificationManager.FormatBar()

	// Only update if changed (avoid unnecessary tmux calls)
	h.lastBarTextMu.Lock()
	if barText != h.lastBarText {
		h.lastBarText = barText
		h.lastBarTextMu.Unlock()

		if barText == "" {
			_ = tmux.ClearStatusLeftGlobal()
		} else {
			_ = tmux.SetStatusLeftGlobal(barText)
		}

		// Force immediate visual update (bypasses 15-second status-interval)
		_ = tmux.RefreshStatusBarImmediate()

		notifLog.Info("bar_updated", slog.String("text", barText))
	} else {
		h.lastBarTextMu.Unlock()
	}

	// CRITICAL: Update key bindings in background too!
	// This fixes the bug where key bindings became stale when TUI was paused (tea.Exec).
	// updateKeyBindings() is thread-safe via boundKeysMu.
	h.updateKeyBindings()
}

// updateKeyBindings updates tmux key bindings based on current notification entries.
// Thread-safe via boundKeysMu. Can be called from both foreground and background.
func (h *Home) updateKeyBindings() {
	if !h.manageTmuxNotifications {
		return
	}

	// Minimal mode shows counts only — no named slots, no key bindings to manage
	if h.notificationManager.IsMinimal() {
		return
	}

	entries := h.notificationManager.GetEntries()

	// Phase 1: Collect binding info while holding instancesMu (read-only)
	type bindingInfo struct {
		key        string
		sessionID  string
		tmuxName   string
		bindingKey string // "sessionID:tmuxName"
	}
	bindings := make([]bindingInfo, 0, len(entries))
	currentKeys := make(map[string]string) // key -> sessionID

	h.instancesMu.RLock()
	for _, e := range entries {
		currentKeys[e.AssignedKey] = e.SessionID

		// Look up CURRENT TmuxName from instance (cached entry may be stale)
		currentTmuxName := e.TmuxName
		if inst, ok := h.instanceByID[e.SessionID]; ok {
			if ts := inst.GetTmuxSession(); ts != nil {
				currentTmuxName = ts.TmuxName()
			}
		}

		bindings = append(bindings, bindingInfo{
			key:        e.AssignedKey,
			sessionID:  e.SessionID,
			tmuxName:   currentTmuxName,
			bindingKey: e.SessionID + ":" + currentTmuxName,
		})
	}
	h.instancesMu.RUnlock()

	// Phase 2: Update key bindings while holding boundKeysMu
	h.boundKeysMu.Lock()
	for _, b := range bindings {
		existingBinding, isBound := h.boundKeys[b.key]
		if !isBound || existingBinding != b.bindingKey {
			_ = tmux.BindSwitchKeyWithAck(b.key, b.tmuxName, b.sessionID)
			h.boundKeys[b.key] = b.bindingKey
		}
	}

	// Unbind keys no longer needed
	for key := range h.boundKeys {
		if _, stillNeeded := currentKeys[key]; !stillNeeded {
			_ = tmux.UnbindKey(key)
			delete(h.boundKeys, key)
		}
	}
	h.boundKeysMu.Unlock()
}

// noteGroupToggled treats a group EXPAND as a first-class refresh trigger
// (#1753). Expanding a large group makes many rows newly visible at once; they
// render instantly from the render snapshot (rebuildFlatItems does no tmux
// work), and this nudge asks the status worker to start filling them in through
// the budgeted visible round-robin — amortized over passes, never as a
// synchronous whole-group burst. The cursor reset makes the newly revealed
// rows' refresh order start from the top of the visible set. Collapse needs no
// nudge: it only removes rows.
func (h *Home) noteGroupToggled(groupPath string) {
	group, ok := h.groupTree.Groups[groupPath]
	if !ok || !group.Expanded {
		return
	}
	h.visibleStatusUpdateIndex.Store(0)
	h.triggerStatusUpdate()
}

// triggerStatusUpdate sends a non-blocking request to the background worker
// If the worker is busy, the request is dropped (next tick will retry)
func (h *Home) triggerStatusUpdate() {
	// Build list of session IDs from flatItems for visible detection
	flatItemIDs := make([]string, 0, len(h.flatItems))
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			flatItemIDs = append(flatItemIDs, item.Session.ID)
		}
	}

	visibleHeight := h.height - 8
	if visibleHeight < 5 {
		visibleHeight = 5
	}

	req := statusUpdateRequest{
		viewOffset:    h.viewOffset,
		visibleHeight: visibleHeight,
		flatItemIDs:   flatItemIDs,
	}
	h.noteSweepVisibleRows(req)

	// Non-blocking send - if worker is busy, skip this tick
	select {
	case h.statusTrigger <- req:
		// Request sent successfully
	default:
		// Worker busy, will retry next tick
	}
}

// persistPolledStatuses writes the status of every session this process
// polls to the shared row, skipping unchanged ones, and returns the set of
// current instance IDs. A write that loses to a status another process
// committed (a CLI `session stop`, #2669) adopts the committed status.
func (h *Home) persistPolledStatuses(db *statedb.StateDB, instances []*session.Instance) map[string]struct{} {
	currentIDs := make(map[string]struct{}, len(instances))
	adoptedCommitted := false
	for _, inst := range instances {
		currentIDs[inst.ID] = struct{}{}
		if !h.isPolledByMe(inst.ID) {
			continue
		}
		status := string(inst.GetStatusThreadSafe())
		prev, ok := h.lastPersistedStatus[inst.ID]
		if ok && prev == status {
			continue
		}
		if !ok {
			_ = db.WriteStatus(inst.ID, status, inst.Tool)
			h.lastPersistedStatus[inst.ID] = status
			continue
		}
		// Conditional on the row still holding what this Home last wrote
		// (#2669): a CLI `session stop` commits "stopped" while this
		// process still holds the pre-stop status in memory, and the
		// sweep then classifies the vanished pane as "error". Last writer
		// wins would turn the deliberate stop into an error.
		committed, applied := writeStatusIfCurrent(db, inst.ID, prev, status, inst.Tool)
		if !applied && committed != "" {
			inst.SetStatusThreadSafe(session.Status(committed))
			status = committed
			adoptedCommitted = true
		}
		h.lastPersistedStatus[inst.ID] = status
	}
	if adoptedCommitted {
		h.cachedStatusCounts.valid.Store(false)
		h.publishWebSessionStates(instances)
		h.refreshSessionRenderSnapshot(instances)
	}
	return currentIDs
}

// writeStatusIfCurrent persists status only while the shared row still holds
// expected, so a status another process committed in between (a CLI
// `session stop`, #2669) is not overwritten by this process's stale sample.
// When the write loses, it returns the committed status for the caller to
// adopt; committed is "" when the row could not be read or does not exist.
func writeStatusIfCurrent(db *statedb.StateDB, id, expected, status, tool string) (committed string, applied bool) {
	applied, err := db.WriteStatusIfCurrent(id, expected, status, tool)
	if err != nil {
		return "", false
	}
	if applied {
		return status, true
	}
	rows, err := db.ReadAllStatuses()
	if err != nil {
		return "", false
	}
	row, ok := rows[id]
	if !ok {
		return "", false
	}
	return row.Status, false
}

func (h *Home) publishCurrentSessionStates() {
	h.instancesMu.RLock()
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)
	h.instancesMu.RUnlock()
	h.publishWebSessionStates(instances)
}

// processStatusUpdate implements round-robin status updates (Priority 1A + 1B)
// Called by the background worker goroutine
// Instead of updating ALL sessions every tick (which causes lag with 100+ sessions),
// we update in batches:
//   - Always update visible sessions first (ensures UI responsiveness)
//   - Round-robin through remaining sessions (spreads CPU load over time)
//
// Performance: With 10 sessions, updating all takes ~1-2s of cumulative time per tick.
// With batching (3 visible + 2 non-visible per tick), we keep each tick under 100ms.
func (h *Home) processStatusUpdate(req statusUpdateRequest) {
	const batchSize = 2 // Reduced from 5 to 2 - fewer CapturePane() calls per tick
	// Visible rows are budgeted too (#1753): see Step 1 below. 4 rows per pass
	// keeps a screenful fresh within a few passes while bounding the worst case
	// (large group expanded => visible ≈ fleet) to a constant per pass.
	const visibleStatusBatchSize = 4
	if hotUntil := h.navigationHotUntil.Load(); hotUntil > 0 && time.Now().UnixNano() < hotUntil {
		return
	}
	if last := h.lastFullStatusSweep.Load(); last > 0 {
		if time.Since(time.Unix(0, last)) < 1500*time.Millisecond {
			// A full all-session sweep just ran; skip redundant incremental update.
			return
		}
	}

	// CRITICAL FIX: Refresh session cache in background worker, NOT main goroutine
	// This prevents UI freezing when subprocess spawning is slow (high system load)
	// The cache refresh spawns `tmux list-sessions` which can block for 50-200ms
	tmux.RefreshExistingSessions()

	// Take a snapshot of instances under read lock (thread-safe)
	h.instancesMu.RLock()
	if len(h.instances) == 0 {
		h.instancesMu.RUnlock()
		return
	}
	instancesCopy := make([]*session.Instance, len(h.instances))
	copy(instancesCopy, h.instances)
	h.instancesMu.RUnlock()

	// Build set of visible session IDs for quick lookup
	visibleIDs := make(map[string]bool)

	// Find visible sessions based on viewOffset and flatItemIDs
	for i := req.viewOffset; i < len(req.flatItemIDs) && i < req.viewOffset+req.visibleHeight; i++ {
		visibleIDs[req.flatItemIDs[i]] = true
	}

	// Track which sessions we've updated this tick
	updated := make(map[string]bool)
	// Track if any status actually changed (for cache invalidation)
	statusChanged := false

	// Step 1: Visible sessions — budgeted round-robin (#1753).
	//
	// This used to refresh EVERY visible row each pass. That is fine when the
	// visible set is a screenful of a mostly-collapsed list, but with a large
	// group expanded the visible set approaches fleet size and this became an
	// unbudgeted burst of per-row UpdateStatus calls — each of which takes the
	// Instance write lock, sometimes across tmux subprocess round-trips. On the
	// reporter's ~57-session deck that burst is what stretched the first
	// post-detach frames into a visible black screen and made group expansion
	// lag. Visible rows now cycle through their own cursor with a fixed budget
	// per pass, and an idle row whose cached tmux window activity has not moved
	// since its last refresh is skipped outright (cost zero). The full
	// background sweep (2-10s cadence) remains the freshness backstop for every
	// row, visible or not, so the budget only bounds burst size, not eventual
	// freshness.
	if h.visibleRefreshFingerprint == nil {
		h.visibleRefreshFingerprint = make(map[string]int64)
	}
	visibleInstances := make([]*session.Instance, 0, len(visibleIDs))
	for _, inst := range instancesCopy {
		if visibleIDs[inst.ID] {
			visibleInstances = append(visibleInstances, inst)
		}
	}
	visRemaining := visibleStatusBatchSize
	visStart := int(h.visibleStatusUpdateIndex.Load())
	for i := 0; i < len(visibleInstances) && visRemaining > 0; i++ {
		idx := (visStart + i) % len(visibleInstances)
		inst := visibleInstances[idx]
		// Skip sessions this instance neither owns nor is orphan-polling this
		// sweep: mirrors the background sweep's gate so the incremental poll
		// path can't defeat the dedup claim polling promises (flag off or nil
		// owned map keeps isPolledByMe fail-open, so no behavior change there).
		if !h.isPolledByMe(inst.ID) {
			continue
		}
		// Cheap-fingerprint skip: an idle visible row whose cached window
		// activity is unchanged since its last refresh cannot have new status.
		// Skipping here (before UpdateStatus) means it does not even pay the
		// Instance write lock. Non-idle rows always go through — their status
		// can change without a tmux activity bump (hook files, process exit).
		if inst.GetStatusThreadSafe() == session.StatusIdle {
			if ts := inst.GetTmuxSession(); ts != nil {
				fp := ts.GetCachedWindowActivity()
				if fp != 0 && fp == h.visibleRefreshFingerprint[inst.ID] {
					continue
				}
			}
		}
		oldStatus := inst.GetStatusThreadSafe()
		_ = inst.UpdateStatus() // Ignore errors in background worker
		if inst.GetStatusThreadSafe() != oldStatus {
			statusChanged = true
		}
		if ts := inst.GetTmuxSession(); ts != nil {
			h.visibleRefreshFingerprint[inst.ID] = ts.GetCachedWindowActivity()
		}
		updated[inst.ID] = true
		visRemaining--
		h.visibleStatusUpdateIndex.Store(int32((idx + 1) % len(visibleInstances))) // #nosec G115 -- idx bounded by slice length
	}

	// Step 2: Round-robin through non-visible sessions (Priority 1A - batching)
	// OPTIMIZATION: Skip idle sessions - they need user interaction to become active.
	// This significantly reduces CapturePane() calls for large session lists.
	remaining := batchSize
	startIdx := int(h.statusUpdateIndex.Load())
	instanceCount := len(instancesCopy)

	for i := 0; i < instanceCount && remaining > 0; i++ {
		idx := (startIdx + i) % instanceCount
		inst := instancesCopy[idx]

		// Skip visible rows here: they are the visible round-robin's job (Step 1,
		// budgeted). Before the visible budget existed, `updated` covered every
		// visible row so this loop was implicitly off-screen-only; now that Step 1
		// refreshes at most visibleStatusBatchSize of them per pass, the skip must
		// be explicit or off-screen rows would compete with visible ones here.
		if visibleIDs[inst.ID] || updated[inst.ID] {
			continue
		}

		// Skip sessions this instance neither owns nor is orphan-polling this
		// sweep (mirrors the background sweep's gate); just advance the cursor
		// past it like any other skip below, don't stall the round-robin.
		if !h.isPolledByMe(inst.ID) {
			continue
		}

		// Skip idle sessions - they require user interaction to change state
		// Background polling will catch any activity when user interacts
		if inst.GetStatusThreadSafe() == session.StatusIdle {
			continue
		}

		oldStatus := inst.GetStatusThreadSafe()
		_ = inst.UpdateStatus() // Ignore errors in background worker
		if inst.GetStatusThreadSafe() != oldStatus {
			statusChanged = true
		}
		remaining--
		h.statusUpdateIndex.Store(int32((idx + 1) % instanceCount)) // #nosec G115 -- idx is bounded by instanceCount (slice length), fits in int32
	}

	// Only invalidate status counts cache if status actually changed
	// This reduces View() overhead by keeping cache valid when no changes occurred
	if statusChanged {
		h.cachedStatusCounts.valid.Store(false)
		h.publishWebSessionStates(instancesCopy)
	}
	h.refreshSessionRenderSnapshot(instancesCopy)
}
