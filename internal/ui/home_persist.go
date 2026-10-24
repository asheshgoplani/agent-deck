package ui

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// applySavedLayoutSettings applies the [ui] values the settings panel just
// saved. sidebar_density is live. embedded_terminal is not: main negotiates
// the terminal protocol for the layout once, at startup (all-motion mouse,
// focus reporting, the stdin router and stdout wrapper), and the classic path
// stays byte-identical when the option is off. Flipping h.embeddedLayout here
// would advertise the embedded terminal while Enter fell back to the legacy
// key-sender, so a changed value is stored and takes effect at the next
// launch, and the user is told so.
func (h *Home) applySavedLayoutSettings(ui session.UISettings) {
	h.sidebarDensity = ui.GetSidebarDensity()
	if want := ui.GetEmbeddedTerminal(); want != h.embeddedLayout {
		state := "off"
		if want {
			state = "on"
		}
		h.setError(fmt.Errorf("embedded terminal %s saved: restart agent-deck to switch layouts", state))
	}
}

// loadSessions loads sessions from storage and initializes the pool
func (h *Home) loadSessions() tea.Msg {
	return h.sessionLoadCmd(nil, true)()
}

// sessionLoadCmd issues ordering without database I/O. The returned command
// probes and reads the captured storage asynchronously, including initial loads.
func (h *Home) sessionLoadCmd(restore *reloadState, initializePool bool) tea.Cmd {
	h.sessionLoadSequence++
	sequence := h.sessionLoadSequence
	storage, watcher := h.storage, h.storageWatcher
	var issuedTicket storageLoadTicket
	if watcher != nil {
		issuedTicket = watcher.issueLoad()
	}
	return func() tea.Msg {
		ticket := issuedTicket
		msg := loadSessionsMsg{loadSequence: sequence, loadWatcher: watcher, restoreState: restore}
		if storage == nil {
			msg.err = fmt.Errorf("storage not initialized")
			return msg
		}
		if watcher != nil {
			var err error
			ticket, err = watcher.probeLoad(ticket)
			msg.watcherTicket = &ticket
			if err != nil {
				msg.err = err
				return msg
			}
		}
		msg.loadMtime, _ = storage.GetFileMtime()
		msg.instances, msg.groups, msg.persistedSnapshot, msg.err = storage.LoadWithGroupsSnapshot()
		if watcher != nil {
			finished, err := watcher.endLoad(ticket)
			msg.watcherTicket = &finished
			if msg.err == nil {
				msg.err = err
			}
		}
		if initializePool {
			userConfig, configErr := session.LoadUserConfig()
			if configErr == nil && userConfig != nil && userConfig.MCPPool.Enabled {
				pool, poolErr := session.InitializeGlobalPool(h.ctx, userConfig, msg.instances)
				if poolErr != nil {
					mcpUILog.Warn("pool_init_failed", slog.String("error", poolErr.Error()))
					msg.poolError = poolErr
				} else if pool != nil {
					proxies := pool.ListServers()
					mcpUILog.Info("pool_initialized", slog.Int("proxies", len(proxies)))
					msg.poolProxies = len(proxies)
				}
			}
		}
		return msg
	}
}

// SetHeadless marks this Home as backing a headless (`web --no-tui`) server.
// In headless mode no bubbletea loop runs, so the WebMutator must hydrate the
// in-memory registry from storage on each mutation (#1397).
func (h *Home) SetHeadless(v bool) { h.headless = v }

// IsHeadless reports whether this Home backs a headless web server.
func (h *Home) IsHeadless() bool { return h.headless }

