package ui

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/menudata"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
	"github.com/asheshgoplani/agent-deck/internal/vcsbackend"
)

// Compile-time check: WebMutator must implement menudata.SessionMutator.
var _ menudata.SessionMutator = (*WebMutator)(nil)

// WebMutator bridges the web HTTP handlers to the TUI session/group management
// methods. It wraps the Home model and implements menudata.SessionMutator.
//
// The undoStack/undoWindow fields support the web's Chrome-style undo of
// deletes (POST /api/sessions/undelete). The TUI maintains its own
// in-memory stack in Home; the web stack is kept here so that web
// deletes/undos don't race with the Tea Update goroutine.
type WebMutator struct {
	h *Home

	undoMu     sync.Mutex
	undoStack  []webDeletedEntry
	undoWindow time.Duration

	// headlessTxMu serializes the full hydrate -> mutate -> persist transaction
	// in headless (`web --no-tui`) mode (#1397). Without it, two concurrent HTTP
	// handlers could each hydrate (replacing h.instances/instanceByID/groupTree),
	// mutate a now-detached snapshot, and persist over each other — a lost
	// update. Only contended in headless mode; in live-TUI mode the Tea loop
	// owns that state and the mutator never hydrates, so this is uncontended.
	headlessTxMu sync.Mutex
}

type webDeletedEntry struct {
	instance  *session.Instance
	deletedAt time.Time
}

// NewWebMutator returns a WebMutator backed by the given Home. The undo
// window defaults to menudata.DefaultUndoWindow (30s).
func NewWebMutator(h *Home) *WebMutator {
	return &WebMutator{h: h, undoWindow: menudata.DefaultUndoWindow}
}

// WithUndoWindow overrides the undo grace period (useful for tests that
// need to force expiry without sleeping).
func (m *WebMutator) WithUndoWindow(d time.Duration) *WebMutator {
	m.undoWindow = d
	return m
}

// beginHeadlessTx serializes and hydrates a headless mutation (#1397). It
// returns an unlock function the caller MUST defer.
//
// In `web --no-tui` mode no bubbletea loop ever populates
// h.instances/instanceByID/groupTree, so every lookup would miss pre-existing
// sessions and persistAllInstances([]) would trip the empty-sweep guard. This
// helper:
//
//  1. takes headlessTxMu so the whole hydrate -> mutate -> persist sequence runs
//     as one critical section (no concurrent handler can replace the in-memory
//     snapshot mid-mutation — prevents lost updates), and
//  2. reloads the registry from storage so the mutation sees the current state,
//     including out-of-band changes from a concurrent CLI add/rm.
//
// In live-TUI mode it is a pure no-op (returns a no-op unlock and does NOT
// hydrate): the Tea loop owns that state and re-reading/locking here would
// race it.
//
// Callers in live mode pay only a nil check and a closure; the mutex is never
// contended because hydration never runs there.
func (m *WebMutator) beginHeadlessTx() (unlock func(), err error) {
	if m.h == nil || !m.h.IsHeadless() {
		return func() {}, nil
	}
	m.headlessTxMu.Lock()
	if hErr := m.h.HydrateInstancesFromStorage(); hErr != nil {
		m.headlessTxMu.Unlock()
		return func() {}, hErr
	}
	return m.headlessTxMu.Unlock, nil
}

// suppressHookPromptForWeb keeps worktree hooks reached from a web request
// fail-closed (skipped with a notice) instead of opening the TUI's hook
// approval dialog for a request the local operator did not make. Work a
// mutation defers past its return (a queued tea.Cmd) is not covered and may
// still ask the local operator, which can only ever result in a local human
// decision, never an unapproved run.
func suppressHookPromptForWeb() (restore func()) { return git.SuppressScriptConsentPrompter() }

