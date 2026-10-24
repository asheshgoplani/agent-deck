package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// resolveNewSessionGroup returns the group path a freshly-created session
// should land in when no explicit group is chosen via a dialog.
//
// Issue #666: the createSessionFromGlobalSearch path (a Claude
// conversation imported from global search) previously called
// getCurrentGroupPath() directly and passed its return value into
// NewInstanceWithGroupAndTool. When the cursor was on a Window,
// RemoteGroup, RemoteSession, or creating-placeholder item,
// getCurrentGroupPath returned "" (home.go:4810). That empty string
// OVERRODE the extractGroupPath default set at construction time inside
// NewInstanceWithGroupAndTool — producing an Instance with
// GroupPath="". At the next reload the empty row silently re-derived
// via extractGroupPath(ProjectPath), making the imported session appear
// under a path-derived group instead of the one the user was browsing.
// Exact user-visible symptom: "TUI-created session ends up in a
// different group, sometimes with a path-derived name."
//
// The rescue contract: always return a non-empty, user-recoverable
// group. Prefer the cursor's group, then the scoped-view root, then
// DefaultGroupPath as the universal safe default.
func (h *Home) resolveNewSessionGroup() string {
	if gp := h.getCurrentGroupPath(); gp != "" {
		return gp
	}
	if h.groupScope != "" {
		return h.groupScope
	}
	return session.DefaultGroupPath
}

// handleNewDialogKey handles keys when new dialog is visible
func (h *Home) handleNewDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When the recent sessions picker is open, let the dialog handle all keys first.
	if h.newDialog.IsRecentPickerOpen() {
		var cmd tea.Cmd
		h.newDialog, cmd = h.newDialog.Update(msg)
		return h, cmd
	}
	// When branch search results are visible, let the dialog consume Enter/Esc/navigation
	// before the outer dialog-level handlers create/cancel the session.
	if h.newDialog.IsBranchPickerOpen() {
		var cmd tea.Cmd
		h.newDialog, cmd = h.newDialog.Update(msg)
		return h, cmd
	}

	if h.newDialog.IsSuggestionsActive() {
		var cmd tea.Cmd
		h.newDialog, cmd = h.newDialog.Update(msg)
		return h, cmd
	}

	if h.newDialog.IsModelSuggestionsActive() {
		var cmd tea.Cmd
		h.newDialog, cmd = h.newDialog.Update(msg)
		return h, cmd
	}

	// Ctrl+S is an explicit "create now" shortcut that submits from any field,
	// including Name/Branch where Enter advances focus instead of submitting.
	// Route it through the same path as a submitting Enter by falling through to
	// the submit block below.
	submitNow := h.newDialog.WantsSubmit(msg)

	switch {
	case msg.String() == "enter" || submitNow:
		if !submitNow && h.newDialog.shouldHandleEnterLocally() {
			var cmd tea.Cmd
			h.newDialog, cmd = h.newDialog.Update(msg)
			return h, cmd
		}

		// Ctrl+S can fire mid-edit of a multi-repo path. The in-flight text
		// lives only in pathInput (Enter is the sole committer), so flush it
		// into multiRepoPaths first; otherwise submit would use stale data.
		if submitNow {
			h.newDialog.CommitInFlightMultiRepoEdit()
		}

		// Validate before creating session
		if validationErr := h.newDialog.Validate(); validationErr != "" {
			h.newDialog.SetError(validationErr)
			return h, nil
		}

		// #1353: when the dialog was opened on a remote group/session, create
		// the session on that remote via SSH with the chosen tool. All
		// local-only logic below (worktree resolution, directory-exists
		// check, local create) must be skipped — it would act on the LOCAL
		// filesystem (#743).
		if h.pendingRemoteName != "" {
			remoteName := h.pendingRemoteName
			// Everything the dialog collected travels to the remote's own
			// `add` (account slot, model, Claude toggles, sandbox, worktree,
			// yolo); a field that command cannot express is refused here,
			// while the dialog is still open to show the message.
			opts, optsErr := h.newDialog.GetRemoteCreateOptions()
			if optsErr != "" {
				h.newDialog.SetError(optsErr)
				return h, nil
			}
			h.newDialog.Hide()
			h.pendingRemoteName = ""
			h.clearError()
			create := h.createRemoteSessionWithOptions
			if h.remoteCreateSink != nil {
				create = h.remoteCreateSink
			}
			return h, create(remoteName, opts)
		}

		// Get values including worktree settings.
		name, path, command, branchName, worktreeEnabled := h.newDialog.GetValuesWithWorktree()
		// #1706: a relative entry must be anchored to this process's cwd here,
		// before it reaches the directory-exists check, os.MkdirAll, the
		// worktree/VCS probe or the instance itself — tmux would otherwise
		// resolve it against the tmux server's cwd and put the session
		// somewhere other than the folder we created. Runs after the remote
		// branch above returns, so remote paths are never touched.
		//
		// Multi-repo paths are resolved here too (not at their use site further
		// down) so the single "cannot resolve" refusal happens while the dialog
		// is still open and can show the error. A declared path that stayed
		// relative could also never match a hook-reported cwd (#1731).
		multiRepoPaths, multiRepoEnabled := h.newDialog.GetMultiRepoPaths()
		absPath, absErr := absLocalProjectPath(path)
		if absErr == nil {
			path = absPath
			for i, p := range multiRepoPaths {
				if multiRepoPaths[i], absErr = absLocalProjectPath(p); absErr != nil {
					break
				}
			}
		}
		if absErr != nil {
			// filepath.Abs only fails when this process has no usable cwd, so
			// there is nothing to anchor a relative path to.
			h.newDialog.SetError("Cannot resolve a relative path here — enter an absolute path")
			return h, nil
		}

		// Remember the submitted tool so the next new-session dialog preselects
		// it (UX top-3 #2). Best-effort: persisted in the profile StateDB, never
		// config.toml. An explicit [default_tool] still wins on the next open.
		rememberTool(h.stateDB(), command)

		groupPath := h.newDialog.GetSelectedGroup()
		claudeOpts := h.newDialog.GetClaudeOptions() // Get Claude options if applicable.
		launchModelID := h.newDialog.GetLaunchModelID()

		// Resolve worktree/workspace target if enabled; actual creation runs in async command.
		var worktreePath, worktreeRepoRoot string
		if worktreeEnabled && branchName != "" {
			// resolveWorktreeTarget validates the path is a supported VCS repo
			// (git or jujutsu; including bare-repo project roots) and implements the #1185
			// fallback: a worktree enabled by config default (not an explicit
			// user toggle) on a non-repo dir falls back to a normal session
			// instead of erroring, while an explicit worktree still fails loud.
			wtPath, repoRoot, fallback, errMsg := resolveWorktreeTarget(path, branchName, h.newDialog.IsWorktreeExplicit())
			if errMsg != "" {
				h.newDialog.SetError(errMsg)
				return h, nil
			}
			if fallback {
				// #1185: create a normal session on this non-repo dir.
				worktreeEnabled = false
				branchName = ""
			} else {
				worktreePath = wtPath
				worktreeRepoRoot = repoRoot
			}
		}

		// Build generic toolOptionsJSON from tool-specific options
		var toolOptionsJSON json.RawMessage
		var claudeExtraArgs []string
		var claudeStartQuery string
		var claudeAccount string
		if command == "claude" && claudeOpts != nil {
			toolOptionsJSON, _ = session.MarshalToolOptions(claudeOpts)
			claudeExtraArgs = h.newDialog.GetClaudeExtraArgs()
			persistClaudeDialogDefaults(claudeOpts, claudeExtraArgs)
			claudeStartQuery = h.newDialog.GetClaudeStartQuery()
		} else if command == "codex" {
			yolo := h.newDialog.GetCodexYoloMode()
			codexOpts := &session.CodexOptions{
				YoloMode:        &yolo,
				ReasoningEffort: h.newDialog.GetLaunchReasoningEffort(),
			}
			toolOptionsJSON, _ = session.MarshalToolOptions(codexOpts)
		} else if command == "hermes" {
			yolo := h.newDialog.GetHermesYoloMode()
			hermesOpts := &session.HermesOptions{YoloMode: &yolo}
			toolOptionsJSON, _ = session.MarshalToolOptions(hermesOpts)
		} else if command == "omp" {
			toolOptionsJSON, _ = session.MarshalToolOptions(h.newDialog.GetOMPOptions())
		}

		// The account row is offered for every claude-compatible tool (the
		// options panel's own gate), so harvest it on the same terms rather
		// than only for the literal "claude" preset — otherwise a custom
		// claude-compatible tool silently drops the pick.
		if session.IsClaudeCompatible(command) {
			claudeAccount = h.newDialog.GetClaudeAccount()
		}

		parentSessionID := h.newDialog.GetParentSessionID()
		parentProjectPath := h.newDialog.GetParentProjectPath()

		// Only non-worktree sessions may need interactive "create directory" confirmation.
		if !worktreeEnabled {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				h.newDialog.Hide()
				h.confirmDialog.ShowCreateDirectory(path, name, command, groupPath, toolOptionsJSON, claudeExtraArgs, claudeStartQuery, claudeAccount, launchModelID, parentSessionID, parentProjectPath)
				return h, nil
			}
		}

		h.newDialog.Hide()
		h.clearError()

		geminiYoloMode := h.newDialog.IsGeminiYoloMode()
		sandboxMode := h.newDialog.IsSandboxEnabled()
		var additionalPaths []string
		if multiRepoEnabled && len(multiRepoPaths) > 1 {
			// First path stays as ProjectPath, rest are additional.
			// Already absolutized above (#1706).
			path = multiRepoPaths[0]
			additionalPaths = multiRepoPaths[1:]
		}

		// Show immediate placeholder in UI while worktree + session is created async
		var tempID string
		if worktreeEnabled && branchName != "" {
			tempID = session.GenerateID()
			h.creatingSessions[tempID] = &CreatingSession{
				ID:        tempID,
				Title:     name,
				Tool:      command,
				GroupPath: groupPath,
				StartTime: time.Now(),
			}
			h.rebuildFlatItems()
			// Auto-select the placeholder
			for i, item := range h.flatItems {
				if item.CreatingID == tempID {
					h.cursor = i
					h.syncViewport()
					break
				}
			}
		}

		return h, h.createSessionInGroupWithWorktreeAndOptions(
			name,
			path,
			"",
			command,
			groupPath,
			worktreePath,
			worktreeRepoRoot,
			branchName,
			geminiYoloMode,
			sandboxMode,
			toolOptionsJSON,
			claudeExtraArgs,
			claudeStartQuery,
			claudeAccount,
			launchModelID,
			multiRepoEnabled,
			additionalPaths,
			parentSessionID,
			parentProjectPath,
			tempID,
			false, // not auto-named — user went through the full create dialog
		)

	case msg.String() == "esc":
		// #1162: when the model picker dropdown is open, Esc dismisses only the
		// picker and keeps the new-session form alive (focus stays on the model
		// field) rather than cancelling the whole flow. Forward to the dialog so
		// its picker-level Esc handler runs.
		if h.newDialog.IsModelPickerOpen() {
			var cmd tea.Cmd
			h.newDialog, cmd = h.newDialog.Update(msg)
			return h, cmd
		}
		h.newDialog.Hide()
		h.clearError()           // Clear any validation error
		h.pendingRemoteName = "" // #1353: drop the remote target on cancel
		return h, nil
	}

	var cmd tea.Cmd
	h.newDialog, cmd = h.newDialog.Update(msg)
	return h, cmd
}