// HydrateInstancesFromStorage reloads the in-memory instances, instanceByID
// map, and groupTree from storage. It is the headless-mode equivalent of the
// loadSessions/loadSessionsMsg cycle that the bubbletea loop runs in TUI mode:
// without a running TUI, h.instances stays empty and every WebMutator lookup
// fails with "session not found" while persistAllInstances ([]) trips the
// empty-sweep guard (#1397). Safe to call repeatedly; it picks up out-of-band
// changes (e.g. a concurrent CLI `add`/`rm`) so each web mutation operates on
// the current registry.
//
// No-op (returns nil) when storage is unavailable so the caller can proceed
// with whatever it already has rather than panicking.
func (h *Home) HydrateInstancesFromStorage() error {
	if h.storage == nil {
		return nil
	}
	instances, groups, err := h.storage.LoadWithGroups()
	if err != nil {
		return fmt.Errorf("hydrate instances from storage: %w", err)
	}

	h.instancesMu.Lock()
	h.instances = instances
	h.instanceByID = make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		h.instanceByID[inst.ID] = inst
	}
	h.instancesMu.Unlock()

	if len(groups) > 0 {
		h.groupTree = session.NewGroupTreeWithGroups(instances, groups)
	} else {
		h.groupTree = session.NewGroupTree(instances)
	}
	return nil
}

// saveInstances saves instances to storage
func (h *Home) saveInstances() {
	h.saveInstancesWithForce(false)
}

// forceSaveInstances saves instances regardless of isReloading flag.
// Use this for critical updates that MUST persist (e.g., OpenCode detection results)
// that would otherwise be lost due to race conditions with storage watcher reloads.
func (h *Home) forceSaveInstances() {
	h.saveInstancesWithForce(true)
}

// saveInstancesWithForce is the internal save implementation.
// force=true bypasses the isReloading check for critical updates.
func (h *Home) saveInstancesWithForce(force bool) {
	// Skip saving during reload to avoid overwriting external changes (CLI)
	// Unless force=true for critical updates like detection results
	h.reloadMu.Lock()
	reloading := h.isReloading
	h.reloadMu.Unlock()

	if reloading && !force {
		uiLog.Debug("save_skip_during_reload", slog.Bool("force", force))
		return
	}
	if force && reloading {
		uiLog.Debug("save_force_during_reload")
	}

	// EXTERNAL CHANGE DETECTION: Check if file was modified since we last loaded.
	// This catches external changes (e.g., from CLI) even when fsnotify fails
	// (common on 9p/NFS filesystems in WSL2).
	// #1550: force saves no longer skip this check. They still persist (they
	// carry critical mutations like create/fork/delete, and the save is now
	// upsert-only so it cannot delete another process's rows), but a detected
	// external change schedules a reload after the save so this TUI stops
	// holding a stale snapshot.
	externalChange := false
	h.reloadMu.Lock()
	ourLoadMtime := h.lastLoadMtime
	h.reloadMu.Unlock()

	if h.storage != nil && !ourLoadMtime.IsZero() {
		currentMtime, err := h.storage.GetFileMtime()
		if err == nil && !currentMtime.IsZero() && currentMtime.After(ourLoadMtime) {
			externalChange = true
			uiLog.Warn("save_external_change_detected",
				slog.Bool("force", force),
				slog.Time("our_load", ourLoadMtime),
				slog.Time("current_mtime", currentMtime))
		}
	}
	if externalChange && !force {
		// Routine save: abort and reload instead of overwriting with stale rows.
		if h.storageWatcher != nil {
			h.storageWatcher.TriggerReload()
		}
		return
	}

	if h.storage != nil {
		// DEFENSIVE CHECK: Verify we're saving to the correct profile's database
		// This prevents catastrophic cross-profile contamination
		expectedPath, err := session.GetDBPathForProfile(h.profile)
		if err != nil {
			uiLog.Warn(
				"save_expected_path_failed",
				slog.String("profile", h.profile),
				slog.String("error", err.Error()),
			)
			return
		}
		if h.storage.Path() != expectedPath {
			uiLog.Error(
				"save_path_mismatch",
				slog.String("profile", h.profile),
				slog.String("expected", expectedPath),
				slog.String("got", h.storage.Path()),
			)
			h.setError(
				fmt.Errorf(
					"storage path mismatch (profile=%s): expected %s, got %s",
					h.profile,
					expectedPath,
					h.storage.Path(),
				),
			)
			return
		}

		// Take snapshot under lock for defensive programming
		// This ensures consistency even if architecture changes in the future
		h.instancesMu.RLock()
		instancesCopy := make([]*session.Instance, len(h.instances))
		copy(instancesCopy, h.instances)
		instanceCount := len(h.instances)
		h.instancesMu.RUnlock()

		uiLog.Debug(
			"save_instances",
			slog.Int("count", instanceCount),
			slog.String("profile", h.profile),
			slog.String("path", h.storage.Path()),
			slog.Bool("force", force),
		)

		// DEFENSIVE: Never save empty instances if storage file has data
		// This prevents catastrophic data loss from transient load failures
		if instanceCount == 0 {
			// Check if storage file exists and has data before overwriting with empty
			if info, err := os.Stat(h.storage.Path()); err == nil && info.Size() > 100 {
				uiLog.Warn("save_refusing_empty_overwrite", slog.Int64("file_bytes", info.Size()))
				return
			}
		}

		groupTreeCopy := h.groupTree.ShallowCopyForSave()

		// CRITICAL FIX: NotifySave MUST be called immediately before SaveWithGroups
		// Previously it was called 25 lines earlier, creating a race window where the
		// 500ms ignore window could expire before the save completed under load
		if h.storageWatcher != nil {
			h.storageWatcher.NotifySave()
		}

		// Save both instances and groups (including empty ones)
		if err := h.storage.SaveWithGroups(instancesCopy, groupTreeCopy); err != nil {
			h.setError(fmt.Errorf("failed to save: %w", err))
		} else {
			// CRITICAL FIX: Update lastLoadMtime after successful save.
			// Without this, subsequent saves incorrectly detect the TUI's own previous
			// save as an "external change" (currentMtime > stale lastLoadMtime) and abort.
			// This caused session renames and other non-force saves to silently fail.
			// See: https://github.com/asheshgoplani/agent-deck/issues/141
			if newMtime, err := h.storage.GetFileMtime(); err == nil && !newMtime.IsZero() {
				h.reloadMu.Lock()
				h.lastLoadMtime = newMtime
				h.reloadMu.Unlock()
			}
			// Clear pending title changes on successful save (rename was persisted)
			if len(h.pendingTitleChanges) > 0 {
				h.pendingTitleChanges = make(map[string]pendingTitle)
			}
			// Clear pending group ops on successful save: the whole in-memory
			// tree (including every recorded op's mutation) was just persisted,
			// so there is nothing to re-apply. Leaving them would let a later,
			// unrelated reload blindly re-apply a stale op against a diverged
			// tree (resurrecting a deleted group, or a rename collision that
			// drops sessions). Scopes op lifetime to record → save-or-abort,
			// mirroring pendingTitleChanges.
			h.pendingGroupOps = nil
			// #1550: a force save raced an external change. Our rows are now
			// persisted (upsert-only, nothing deleted); reload to pick up what
			// the other process wrote while we were stale.
			if externalChange && h.storageWatcher != nil {
				h.storageWatcher.TriggerReload()
			}
		}
	}
}

