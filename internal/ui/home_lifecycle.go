package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// sessionDeletedMsg signals that a session was deleted
type sessionDeletedMsg struct {
	deletedID string
	killErr   error // Error from Kill() if any
}

// sessionClosedMsg signals that a session process was closed without deleting metadata.
type sessionClosedMsg struct {
	sessionID string
	killErr   error
}

type sessionArchivedMsg struct {
	sessionID string
	killErr   error
}

type sessionUnarchivedMsg struct {
	sessionID string
}

// sessionRestoredMsg signals that an undo-delete restore completed
type sessionRestoredMsg struct {
	instance *session.Instance
	err      error
	warning  string
}

// deleteSession deletes a session
func (h *Home) deleteSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	isWorktree := inst.IsWorktree()
	worktreePath := inst.WorktreePath
	worktreeRepoRoot := inst.WorktreeRepoRoot
	isMultiRepo := inst.IsMultiRepo()
	multiRepoTempDir := inst.MultiRepoTempDir
	multiRepoWorktrees := inst.MultiRepoWorktrees
	// #1449: snapshot whether another live session still shares this worktree,
	// under the lock, before the async closure runs (which must not touch
	// h.instances). When shared, the worktree dir + branch are left intact and
	// only this session's record is dropped.
	sharedWorktree := false
	if isWorktree {
		h.instancesMu.RLock()
		sharedWorktree = session.OtherSessionsShareWorktree(
			&session.Instance{ID: id, WorktreePath: worktreePath, WorktreeRepoRoot: worktreeRepoRoot},
			h.instances,
		)
		h.instancesMu.RUnlock()
	}
	return func() tea.Msg {
		killErr := inst.Kill()
		inst.RecordTelemetryEnd(telemetry.EndDelete)
		if isWorktree && sharedWorktree {
			// #1449: another live session still references this worktree; skip
			// the destructive removal + branch delete and merely drop this
			// session's record so the siblings are not stranded.
			uiLog.Info("worktree_remove_skipped", slog.String("path", worktreePath), slog.String("repo", worktreeRepoRoot), slog.String("reason", "another live session still references this worktree (#1449)"))
		} else if isWorktree {
			// #1200: route worktree teardown through the session guard so a
			// worktree_reuse session (WorktreePath == the user's original repo)
			// is never os.RemoveAll'd. Only genuine agent-deck-created linked
			// worktrees are removed; a reused repo is left intact and merely
			// dropped from the registry.
			snap := &session.Instance{ID: id, WorktreePath: worktreePath, WorktreeRepoRoot: worktreeRepoRoot}
			switch removed, err := session.RemoveSessionWorktree(snap); {
			case err != nil:
				uiLog.Warn("worktree_remove_err", slog.String("path", worktreePath), slog.String("err", err.Error()))
			case !removed:
				uiLog.Info("worktree_remove_skipped", slog.String("path", worktreePath), slog.String("repo", worktreeRepoRoot), slog.String("reason", "reused or non-linked worktree (#1200 guard)"))
			}
		}
		if isMultiRepo {
			// Clean up multi-repo temp directory
			if multiRepoTempDir != "" {
				_ = os.RemoveAll(multiRepoTempDir)
			}
			// Clean up per-repo worktrees
			for _, wt := range multiRepoWorktrees {
				if err := git.RemoveWorktree(wt.RepoRoot, wt.WorktreePath, true); err != nil {
					uiLog.Warn("worktree_remove_err", slog.String("path", wt.WorktreePath), slog.String("err", err.Error()))
				}
				if err := git.PruneWorktrees(wt.RepoRoot); err != nil {
					uiLog.Warn("worktree_prune_err", slog.String("repo", wt.RepoRoot), slog.String("err", err.Error()))
				}
			}
		}
		return sessionDeletedMsg{deletedID: id, killErr: killErr}
	}
}

// captureAutoNameBeforeStop persists an auto-named session's live Claude task
// description right before its process is stopped. Auto-named rows render the
// live tmux pane title; once Kill() tears the process down that live title is
// gone and displaySessionTitle falls back to the persisted auto-name
// description. The background status tick is the only other writer, so a
// session stopped before that tick fires would revert to its bare random
// handle. Capturing here closes that window for archive (and any future stop
// path that calls it).
//
// No-op unless the session is auto-named with a non-empty live title that
// differs from what is already stored — an empty/idle pane must never clobber a
// previously captured description (mirrors shouldPersistAutoNameDesc). Writes
// the in-memory field (instance-locked) and the DB column via h.storage;
// deliberately does NOT touch h.lastPersistedAutoNameDesc, which is owned by
// the statusWorker goroutine — writing it from the UI goroutine would be a
// data race.
func (h *Home) captureAutoNameBeforeStop(inst *session.Instance) {
	if inst == nil || !inst.GetAutoName() {
		return
	}
	live := ""
	if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
		tmux.RefreshPaneInfoCache()
		if paneInfo, ok := tmux.GetCachedPaneInfo(tmuxSess.TmuxName()); ok {
			live = cleanPaneTitle(paneInfo.Title)
		}
	}
	if live == "" {
		live = h.getSessionRenderState(inst).paneTitle
	}
	if live == "" || live == inst.GetAutoNameDescription() {
		return
	}
	inst.SetAutoNameDescription(live)
	if h.storage == nil {
		return
	}
	if err := h.storage.WriteAutoNameDescription(inst.ID, live); err != nil {
		uiLog.Warn("autoname_capture_failed",
			slog.String("id", inst.ID), slog.String("error", err.Error()))
	}
}