// showRemoteNewSessionDialog opens the new-session dialog for a remote target
// and fetches one authoritative snapshot of its creation fields and catalogs.
func (h *Home) showRemoteNewSessionDialog(item session.Item) tea.Cmd {
	remoteName := item.RemoteName
	if remoteName == "" {
		return nil
	}

	paths := h.remotePathSuggestions(remoteName)
	h.newDialog.SetPathSuggestions(paths)
	h.newDialog.SetRecentSessions(nil)
	h.pendingRemoteName = remoteName

	groupPath := session.DefaultGroupPath
	groupName := session.DefaultGroupName
	defaultPath := ""
	// remoteRoot: the cursor is on the "remotes/<host>" header itself (level
	// 0), a synthetic local UI bucket rather than a user-defined remote
	// group. That row has no group on the remote, so the dialog must default
	// to the remote's own top level — forwarding the local "My Sessions"
	// default instead would file the session into an unrelated remote
	// subgroup. A deeper header is one of the remote's real groups, so the
	// session is offered there, exactly as `n` on a local group header does.
	remoteRoot := false
	if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
		if item.RemoteSession.Group != "" {
			groupPath = item.RemoteSession.Group
			groupName = item.RemoteSession.Group
		}
		defaultPath = item.RemoteSession.Path
	} else if item.Type == session.ItemTypeRemoteGroup {
		if gp := remoteGroupPathFromItem(item); item.Level > 0 && gp != "" {
			groupPath = gp
			groupName = displayGroupName(gp)
		} else {
			remoteRoot = true
		}
		defaultPath = "."
	} else if len(paths) > 0 {
		defaultPath = paths[0]
	}

	h.newDialog.ShowInGroup(groupPath, groupName, defaultPath, nil, "")
	if remoteRoot {
		// ShowInGroup coerces an empty group to the local "my-sessions"
		// default, so clear it afterwards: no group is forwarded (remote
		// root) and the dialog names the remote itself.
		h.newDialog.SetParentGroupOverride("", remoteName)
	}
	if defaultPath == "" {
		h.newDialog.pathInput.SetValue(".")
		h.newDialog.pathSoftSelected = true
	}
	// Remote creation goes through the remote CLI, which applies the server's
	// own defaults. Drop everything ShowInGroup inherited from this machine's
	// config so only what the user sets in the dialog is forwarded.
	h.newDialog.ResetRemoteDefaults()
	h.newDialog.SetRemoteName(remoteName)
	// The account and MCP rows now list the server's slots and MCPs, not this
	// machine's: they arrive asynchronously so opening the dialog never
	// blocks on SSH.
	h.remoteAccountsGen++
	gen := h.remoteAccountsGen
	fetchCatalog := h.fetchRemoteCreationCatalog
	if h.remoteCreationCatalogFetcher != nil {
		fetchCatalog = h.remoteCreationCatalogFetcher
	}
	return stampRemoteFetch(gen, fetchCatalog(remoteName))
}

func persistClaudeDialogDefaults(opts *session.ClaudeOptions, args []string) {
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil || opts == nil {
		return
	}
	cleaned := make([]string, 0, len(args))
	for _, arg := range args {
		if tok := strings.TrimSpace(arg); tok != "" {
			cleaned = append(cleaned, tok)
		}
	}
	if len(cleaned) == 0 {
		cfg.Claude.ExtraArgs = nil
	} else {
		cfg.Claude.ExtraArgs = cleaned
	}
	cfg.Claude.DangerousMode = &opts.SkipPermissions
	cfg.Claude.AllowDangerousMode = opts.AllowSkipPermissions
	cfg.Claude.AutoMode = opts.AutoMode
	cfg.Claude.UseChrome = opts.UseChrome
	cfg.Claude.UseTeammateMode = opts.UseTeammateMode
	_ = session.SaveUserConfig(cfg)
}

// handleConfirmDialogKey handles keys when confirmation dialog is visible
func (h *Home) handleConfirmDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Let the dialog handle arrow/tab navigation first.
	h.confirmDialog.Update(msg)

	switch h.confirmDialog.GetConfirmType() {
	case ConfirmQuitWithPool:
		switch msg.String() {
		case "k", "K":
			h.confirmDialog.Hide()
			h.isQuitting = true
			return h, h.performQuit(false)
		case "s", "S":
			h.confirmDialog.Hide()
			h.isQuitting = true
			return h, h.performQuit(true)
		case "enter":
			// Activate focused button: 0=keep, 1=shutdown
			shutdown := h.confirmDialog.GetFocusedButton() == 1
			h.confirmDialog.Hide()
			h.isQuitting = true
			return h, h.performQuit(shutdown)
		case "esc":
			h.confirmDialog.Hide()
			h.isQuitting = false
			return h, nil
		}
		return h, nil

	case ConfirmCreateDirectory:
		switch msg.String() {
		case "y", "Y":
			return h, h.confirmCreateDirectory()
		case "enter":
			if h.confirmDialog.GetFocusedButton() == 0 {
				return h, h.confirmCreateDirectory()
			}
			h.pendingRemoteCreate = nil
			h.confirmDialog.Hide()
			return h, nil
		case "n", "N", "esc":
			h.pendingRemoteCreate = nil
			h.confirmDialog.Hide()
			return h, nil
		}
		return h, nil

	case ConfirmNotice:
		// Acknowledge-only: any of the usual dismiss keys closes it.
		switch msg.String() {
		case "enter", "esc", "o", "O", "y", "Y", "n", "N", " ":
			h.confirmDialog.Hide()
			return h, nil
		}
		return h, nil

	case ConfirmInstallHooks, ConfirmInstallHermesHooks:
		switch msg.String() {
		case "y", "Y":
			if h.confirmDialog.GetConfirmType() == ConfirmInstallHermesHooks {
				return h, h.confirmInstallHermesHooks()
			}
			return h, h.confirmInstallHooks()
		case "enter":
			if h.confirmDialog.GetFocusedButton() == 0 {
				if h.confirmDialog.GetConfirmType() == ConfirmInstallHermesHooks {
					return h, h.confirmInstallHermesHooks()
				}
				return h, h.confirmInstallHooks()
			}
			if h.confirmDialog.GetConfirmType() == ConfirmInstallHermesHooks {
				return h, h.declineInstallHermesHooks()
			}
			return h, h.declineInstallHooks()
		case "n", "N", "esc":
			if h.confirmDialog.GetConfirmType() == ConfirmInstallHermesHooks {
				return h, h.declineInstallHermesHooks()
			}
			return h, h.declineInstallHooks()
		}
		return h, nil

	default:
		// Handle delete/close confirmations (session/group/remote)
		switch msg.String() {
		case "y", "Y":
			return h, h.confirmAction()
		case "enter":
			if h.confirmDialog.GetFocusedButton() == 0 {
				return h, h.confirmAction()
			}
			h.dismissConfirmDialog()
			return h, nil
		case "n", "N", "esc":
			h.dismissConfirmDialog()
			return h, nil
		}
	}

	return h, nil
}