// CreateSession creates and starts a new session, persisting it to storage.
func (m *WebMutator) CreateSession(title, tool, projectPath, groupPath, modelID, reasoningEffort string) (string, error) {
	defer suppressHookPromptForWeb()()
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return "", err
	}
	defer unlock()
	// #1706: project_path is identity and must be absolute — the request may
	// carry a relative path, which tmux would resolve against the tmux server's
	// cwd rather than this process's.
	projectPath, err = session.ResolveProjectPath(projectPath)
	if err != nil {
		return "", err
	}
	var inst *session.Instance
	if groupPath != "" {
		inst = session.NewInstanceWithGroupAndTool(title, projectPath, groupPath, tool)
	} else {
		inst = session.NewInstanceWithTool(title, projectPath, tool)
	}
	if tool != "" && tool != "shell" {
		inst.Command = tool
	}

	if modelID = strings.TrimSpace(modelID); modelID != "" {
		if err := inst.ApplyLaunchModel(modelID); err != nil {
			return "", err
		}
	}
	if reasoningEffort = strings.TrimSpace(reasoningEffort); reasoningEffort != "" {
		if err := inst.ApplyLaunchReasoningEffort(reasoningEffort); err != nil {
			return "", err
		}
	}

	if err := inst.Start(); err != nil {
		return "", fmt.Errorf("start session: %w", err)
	}
	inst.RecordTelemetryCreate(telemetry.ViaWeb)

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return "", fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	m.h.instancesMu.RLock()
	existing := make([]*session.Instance, len(m.h.instances))
	copy(existing, m.h.instances)
	m.h.instancesMu.RUnlock()

	allInstances := append(existing, inst) //nolint:gocritic
	if err := storage.SaveWithGroups(allInstances, m.h.groupTree); err != nil {
		return "", fmt.Errorf("save session: %w", err)
	}
	return inst.ID, nil
}

// StartSession starts a stopped/idle session by ID.
func (m *WebMutator) StartSession(id string) error {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	inst := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if inst == nil {
		return fmt.Errorf("session not found: %s", id)
	}
	return inst.Start()
}

// StopSession kills (stops) a running session by ID.
func (m *WebMutator) StopSession(id string) error {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	inst := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if inst == nil {
		return fmt.Errorf("session not found: %s", id)
	}
	loaded := session.BeforeStopKill(m.h.storage, id)
	if err := inst.Kill(); err != nil {
		return err
	}
	// Defect 317: the stop's own verdict lands now, so the daemon's error
	// sample of this kill cannot refuse the later full save.
	session.WriteStoppedAfterKill(m.h.storage, inst, loaded)
	inst.RecordTelemetryEndFrom(telemetry.EndStop, telemetry.SurfaceWeb)
	return nil
}

// RestartSession restarts a session by ID.
func (m *WebMutator) RestartSession(id string) error {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	inst := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if inst == nil {
		return fmt.Errorf("session not found: %s", id)
	}
	return inst.RestartFrom(telemetry.SurfaceWeb)
}

// DeleteSession kills a session and removes it from persistent storage.
// Before removal, the instance is pushed onto the web undo stack so a
// subsequent UndoDelete (POST /api/sessions/undelete) can restore it.
func (m *WebMutator) DeleteSession(id string) error {
	defer suppressHookPromptForWeb()()
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	inst := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if inst == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	// Kill the tmux session (ignore errors — may already be stopped)
	_ = inst.Kill()

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	// Undoable: record the pending sweep before the row goes, so a crash
	// after the delete still leaves it for the startup reconcile.
	if err := storage.MarkPendingDeleteSweep(id); err != nil {
		// The id comes from the web request; strip line breaks so it cannot
		// forge log lines (code-scanning go/log-injection).
		uiLog.Warn("web_mark_pending_sweep_err",
			slog.String("id", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(id), "\n", ""), "\r", "")),
			slog.String("err", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(err.Error()), "\n", ""), "\r", "")))
	}
	if err := storage.DeleteInstance(id); err != nil {
		return err
	}
	// Undoable (POST /api/sessions/undelete): announce it now; the inbox and
	// notify-state sweep (#910) waits until the delete is final (the entry
	// expires, leaves the undo stack or the server stops, see pushUndo), so
	// an undo keeps the
	// session's pending inbox events.
	session.PublishSessionClosed(storage.Profile(), id, session.SessionClosedRemoved)
	inst.RecordTelemetryEndFrom(telemetry.EndDelete, telemetry.SurfaceWeb)
	m.pushUndo(inst)
	return nil
}