// closeSession stops a session process but keeps metadata in list/storage.
func (h *Home) closeSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	storage := h.storage
	return func() tea.Msg {
		// Defect 317: write stopped right after the kill; the daemon's error
		// sample of it would otherwise refuse the full save below, leaving the
		// row in error for the idle-death watch (#2806).
		loaded := session.BeforeStopKill(storage, id)
		killErr := inst.Kill()
		if killErr == nil {
			session.WriteStoppedAfterKill(storage, inst, loaded)
		}
		inst.RecordTelemetryEnd(telemetry.EndStop)
		return sessionClosedMsg{sessionID: id, killErr: killErr}
	}
}

// persistArchived writes the instance's archive timestamp to the database with a
// targeted single-row UPDATE. This deliberately bypasses saveInstances(), whose
// external-change guard aborts (and reloads) under concurrent writers, silently
// reverting the archive. The in-memory inst.ArchivedAt is already set by
// archiveSession/unarchiveSession; this only persists it.
func (h *Home) persistArchived(inst *session.Instance) error {
	if h.storage == nil {
		return nil
	}
	db := h.storage.GetDB()
	if db == nil {
		return nil
	}
	return db.SetArchived(inst.ID, inst.ArchivedAtThreadSafe())
}

// archiveSession stops a session and marks it archived.
func (h *Home) archiveSession(inst *session.Instance) tea.Cmd {
	// Snapshot the live Claude task description on the UI goroutine before the
	// background Kill tears down the pane that title comes from.
	h.captureAutoNameBeforeStop(inst)
	id := inst.ID
	storage := h.storage
	return func() tea.Msg {
		loaded := session.BeforeStopKill(storage, id)
		if killErr := inst.Kill(); killErr != nil {
			return sessionArchivedMsg{sessionID: id, killErr: killErr}
		}
		// Defect 317: the row reads stopped, not the daemon's error sample of
		// this kill (archived_at alone already keeps #2806 quiet).
		session.WriteStoppedAfterKill(storage, inst, loaded)
		inst.SetArchivedAtThreadSafe(time.Now().UTC())
		return sessionArchivedMsg{sessionID: id}
	}
}

// unarchiveSession clears the archive flag without restarting tmux.
func (h *Home) unarchiveSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	return func() tea.Msg {
		inst.SetArchivedAtThreadSafe(time.Time{})
		return sessionUnarchivedMsg{sessionID: id}
	}
}

// removeSession removes a session from the registry without killing the
// process or cleaning its worktree. The key handler already enforced the
// stopped/error gate. Emits sessionDeletedMsg so the existing delete
// handler in Update persists the change.
func (h *Home) removeSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	return func() tea.Msg {
		return sessionDeletedMsg{deletedID: id}
	}
}

// bulkRemoveErrored removes every session currently in the 'error' state.
// Emits one sessionDeletedMsg per removed session; Update is idempotent
// on repeated deletedIDs.
func (h *Home) bulkRemoveErrored() tea.Cmd {
	h.instancesMu.RLock()
	ids := make([]string, 0, len(h.instances))
	for _, inst := range h.instances {
		// pin-protects-from-stop: pinned errored sessions are left alone in
		// bulk removal; an explicit Shift+D on the session still works.
		if inst.Status == session.StatusError && inst.Pin == session.PinNone {
			ids = append(ids, inst.ID)
		}
	}
	h.instancesMu.RUnlock()

	cmds := make([]tea.Cmd, 0, len(ids))
	for _, id := range ids {
		id := id
		cmds = append(cmds, func() tea.Msg { return sessionDeletedMsg{deletedID: id} })
	}
	return tea.Batch(cmds...)
}