// dismissConfirmDialog closes a y/n confirmation without acting. A declined
// same-harness account switch returns to the Edit Session dialog, which is
// still open underneath, with focus on the account row and nothing written.
func (h *Home) dismissConfirmDialog() {
	switchDeclined := h.confirmDialog.GetConfirmType() == ConfirmSwitchAccount
	h.confirmDialog.Hide()
	if switchDeclined && h.editSessionDialog.IsVisible() {
		h.editSessionDialog.FocusField(session.FieldAccount)
	}
}

// confirmAction executes the confirmed destructive action.
func (h *Home) confirmAction() tea.Cmd {
	// A switch confirmation opened for a remote row runs on that remote.
	if h.confirmDialog.GetRemoteName() != "" {
		switch h.confirmDialog.GetConfirmType() {
		case ConfirmSwitchAccount, ConfirmCrossHarnessTransfer:
			return h.confirmRemoteSwitch()
		}
	}
	switch h.confirmDialog.GetConfirmType() {
	case ConfirmSwitchAccount:
		sessionID, harness, account := h.confirmDialog.GetTargetID(), h.confirmDialog.TargetHarness(), h.confirmDialog.TargetAccount()
		h.instancesMu.RLock()
		inst := h.instanceByID[sessionID]
		unchanged := h.confirmDialog.CrossHarnessSourceMatches(inst)
		h.instancesMu.RUnlock()
		if !unchanged {
			h.confirmDialog.ShowNotice("Account switch cancelled", "The source session changed while this confirmation was open. Review the current session and confirm a new switch.")
			return nil
		}
		h.confirmDialog.Hide()
		h.editSessionDialog.Hide()
		h.resumingSessions[sessionID] = time.Now()
		return h.switchSessionHarness(sessionID, harness, account)
	case ConfirmArchiveDestinationSwitch:
		sessionID, harness, account := h.confirmDialog.GetTargetID(), h.confirmDialog.TargetHarness(), h.confirmDialog.TargetAccount()
		h.instancesMu.RLock()
		inst := h.instanceByID[sessionID]
		unchanged := h.confirmDialog.CrossHarnessSourceMatches(inst)
		h.instancesMu.RUnlock()
		if !unchanged {
			h.confirmDialog.ShowNotice("Account switch cancelled", "The source session changed while this confirmation was open. Review the current session and confirm a new switch.")
			return nil
		}
		h.confirmDialog.Hide()
		h.resumingSessions[sessionID] = time.Now()
		return h.switchSessionHarnessWithOptions(sessionID, harness, account, true)
	case ConfirmCrossHarnessTransfer:
		sessionID, harness, account := h.confirmDialog.GetTargetID(), h.confirmDialog.TargetHarness(), h.confirmDialog.TargetAccount()
		h.instancesMu.RLock()
		inst := h.instanceByID[sessionID]
		unchanged := h.confirmDialog.CrossHarnessSourceMatches(inst)
		h.instancesMu.RUnlock()
		if !unchanged {
			h.confirmDialog.ShowNotice("Harness transfer cancelled", "The source session changed while this confirmation was open. Review the current session and confirm a new transfer.")
			return nil
		}
		h.confirmDialog.Hide()
		h.resumingSessions[sessionID] = time.Now()
		return h.switchSessionHarness(sessionID, harness, account)
	case ConfirmDeleteSession:
		sessionID := h.confirmDialog.GetTargetID()
		if inst := h.getInstanceByID(sessionID); inst != nil {
			h.confirmDialog.Hide()
			return h.deleteSession(inst)
		}
	case ConfirmKillWindow:
		sessionID := h.confirmDialog.GetTargetID()
		windowIndex := h.confirmDialog.GetWindowIndex()
		windowID := h.confirmDialog.GetWindowID()
		windowName := h.confirmDialog.GetWindowName()
		h.confirmDialog.Hide()
		if inst := h.getInstanceByID(sessionID); inst != nil {
			if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
				// The dialog showed the id and name of one cached row, but
				// windows can change before the user answers: the other
				// window can close (last-window guard), or the selected
				// window can close or be renamed (identity guard). KillWindow
				// re-reads the live window by id, checks both, and kills
				// atomically server-side; never by index or name alone.
				if err := tmuxSess.KillWindow(windowID, windowName); err != nil {
					label := fmt.Sprintf("window %d (%s) %q", windowIndex, windowID, windowName)
					switch {
					case errors.Is(err, tmux.ErrLastWindow):
						err = fmt.Errorf("not killing %s: it is the session's last window", label)
					case errors.Is(err, tmux.ErrWindowChanged):
						err = fmt.Errorf("not killing %s: it changed since it was selected, refusing", label)
					default:
						err = fmt.Errorf("kill %s: %w", label, err)
					}
					h.setError(err)
					// The footer error can be clamped off the bottom of a
					// full viewport, so a refusal there is invisible; keep
					// it in the modal the user is already looking at.
					h.confirmDialog.ShowKillWindowRefused(err.Error())
					return nil
				}
				// Refresh the whole window cache for this session so the row
				// disappears now rather than on the next background tick, and
				// so does any other row closed externally meanwhile. If the
				// re-query fails, prune just the killed window.
				if err := tmux.RefreshCachedWindows(tmuxSess); err != nil {
					tmux.RemoveCachedWindow(tmuxSess.Name, windowID)
				}
				h.rebuildFlatItems()
			}
		}
	case ConfirmCloseSession:
		sessionID := h.confirmDialog.GetTargetID()
		if inst := h.getInstanceByID(sessionID); inst != nil {
			h.confirmDialog.Hide()
			return h.closeSession(inst)
		}
	case ConfirmArchiveSession:
		sessionID := h.confirmDialog.GetTargetID()
		if inst := h.getInstanceByID(sessionID); inst != nil {
			h.confirmDialog.Hide()
			return h.archiveSession(inst)
		}
	case ConfirmUnarchiveSession:
		sessionID := h.confirmDialog.GetTargetID()
		if inst := h.getInstanceByID(sessionID); inst != nil {
			h.confirmDialog.Hide()
			return h.unarchiveSession(inst)
		}
	case ConfirmDeleteGroup:
		groupPath := h.confirmDialog.GetTargetID()
		h.groupTree.DeleteGroup(groupPath)

		h.instancesMu.Lock()
		h.instances = h.groupTree.GetAllInstances()
		h.instancesMu.Unlock()
		h.rebuildFlatItems()
		h.saveInstances()
	case ConfirmDeleteRemoteSession:
		sessionID := h.confirmDialog.GetTargetID()
		remoteName := h.confirmDialog.GetRemoteName()
		title := h.confirmDialog.targetName
		h.confirmDialog.Hide()
		return h.deleteRemoteSession(remoteName, sessionID, title)
	case ConfirmDeleteRemoteGroup:
		groupPath := h.confirmDialog.GetTargetID()
		remoteName := h.confirmDialog.GetRemoteName()
		h.confirmDialog.Hide()
		return h.deleteRemoteGroup(groupPath, remoteName)
	case ConfirmCloseRemoteSession:
		sessionID := h.confirmDialog.GetTargetID()
		remoteName := h.confirmDialog.GetRemoteName()
		title := h.confirmDialog.targetName
		h.confirmDialog.Hide()
		return h.closeRemoteSession(remoteName, sessionID, title)
	case ConfirmArchiveRemoteSession, ConfirmUnarchiveRemoteSession:
		sessionID := h.confirmDialog.GetTargetID()
		remoteName := h.confirmDialog.GetRemoteName()
		title := h.confirmDialog.targetName
		archive := h.confirmDialog.GetConfirmType() == ConfirmArchiveRemoteSession
		h.confirmDialog.Hide()
		return h.setRemoteSessionArchived(remoteName, sessionID, title, archive)
	case ConfirmUpdateRemote:
		remoteName := h.confirmDialog.GetRemoteName()
		from := h.confirmDialog.targetName
		to := h.confirmDialog.GetTargetID()
		h.confirmDialog.Hide()
		h.setError(fmt.Errorf("updating remote %s to v%s…", remoteName, to))
		return h.updateRemote(remoteName, from, to)
	case ConfirmRemoveSession:
		sessionID := h.confirmDialog.GetTargetID()
		if inst := h.getInstanceByID(sessionID); inst != nil {
			h.confirmDialog.Hide()
			return h.removeSession(inst)
		}
	case ConfirmBulkRemoveErrored:
		h.confirmDialog.Hide()
		return h.bulkRemoveErrored()
	}
	h.confirmDialog.Hide()
	return nil
}