// CloseSession stops the session process but keeps its metadata in
// storage. Mirrors the TUI's Shift+D handler (internal/ui/home.go
// closeSession). Identical to StopSession at the session.Instance level
// — both call Kill() — but is kept distinct so the parity matrix and
// the front-end can express the user-visible intent ("close, but don't
// delete").
func (m *WebMutator) CloseSession(id string) error {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	inst := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if inst == nil {
		return fmt.Errorf("session not found: %s", id)
	}
	if err := inst.Kill(); err != nil {
		return err
	}
	inst.RecordTelemetryEndFrom(telemetry.EndStop, telemetry.SurfaceWeb)
	return nil
}

// ArchiveSession stops the session process and marks it archived so it
// is hidden from active lists but retained in storage.
func (m *WebMutator) ArchiveSession(id string) error {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	inst := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if inst == nil {
		return fmt.Errorf("session not found: %s", id)
	}
	if err := inst.Kill(); err != nil {
		return fmt.Errorf("failed to stop session: %w", err)
	}
	m.h.instancesMu.Lock()
	inst.SetArchivedAtThreadSafe(time.Now().UTC())
	m.h.instancesMu.Unlock()
	return m.persistAllInstances()
}

// UnarchiveSession clears the archive flag without starting tmux.
func (m *WebMutator) UnarchiveSession(id string) error {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	inst := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if inst == nil {
		return fmt.Errorf("session not found: %s", id)
	}
	m.h.instancesMu.Lock()
	if !inst.IsArchived() {
		m.h.instancesMu.Unlock()
		return fmt.Errorf("session is not archived: %s", id)
	}
	inst.SetArchivedAtThreadSafe(time.Time{})
	m.h.instancesMu.Unlock()
	return m.persistAllInstances()
}

func (m *WebMutator) persistAllInstances() error {
	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	m.h.instancesMu.RLock()
	instances := make([]*session.Instance, len(m.h.instances))
	copy(instances, m.h.instances)
	m.h.instancesMu.RUnlock()

	if err := storage.SaveWithGroups(instances, m.h.groupTree); err != nil {
		return fmt.Errorf("save sessions: %w", err)
	}
	return nil
}

// UndoDelete restores the most-recently deleted session if its delete
// timestamp is within the configured undo window. Returns the restored
// session id. Returns menudata.ErrUndoNothing if the stack is empty, or
// menudata.ErrUndoExpired if the most recent entry is older than the window.
func (m *WebMutator) UndoDelete() (string, error) {
	m.undoMu.Lock()
	if len(m.undoStack) == 0 {
		m.undoMu.Unlock()
		return "", menudata.ErrUndoNothing
	}
	entry := m.undoStack[len(m.undoStack)-1]
	m.undoStack = m.undoStack[:len(m.undoStack)-1]
	window := m.undoWindow
	m.undoMu.Unlock()

	if window == 0 {
		window = menudata.DefaultUndoWindow
	}
	if time.Since(entry.deletedAt) > window {
		m.finalizeWebDeletes([]webDeletedEntry{entry})
		return "", menudata.ErrUndoExpired
	}
	id, err := m.restoreDeleted(entry)
	if err != nil {
		// The entry is popped and the session is not back: the delete is
		// final, so its deferred sweep runs now (it skips a row that did
		// make it back into storage).
		m.finalizeWebDeletes([]webDeletedEntry{entry})
		return "", err
	}
	return id, nil
}

// restartForUndo restarts a session an undo brings back (a seam for tests).
var restartForUndo = func(inst *session.Instance) error { return inst.Restart() }