// sessionRestartedMsg signals that a session was restarted.
// accountSwitchedMsg reports the outcome of a TUI account switch (#924).
// committed says whether the account field was actually changed on the
// instance — the caller must persist session state exactly when it is true,
// including the partial case where the switch succeeded but the restart did
// not.
type accountSwitchedMsg struct {
	sessionID  string
	generation uint64
	// oldPIDs is the pane's process tree before the switch restarted it,
	// for the hands-free settle that follows (SWFB1018).
	oldPIDs        []int
	sourceTool     string
	sourceAccount  string
	sourceProject  string
	sourceTitle    string
	sourceGroup    string
	sourceCommand  string
	sourceStatus   session.Status
	sourceClaudeID string
	sourceCodexID  string
	sourceCWD      string
	targetHarness  string
	account        string
	summary        string
	committed      bool
	pending        bool // durable target/account update without verified native readiness
	targetReady    bool // explicit positive target-native readiness evidence
	warnings       []string
	target         *session.Instance // distinct cross-harness target; source remains unchanged
	nativeResult   *session.HarnessSwitchResult
	// destinationDivergent is set when err is (or wraps)
	// session.ErrSwitchDestinationDivergent: the destination transcript is not
	// provably stale, so the refusal is not a dead end — the user can retry
	// with an explicit "archive destination and switch" action.
	destinationDivergent bool
	err                  error
}

func switchTUIStatus(committed, ready, pending bool) string {
	if pending {
		return "pending"
	}
	if !committed {
		return "failed"
	}
	if !ready {
		return "pending"
	}
	return "success"
}

func switchTUIReadiness(ready bool) string {
	if ready {
		return "ready"
	}
	return "pending"
}

// crossHarnessSwitchMessage retains a backend result's durable metadata while
// making an absent target explicit. Store failures can correctly return a
// result without a target, so presentation must never assume Target is nonnil.
func crossHarnessSwitchMessage(msg accountSwitchedMsg, preview *session.SwitchPreview, result *session.CrossHarnessSwitchResult, err error) accountSwitchedMsg {
	msg.err = err
	if result == nil {
		return msg
	}
	msg.target, msg.targetReady, msg.pending = result.Target, result.TargetReady, result.Pending
	if result.Pending {
		msg.warnings = append(msg.warnings, result.MissingContract)
	}
	recoveryRequired := result.TargetCreated || errors.Is(err, session.ErrCrossHarnessRecoveryRequired)
	if result.Target == nil {
		msg.summary = fmt.Sprintf("cross-harness switch failed; status=failed; target_created=%t; recovery_required=%t", result.TargetCreated, recoveryRequired)
		return msg
	}

	status := switchTUIStatus(true, result.TargetReady, result.Pending)
	failed := err != nil && (!result.Pending || errors.Is(err, session.ErrCrossHarnessRecoveryRequired))
	if failed {
		status = "failed"
	}
	harness := "target"
	if preview != nil {
		harness = preview.TargetHarness
	}
	msg.summary = fmt.Sprintf("distinct %s target %s created; status=%s; recovery_required=%t; readiness=%s; account=%s; authentication=%s; context=%s; semantic acceptance=%s", harness, result.Target.ID, status, failed && recoveryRequired, switchTUIReadiness(result.TargetReady), result.ConfiguredAccount, result.Authentication, result.ContextDelivery, result.SemanticAcceptance)
	return msg
}

// nativeHarnessSwitchMessage maps a native switch result to its TUI message.
// A result can retain a committed account after persisting a later journal
// transition fails. That is recovery-required, not readiness-pending: errors
// must never be presented as a pending or successful switch.
func nativeHarnessSwitchMessage(msg accountSwitchedMsg, result *session.HarnessSwitchResult, err error) accountSwitchedMsg {
	msg.err = err
	msg.destinationDivergent = errors.Is(err, session.ErrSwitchDestinationDivergent)
	if result == nil {
		return msg
	}

	msg.committed = result.Committed
	msg.nativeResult = result
	msg.targetReady = result.DestinationReady
	msg.pending = err == nil && result.Committed && !result.DestinationReady
	msg.warnings = result.Warnings

	status := switchTUIStatus(result.Committed, result.DestinationReady, msg.pending)
	recoveryRequired := err != nil && result.Committed
	if err != nil {
		status = "failed"
	}
	msg.summary = fmt.Sprintf("status=%s; readiness=%s; recovery_required=%t; %s", status, switchTUIReadiness(result.DestinationReady), recoveryRequired, result.Conversation)
	return msg
}

// switchSourceSnapshot reads route metadata without mutating it. A malformed
// or inaccessible route file is an ownership uncertainty, so cross-harness
// replacement fails closed rather than guessing a bridge destination.
func (h *Home) switchSourceSnapshot(inst *session.Instance) session.SwitchSourceSnapshot {
	h.instancesMu.RLock()
	instances := append([]*session.Instance(nil), h.instances...)
	h.instancesMu.RUnlock()
	return switchSourceSnapshotFromInstances(inst, instances)
}