// confirmCreateDirectory handles the "yes" action for ConfirmCreateDirectory.
func (h *Home) confirmCreateDirectory() tea.Cmd {
	if pending := h.pendingRemoteCreate; pending != nil {
		h.pendingRemoteCreate = nil
		h.confirmDialog.Hide()
		opts := pending.opts
		opts.CreateDir = true
		create := h.createRemoteSessionWithOptions
		if h.remoteCreateSink != nil {
			create = h.remoteCreateSink
		}
		return create(pending.remoteName, opts)
	}
	name, path, command, groupPath, pendingToolOpts, pendingExtraArgs, pendingStartQuery, pendingAccount, pendingLaunchModelID, parentSessionID, parentProjectPath := h.confirmDialog.GetPendingSession()
	h.confirmDialog.Hide()
	if err := os.MkdirAll(path, 0o755); err != nil {
		h.setError(fmt.Errorf("failed to create directory: %w", err))
		return nil
	}
	return h.createSessionInGroupWithWorktreeAndOptions(
		name,
		path,
		"",
		command,
		groupPath,
		"",
		"",
		"",
		false,
		false,
		pendingToolOpts,
		pendingExtraArgs,
		pendingStartQuery,
		pendingAccount,
		pendingLaunchModelID,
		false,
		nil,
		parentSessionID,
		parentProjectPath,
		"",    // no placeholder — non-worktree sessions are fast
		false, // not auto-named
	)
}

// logUsageFeedResults logs what the accounts usage feed heal changed.
func logUsageFeedResults(results []session.UsageFeedResult) {
	for _, r := range results {
		switch {
		case r.Err != nil:
			uiLog.Warn("claude_usage_feed_heal_failed", slog.String("slot", r.Slot), slog.String("error", r.Err.Error()))
		case r.Changed:
			uiLog.Info("claude_usage_feed_wired", slog.String("slot", r.Slot), slog.String("command", r.Feed.Command))
		}
	}
}

// confirmInstallHooks handles the "yes" action for ConfirmInstallHooks.
func (h *Home) confirmInstallHooks() tea.Cmd {
	h.confirmDialog.Hide()
	h.pendingHooksPrompt = false
	configDir := session.GetClaudeConfigDir()
	if _, err := session.InjectClaudeHooks(configDir); err != nil {
		uiLog.Warn("hook_install_failed", slog.String("error", err.Error()))
	} else {
		uiLog.Info("claude_hooks_installed", slog.String("config_dir", configDir))
	}
	if config, err := session.LoadUserConfig(); err == nil {
		logUsageFeedResults(session.HealUsageFeeds(config))
	}
	// Start the status file watcher
	hookWatcher, err := session.NewStatusFileWatcher(nil)
	if err != nil {
		uiLog.Warn("hook_watcher_init_failed", slog.String("error", err.Error()))
	} else {
		h.hookWatcher = hookWatcher
		go hookWatcher.Start()
	}
	// Remember user's choice
	if db := statedb.GetGlobal(); db != nil {
		_ = db.SetMeta("hooks_prompted", "accepted")
	}
	h.showPendingHooksPrompt()
	return nil
}

// declineInstallHooks handles the "no" action for ConfirmInstallHooks.
func (h *Home) declineInstallHooks() tea.Cmd {
	h.confirmDialog.Hide()
	h.pendingHooksPrompt = false
	// Remember user declined
	if db := statedb.GetGlobal(); db != nil {
		_ = db.SetMeta("hooks_prompted", "declined")
	}
	h.showPendingHooksPrompt()
	return nil
}

func (h *Home) showPendingHooksPrompt() {
	if h.pendingHooksPrompt {
		h.confirmDialog.ShowInstallHooks()
	} else if h.pendingHermesHooksPrompt {
		h.confirmDialog.ShowInstallHermesHooks(filepath.Join(session.GetHermesConfigDir(), "config.yaml"), session.HermesHookEventsForInstall())
	} else {
		return
	}
	h.confirmDialog.SetSize(h.width, h.height)
}

func (h *Home) confirmInstallHermesHooks() tea.Cmd {
	h.confirmDialog.Hide()
	h.pendingHermesHooksPrompt = false
	configDir := session.GetHermesConfigDir()
	if _, err := session.InjectHermesHooks(configDir); err != nil {
		uiLog.Warn("hermes_hooks_install_failed", slog.String("error", err.Error()))
		return nil
	}
	if db := statedb.GetGlobal(); db != nil {
		_ = db.SetMeta("hermes_hooks_prompted", "accepted")
	}
	if h.hookWatcher == nil {
		if hookWatcher, err := session.NewStatusFileWatcher(nil); err != nil {
			uiLog.Warn("hook_watcher_init_failed", slog.String("error", err.Error()))
		} else {
			h.hookWatcher = hookWatcher
			go hookWatcher.Start()
		}
	}
	h.showPendingHooksPrompt()
	return nil
}

func (h *Home) declineInstallHermesHooks() tea.Cmd {
	h.confirmDialog.Hide()
	h.pendingHermesHooksPrompt = false
	if db := statedb.GetGlobal(); db != nil {
		_ = db.SetMeta("hermes_hooks_prompted", "declined")
	}
	h.showPendingHooksPrompt()
	return nil
}

// handleMCPDialogKey handles keys when MCP dialog is visible
func (h *Home) handleMCPDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// DEBUG: Log entry point
		mcpUILog.Debug("dialog_enter_pressed")

		// Apply changes and close dialog
		hasChanged := h.mcpDialog.HasChanged()
		mcpUILog.Debug("dialog_has_changed", slog.Bool("changed", hasChanged))

		if hasChanged {
			// Apply changes (saves state + writes .mcp.json)
			if err := h.mcpDialog.Apply(); err != nil {
				mcpUILog.Debug("dialog_apply_failed", slog.String("error", err.Error()))
				h.setError(err)
				h.mcpDialog.Hide() // Hide dialog even on error
				return h, nil
			}
			mcpUILog.Debug("dialog_apply_succeeded")

			// Find the session by ID (stored when dialog opened - same as Shift+S uses)
			sessionID := h.mcpDialog.GetSessionID()
			mcpUILog.Debug("dialog_looking_for_session", slog.String("session_id", sessionID))

			// O(1) lookup - no lock needed as Update() runs on main goroutine
			targetInst := h.getInstanceByID(sessionID)
			if targetInst != nil {
				mcpUILog.Debug(
					"dialog_session_found",
					slog.String("session_id", targetInst.ID),
					slog.String("title", targetInst.Title),
				)
			}

			if targetInst != nil {
				mcpUILog.Debug("dialog_restarting_session", slog.String("session_id", targetInst.ID))
				// Track as MCP loading for animation in preview pane
				h.mcpLoadingSessions[targetInst.ID] = time.Now()
				// Set flag to skip MCP regeneration (Apply just wrote the config)
				targetInst.SkipMCPRegenerate = true
				// Restart the session to apply MCP changes
				h.mcpDialog.Hide()
				return h, h.restartSession(targetInst)
			} else {
				mcpUILog.Debug("dialog_session_not_found", slog.String("session_id", sessionID))
			}
		}
		mcpUILog.Debug("dialog_hiding_without_restart")
		h.mcpDialog.Hide()
		return h, nil

	case "esc":
		h.mcpDialog.Hide()
		return h, nil

	default:
		h.mcpDialog.Update(msg)
		return h, nil
	}
}