// restoreDeleted restarts a popped undo entry and re-persists it.
func (m *WebMutator) restoreDeleted(entry webDeletedEntry) (string, error) {

	// Restart the session and re-persist alongside the rest of the
	// current in-memory list. Note: Restart() may not succeed for every
	// tool (e.g. a tool the user has since uninstalled). Bubble the
	// error up so the handler returns 500; the entry has already been
	// popped, mirroring the TUI's ctrl+z semantics.
	if err := restartForUndo(entry.instance); err != nil {
		return "", fmt.Errorf("restart session: %w", err)
	}

	// #1397: hydrate + serialize before reading/persisting the in-memory list so
	// the restored row is appended to the CURRENT registry (in headless mode the
	// list would otherwise be empty, dropping every other session) and so this
	// re-persist does not race a concurrent mutation.
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return "", err
	}
	defer unlock()

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return "", fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	m.h.instancesMu.RLock()
	existing := make([]*session.Instance, len(m.h.instances))
	copy(existing, m.h.instances)
	m.h.instancesMu.RUnlock()
	allInstances := append(existing, entry.instance) //nolint:gocritic
	if err := storage.SaveWithGroups(allInstances, m.h.groupTree); err != nil {
		return "", fmt.Errorf("save session: %w", err)
	}
	// The row is back: the delete has nothing left to sweep.
	if err := storage.ClearPendingDeleteSweep(entry.instance.ID); err != nil {
		// Strip line breaks so neither can forge log lines (go/log-injection).
		uiLog.Warn("web_clear_pending_sweep_err",
			slog.String("id", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(entry.instance.ID), "\n", ""), "\r", "")),
			slog.String("err", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(err.Error()), "\n", ""), "\r", "")))
	}
	return entry.instance.ID, nil
}

// pushUndo records a freshly-deleted instance onto the web undo stack,
// capped at 10 entries (FIFO eviction) to bound memory. Entries past the undo
// window, or evicted by the cap, can no longer be undone: their deletes are
// final and the deferred inbox and notify-state sweep (#910) runs for them.
func (m *WebMutator) pushUndo(inst *session.Instance) {
	// The row is deleted: an undo must re-insert it, not update it.
	inst.ForgetStorageSnapshot()
	m.undoMu.Lock()
	window := m.undoWindow
	if window == 0 {
		window = menudata.DefaultUndoWindow
	}
	var final []webDeletedEntry
	kept := m.undoStack[:0]
	for _, e := range m.undoStack {
		if time.Since(e.deletedAt) > window {
			final = append(final, e)
		} else {
			kept = append(kept, e)
		}
	}
	m.undoStack = append(kept, webDeletedEntry{
		instance:  inst,
		deletedAt: time.Now(),
	})
	if len(m.undoStack) > 10 {
		final = append(final, m.undoStack[:len(m.undoStack)-10]...)
		m.undoStack = append([]webDeletedEntry(nil), m.undoStack[len(m.undoStack)-10:]...)
	}
	m.undoMu.Unlock()
	m.finalizeWebDeletes(final)
	// Sweep this entry once its undo window passes, even when no later web
	// action comes.
	time.AfterFunc(window+time.Millisecond, m.sweepExpiredUndo)
}

// sweepExpiredUndo finalizes the undo entries whose window has passed.
func (m *WebMutator) sweepExpiredUndo() {
	m.undoMu.Lock()
	window := m.undoWindow
	if window == 0 {
		window = menudata.DefaultUndoWindow
	}
	var final []webDeletedEntry
	kept := m.undoStack[:0]
	for _, e := range m.undoStack {
		if time.Since(e.deletedAt) > window {
			final = append(final, e)
		} else {
			kept = append(kept, e)
		}
	}
	m.undoStack = kept
	m.undoMu.Unlock()
	m.finalizeWebDeletes(final)
}

// FinalizePendingDeletes ends every delete still on the web undo stack: the
// server is stopping, so they can no longer be undone and their deferred
// sweep runs now. Called by menudata.Server.Shutdown and on a signal exit.
func (m *WebMutator) FinalizePendingDeletes() {
	m.FinalizePendingDeletesWithin(0)
}

// FinalizePendingDeletesWithin is FinalizePendingDeletes waiting at most
// timeout for the sweep (0 = no limit); what is not swept by then keeps its
// pending record for the next start's reconcile.
func (m *WebMutator) FinalizePendingDeletesWithin(timeout time.Duration) {
	m.undoMu.Lock()
	final := m.undoStack
	m.undoStack = nil
	m.undoMu.Unlock()
	if timeout <= 0 {
		m.finalizeWebDeletes(final)
		return
	}
	runBounded(timeout, func() { m.finalizeWebDeletes(final) })
}