// adoptRestartRecord accounts for the targeted write Instance.restart makes when
// it records what a restart produced.
//
// That write moves the state DB's last_modified. This TUI's freshness marker
// does not move with it, so the save that follows a restart would read the
// TUI's OWN write as an external change, abort, and reload -- discarding the
// rest of the restart's in-memory state (sandbox container, tool session ids,
// the dedup pass) for no reason. That self-inflicted false positive is what
// #1868 was really hitting.
//
// The conditions under which the marker actually moves live on adoptOwnWrite,
// which the mark-unread path shares.
func (h *Home) adoptRestartRecord(sessionID string) {
	inst := h.getInstanceByID(sessionID)
	if inst == nil {
		return
	}
	h.adoptOwnWrite(inst.RestartRecordStamps(), "restart_record")
}

// adoptOwnWrite advances this TUI's freshness marker past a targeted write it
// just made itself, so the save that follows does not read its own bump as
// another process's change.
//
// Shared by every caller that makes a targeted write and then saves. `what`
// names the write for the diagnostic log only.
//
// The marker moves only when this write was the sole change since this TUI
// loaded: nothing landed between the load and the write, and nothing has landed
// since. Anything else means the database really has moved on, the abort is
// correct, and it stays -- the reload picks up what the other process wrote,
// and the targeted write's own outcome is durable either way because it already
// landed.
//
// last_modified is a UnixNano stamp, so this is an exact identity test on our
// own write rather than a time window that could swallow somebody else's.
// Returns true when the marker moved.
func (h *Home) adoptOwnWrite(stamps statedb.WriteStamps, what string) bool {
	if h.storage == nil || stamps.After == 0 {
		return false
	}
	current, err := h.storage.GetFileMtime()
	if err != nil || current.IsZero() {
		return false
	}

	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	if !stamps.SoleWriterSince(h.lastLoadMtime.UnixNano(), current.UnixNano()) {
		uiLog.Debug("own_write_not_sole_writer",
			slog.String("write", what),
			slog.Int64("before", stamps.Before),
			slog.Int64("after", stamps.After),
			slog.Int64("current", current.UnixNano()))
		return false
	}
	h.lastLoadMtime = current
	if h.storageWatcher != nil {
		// Our own bump should not make the watcher schedule a reload either.
		h.storageWatcher.NotifySave()
	}
	return true
}