// handlePluginDialogKey routes key events to the plugin manager dialog.
// Apply path: persist via session.SetField(FieldPlugins,...) and restart
// the session to reload claude's enabledPlugins from the per-session
// scratch settings.json. RFC: docs/rfc/PLUGIN_ATTACH.md.
func (h *Home) handlePluginDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// Persist if anything changed; otherwise just close.
		if !h.pluginDialog.HasChanged() {
			h.pluginDialog.Hide()
			return h, nil
		}
		sessionID := h.pluginDialog.GetSessionID()
		newNames := h.pluginDialog.SelectedPluginNames()

		var targetInst *session.Instance
		h.instancesMu.RLock()
		for _, inst := range h.instances {
			if inst.ID == sessionID {
				targetInst = inst
				break
			}
		}
		h.instancesMu.RUnlock()
		if targetInst == nil {
			h.pluginDialog.Hide()
			return h, nil
		}

		oldValue, _, mutErr := session.SetField(targetInst, session.FieldPlugins, strings.Join(newNames, ","), nil)
		if mutErr != nil {
			h.setError(mutErr)
			return h, nil
		}
		_ = oldValue
		h.forceSaveInstances()
		h.pluginDialog.Hide()

		if targetInst.CanRestart() && !h.hasActiveAnimation(targetInst.ID) {
			return h, h.restartSession(targetInst)
		}
		return h, nil

	case "esc":
		h.pluginDialog.Hide()
		return h, nil

	default:
		h.pluginDialog.Update(msg)
		return h, nil
	}
}

// handleEditPathsDialogKey handles key events for the edit paths dialog.
func (h *Home) handleEditPathsDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if h.editPathsDialog.IsEditing() {
			h.editPathsDialog.Update(msg)
			return h, nil
		}
		// Confirm changes
		if h.editPathsDialog.HasChanged() {
			if errMsg := h.editPathsDialog.Validate(); errMsg != "" {
				h.editPathsDialog.validationErr = errMsg
				return h, nil
			}
			newPaths := h.editPathsDialog.GetPaths()
			sessionID := h.editPathsDialog.GetSessionID()
			h.editPathsDialog.Hide()
			inst := h.getInstanceByID(sessionID)
			if inst != nil {
				return h, h.applyMultiRepoPathChanges(inst, newPaths)
			}
		}
		h.editPathsDialog.Hide()
		return h, nil
	case "esc":
		if h.editPathsDialog.IsEditing() {
			h.editPathsDialog.Update(msg)
			return h, nil
		}
		h.editPathsDialog.Hide()
		return h, nil
	default:
		h.editPathsDialog.Update(msg)
		return h, nil
	}
}

func (h *Home) handleEditSessionDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if errMsg := h.editSessionDialog.Validate(); errMsg != "" {
			h.editSessionDialog.SetError(errMsg)
			return h, nil
		}
		if h.editSessionDialog.IsRemote() {
			return h, h.commitRemoteEditSession()
		}

		sessionID := h.editSessionDialog.SessionID()
		inst := h.getInstanceByID(sessionID)
		if inst == nil {
			h.editSessionDialog.Hide()
			return h, nil
		}

		changes := h.editSessionDialog.GetChanges(inst)
		fields := make([]string, len(changes))
		for i, c := range changes {
			fields[i] = c.Field
		}
		uiLog.Debug("edit_session_commit",
			slog.String("session_id", sessionID),
			slog.Any("fields", fields))
		if len(changes) == 0 {
			h.editSessionDialog.Hide()
			return h, nil
		}

		// The account slot is NOT a plain field write: moving a session to
		// another Claude login has to carry its conversation into that
		// account's config dir, or the restarted `claude --resume` finds no
		// conversation. It is pulled out here and handed to
		// session.SwitchAccount (the same flow as `agent-deck session
		// switch-account`) once the ordinary field writes are persisted.
		// Clearing the slot back to "inherit" is NOT a switch: there is no
		// target config dir to migrate into, and SetField already treats an
		// empty value as "drop the override" (back to the conductor/group/env
		// chain). Only a named target goes through the switch flow; the empty
		// value stays in the ordinary field loop below.
		accountSwitch := ""
		switchHarness := ""
		switchAccount := false
		for _, c := range changes {
			if c.Field == session.FieldAccount && strings.TrimSpace(c.Value) != "" {
				accountSwitch, switchAccount = c.Value, true
			}
			if c.Field == session.FieldTool && strings.TrimSpace(c.Value) != "" && session.CanonicalToolName(c.Value) != session.CanonicalToolName(inst.Tool) {
				switchHarness = c.Value
			}
		}
		// Harness/account changes are one transactional operation. Do not apply
		// unrelated edits first: a failed destination must leave title, settings,
		// cwd, group and routing untouched.
		if switchAccount || switchHarness != "" {
			for _, c := range changes {
				if c.Field != session.FieldAccount && c.Field != session.FieldTool {
					h.editSessionDialog.SetError("save harness/account switch separately from other edits")
					return h, nil
				}
			}
			if switchHarness == "" {
				switchHarness = inst.Tool
			}
			// A different harness is necessarily a bounded, lossy export. Show
			// the same fidelity disclosure as the CLI preview and require an
			// explicit confirmation before any target lifecycle operation.
			if session.CanonicalSwitchHarnessForUI(switchHarness) != session.CanonicalSwitchHarnessForUI(inst.Tool) {
				cfg, _ := session.LoadUserConfig()
				sourceSnapshot := h.switchSourceSnapshot(inst)
				preview := session.PreviewSwitchWithMaxBytesAndSnapshot(cfg, inst, session.SwitchPreviewTarget{Harness: switchHarness, Account: accountSwitch}, session.DefaultHandoffMaxChars, &sourceSnapshot)
				if preview.Refusal != nil {
					h.confirmDialog.ShowNotice("Harness transfer refused", preview.Refusal.Message)
					return h, nil
				}
				h.editSessionDialog.Hide()
				h.confirmDialog.ShowCrossHarnessTransfer(inst, switchHarness, accountSwitch, preview.Fidelity.Exclusions)
				return h, nil
			}
			// Same harness, other account: also a stop + conversation move +
			// restart, so ask once (the cross-harness branch above already
			// does). The dialog stays open underneath; Switch runs it,
			// Cancel returns to the account row with nothing written.
			h.confirmDialog.SetSize(h.width, h.height)
			h.confirmDialog.ShowSwitchAccount(inst, switchHarness, accountSwitch)
			return h, nil
		}

		// Apply Tool last so claude-only validation (Skip/Auto/ExtraArgs)
		// sees the pre-edit Tool — otherwise Tool=claude→shell with a
		// Skip toggle in the same submit fails IsClaudeCompatible on
		// the toggle.
		orderedChanges := make([]Change, 0, len(changes))
		for _, c := range changes {
			if c.Field != session.FieldTool && !(c.Field == session.FieldAccount && switchAccount) {
				orderedChanges = append(orderedChanges, c)
			}
		}
		for _, c := range changes {
			if c.Field == session.FieldTool {
				orderedChanges = append(orderedChanges, c)
			}
		}

		titleChanged := false
		hadRestartRequired := false
		var postCommits []func()
		h.instancesMu.Lock()
		for _, c := range orderedChanges {
			_, postCommit, err := session.SetField(inst, c.Field, c.Value, nil)
			if err != nil {
				h.instancesMu.Unlock()
				h.editSessionDialog.SetError(err.Error())
				return h, nil
			}
			if postCommit != nil {
				postCommits = append(postCommits, postCommit)
			}
			if c.Field == session.FieldTitle {
				titleChanged = true
			}
			if !c.IsLive {
				hadRestartRequired = true
			}
		}
		h.instancesMu.Unlock()
		// postCommits run AFTER unlocking so slow tmux subprocesses don't
		// stall the status worker / preview cache / reconciler.
		for _, fn := range postCommits {
			fn()
		}

		// Mirror the rename-path #697 race fix: queue title so a watcher
		// reload can re-apply it after the load swap.
		if titleChanged {
			h.pendingTitleChanges[sessionID] = pendingTitle{title: inst.Title, locked: inst.TitleLocked}
			h.invalidatePreviewCache(sessionID)
		}
		h.rebuildFlatItems()
		// forceSaveInstances bypasses the isReloading no-op in
		// saveInstances. Title-only loss is caught by pendingTitleChanges
		// re-application; non-Title fields have no such net.
		h.forceSaveInstances()

		h.editSessionDialog.Hide()

		// An account change owns the restart: SwitchAccount stops the
		// session, migrates the conversation and starts it again, so the
		// generic restart below would either double-start it or race the
		// migration. Every other edit in the same submit is already saved
		// above, so the switch's restart picks those up too.
		if switchAccount {
			h.resumingSessions[sessionID] = time.Now()
			return h, h.switchSessionAccount(sessionID, accountSwitch)
		}

		// Auto-restart on restart-required edits — Tool/Skip/Auto/ExtraArgs
		// only take effect on next launch, so deferring would just leave
		// the user staring at old behavior. Mirrors the manual `R` path,
		// skipping when an animation is in flight or CanRestart says no.
		if hadRestartRequired {
			if h.hasActiveAnimation(sessionID) {
				uiLog.Debug("edit_session_skip_restart_active_anim", slog.String("session_id", sessionID))
				h.setError(fmt.Errorf("saved — restart skipped, session is starting; press R when ready"))
				return h, nil
			}
			if !inst.CanRestart() {
				uiLog.Debug("edit_session_skip_restart_cannot", slog.String("session_id", sessionID))
				h.setError(fmt.Errorf("saved — start the session to apply tool/extra-args/permission changes"))
				return h, nil
			}
			uiLog.Debug("edit_session_auto_restart", slog.String("session_id", sessionID))
			h.resumingSessions[sessionID] = time.Now()
			return h, h.restartSession(inst)
		}
		return h, nil

	case "esc":
		h.editSessionDialog.Hide()
		return h, nil

	default:
		var cmd tea.Cmd
		h.editSessionDialog, cmd = h.editSessionDialog.Update(msg)
		return h, cmd
	}
}