// finalizeWebDeletes runs the deferred sweep for web deletes that can no
// longer be undone (see session.Storage.FinalizeRemovedSessions).
func (m *WebMutator) finalizeWebDeletes(entries []webDeletedEntry) {
	var ids []string
	for _, e := range entries {
		if e.instance != nil && e.instance.ID != "" {
			ids = append(ids, e.instance.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	// The Home's storage is open for the whole process (its methods lock);
	// open one only when the Home has none.
	var storage *session.Storage
	if m.h != nil {
		storage = m.h.storage
	}
	if storage == nil {
		profile := ""
		if m.h != nil {
			profile = m.h.profile
		}
		opened, err := session.NewStorageWithProfile(profile)
		if err != nil {
			uiLog.Warn("web_delete_finalize_sweep_skipped", slog.Int("count", len(ids)), slog.String("err", err.Error()))
			return
		}
		defer opened.Close()
		storage = opened
	}
	finalizeRemovedSessionsFn(storage, "web_delete", ids)
}

// ForkSession forks an existing session using the proper tool-specific fork command.
func (m *WebMutator) ForkSession(id string) (string, error) {
	defer suppressHookPromptForWeb()()
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return "", err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	parent := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if parent == nil {
		return "", fmt.Errorf("session not found: %s", id)
	}

	forked, _, err := parent.CreateForkedInstanceForTool(parent.Title+" (fork)", parent.GroupPath, nil)
	if err != nil {
		return "", fmt.Errorf("fork session: %w", err)
	}

	if err := forked.Start(); err != nil {
		return "", fmt.Errorf("start forked session: %w", err)
	}
	forked.RecordTelemetryCreate(telemetry.ViaWeb)

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return "", fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	m.h.instancesMu.RLock()
	existing := make([]*session.Instance, len(m.h.instances))
	copy(existing, m.h.instances)
	m.h.instancesMu.RUnlock()

	allInstances := append(existing, forked) //nolint:gocritic
	if err := storage.SaveWithGroups(allInstances, m.h.groupTree); err != nil {
		return "", fmt.Errorf("save forked session: %w", err)
	}
	return forked.ID, nil
}

// UpdateSession applies one or more field edits via session.SetField (the
// same path the TUI EditSessionDialog uses) and persists. Returns the list
// of fields that actually changed and whether any change requires a restart.
//
// instancesMu is held only across the SetField loop — postCommits and the
// storage flush run after unlock, mirroring the TUI's home.go edit handler
// so slow tmux subprocesses don't stall the status worker.
func (m *WebMutator) UpdateSession(id string, updates map[string]string) ([]string, bool, error) {
	if len(updates) == 0 {
		return nil, false, nil
	}
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	inst := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if inst == nil {
		return nil, false, fmt.Errorf("session not found: %s", id)
	}

	changed := make([]string, 0, len(updates))
	restartRequired := false
	var postCommits []func()

	m.h.instancesMu.Lock()
	for field, value := range updates {
		oldValue, postCommit, err := session.SetField(inst, field, value, nil)
		if err != nil {
			m.h.instancesMu.Unlock()
			return nil, false, err
		}
		// #1706: SetField canonicalizes a project path, so a request carrying
		// another spelling of the stored path is a no-op — compare what was
		// actually stored, not the raw request value, or it would be reported
		// as changed and restart-required.
		newValue := value
		if field == session.FieldPath {
			newValue = inst.ProjectPath
		}
		if oldValue == newValue {
			continue
		}
		changed = append(changed, field)
		if postCommit != nil {
			postCommits = append(postCommits, postCommit)
		}
		if session.RestartPolicyFor(field) == session.FieldRestartRequired {
			restartRequired = true
		}
	}
	m.h.instancesMu.Unlock()

	for _, fn := range postCommits {
		fn()
	}

	if len(changed) == 0 {
		return nil, false, nil
	}

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return nil, false, fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	m.h.instancesMu.RLock()
	instances := make([]*session.Instance, len(m.h.instances))
	copy(instances, m.h.instances)
	m.h.instancesMu.RUnlock()

	if err := storage.SaveWithGroups(instances, m.h.groupTree); err != nil {
		return nil, false, fmt.Errorf("save session: %w", err)
	}
	return changed, restartRequired, nil
}

// CreateGroup creates a new group (or subgroup if parentPath is non-empty) and
// persists the group tree to storage.
func (m *WebMutator) CreateGroup(name, parentPath string) (string, error) {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return "", err
	}
	defer unlock()
	// Refuse an unknown parent like the CLI does; CreateSubgroup would
	// otherwise store an orphan "<parent>/<name>" row with no parent group.
	if parentPath != "" {
		if _, ok := m.h.groupTree.Groups[parentPath]; !ok {
			return "", fmt.Errorf("parent group %q: %w", parentPath, menudata.ErrGroupNotFound)
		}
	}
	// Seed the new-group default from [group_defaults].max_concurrent.
	if cfg, _ := session.LoadUserConfig(); cfg != nil {
		m.h.groupTree.DefaultMaxConcurrent = cfg.GroupDefaults.MaxConcurrent
	}
	var grp *session.Group
	if parentPath != "" {
		grp = m.h.groupTree.CreateSubgroup(parentPath, name)
	} else {
		grp = m.h.groupTree.CreateGroup(name)
	}
	if grp == nil {
		return "", fmt.Errorf("failed to create group %q", name)
	}

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return "", fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	m.h.instancesMu.RLock()
	instances := make([]*session.Instance, len(m.h.instances))
	copy(instances, m.h.instances)
	m.h.instancesMu.RUnlock()

	if err := storage.SaveWithGroups(instances, m.h.groupTree); err != nil {
		return "", fmt.Errorf("save group: %w", err)
	}
	return grp.Path, nil
}

// RenameGroup renames a group identified by groupPath to newName, persists,
// and returns the group's path after the rename.
func (m *WebMutator) RenameGroup(groupPath, newName string) (string, error) {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return "", err
	}
	defer unlock()
	newPath := m.h.groupTree.RenameTargetPath(groupPath, newName)
	if err := m.h.groupTree.RenameGroup(groupPath, newName); err != nil {
		return "", err
	}

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return "", fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	m.h.instancesMu.RLock()
	instances := make([]*session.Instance, len(m.h.instances))
	copy(instances, m.h.instances)
	m.h.instancesMu.RUnlock()

	if err := storage.SaveWithGroups(instances, m.h.groupTree); err != nil {
		return "", err
	}
	return newPath, nil
}

// SetGroupExpanded persists a group's collapse state, so the web sidebar and
// the TUI stop drifting apart.
//
// SCOPE: this reaches a TUI sharing this process immediately, because the flip
// lands on the very GroupTree that TUI renders from. A SEPARATE TUI process on
// the same profile (the `agent-deck web --no-tui` daemon deployment) is a
// weaker story on two counts: SaveGroupsOnly deliberately skips Touch()
// (storage.go:1152), so no StorageWatcher reload is scheduled; and when that
// TUI does reload for any other reason it re-applies its own in-memory
// Expanded over the freshly-loaded rows (home.go:7639-7656). Making the
// cross-process direction reliable belongs in that reload branch, not here.
//
// Deliberately mirrors the TUI's own toggle path (home.go: ToggleGroup then
// saveGroupState) rather than RenameGroup's: collapsing touches only group
// metadata, so it uses SaveGroupsOnly and never rewrites the instance rows.
// Expand/CollapseGroup do not cascade to children — visibility is derived by
// walking the ancestor chain at render time (GroupTree.ancestorsExpanded) —
// so a single-group write is the whole change.
//
// A derived group (one implied by a session's path but never stored) is
// allowed here and becomes a real row on save: its storage snapshot carries
// ensure:true, which is exactly what happens when the TUI collapses one.
func (m *WebMutator) SetGroupExpanded(groupPath string, expanded bool) error {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return err
	}
	defer unlock()

	if m.h == nil {
		return menudata.ErrGroupNotFound
	}
	// Resolve the tree ONCE. beginHeadlessTx's hydrate replaces h.groupTree
	// wholesale (home.go:5153-5157), and in live-TUI mode the Tea loop can
	// swap it on a reload, so re-reading the field between the lookup, the
	// flip and the save could persist a tree that never saw the flip.
	tree := m.h.groupTree
	if tree == nil {
		return menudata.ErrGroupNotFound
	}
	if _, ok := tree.Groups[groupPath]; !ok {
		return menudata.ErrGroupNotFound
	}
	prior := tree.Groups[groupPath].Expanded
	if expanded {
		tree.ExpandGroup(groupPath)
	} else {
		tree.CollapseGroup(groupPath)
	}

	if m.h.storage == nil {
		// Headless/no-persistence configuration: the in-memory flip above is
		// all there is to do, and the next snapshot already reflects it.
		return nil
	}
	if err := m.h.storage.SaveGroupsOnly(tree.ShallowCopyForSave()); err != nil {
		// Roll the flip back. The handler turns this into a 500 and the client
		// reverts its optimistic toggle, so leaving the tree flipped would have
		// the very next snapshot publish a collapse state that was never
		// stored — and a TUI sharing this process render it.
		if g, ok := tree.Groups[groupPath]; ok {
			g.Expanded = prior
			tree.Expanded[groupPath] = prior
		}
		return err
	}
	return nil
}