func switchSourceSnapshotFromInstances(inst *session.Instance, instances []*session.Instance) session.SwitchSourceSnapshot {
	watcherTarget, managementUnknown := false, false
	watcherDir, err := session.WatcherDir()
	if err != nil {
		managementUnknown = true
	} else {
		clientsPath := filepath.Join(watcherDir, "clients.json")
		if _, statErr := os.Stat(clientsPath); statErr == nil {
			clients, loadErr := watcher.LoadClientsJSON(clientsPath)
			if loadErr != nil {
				managementUnknown = true
			} else {
				for _, client := range clients {
					if client.Conductor == inst.ID || client.Conductor == inst.Title || session.ConductorSessionTitle(client.Conductor) == inst.Title || inst.IsConductorNamed(client.Conductor, inst.Title) {
						watcherTarget = true
						break
					}
				}
			}
		} else if !os.IsNotExist(statErr) {
			managementUnknown = true
		}
	}
	return session.SnapshotSwitchSource(inst, instances, watcherTarget, managementUnknown)
}

// crossHarnessSourceOwnershipValidator re-reads storage instead of relying on
// the Home's displayed rows. The executor repeats it before staging and final
// commit; watcher routing remains external to SQLite and is not atomic with
// the final storage CAS.
func (h *Home) crossHarnessSourceOwnershipValidator() session.CrossHarnessSourceOwnershipValidator {
	return session.CrossHarnessSourceOwnershipValidatorFunc(func(source *session.Instance) (session.SwitchSourceSnapshot, error) {
		if h.storage == nil {
			return session.SwitchSourceSnapshot{ManagementUnknown: true}, fmt.Errorf("session storage is unavailable")
		}
		instances, err := h.storage.Load()
		if err != nil {
			return session.SwitchSourceSnapshot{ManagementUnknown: true}, fmt.Errorf("load current session graph: %w", err)
		}
		var current *session.Instance
		for _, candidate := range instances {
			if candidate != nil && source != nil && candidate.ID == source.ID {
				current = candidate
				break
			}
		}
		if current == nil {
			return session.AuthoritativeSwitchSourceSnapshot(source, instances, false, true)
		}
		snapshot := switchSourceSnapshotFromInstances(current, instances)
		return session.AuthoritativeSwitchSourceSnapshot(source, instances, snapshot.WatcherBridgeTarget, snapshot.ManagementUnknown)
	})
}

// switchSessionAccount moves a session to another named Claude account,
// carrying its conversation across, and reports the outcome as an
// accountSwitchedMsg. Same flow as `agent-deck session switch-account`: it
// stops the session, copies the conversation into the target account's config
// dir, verifies the copy, commits the slot and restarts with `--resume`.
//
// Runs as a tea.Cmd because the stop/copy/start sequence talks to tmux and the
// filesystem; doing it inline would freeze the UI for the duration.
func (h *Home) switchSessionAccount(sessionID, account string) tea.Cmd {
	return h.switchSessionHarness(sessionID, "claude", account)
}

// switchSessionHarness is the TUI adapter for ExecuteHarnessSwitch. The
// confirmation/result notice is shared with the legacy account-only path, so
// the TUI cannot accidentally bypass the journalled backend.
func (h *Home) switchSessionHarness(sessionID, harness, account string) tea.Cmd {
	return h.switchSessionHarnessWithOptions(sessionID, harness, account, false)
}