// applyMultiRepoPathChanges updates the symlink directory and restarts the session.
func (h *Home) applyMultiRepoPathChanges(inst *session.Instance, newPaths []string) tea.Cmd {
	id := inst.ID
	return func() tea.Msg {
		h.instancesMu.RLock()
		current := h.instanceByID[id]
		h.instancesMu.RUnlock()
		if current == nil {
			return sessionRestartedMsg{sessionID: id, err: fmt.Errorf("session no longer exists")}
		}

		tempDir := current.MultiRepoTempDir
		if tempDir == "" {
			return sessionRestartedMsg{sessionID: id, err: fmt.Errorf("no multi-repo temp dir")}
		}

		// Remove all existing symlinks/entries in tempDir
		entries, _ := os.ReadDir(tempDir)
		for _, entry := range entries {
			_ = os.RemoveAll(filepath.Join(tempDir, entry.Name()))
		}

		// Create new symlinks
		dirnames := session.DeduplicateDirnames(newPaths)
		var newProjectPath string
		var newAdditionalPaths []string
		for i, p := range newPaths {
			linkPath := filepath.Join(tempDir, dirnames[i])
			_ = os.Symlink(p, linkPath)
			if i == 0 {
				newProjectPath = linkPath
			} else {
				newAdditionalPaths = append(newAdditionalPaths, linkPath)
			}
		}

		// Update instance fields under write lock to avoid races with
		// the background status worker that reads via instanceByID.
		h.instancesMu.Lock()
		current.ProjectPath = newProjectPath
		current.AdditionalPaths = newAdditionalPaths
		if current.GetTmuxSession() != nil {
			current.GetTmuxSession().WorkDir = tempDir
		}
		h.instancesMu.Unlock()

		h.saveInstances()

		err := current.Restart()
		return sessionRestartedMsg{sessionID: id, err: err}
	}
}

// multiRepoWorktreesRoot resolves the persistent parent directory for
// multi-repo worktrees and symlink trees. The returned path is recorded in
// session state (Instance.MultiRepoTempDir, worktree paths), so it MUST be a
// stable, non-ephemeral location. We therefore propagate any resolution error
// to the caller rather than falling back to os.TempDir(): worktree/symlink
// state placed under temp storage would be silently removed by a reboot or
// tmp cleanup, breaking the affected sessions (Codex round-2 P1).
func multiRepoWorktreesRoot() (string, error) {
	dir, err := agentpaths.EffectiveDataPath("multi-repo-worktrees", "multi-repo-worktrees")
	if err != nil {
		return "", fmt.Errorf("resolve multi-repo worktrees root: %w", err)
	}
	return dir, nil
}

// handleSkillDialogKey handles keys when Skills dialog is visible
func (h *Home) handleSkillDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		needsApply := h.skillDialog.NeedsApply()
		if needsApply {
			if err := h.skillDialog.Apply(); err != nil {
				h.setError(err)
				h.skillDialog.Hide()
				return h, nil
			}

			sessionID := h.skillDialog.GetSessionID()
			targetInst := h.getInstanceByID(sessionID)
			if targetInst != nil && session.ShouldRestartProjectSkills(targetInst.Tool) {
				h.skillDialog.Hide()
				return h, h.restartSession(targetInst)
			}
		}
		h.skillDialog.Hide()
		return h, nil

	case "esc":
		h.skillDialog.Hide()
		return h, nil

	default:
		h.skillDialog.Update(msg)
		return h, nil
	}
}

// groupOpKind identifies a group mutation recorded for reload-race recovery.
type groupOpKind int

const (
	groupOpCreate groupOpKind = iota
	groupOpCreateSub
	groupOpRename
	groupOpMove
)

// pendingGroupOp records a group create/rename/move so it can be re-applied if
// the save-abort → reload race discards it (see Home.pendingGroupOps).
type pendingGroupOp struct {
	kind        groupOpKind
	name        string // new/target group name (create, createSub, rename)
	parentPath  string // parent group path (createSub)
	oldPath     string // group path being renamed (rename)
	defaultPath string // optional default path captured in the dialog (create, createSub)
	sessionID   string // session being moved (move)
	targetPath  string // destination group path (move)
	// maxConcurrent is the [group_defaults].max_concurrent default seeded at
	// record time (create/createSub), re-seeded on reapply because the
	// reloaded tree loses DefaultMaxConcurrent. Pointer (nil = unset) to match
	// GroupTree.DefaultMaxConcurrent.
	maxConcurrent *int
}

// reapplyPendingGroupOps re-applies group mutations that a reload race may have
// discarded, on top of the freshly-reloaded (external) tree. It returns true if
// any op actually changed the tree, so the caller force-saves. Each op is
// idempotent: create/createSub return the existing group, rename no-ops when
// its old path is gone, and move no-ops when the session already sits in the
// target — so a lingering op (save succeeded, cleared only on the next reload)
// causes no duplicate or spurious save. Mirrors the pendingTitleChanges reapply.
func (h *Home) reapplyPendingGroupOps() bool {
	if len(h.pendingGroupOps) == 0 {
		return false
	}
	// reapply temporarily overrides DefaultMaxConcurrent to honour each op's
	// captured value; restore it afterwards so we don't leave transient state
	// on the shared tree for later callers.
	savedDefaultMaxConcurrent := h.groupTree.DefaultMaxConcurrent
	defer func() { h.groupTree.DefaultMaxConcurrent = savedDefaultMaxConcurrent }()

	applied := false
	pending := make([]pendingGroupOp, 0, len(h.pendingGroupOps))
	for _, op := range h.pendingGroupOps {
		opApplied := false
		switch op.kind {
		case groupOpCreate:
			h.groupTree.DefaultMaxConcurrent = op.maxConcurrent
			before := h.groupTree.GroupCount()
			g := h.groupTree.CreateGroup(op.name)
			if g != nil && h.groupTree.GroupCount() > before {
				if op.defaultPath != "" {
					h.groupTree.SetDefaultPathForGroup(g.Path, op.defaultPath)
				}
				opApplied = true
				uiLog.Info("pending_group_reapplied", slog.String("kind", "create"), slog.String("name", op.name))
			}
		case groupOpCreateSub:
			h.groupTree.DefaultMaxConcurrent = op.maxConcurrent
			before := h.groupTree.GroupCount()
			g := h.groupTree.CreateSubgroup(op.parentPath, op.name)
			if g != nil && h.groupTree.GroupCount() > before {
				if op.defaultPath != "" {
					h.groupTree.SetDefaultPathForGroup(g.Path, op.defaultPath)
				}
				opApplied = true
				uiLog.Info("pending_group_reapplied", slog.String("kind", "createSub"), slog.String("name", op.name))
			}
		case groupOpRename:
			if _, exists := h.groupTree.Groups[op.oldPath]; exists {
				// Collision guard: if the reloaded (external) tree already has a
				// DIFFERENT group at the rename's target path, skip. Renaming
				// would overwrite that group's map entry and silently orphan its
				// sessions (they'd vanish from GetAllInstances and then be
				// force-saved away). Better to drop our rename than lose data.
				target := h.groupTree.RenameTargetPath(op.oldPath, op.name)
				if _, collision := h.groupTree.Groups[target]; collision && target != op.oldPath {
					uiLog.Warn("pending_group_rename_skipped_collision",
						slog.String("old_path", op.oldPath), slog.String("target", target))
					continue
				}
				if err := h.groupTree.RenameGroup(op.oldPath, op.name); err != nil {
					uiLog.Warn("pending_group_rename_failed",
						slog.String("old_path", op.oldPath), slog.String("name", op.name), slog.String("err", err.Error()))
					continue
				}
				h.instancesMu.Lock()
				h.instances = h.groupTree.GetAllInstances()
				h.instancesMu.Unlock()
				opApplied = true
				uiLog.Info("pending_group_reapplied", slog.String("kind", "rename"), slog.String("old_path", op.oldPath), slog.String("name", op.name))
			}
		case groupOpMove:
			if inst := h.getInstanceByID(op.sessionID); inst != nil && inst.GroupPath != op.targetPath {
				h.groupTree.MoveSessionToGroup(inst, op.targetPath)
				h.instancesMu.Lock()
				h.instances = h.groupTree.GetAllInstances()
				h.instancesMu.Unlock()
				opApplied = true
				uiLog.Info("pending_group_reapplied", slog.String("kind", "move"), slog.String("session_id", op.sessionID), slog.String("target", op.targetPath))
			}
		}
		if opApplied {
			pending = append(pending, op)
			applied = true
		}
	}
	h.pendingGroupOps = pending
	return applied
}