// MoveSessionToGroup moves a session to another group with the same target
// resolution as `agent-deck group move` (session.GroupTree.
// ResolveMoveTargetGroup) and persists. Like the CLI and the TUI's M, nothing
// is migrated: when the destination group resolves a different Claude config
// dir, restartRequired reports that the session picks it up on its next
// restart (#2368).
func (m *WebMutator) MoveSessionToGroup(id, groupPath string) (string, bool, error) {
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return "", false, err
	}
	defer unlock()

	m.h.instancesMu.Lock()
	inst := m.h.instanceByID[id]
	if inst == nil {
		m.h.instancesMu.Unlock()
		return "", false, menudata.ErrSessionNotFound
	}
	// Seed the new-group default in case the target must be auto-created.
	if cfg, _ := session.LoadUserConfig(); cfg != nil {
		m.h.groupTree.DefaultMaxConcurrent = cfg.GroupDefaults.MaxConcurrent
	}
	target := m.h.groupTree.ResolveMoveTargetGroup(groupPath)
	restartRequired := session.IsClaudeCompatible(inst.Tool) &&
		session.GetClaudeConfigDirForInstanceInGroup(inst, target) != session.GetClaudeConfigDirForInstance(inst)
	m.h.groupTree.MoveSessionToGroup(inst, target)
	instances := make([]*session.Instance, len(m.h.instances))
	copy(instances, m.h.instances)
	m.h.instancesMu.Unlock()

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return "", false, fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups(instances, m.h.groupTree); err != nil {
		return "", false, fmt.Errorf("save session: %w", err)
	}
	return target, restartRequired, nil
}