// switchSessionHarnessWithOptions is switchSessionHarness plus the ability to
// authorize archiving an already-refused destination transcript, used by the
// "Archive destination copy and switch" retry action.
func (h *Home) switchSessionHarnessWithOptions(sessionID, harness, account string, archiveDestination bool) tea.Cmd {
	// Capture immutable source identity and claim an operation generation before
	// releasing the registry lock. A reload or newer operation may replace the
	// pointer while the filesystem/lifecycle work is in flight.
	h.instancesMu.Lock()
	captured := h.instanceByID[sessionID]
	if captured == nil {
		h.instancesMu.Unlock()
		return func() tea.Msg {
			return accountSwitchedMsg{sessionID: sessionID, account: account, targetHarness: harness, err: fmt.Errorf("session no longer exists")}
		}
	}
	h.switchGenerations[sessionID]++
	generation := h.switchGenerations[sessionID]
	sourceTool, sourceAccount := captured.Tool, captured.Account
	sourceProject, sourceTitle, sourceGroup := captured.ProjectPath, captured.Title, captured.GroupPath
	sourceCommand, sourceStatus := captured.Command, captured.Status
	sourceClaudeID, sourceCodexID, sourceCWD := captured.ClaudeSessionID, captured.CodexSessionID, captured.EffectiveWorkingDir()
	modalIdentity := session.CaptureSwitchModalIdentity(captured)
	h.instancesMu.Unlock()
	return func() tea.Msg {
		// Resolve by ID at execution time, then require the captured identity to
		// remain unchanged. Never mutate a replacement snapshot.
		h.instancesMu.RLock()
		inst := h.instanceByID[sessionID]
		valid := inst != nil && modalIdentity.Matches(inst) && h.switchGenerations[sessionID] == generation
		h.instancesMu.RUnlock()
		if !valid {
			return accountSwitchedMsg{sessionID: sessionID, generation: generation, sourceTool: sourceTool, sourceAccount: sourceAccount, sourceProject: sourceProject, sourceTitle: sourceTitle, sourceGroup: sourceGroup, sourceCommand: sourceCommand, sourceStatus: sourceStatus, sourceClaudeID: sourceClaudeID, sourceCodexID: sourceCodexID, sourceCWD: sourceCWD, targetHarness: harness, account: account, err: fmt.Errorf("session changed while switch was pending")}
		}

		cfg, cfgErr := session.LoadUserConfig()
		baseMsg := accountSwitchedMsg{sessionID: sessionID, generation: generation, sourceTool: sourceTool, sourceAccount: sourceAccount, sourceProject: sourceProject, sourceTitle: sourceTitle, sourceGroup: sourceGroup, sourceCommand: sourceCommand, sourceStatus: sourceStatus, sourceClaudeID: sourceClaudeID, sourceCodexID: sourceCodexID, sourceCWD: sourceCWD, targetHarness: harness, account: account}
		if cfgErr != nil {
			baseMsg.err = cfgErr
			return baseMsg
		}
		target := session.SwitchPreviewTarget{Harness: harness, Account: account}
		sourceSnapshot := h.switchSourceSnapshot(inst)
		preview := session.PreviewSwitchWithMaxBytesAndSnapshot(cfg, inst, target, session.DefaultHandoffMaxChars, &sourceSnapshot)
		msg := baseMsg
		if preview.Execution == session.ExecutionPlanned && preview.Refusal == nil {
			crossResult, err := session.ExecuteCrossHarnessSwitch(context.Background(), cfg, inst, session.CrossHarnessSwitchOptions{Target: target, SourceSnapshot: &sourceSnapshot}, session.CrossHarnessSwitchDependencies{
				Store:           session.StorageCrossHarnessTargetStore{Storage: h.storage},
				Lifecycle:       session.InstanceCrossHarnessLifecycle{},
				Observer:        session.NewNativeCrossHarnessTargetObserver(),
				SourceOwnership: h.crossHarnessSourceOwnershipValidator(),
			})
			return crossHarnessSwitchMessage(msg, preview, crossResult, err)
		}
		// The tree the restart replaces; the settle afterwards waits for it.
		msg.oldPIDs = session.SnapshotPaneProcesses(inst)
		result, err := session.ExecuteHarnessSwitch(cfg, inst, session.HarnessSwitchOptions{Target: target, Storage: h.storage, ArchiveDestination: archiveDestination})
		return nativeHarnessSwitchMessage(msg, result, err)
	}
}

type sessionRestartedMsg struct {
	sessionID  string
	err        error
	warning    string
	fresh      bool
	unarchived bool
}

// mcpRestartedMsg signals that an MCP-triggered restart completed and should auto-attach
type mcpRestartedMsg struct {
	session *session.Instance
	err     error
}

// restartWithArchiveTransition makes restart from the archived view a durable
// unarchive + restart operation. If restart fails, it restores the exact archive
// timestamp so the session does not move lists without a running process.
func restartWithArchiveTransition(
	inst *session.Instance,
	persist func(*session.Instance) error,
	restart func() error,
) (bool, error) {
	if !inst.IsArchived() {
		return false, restart()
	}

	archivedAt := inst.ArchivedAtThreadSafe()
	inst.SetArchivedAtThreadSafe(time.Time{})
	if err := persist(inst); err != nil {
		inst.SetArchivedAtThreadSafe(archivedAt)
		return false, fmt.Errorf("failed to unarchive session before restart: %w", err)
	}

	if err := restart(); err != nil {
		inst.SetArchivedAtThreadSafe(archivedAt)
		if rollbackErr := persist(inst); rollbackErr != nil {
			return false, fmt.Errorf("restart failed: %w; restoring archive state failed: %v", err, rollbackErr)
		}
		return false, err
	}
	return true, nil
}