// handleGroupDialogKey handles keys when group dialog is visible
func (h *Home) handleGroupDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// Enter with a highlighted path suggestion accepts it instead of
		// submitting the form; the dropdown closes so the next Enter submits.
		if h.groupDialog.ApplyHighlightedPathSuggestion() {
			return h, nil
		}
		// Validate before proceeding
		if validationErr := h.groupDialog.Validate(); validationErr != "" {
			h.groupDialog.SetError(validationErr)
			return h, nil
		}
		h.clearError() // Clear any previous validation error

		// remoteCmd is set when the confirmed action runs over SSH against a
		// remote's own state DB (remote-session move, remote group create) and
		// reports back via remoteMoveResultMsg / remoteGroupResultMsg.
		var remoteCmd tea.Cmd
		// telCmd counts a completed local action in feature.daily.
		var telCmd tea.Cmd

		switch h.groupDialog.Mode() {
		case GroupDialogCreate:
			name := h.groupDialog.GetValue()
			if name != "" {
				if remoteName := h.groupDialog.RemoteName(); remoteName != "" {
					// g was pressed on a REMOTE row: the group must be created
					// in the remote's own state DB, not the local tree. Run
					// `agent-deck group create` there over SSH (mirrors the
					// remote-move and remote-rename paths); the local group
					// path cache updates when the remote confirms via
					// remoteGroupResultMsg.
					parentPath := ""
					if h.groupDialog.HasParent() {
						parentPath = h.groupDialog.GetParentPath()
					}
					remoteCmd = h.createRemoteGroup(name, remoteName, parentPath, h.groupDialog.GetDefaultPath())
					break
				}
				// Seed the new-group default from [group_defaults].max_concurrent.
				if cfg, _ := session.LoadUserConfig(); cfg != nil {
					h.groupTree.DefaultMaxConcurrent = cfg.GroupDefaults.MaxConcurrent
				}
				// Capture it into the pending op so a reload-race reapply, which
				// runs against a freshly-reloaded tree that was never re-seeded,
				// still honours the configured default instead of falling back to 1.
				maxConcurrent := h.groupTree.DefaultMaxConcurrent
				defaultPath := h.groupDialog.GetDefaultPath()
				var created *session.Group
				if h.groupDialog.HasParent() {
					// Create subgroup under parent
					parentPath := h.groupDialog.GetParentPath()
					created = h.groupTree.CreateSubgroup(parentPath, name)
					h.pendingGroupOps = append(h.pendingGroupOps, pendingGroupOp{
						kind: groupOpCreateSub, name: name, parentPath: parentPath, defaultPath: defaultPath, maxConcurrent: maxConcurrent,
					})
				} else {
					// Create root-level group
					created = h.groupTree.CreateGroup(name)
					h.pendingGroupOps = append(h.pendingGroupOps, pendingGroupOp{
						kind: groupOpCreate, name: name, defaultPath: defaultPath, maxConcurrent: maxConcurrent,
					})
				}
				// Issue #918: persist the optional default path captured in the dialog.
				if created != nil && defaultPath != "" {
					h.groupTree.SetDefaultPathForGroup(created.Path, defaultPath)
				}
				if created != nil {
					telCmd = telemetryFeatureFromTUI("group_create", false)
				}
				h.rebuildFlatItems()
				h.saveInstances() // Persist the new group (reload-race safe via pendingGroupOps)
			}
		case GroupDialogRename:
			name := h.groupDialog.GetValue()
			if name != "" {
				oldPath := h.groupDialog.GetGroupPath()
				if err := h.groupTree.RenameGroup(oldPath, name); err != nil {
					h.setError(err)
					break
				}
				h.pendingGroupOps = append(h.pendingGroupOps, pendingGroupOp{
					kind: groupOpRename, oldPath: oldPath, name: name,
				})

				h.instancesMu.Lock()
				h.instances = h.groupTree.GetAllInstances()
				h.instancesMu.Unlock()
				h.rebuildFlatItems()
				h.saveInstances()
			}
		case GroupDialogMove:
			targetGroupPath := h.groupDialog.GetSelectedGroup()
			if targetGroupPath != "" && h.cursor < len(h.flatItems) {
				item := h.flatItems[h.cursor]
				// A creating-session placeholder (Type == ItemTypeSession,
				// Session == nil) must not reach MoveSessionToGroup or the
				// item.Session.ID deref below — both panic (#1540).
				if item.Type == session.ItemTypeSession && item.Session != nil {
					h.groupTree.MoveSessionToGroup(item.Session, targetGroupPath)
					h.pendingGroupOps = append(h.pendingGroupOps, pendingGroupOp{
						kind: groupOpMove, sessionID: item.Session.ID, targetPath: targetGroupPath,
					})
					telCmd = telemetryFeatureFromTUI("move_group", false)
					h.instancesMu.Lock()
					h.instances = h.groupTree.GetAllInstances()
					h.instancesMu.Unlock()
					h.rebuildFlatItems()
					h.saveInstances()
				} else if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil && item.RemoteName != "" {
					// The row lives in the remote's own DB; run
					// `agent-deck group move` there over SSH (mirrors the
					// remote-rename path) and refresh the fleet cache when the
					// remote confirms via remoteMoveResultMsg.
					remoteCmd = h.moveRemoteSessionToGroup(item.RemoteSession.Title, item.RemoteName, item.RemoteSession.ID, targetGroupPath)
				}
			}
		case GroupDialogRenameSession:
			newName := h.groupDialog.GetValue()
			if newName != "" {
				sessionID := h.groupDialog.GetSessionID()

				// Handle remote session rename
				if strings.HasPrefix(sessionID, "remote:") {
					parts := strings.SplitN(sessionID, ":", 3) // "remote", remoteName, actualID
					if len(parts) == 3 {
						remoteName, remoteID := parts[1], parts[2]
						// Show the new title now; the remote's answer arrives as
						// remoteRenameResultMsg, which reverts it with a message
						// if the remote refused or could not be reached.
						oldTitle := h.setRemoteSessionTitle(remoteName, remoteID, newName)
						remoteCmd = h.renameRemoteSession(remoteName, remoteID, oldTitle, newName)
					}
				} else {
					// Local session rename
					// Find and rename the session (O(1) lookup). Route through
					// SetField so the rename also sets TitleLocked — a direct
					// Title assignment would be reverted by the #572
					// Claude-name sync on the next hook event.
					// Mutate under instancesMu to match the edit-dialog rename
					// path (SetField writes inst.Title/TitleLocked, which the
					// status worker and reconciler read concurrently). Run the
					// tmux-heavy postCommit after unlocking so a slow subprocess
					// can't stall those readers.
					locked := true // SetField(FieldTitle) locks; default for the nil-inst path
					var postCommit func()
					var setErr error
					h.instancesMu.Lock()
					if inst := h.getInstanceByID(sessionID); inst != nil {
						_, postCommit, setErr = session.SetField(inst, session.FieldTitle, newName, nil)
						locked = inst.TitleLocked
						telCmd = telemetryFeatureFromTUI("rename", setErr != nil)
					}
					h.instancesMu.Unlock()
					if setErr != nil {
						h.setError(setErr)
						break
					}
					if postCommit != nil {
						postCommit()
					}
					// Store pending title change so it survives reload races.
					// pendingTitleChanges is the reload-recovery net (see the
					// reapply block in loadSessionsMsg), but it only helps AFTER
					// a reload happens. Use forceSaveInstances (not saveInstances)
					// so the lock reaches disk immediately: otherwise the row
					// sits unlocked on disk for however long it takes a reload to
					// trigger and reapply, and a Claude hook firing in that
					// window sees a genuinely-unlocked row (not a stale read) and
					// legitimately syncs Claude's own name over it before the
					// rename ever lands — the "first rename doesn't take"
					// variant of #572/#697. Mirrors the edit-dialog rename path,
					// which already force-saves for the same reason.
					h.pendingTitleChanges[sessionID] = pendingTitle{title: newName, locked: locked}
					// Invalidate preview cache since title changed
					h.invalidatePreviewCache(sessionID)
					h.rebuildFlatItems()
					h.forceSaveInstances()
				}
			}
		}
		h.groupDialog.Hide()
		return h, tea.Batch(remoteCmd, telCmd)
	case "esc":
		// First Esc dismisses the path suggestion dropdown only; the dialog
		// stays open (mirrors the model-picker Esc handling in #1162).
		if h.groupDialog.DismissPathSuggestions() {
			return h, nil
		}
		h.groupDialog.Hide()
		h.clearError() // Clear any validation error
		return h, nil
	}

	var cmd tea.Cmd
	h.groupDialog, cmd = h.groupDialog.Update(msg)
	return h, cmd
}