// FinishWorktree merges (or skips), removes the worktree, optionally
// deletes the source branch, kills the tmux session, and removes the
// session from storage. Mirrors `agent-deck worktree finish` (see
// cmd/agent-deck/worktree_cmd.go handleWorktreeFinish) — the
// orchestration is duplicated rather than refactored to keep the
// fix minimally invasive (issue #1126).
func (m *WebMutator) FinishWorktree(id string, opts menudata.WorktreeFinishOptions) (menudata.WorktreeFinishResult, error) {
	defer suppressHookPromptForWeb()()
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return menudata.WorktreeFinishResult{}, err
	}
	defer unlock()
	m.h.instancesMu.RLock()
	inst := m.h.instanceByID[id]
	m.h.instancesMu.RUnlock()
	if inst == nil {
		return menudata.WorktreeFinishResult{}, menudata.ErrSessionNotFound
	}
	if !inst.IsWorktree() {
		return menudata.WorktreeFinishResult{}, menudata.ErrNotAWorktree
	}

	repoRoot := inst.WorktreeRepoRoot
	worktreePath := inst.WorktreePath
	worktreeBranch := inst.WorktreeBranch

	backend, err := vcsbackend.Detect(repoRoot)
	if err != nil {
		return menudata.WorktreeFinishResult{}, fmt.Errorf("initialize VCS: %w", err)
	}

	if !opts.Force {
		dirty, dErr := git.HasUncommittedChanges(worktreePath)
		if dErr != nil {
			if _, statErr := os.Stat(worktreePath); os.IsNotExist(statErr) {
				dirty = false
			} else {
				return menudata.WorktreeFinishResult{}, fmt.Errorf("check worktree status: %w", dErr)
			}
		}
		if dirty {
			return menudata.WorktreeFinishResult{}, fmt.Errorf("worktree has uncommitted changes (set force=true to override)")
		}
	}

	targetBranch := opts.Into
	if targetBranch == "" && !opts.NoMerge {
		targetBranch, err = backend.GetDefaultBranch()
		if err != nil {
			return menudata.WorktreeFinishResult{}, fmt.Errorf("determine target branch: %w (set into=<branch>)", err)
		}
	}
	if !opts.NoMerge && targetBranch == worktreeBranch {
		return menudata.WorktreeFinishResult{}, fmt.Errorf("cannot merge branch %q into itself", worktreeBranch)
	}

	if !opts.NoMerge {
		// Checkout target in main repo, then merge.
		checkout := exec.Command("git", "-C", repoRoot, "checkout", targetBranch)
		if out, cErr := checkout.CombinedOutput(); cErr != nil {
			return menudata.WorktreeFinishResult{}, fmt.Errorf("checkout %s: %s", targetBranch, strings.TrimSpace(string(out)))
		}
		if mErr := backend.MergeBranch(worktreeBranch); mErr != nil {
			if backend.Type() == vcs.TypeGit {
				_ = exec.Command("git", "-C", repoRoot, "merge", "--abort").Run()
			}
			return menudata.WorktreeFinishResult{}, fmt.Errorf("merge failed (aborted): %w", mErr)
		}
	}

	if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
		// Best-effort: log via error wrapping only if it bubbles. CLI
		// treats this as a warning; we mirror that by swallowing here so
		// the rest of cleanup proceeds.
		_ = backend.RemoveWorktree(worktreePath, opts.Force)
	}
	_ = backend.PruneWorktrees()

	branchDeleted := false
	if !opts.KeepBranch {
		if dErr := backend.DeleteBranch(worktreeBranch, opts.Force); dErr == nil {
			branchDeleted = true
		}
	}

	if inst.Exists() {
		_ = inst.Kill()
	}

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return menudata.WorktreeFinishResult{}, fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	m.h.instancesMu.RLock()
	existing := make([]*session.Instance, 0, len(m.h.instances))
	for _, x := range m.h.instances {
		if x.ID != id {
			existing = append(existing, x)
		}
	}
	m.h.instancesMu.RUnlock()
	// #1396: use the targeted RemoveSessionAndVerify path, NOT
	// SaveWithGroups(existing, ...). Historically an empty `existing` tripped
	// the S1 empty-sweep guard AFTER the irreversible git steps, orphaning the
	// row; since #1550 SaveWithGroups is upsert-only and would not delete the
	// row at all. Either way, removal requires the targeted DELETE.
	if sErr := storage.RemoveSessionAndVerify(id, existing, m.h.groupTree); sErr != nil {
		return menudata.WorktreeFinishResult{}, fmt.Errorf("save session data: %w", sErr)
	}

	// Issue #1576: sweep transition-notifier state (inbox JSONL lines +
	// runtime/transition-notify-state.json dedup record) for the removed
	// session, mirroring the #910 cleanup on `agent-deck rm`. Best-effort —
	// never fails the finish. The same helper publishes macapp.session.closed.
	session.CleanupRemovedSession(storage.Profile(), id)

	mergedInto := targetBranch
	if opts.NoMerge {
		mergedInto = ""
	}
	return menudata.WorktreeFinishResult{
		SessionID:     id,
		Branch:        worktreeBranch,
		MergedInto:    mergedInto,
		Merged:        !opts.NoMerge,
		BranchDeleted: branchDeleted,
	}, nil
}

// DeleteGroup deletes a group (and its subgroups), moving sessions to the default
// group. Returns an error if groupPath is the default group.
func (m *WebMutator) DeleteGroup(groupPath string) error {
	if groupPath == session.DefaultGroupPath {
		return fmt.Errorf("cannot delete default group")
	}
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		return err
	}
	defer unlock()

	m.h.groupTree.DeleteGroup(groupPath)

	storage, err := session.NewStorageWithProfile(m.h.profile)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	m.h.instancesMu.RLock()
	instances := make([]*session.Instance, len(m.h.instances))
	copy(instances, m.h.instances)
	m.h.instancesMu.RUnlock()

	return storage.SaveWithGroups(instances, m.h.groupTree)
}