// restartSession restarts a session, unarchiving it first when invoked from
// the archived view.
func (h *Home) restartSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	previewSize := h.detachedRestartPreviewSize(id)
	mcpUILog.Debug(
		"restart_session_called",
		slog.String("id", inst.ID),
		slog.String("title", inst.Title),
		slog.String("tool", inst.Tool),
	)
	return func() tea.Msg {
		mcpUILog.Debug("restart_session_executing", slog.String("id", id))

		// Resolve current instance by ID at execution time. During storage reloads,
		// the pointer captured from the key event can be replaced before this cmd runs.
		h.instancesMu.RLock()
		current := h.instanceByID[id]
		h.instancesMu.RUnlock()
		if current == nil {
			err := fmt.Errorf("session no longer exists")
			mcpUILog.Debug("restart_session_result", slog.String("id", id), slog.Any("error", err))
			return sessionRestartedMsg{sessionID: id, err: err}
		}

		unarchived, err := restartWithArchiveTransition(current, h.persistArchived, current.Restart)
		if err == nil {
			if fitErr := fitRestartedPreview(current, previewSize); fitErr != nil {
				uiLog.Debug("restart_preview_resize_failed", slog.String("session", id), slog.Any("error", fitErr))
			}
		}
		mcpUILog.Debug("restart_session_result", slog.String("id", id), slog.Any("error", err))
		return sessionRestartedMsg{
			sessionID:  id,
			err:        err,
			warning:    current.ConsumeCodexRestartWarning(),
			unarchived: unarchived,
		}
	}
}

// restartSessionFresh restarts a session without resuming the previous tool session.
func (h *Home) restartSessionFresh(inst *session.Instance) tea.Cmd {
	return h.restartSessionFreshWith(inst, h.persistArchived, (*session.Instance).RestartFresh)
}

func (h *Home) restartSessionFreshWith(
	inst *session.Instance,
	persist func(*session.Instance) error,
	restartFresh func(*session.Instance) error,
) tea.Cmd {
	id := inst.ID
	previewSize := h.detachedRestartPreviewSize(id)
	mcpUILog.Debug(
		"restart_session_fresh_called",
		slog.String("id", inst.ID),
		slog.String("title", inst.Title),
		slog.String("tool", inst.Tool),
	)
	return func() tea.Msg {
		mcpUILog.Debug("restart_session_fresh_executing", slog.String("id", id))

		h.instancesMu.RLock()
		current := h.instanceByID[id]
		h.instancesMu.RUnlock()
		if current == nil {
			err := fmt.Errorf("session no longer exists")
			mcpUILog.Debug("restart_session_fresh_result", slog.String("id", id), slog.Any("error", err))
			return sessionRestartedMsg{sessionID: id, err: err, fresh: true}
		}

		unarchived, err := restartWithArchiveTransition(current, persist, func() error {
			return restartFresh(current)
		})
		if err == nil {
			if fitErr := fitRestartedPreview(current, previewSize); fitErr != nil {
				uiLog.Debug("restart_preview_resize_failed", slog.String("session", id), slog.Any("error", fitErr))
			}
		}
		mcpUILog.Debug("restart_session_fresh_result", slog.String("id", id), slog.Any("error", err))
		return sessionRestartedMsg{
			sessionID:  id,
			err:        err,
			warning:    current.ConsumeCodexRestartWarning(),
			fresh:      true,
			unarchived: unarchived,
		}
	}
}

type remoteSessionDeletedMsg struct {
	remoteName string
	sessionID  string
	title      string
	err        error
}

type remoteSessionClosedMsg struct {
	remoteName string
	sessionID  string
	title      string
	err        error
}

// remoteSessionArchivedMsg reports the outcome of a remote archive (archived
// true) or unarchive (archived false).
type remoteSessionArchivedMsg struct {
	remoteName string
	sessionID  string
	title      string
	archived   bool
	err        error
}

type remoteSessionRestartedMsg struct {
	remoteName string
	sessionID  string
	title      string
	err        error
}

func remoteRestartAnimationID(remoteName, sessionID string) string {
	return "remote:" + remoteName + ":" + sessionID
}

type remoteSessionCreatedMsg struct {
	err error
	// notice is a non-error outcome worth a footer line (e.g. queued).
	notice string
	// created describes the session the remote confirmed, drawn at once.
	created *session.RemoteSessionInfo
	// preview, when set, seeds the preview pane for created so a session the
	// remote recorded as failed shows its explainer before any fetch.
	preview string
}

// remoteRenameResultMsg reports the remote's answer to a rename started from
// the rename dialog on a remote session row.
type remoteRenameResultMsg struct {
	remoteName string
	sessionID  string
	oldTitle   string
	newTitle   string
	err        error
}