// saveGroupState saves only group expanded/collapsed state to SQLite.
// This is lightweight (no Touch, no StorageWatcher trigger) and safe to call after every toggle.
func (h *Home) saveGroupState() {
	if h.storage == nil || h.groupTree == nil {
		return
	}
	groupTreeCopy := h.groupTree.ShallowCopyForSave()
	if err := h.storage.SaveGroupsOnly(groupTreeCopy); err != nil {
		uiLog.Warn("save_group_state_failed", slog.String("error", err.Error()))
	}
}

// saveUIState persists cursor position, preview mode, and status filter to SQLite metadata.
// Failures are logged only; use saveUIStateErr when the caller has to tell the
// user that their action did not stick.
func (h *Home) saveUIState() {
	_ = h.saveUIStateErr()
}

// saveUIStateErr is saveUIState with the write outcome returned.
//
// The periodic and incidental callers do not care — a dropped autosave is
// recoverable on the next tick. An explicit user action does care: #1875 exists
// because a remote row silently swallowed a keystroke, and "the rows moved but
// the order was never written" is the same lie one layer down. A nil storage or
// DB is not a failure: that is the headless/no-persistence configuration, not a
// write that went wrong.
func (h *Home) saveUIStateErr() error {
	if h.storage == nil {
		return nil
	}
	db := h.storage.GetDB()
	if db == nil {
		return nil
	}

	state := uiState{
		PreviewMode:             int(h.previewMode),
		StatusFilter:            string(h.statusFilter),
		ActiveFilterHideStopped: h.activeFilterHideStopped,
		GroupViewMode:           int(h.groupViewMode),
		TimeFilterMode:          int(h.timeFilter),
		RemoteSessionOrder:      h.remoteSessionOrder,
		SidebarMode:             string(h.sidebarMode),
		SidebarWidthCustomized:  h.embeddedLayout && !h.compactSidebar,
	}

	// Sorted so an unchanged fold state marshals byte-identically and doesn't
	// churn the metadata row on every save.
	if len(h.remoteGroupsCollapsed) > 0 {
		paths := make([]string, 0, len(h.remoteGroupsCollapsed))
		for path, isCollapsed := range h.remoteGroupsCollapsed {
			if isCollapsed {
				paths = append(paths, path)
			}
		}
		sort.Strings(paths)
		state.RemoteGroupsCollapsed = paths
	}

	// Parent-child folds (#2631), as parent session IDs.
	if len(h.parentChildrenCollapsed) > 0 {
		foldedIDs := make([]string, 0, len(h.parentChildrenCollapsed))
		for sid, isFolded := range h.parentChildrenCollapsed {
			if isFolded {
				foldedIDs = append(foldedIDs, sid)
			}
		}
		sort.Strings(foldedIDs)
		state.ParentChildrenCollapsed = foldedIDs
	}
	// Capture cursor position
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		switch item.Type {
		case session.ItemTypeSession:
			if item.Session != nil {
				state.CursorSessionID = item.Session.ID
			}
		case session.ItemTypeWindow:
			state.CursorSessionID = item.WindowSessionID
		case session.ItemTypeGroup:
			state.CursorGroupPath = item.Path
		}
	}

	data, err := json.Marshal(state)
	if err != nil {
		uiLog.Warn("save_ui_state_marshal_failed", slog.String("error", err.Error()))
		return err
	}
	if err := db.SetMeta("ui_state", string(data)); err != nil {
		uiLog.Warn("save_ui_state_failed", slog.String("error", err.Error()))
		return err
	}
	return nil
}