func (h *Home) handleZoxidePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		h.zoxidePicker.Hide()
		return h, nil
	case "enter":
		selected := h.zoxidePicker.Selected()
		h.zoxidePicker.Hide()
		if selected == "" {
			return h, nil
		}
		return h, h.quickCreateSessionAt(selected)
	default:
		h.zoxidePicker, _ = h.zoxidePicker.Update(msg)
		return h, nil
	}
}

// --- Copy & Send Output helpers ---

const maxTransferSize = 500 * 1024 // 500KB max for inter-session transfer

// copySessionOutput returns a tea.Cmd that copies the session's last response to clipboard.
func (h *Home) copySessionOutput(inst *session.Instance) tea.Cmd {
	return func() tea.Msg {
		content, err := getSessionContent(inst)
		if err != nil {
			return copyResultMsg{err: err}
		}

		termInfo := tmux.GetTerminalInfo()
		result, err := clipboard.Copy(content, termInfo.SupportsOSC52)
		if err != nil {
			return copyResultMsg{err: fmt.Errorf("clipboard: %w", err)}
		}
		return copyResultMsg{
			sessionTitle: inst.Title,
			lineCount:    result.LineCount,
		}
	}
}

// sendOutputToSession returns a tea.Cmd that sends the source session's output to the target.
func (h *Home) sendOutputToSession(source, target *session.Instance) tea.Cmd {
	return func() tea.Msg {
		content, err := getSessionContent(source)
		if err != nil {
			return sendOutputResultMsg{
				targetTitle: target.Title,
				err:         err,
			}
		}

		// Truncate if too large
		if len(content) > maxTransferSize {
			content = content[:maxTransferSize] + "\n[Truncated at 500KB]"
		}

		// Wrap with header/footer
		wrapped := fmt.Sprintf("--- Output from [%s] ---\n%s\n--- End output from [%s] ---\n",
			source.Title, content, source.Title)

		tmuxSession := target.GetTmuxSession()
		if tmuxSession == nil {
			return sendOutputResultMsg{
				targetTitle: target.Title,
				err:         fmt.Errorf("target session has no tmux pane"),
			}
		}

		if err := tmuxSession.SendKeysChunked(wrapped); err != nil {
			return sendOutputResultMsg{
				targetTitle: target.Title,
				err:         fmt.Errorf("send failed: %w", err),
			}
		}

		lineCount := strings.Count(content, "\n")
		return sendOutputResultMsg{
			sourceTitle: source.Title,
			targetTitle: target.Title,
			lineCount:   lineCount,
		}
	}
}

// handleSessionPickerDialogKey handles key events when the session picker is visible.
func (h *Home) handleSessionPickerDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		selected := h.sessionPickerDialog.GetSelected()
		source := h.sessionPickerDialog.GetSource()
		h.sessionPickerDialog.Hide()
		if selected != nil && source != nil {
			return h, h.sendOutputToSession(source, selected)
		}
		return h, nil
	case "esc":
		h.sessionPickerDialog.Hide()
		return h, nil
	default:
		h.sessionPickerDialog.Update(msg)
		return h, nil
	}
}

// handleWorktreeFinishDialogKey processes key events for the worktree finish dialog
func (h *Home) handleWorktreeFinishDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	action := h.worktreeFinishDialog.HandleKey(msg.String())

	switch action {
	case "close":
		return h, nil

	case "confirm":
		// Execute the finish operation
		mergeEnabled, targetBranch, keepBranch := h.worktreeFinishDialog.GetOptions()
		h.worktreeFinishDialog.SetExecuting(true)

		sid := h.worktreeFinishDialog.sessionID
		sTitle := h.worktreeFinishDialog.sessionTitle
		branch := h.worktreeFinishDialog.branchName
		repoRoot := h.worktreeFinishDialog.repoRoot
		wtPath := h.worktreeFinishDialog.worktreePath

		// Find the instance for kill/remove, and snapshot whether any OTHER
		// live session still shares this worktree (#1449). Computed here under
		// the lock so the async finishWorktree closure never touches h.instances.
		h.instancesMu.RLock()
		inst := h.instanceByID[sid]
		shared := session.OtherSessionsShareWorktree(
			&session.Instance{ID: sid, WorktreePath: wtPath, WorktreeRepoRoot: repoRoot},
			h.instances,
		)
		h.instancesMu.RUnlock()

		return h, h.finishWorktree(inst, sid, sTitle, branch, repoRoot, wtPath, mergeEnabled, targetBranch, keepBranch, shared)

	case "input":
		// Pass through to text input
		h.worktreeFinishDialog.UpdateTargetInput(msg)
		return h, nil
	}

	return h, nil
}

// getOtherActiveSessions returns sessions excluding the given ID and error/stopped-status sessions.
func (h *Home) getOtherActiveSessions(excludeID string) []*session.Instance {
	var result []*session.Instance
	for _, inst := range h.instances {
		if inst.ID == excludeID {
			continue
		}
		s := inst.GetStatusThreadSafe()
		if s == session.StatusError || s == session.StatusStopped {
			continue
		}
		result = append(result, inst)
	}
	return result
}

// getSessionContent retrieves displayable content from a session.
//
// Refreshes tool session IDs from the live tmux env BEFORE reading, so that
// a stale ClaudeSessionID (e.g. from a prior resumed conversation) cannot
// surface old JSONL content as "the last response". Fix for issue #598
// (cross-session `x` transferred unpredictable content).
//
// Falls back to tmux scrollback capture when structured response lookup
// returns no content.
func getSessionContent(inst *session.Instance) (string, error) {
	var live string
	if session.IsClaudeCompatible(inst.Tool) {
		live = inst.GetSessionIDFromTmux()
	}
	return getSessionContentWithLive(inst, live)
}

// getSessionContentWithLive is the testable core: given a live Claude session
// ID (may be empty), prefer it over any stored ID before reading the last
// response, then fall back to tmux scrollback.
func getSessionContentWithLive(inst *session.Instance, liveClaudeID string) (string, error) {
	if session.IsClaudeCompatible(inst.Tool) && liveClaudeID != "" && liveClaudeID != inst.ClaudeSessionID {
		inst.ClaudeSessionID = liveClaudeID
		// #1815: read from this session's OWN pane env — weak vouch.
		session.NoteClaudeSessionIDFromOwnPane(inst)
	}

	// Use best-effort: richer recovery than GetLastResponse if the refreshed
	// ID still doesn't resolve to a readable JSONL.
	if resp, err := inst.GetLastResponseBestEffort(); err == nil && resp != nil && resp.Content != "" {
		return resp.Content, nil
	}

	tmuxSession := inst.GetTmuxSession()
	if tmuxSession == nil {
		return "", fmt.Errorf("no output available for this session")
	}

	content, err := tmuxSession.CaptureFullHistory()
	if err != nil {
		return "", fmt.Errorf("failed to capture output: %w", err)
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("no output available for this session")
	}

	return content, nil
}