// setRemoteSessionTitle patches the cached title of a remote session and
// rebuilds the rows; it returns the previous title so a refused rename can
// be reverted.
func (h *Home) setRemoteSessionTitle(remoteName, sessionID, title string) string {
	old := ""
	h.remoteSessionsMu.Lock()
	if sessions, ok := h.remoteSessions[remoteName]; ok {
		for i := range sessions {
			if sessions[i].ID == sessionID {
				old = sessions[i].Title
				sessions[i].Title = title
				break
			}
		}
	}
	h.remoteSessionsMu.Unlock()
	h.rebuildFlatItems()
	return old
}

// renameRemoteSession runs `agent-deck rename` on the remote and reports the
// outcome, instead of firing it and forgetting (a refused or unreachable
// rename used to show the new title and silently snap back on the next poll).
func (h *Home) renameRemoteSession(remoteName, sessionID, oldTitle, newTitle string) tea.Cmd {
	h.markRemoteAction(remoteName, "rename", sessionID)
	return func() tea.Msg {
		result := remoteRenameResultMsg{remoteName: remoteName, sessionID: sessionID, oldTitle: oldTitle, newTitle: newTitle}
		runner, err := remoteRunnerFor(remoteName)
		if err != nil {
			result.err = err
			return result
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := runner.RunCommand(ctx, "rename", sessionID, newTitle); err != nil {
			result.err = err
		}
		return result
	}
}

// remoteSessionForkedMsg reports the outcome of an SSH-routed quick fork (f
// key on a remote session row). newID is the session the remote created.
type remoteSessionForkedMsg struct {
	remoteName string
	sessionID  string
	title      string
	newID      string
	err        error
}

// remoteCreateDirNeededMsg reports a remote create refused because the
// dialog's path does not exist on the server; Home offers to create it.
type remoteCreateDirNeededMsg struct {
	remoteName string
	opts       session.RemoteAddOptions
	err        error
}

// deleteRemoteSession deletes a remote session and refreshes the remote list.
func (h *Home) deleteRemoteSession(remoteName, sessionID, title string) tea.Cmd {
	h.markRemoteAction(remoteName, "delete", sessionID)
	h.setRemotePending(sessionID, "deleting…")
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return remoteSessionDeletedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("failed to load remote config"),
			}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteSessionDeletedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("remote '%s' not found", remoteName),
			}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = runner.DeleteSession(ctx, sessionID)
		return remoteSessionDeletedMsg{remoteName: remoteName, sessionID: sessionID, title: title, err: err}
	}
}

// closeRemoteSession stops a remote session process without deleting metadata.
func (h *Home) closeRemoteSession(remoteName, sessionID, title string) tea.Cmd {
	h.markRemoteAction(remoteName, "close", sessionID)
	h.setRemotePending(sessionID, "closing…")
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return remoteSessionClosedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("failed to load remote config"),
			}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteSessionClosedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("remote '%s' not found", remoteName),
			}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = runner.StopSession(ctx, sessionID)
		return remoteSessionClosedMsg{remoteName: remoteName, sessionID: sessionID, title: title, err: err}
	}
}

// setRemoteSessionArchived archives (archive true) or unarchives a remote
// session through the remote's own `session archive` / `session unarchive`,
// then refreshes the remote list so the row moves between the active and
// archived (^) views the way a local session does.
func (h *Home) setRemoteSessionArchived(remoteName, sessionID, title string, archive bool) tea.Cmd {
	h.markRemoteAction(remoteName, "archive", sessionID)
	if archive {
		h.setRemotePending(sessionID, "archiving…")
	} else {
		h.setRemotePending(sessionID, "restoring…")
	}
	return func() tea.Msg {
		result := remoteSessionArchivedMsg{remoteName: remoteName, sessionID: sessionID, title: title, archived: archive}
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			result.err = fmt.Errorf("failed to load remote config")
			return result
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			result.err = fmt.Errorf("remote '%s' not found", remoteName)
			return result
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), rc.GetCommandTimeout())
		defer cancel()
		if archive {
			result.err = runner.ArchiveSession(ctx, sessionID)
		} else {
			result.err = runner.UnarchiveSession(ctx, sessionID)
		}
		return result
	}
}

// forkRemoteSession forks a remote session through the remote's own
// `session fork` (mirrors restartRemoteSession). Title, group and tool
// eligibility are decided on the server; the fleet list is refreshed when
// the remote confirms via remoteSessionForkedMsg so the new row appears.
func (h *Home) forkRemoteSession(remoteName, sessionID, title string) tea.Cmd {
	h.markRemoteAction(remoteName, "fork", sessionID)
	h.setRemotePending(sessionID, "forking…")
	return func() tea.Msg {
		result := remoteSessionForkedMsg{remoteName: remoteName, sessionID: sessionID, title: title}
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			result.err = fmt.Errorf("failed to load remote config")
			return result
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			result.err = fmt.Errorf("remote '%s' not found", remoteName)
			return result
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), rc.GetCommandTimeout())
		defer cancel()
		result.newID, result.err = runner.ForkSession(ctx, sessionID)
		return result
	}
}