// loadUIState reads persisted UI state from SQLite metadata.
// Preview mode and status filter are applied immediately.
// Cursor position is stored in pendingCursorRestore for deferred application after initial load.
func (h *Home) loadUIState() {
	if h.storage == nil {
		return
	}
	db := h.storage.GetDB()
	if db == nil {
		return
	}

	val, err := db.GetMeta("ui_state")
	if err != nil || val == "" {
		return
	}

	var state uiState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		uiLog.Warn("load_ui_state_unmarshal_failed", slog.String("error", err.Error()))
		return
	}

	// Remote fold state applies immediately: rebuildFlatItems reads the map on
	// the first paint, before any remote sessions have even been fetched.
	if h.remoteGroupsCollapsed == nil {
		h.remoteGroupsCollapsed = make(map[string]bool)
	}
	for _, path := range state.RemoteGroupsCollapsed {
		h.remoteGroupsCollapsed[path] = true
	}

	// Parent-child folds (#2631): IDs for sessions that no longer exist are
	// harmless — a key nobody renders as a parent is never read.
	if h.parentChildrenCollapsed == nil {
		h.parentChildrenCollapsed = make(map[string]bool)
	}
	for _, sid := range state.ParentChildrenCollapsed {
		h.parentChildrenCollapsed[sid] = true
	}

	// Apply preview mode, status filter, and group view mode immediately
	h.previewMode = PreviewMode(state.PreviewMode)
	h.statusFilter = session.Status(state.StatusFilter)
	h.groupViewMode = session.GroupViewMode(state.GroupViewMode)
	if h.groupViewMode < session.GroupViewNormal || h.groupViewMode >= session.GroupViewModeCount {
		h.groupViewMode = session.GroupViewNormal
	}
	h.timeFilter = session.TimeFilterMode(state.TimeFilterMode)
	if h.timeFilter < session.TimeFilterAll || h.timeFilter >= session.TimeFilterModeCount {
		h.timeFilter = session.TimeFilterAll
	}

	// #1875: restore the manual remote row order. Entries for remotes that no
	// longer exist are harmless — a bucket key nobody builds is never read —
	// and stale session IDs inside a live bucket are skipped when applied.
	h.remoteSessionOrder = make(remoteOrder, len(state.RemoteSessionOrder))
	for remoteName, groups := range state.RemoteSessionOrder {
		for groupPath, ids := range groups {
			if len(ids) > 0 {
				h.remoteSessionOrder.set(remoteName, groupPath, ids)
			}
		}
	}

	h.applyUIState(state)

	// Defer cursor restoration until flatItems are populated
	h.pendingCursorRestore = &state
}

func (h *Home) applyUIState(state uiState) {
	h.activeFilterHideStopped = state.StatusFilter == string(FilterModeActive) && state.ActiveFilterHideStopped
	h.previewMode = PreviewMode(state.PreviewMode)
	h.statusFilter = session.Status(state.StatusFilter)
	h.groupViewMode = session.GroupViewMode(state.GroupViewMode)
	if h.groupViewMode < session.GroupViewNormal || h.groupViewMode >= session.GroupViewModeCount {
		h.groupViewMode = session.GroupViewNormal
	}
	h.sidebarMode = sidebarPresentation(state.SidebarMode)
	if h.sidebarMode != sidebarFlat {
		h.sidebarMode = sidebarGrouped
	}
	if h.embeddedLayout {
		h.compactSidebar = !state.SidebarWidthCustomized
	}
}

// importSessions imports existing tmux sessions
func (h *Home) importSessions() tea.Msg {
	discovered, err := session.DiscoverExistingTmuxSessions(h.instances)
	if err != nil {
		return loadSessionsMsg{err: err}
	}

	h.instancesMu.Lock()
	h.instances = append(h.instances, discovered...)
	h.instancesMu.Unlock()

	// Add discovered sessions to group tree before saving
	for _, inst := range discovered {
		h.groupTree.AddSession(inst)
	}
	// Save both instances AND groups (critical fix: was losing groups!)
	h.saveInstances()
	return importReloadMsg{}
}