// restartRemoteSession restarts a remote session.
func (h *Home) restartRemoteSession(remoteName, sessionID, title string) tea.Cmd {
	h.markRemoteAction(remoteName, "restart", sessionID)
	h.setRemotePending(sessionID, "restarting…")
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return remoteSessionRestartedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("failed to load remote config"),
			}
		}
		rc, ok := config.Remotes[remoteName]
		if !ok {
			return remoteSessionRestartedMsg{
				remoteName: remoteName,
				sessionID:  sessionID,
				title:      title,
				err:        fmt.Errorf("remote '%s' not found", remoteName),
			}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err = runner.RestartSession(ctx, sessionID)
		return remoteSessionRestartedMsg{remoteName: remoteName, sessionID: sessionID, title: title, err: err}
	}
}

// runWorktreeSetup re-runs the repo's worktree setup script for an existing
// worktree session, reporting completion via worktreeSetupResultMsg.
func (h *Home) runWorktreeSetup(inst *session.Instance) tea.Cmd {
	id := inst.ID
	repoRoot := inst.WorktreeRepoRoot
	wtPath := inst.WorktreePath
	title := inst.Title
	return func() tea.Msg {
		scriptPath, _ := git.FindWorktreeSetupScript(repoRoot)
		if scriptPath == "" {
			return worktreeSetupResultMsg{
				sessionID:    id,
				sessionTitle: title,
				err:          fmt.Errorf("no setup script found at .agent-deck/worktree-setup.sh"),
			}
		}
		var buf bytes.Buffer
		// Routed through the consent gate (GateAndRunWorktreeSetupScript) so a
		// manual re-run can never execute a script the user hasn't approved —
		// same trust check as the automatic run at worktree-creation time.
		err := git.GateAndRunWorktreeSetupScript(repoRoot, wtPath, &buf, &buf, session.GetWorktreeSettings().SetupTimeout())
		if err != nil {
			uiLog.Warn("worktree_setup_rerun_failed", slog.String("error", err.Error()), slog.String("output", setupOutputForLog(buf.String())))
			return worktreeSetupResultMsg{sessionID: id, sessionTitle: title, err: withSetupOutputTail(err, buf.String())}
		}
		return worktreeSetupResultMsg{sessionID: id, sessionTitle: title}
	}
}

// finishWorktree performs the worktree finish operation asynchronously:
// merge branch, remove worktree, delete branch, kill session, remove from storage
func (h *Home) finishWorktree(inst *session.Instance, sessionID, sessionTitle, branchName, repoRoot, worktreePath string, mergeEnabled bool, targetBranch string, keepBranch bool, sharedWorktree bool) tea.Cmd {
	return func() tea.Msg {
		merged := false

		// Step 1: Merge (if requested). git.MergeBack handles both regular
		// and bare-repo layouts; in bare layouts the project root has no
		// working tree, so checkout/merge cannot run there (#891).
		if mergeEnabled {
			if err := git.MergeBack(repoRoot, branchName, targetBranch); err != nil {
				return worktreeFinishResultMsg{
					sessionID: sessionID, sessionTitle: sessionTitle,
					err: fmt.Errorf("merge failed: %v", err),
				}
			}
			merged = true
		}

		// #1449: when other live sessions still share this worktree, the
		// destructive git steps (remove the shared dir + delete the branch)
		// would strand those siblings (their `worktree info` → MISSING). Skip
		// them and merely detach THIS session; the last sharer to finish runs
		// the real cleanup. sharedWorktree is snapshotted by the caller under
		// instancesMu so this async closure never touches h.instances.
		if sharedWorktree {
			uiLog.Info("worktree_finish_skipped_shared",
				slog.String("id", sessionID),
				slog.String("path", worktreePath),
				slog.String("reason", "another live session still references this worktree (#1449)"))
		} else {
			// Step 2: Remove worktree
			if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
				_ = git.RemoveWorktree(repoRoot, worktreePath, false)
			}
			_ = git.PruneWorktrees(repoRoot)

			// Step 3: Delete branch (if not keeping)
			if !keepBranch {
				// Use force delete if we merged (branch is fully merged), regular delete otherwise
				_ = git.DeleteBranch(repoRoot, branchName, merged)
			}
		}

		// Step 4: Kill tmux session
		if inst != nil && inst.Exists() {
			_ = inst.Kill()
		}

		return worktreeFinishResultMsg{
			sessionID:    sessionID,
			sessionTitle: sessionTitle,
			targetBranch: targetBranch,
			merged:       merged,
		}
	}
}
