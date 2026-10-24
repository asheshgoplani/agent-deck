package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/docker"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ensureClaudeSessionIDFromDiskForRestart is the Restart()-path variant of
// ensureClaudeSessionIDFromDisk. Issue #956: custom-command Claude sessions
// (Tool=claude with a wrapper Command) bypass happy-path session-id capture,
// and if no hook ever propagated CLAUDE_SESSION_ID back to the Instance the
// ClaudeSessionID field stays empty even after a real conversation has
// written a JSONL transcript to disk. On Restart() the fallback recreate
// branch then re-spawns the wrapper without `--resume`, dropping history.
//
// Start()'s prelude (ensureClaudeSessionIDFromDisk) refuses to discover for
// instances with ClaudeDetectedAt==zero (issue #608) so a brand-new spawn
// does not adopt another session's history from the same project directory.
// Restart() implies the instance previously ran — the tmux session existed
// and (in the bug scenario) had a live Claude conversation — so the gate
// is safe to bypass here. ClaudeDetectedAt is then stamped so subsequent
// callers (status refresh, persistence) see a consistent capture time.
func (i *Instance) ensureClaudeSessionIDFromDiskForRestart() {
	// Issue #1147: an explicit `--session-id <uuid>` in i.Command is the
	// user's authoritative declaration of WHICH conversation this session
	// owns. In multi-session-per-cwd setups (5 tenant sessions sharing one
	// project dir, each with its own --session-id), the pre-#1147
	// disk-discovery walk picks the newest sibling JSONL by mtime and
	// silently hijacks every sibling's id onto whichever transcript was
	// written last. The dup-sweeper then kills 4 of 5 sessions for
	// sharing a CLAUDE_SESSION_ID. Adopting the explicit id BEFORE the
	// non-empty short-circuit ensures it also corrects a previously-
	// hijacked id from an earlier buggy run.
	// Issue #1851: a remote session's conversation lives on the remote host, so
	// a local ~/.claude/projects lookup can never be right for it. Without this
	// bail-out the walk below scans the CONTROLLER's projects dir — keyed on
	// ProjectPath, which for an --ssh session is only a local placeholder
	// defaulting to the controller's cwd — and adopts whichever LOCAL session
	// wrote last. restart() then calls sweepDuplicateToolSessions(), which kills
	// the local tmux session that legitimately owns that conversation.
	//
	// A remote session's local ClaudeSessionID is essentially always empty
	// (SyncSessionIDsToTmux only writes the tmux var when the id is already
	// non-empty), so every remote restart reaches this point.
	if !i.TranscriptIsResolvableLocally() {
		sessionLog.Info("resume: skipped reason=remote_session",
			slog.String("instance_id", i.ID),
			slog.String("ssh_host", i.SSHHost),
			slog.String("ssh_remote_path", i.SSHRemotePath),
			slog.String("reason", "jsonl_discovery_restart_remote_skip"))
		return
	}
	if i.adoptExplicitClaudeSessionID("session_id_flag_explicit_restart") {
		return
	}
	if i.ClaudeSessionID != "" {
		return
	}
	lookupPath := i.EffectiveWorkingDir()
	uuid, found := discoverLatestClaudeJSONL(lookupPath)
	if !found {
		return
	}
	// #1815: see ensureClaudeSessionIDFromDisk — a discovered id is a hint,
	// not proof of ownership, and must not authorize --resume.
	i.adoptDiscoveredClaudeSessionID(uuid)
	if i.ClaudeDetectedAt.IsZero() {
		i.ClaudeDetectedAt = time.Now()
	}
	safeUUID := logging.SanitizeValue(uuid)
	sessionLog.Info("resume: id="+safeUUID+" reason=jsonl_discovery_restart",
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("claude_session_id", safeUUID),
		slog.String("path", logging.SanitizeValue(lookupPath)),
		slog.String("reason", "jsonl_discovery_restart"))
}

func (i *Instance) clearSessionBindingForFreshStart() {
	if IsClaudeCompatible(i.Tool) {
		i.ClaudeSessionID = ""
		i.ClaudeDetectedAt = time.Time{}
	}

	if i.Tool == "gemini" {
		i.GeminiSessionID = ""
		i.GeminiDetectedAt = time.Time{}
	}

	if i.Tool == "opencode" {
		i.OpenCodeSessionID = ""
		i.OpenCodeDetectedAt = time.Time{}
		i.OpenCodeStartedAt = 0
		i.lastOpenCodeScanAt = time.Time{}
	}

	if i.Tool == "codex" {
		i.CodexSessionID = ""
		i.CodexDetectedAt = time.Time{}
		i.CodexStartedAt = 0
		i.lastCodexScanAt = time.Time{}
		i.mu.Lock()
		i.pendingCodexRestartWarning = ""
		i.mu.Unlock()
	}

	if i.Tool == "copilot" {
		i.CopilotSessionID = ""
		i.CopilotDetectedAt = time.Time{}
		i.CopilotStartedAt = 0
	}

	if i.Tool == "hermes" {
		// Drop the recorded resume ID so the next launch starts a new session;
		// its first hook event records the new id.
		i.HermesSessionID = ""
		i.hermesBoundAt = time.Time{}
		i.hermesLastRejected = ""
		i.persistHermesSessionBinding("")
		i.clearHermesOwnedSessionID()
	}

	if i.Tool == "deepseek" {
		// Same contract as hermes: drop the discovered resume ID so the next
		// launch boots a new dsh session instead of reopening the old one.
		//
		// DeepSeekTask is deliberately KEPT. It is not a conversation binding —
		// it is the invocation itself for a one-shot, and a "fresh" restart of
		// `dsh --profile headless "<task>"` still means running that task in a
		// new session. Clearing it here would turn fresh-restart into an
		// unlaunchable command (PR #1942 review, P1c).
		i.DeepSeekSessionID = ""
	}

	// Custom [tools.*]: drop any persisted conversation so a deliberate
	// fresh start does not re-attach resume after reboot. Flag the clear so
	// a subsequent SaveWithGroups writes explicit empty (sticky-safe) even
	// when statedb.GetGlobal() is nil (CLI paths).
	if i.GenericSessionID != "" || !i.GenericDetectedAt.IsZero() || i.genericSessionIDCleared ||
		i.GenericSessionTool != "" || i.GenericSessionCommand != "" || i.GenericSessionLocation != "" {
		cleared := i.GenericSessionID
		i.GenericSessionID = ""
		i.GenericDetectedAt = time.Time{}
		i.GenericSessionTool = ""
		i.GenericSessionCommand = ""
		i.GenericSessionLocation = ""
		i.genericSessionIDCleared = true
		// The flag above means a later save re-applies this clear, so the
		// outcome is eventually right either way — but a write that keeps
		// failing on a data path would otherwise never be visible, and
		// this one deletes a binding the operator asked to be rid of.
		if err := i.withOwningStateDB(func(db *statedb.StateDB) error {
			return db.WriteGenericSessionBinding(i.ID, "", "", "", "", time.Time{})
		}); !errors.Is(err, errNoStateDB) {
			i.setGenericSessionPersistError(err)
			if err != nil {
				sessionLog.Warn("generic_session_clear_persist_failed",
					slog.String("instance_id", logging.SanitizeValue(i.ID)),
					slog.String("tool", logging.SanitizeValue(i.Tool)),
					slog.String("session_id_fingerprint", fingerprintSessionID(cleared)),
					slog.String("error", logging.SanitizeValue(err.Error())))
			}
		}
	}
}

func (i *Instance) recreateTmuxSession() {
	// Issue #663: multi-repo sessions must cwd into MultiRepoTempDir, not
	// ProjectPath (which is a symlink into that parent dir). Delegates to
	// EffectiveWorkingDir so single-repo sessions keep using ProjectPath.
	i.tmuxSession = tmux.NewSession(i.Title, i.EffectiveWorkingDir())
	i.tmuxSession.GroupPath = i.GroupPath
	// Preserve the socket the instance was originally created on (issue
	// #687). A restart/respawn cycle must NOT silently relocate the session
	// to the current default socket — that would strand the old tmux pane
	// on the stored socket and create an invisible duplicate on the new
	// one.
	i.tmuxSession.SocketName = i.TmuxSocketName
	i.tmuxSession.InstanceID = i.ID
	i.tmuxSession.SetInjectStatusLine(GetTmuxSettings().GetInjectStatusLine())
	i.tmuxSession.SetMouse(GetTmuxSettings().GetMouse())
	i.tmuxSession.SetIndicZeroWidthMarks(GetTmuxSettings().IndicZeroWidthMarks)
	i.tmuxSession.SetClearOnRestart(GetTmuxSettings().ClearOnRestart)
	i.tmuxSession.SetTerminalChromeEnabled(GetTerminalSettings().GetITermBadge())
}

func (i *Instance) prepareRestartMCPConfig() {
	// Clear flag immediately to prevent it staying set if restart fails.
	skipRegen := i.SkipMCPRegenerate
	i.SkipMCPRegenerate = false

	if IsClaudeCompatible(i.Tool) && !skipRegen {
		if err := i.regenerateMCPConfig(); err != nil {
			mcpLog.Warn("mcp_config_regen_failed", slog.String("error", err.Error()))
		}
	} else if skipRegen {
		mcpLog.Debug("mcp_regen_skipped", slog.String("reason", "flag_set_by_apply"))
	}
}

// ServiceUnitOwnership snapshots the evidence needed to decide whether
// this instance may retire its transient systemd-user service unit
// (issue #1721). MUST be called BEFORE teardown: it reads the pid of the
// tmux server generation this session belongs to, which is unobservable
// once the session is killed.
//
// Returns a zero value (which always skips the stop) when the instance has
// no tmux session.
func (i *Instance) ServiceUnitOwnership() tmux.ServiceUnitOwnership {
	if i.tmuxSession == nil {
		return tmux.ServiceUnitOwnership{}
	}
	return i.tmuxSession.ServiceUnitOwnership()
}

// RetireServiceUnit stops + resets-failed the transient systemd-user
// service unit associated with this instance's tmux server (if
// LaunchAs=service was used) — but ONLY when `own` proves this session was
// its exclusive owner. Intended for the remove/delete code path ONLY —
// NOT for restart, which needs the unit to persist so it can re-spawn tmux.
//
// `own` must come from ServiceUnitOwnership() called BEFORE teardown. The
// returned decision reports whether the unit was stopped and why (see the
// tmux.UnitStop* reasons); a skip is never an error — leaving a transient
// unit behind is recoverable, killing a sibling session is not.
func (i *Instance) RetireServiceUnit(own tmux.ServiceUnitOwnership) tmux.ServiceUnitDecision {
	return tmux.StopServiceUnitOwned(own)
}

// Kill terminates the tmux session and cleans up sandbox container if present.
func (i *Instance) Kill() error {
	return i.killInternal(false)
}

// KillAndWait is the synchronous companion to Kill. It performs the
// same teardown AND blocks until the pane process tree has been
// verified dead (SIGTERM → SIGKILL escalation inline, not in a
// background goroutine). Callers in short-lived CLI processes
// (`agent-deck remove`, `agent-deck session remove`) MUST use this
// variant — see issue #59 (v1.7.68). The TUI and web callers can
// keep using Kill for the non-blocking path.
func (i *Instance) KillAndWait() error {
	return i.killInternal(true)
}

func (i *Instance) killInternal(sync bool) error {
	// #1580/#1775: supersede any in-flight fast-death watcher BEFORE the tmux
	// kill (not after). watchForFastDeath only writes a SpawnFailureRecord /
	// lifecycle event on an iteration whose gen check passes; bumping here
	// first closes that window so a deliberate stop/kill can never be
	// observed by the watcher as a "fast death" mid-teardown — including the
	// up-to-3s KillAndWait escalation below, which used to run entirely
	// inside the stale-gen window.
	i.bumpSpawnGenAndBarrier()

	// Issue #965 wiring (PR #1000 follow-up): claude/codex/gemini spawn
	// stdio MCP children when they read .mcp.json — agent-deck never
	// has a direct exec.Command for them, so spawn-time PID
	// registration is impossible. Discover descendants from the pane
	// process tree while the shell+tool are still alive, then SIGTERM
	// them before tmux teardown. Without this, detached children
	// (e.g., npx-wrapped MCPs that setsid into their own session)
	// reparent to PID 1 and accumulate.
	i.discoverMCPChildrenFromPaneTree()

	// Reap tracked MCP child PIDs first (issue #965). Stdio MCP children
	// don't die with their parent claude process — they get reparented to
	// PID 1 and accumulate. SIGTERM with a short grace period, then
	// SIGKILL anything still alive.
	i.reapTrackedMCPChildren()

	// Issue #953: kill the tmux session AND publish StatusStopped
	// atomically under i.mu so concurrent UpdateStatus() callers (most
	// notably the TUI's backgroundStatusUpdate poller) cannot observe
	// the intermediate state where the tmux pane is gone but Status
	// still reflects the pre-kill running/idle value. The pre-existing
	// !tmuxSession.Exists() branch in UpdateStatus then short-circuits
	// on `Status == StatusStopped` (lines around 3221/3237) and leaves
	// the status alone. Setting Status only AFTER the tmux Kill (and
	// not before) also prevents the symmetric "Status is stopped, tmux
	// is alive — must be a user-initiated restart, flip to Running"
	// path at line 3245 from firing during the cleanup window.
	//
	// Holding the lock around the kill is safe: tmuxSession.Kill() is
	// a single tmux command (the process-tree reaping is deferred to a
	// goroutine via ensureProcessesDead). The KillAndWait variant can
	// take up to 3s when escalating to SIGKILL — only short-lived CLI
	// processes (session remove) take that path, and they have no
	// concurrent TUI render contending for the lock.
	var tmuxErr error
	i.mu.Lock()
	if i.tmuxSession != nil {
		if sync {
			tmuxErr = i.tmuxSession.KillAndWait()
		} else {
			tmuxErr = i.tmuxSession.Kill()
		}
	}
	i.Status = StatusStopped
	i.stopRevision++
	// A deliberate stop releases any auth hold: the session's whole runtime state
	// is being discarded, and the next start is by definition a user act — the
	// same intent the hold is waiting for. Without this, a session that showed a
	// 401 and was then stopped by hand would stay held, and the user's own
	// `session restart` would be refused with only --force as a way through.
	i.clearAuthHoldLocked()
	i.mu.Unlock()
	// (gen already bumped at the top of killInternal, before the tmux kill —
	// see the comment there for why it must happen first, not here.)
	if i.GetToolThreadSafe() == "hermes" {
		i.clearHermesHookArtifacts()
	}

	// Clean up sandbox container (only if name matches our prefix convention).
	// Runs regardless of tmux kill result to avoid orphaned containers.
	if i.SandboxContainer != "" && docker.IsManagedContainer(i.SandboxContainer) {
		dockerCfg := GetDockerSettings()
		if dockerCfg.GetAutoCleanup() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ctr := docker.FromName(i.SandboxContainer)
			_ = ctr.Remove(ctx, true) // Force remove, ignore errors.
			i.SandboxContainer = ""
		} else {
			sessionLog.Info("sandbox_container_kept", slog.String("container", i.SandboxContainer))
		}
	}

	// The sandbox credential file (seeded once from the macOS Keychain) is
	// deliberately kept across teardown: it is the containers' own OAuth
	// refresh chain, and re-seeding it from the Keychain on the next start
	// would fork the host's chain again (#2153).
	if i.IsSandboxed() {
		// Tear down the per-instance scoped hook bridge dir (…/hooks/sandbox/<id>).
		// Each ended sandbox session otherwise leaks a directory AND (on Linux) an
		// fsnotify inotify watch held by the notify-daemon's StatusFileWatcher →
		// watch exhaustion on long-lived hosts. Removing the dir on disk also makes
		// the kernel auto-drop that watch (IN_IGNORED). Skip when the container is
		// intentionally kept (auto-cleanup off): it still has the dir bind-mounted.
		// Follow-up: an explicit watcher.Remove() from here would require threading
		// the daemon's StatusFileWatcher into the session-end lifecycle (it is not
		// reachable from this layer), so we rely on the on-delete auto-drop.
		if i.ID != "" && GetDockerSettings().GetAutoCleanup() {
			scopedDir := filepath.Join(GetHooksDir(), "sandbox", i.ID)
			if err := os.RemoveAll(scopedDir); err != nil {
				sessionLog.Debug("sandbox_hook_dir_cleanup_failed",
					slog.String("instance", i.ID),
					slog.String("dir", scopedDir),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	// #1873: reap whatever the spawn-time ownership receipt still owns, then
	// clear it. tmux teardown does not reach a tree that has already escaped
	// the pane — that is the whole report — and a deliberate stop is exactly
	// the intent that authorises terminating this session's processes. Only
	// identity-matched processes are signalled; anything unverifiable is left
	// alone and the receipt is kept so it stays visible.
	i.clearOwnershipAfterTeardown(sync)

	// An asynchronous Kill only starts process reaping in the background, so
	// it must not remove the active generation while a delayed flush can still
	// write through it. KillAndWait is the only lifecycle path here that has
	// synchronously completed tmux's process-tree reaping; even then preserve
	// generations when the tmux operation failed or was indeterminate.
	if sync && tmuxErr == nil {
		i.CleanupWorkerScratchConfigDir()
	}

	// Issue #953: StatusStopped was already written under i.mu at the top
	// of this function. Re-asserting it here without the lock would
	// reintroduce the write/write data race with concurrent UpdateStatus.

	if tmuxErr != nil {
		return fmt.Errorf("failed to kill tmux session: %w", tmuxErr)
	}
	return nil
}

// Restart restarts the Claude session
// For Claude sessions with known ID: sends Ctrl+C twice and resume command to existing session
// For dead sessions or unknown ID: recreates the tmux session
//
// Issue #1040: gated by acquireInstanceSpawnLock plus a "spawned-while-
// we-waited" stamp so concurrent callers (TUI poller + RC-exit handler
// in-process; multiple `agent-deck session start` CLI invocations
// cross-process) cannot each race to recreate a tmux session for the
// same instance. A legitimate manual restart still proceeds because the
// stamp generation from any prior spawn is already in the new caller's
// pre-lock snapshot.
func (i *Instance) Restart() error {
	return i.restartRecorded(nil, "")
}

// RestartFrom is Restart with session.end recorded from surface sf ("" =
// the process surface), for web requests served by a CLI or TUI process.
func (i *Instance) RestartFrom(sf telemetry.Surface) error {
	return i.restartRecorded(nil, sf)
}

// RestartWithEnv restarts the session with one-shot environment overrides.
// The values apply to the replacement process only and are not persisted in
// the Instance or tmux session environment for future restarts.
func (i *Instance) RestartWithEnv(env map[string]string) error {
	for key := range env {
		if !IsValidEnvKey(key) {
			return fmt.Errorf("invalid environment variable name (%s, %d bytes)", EnvKeyRejectReason(key), len(key))
		}
	}
	return i.restartRecorded(env, "")
}

// restartRecorded restarts and records session.end(restart) from surface sf
// on success.
func (i *Instance) restartRecorded(env map[string]string, sf telemetry.Surface) error {
	// The old process's exit and the resumed one's boot are not turns: leave
	// a token first, so the parent gets no false completion (#2792,
	// restart_resume.go).
	noteRestartToken(i, time.Now())
	queued := i.leftQueue()
	err := i.restart(env)
	if err != nil {
		clearRestartToken(i.ID)
	} else {
		markRestartSpawned(i, time.Now())
	}
	if err == nil {
		// LastStartedAt covers restarts too (its contract), so the idle
		// build refresh sees a TUI restart as already on the new build.
		i.markStarted()
		i.RecordTelemetryEndFrom(telemetry.EndRestart, sf)
		// A restart of a queued session takes it off the queue: its brief is
		// delivered now, not left for a later start to type into a
		// conversation that has moved on.
		if queued {
			i.takeOffQueueAfterStart()
		}
	}
	return err
}

func (i *Instance) restart(env map[string]string) (err error) {
	if err := i.ValidateAccount(); err != nil {
		return err
	}
	beforeLock := spawnGenerationSnapshot(i.ID)
	release, lockErr := acquireInstanceSpawnLock(i.ID)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	// A one-shot environment request is explicit operator intent. If another
	// spawn won while this call waited for the lock, restart that fresh process
	// so the requested environment is not silently discarded.
	if spawnedSince(i.ID, beforeLock) && len(env) == 0 {
		return nil
	}
	// #1873: the case this issue reports IS a restart — a pane that died during
	// startup, a wrapped tree that escaped it, and a later legitimate restart
	// that spawns a second one. The gate runs before the generation bump and
	// before any tmux work, so a refusal changes nothing at all: no kill, no
	// respawn, no signal, and the receipt left intact for inspection.
	if err := i.guardOwnedProcessesBeforeSpawn("restart"); err != nil {
		return err
	}
	var validatedDeepSeekRestartCommand string
	if i.Tool == "deepseek" {
		i.refreshDeepSeekSessionID()
		var err error
		validatedDeepSeekRestartCommand, err = i.deepSeekRestartCommand()
		if err != nil {
			return err
		}
	}
	// Deferred only once the gate has passed: a refused restart started nothing,
	// so it must not leave a spawn stamp that makes a concurrent caller believe
	// a replacement is already running.
	defer recordInstanceSpawn(i.ID)
	// Cover every successful respawn/recreate branch while holding the spawn
	// lock. Storm-suppressed calls and failed spawns never send recovery input.
	defer func() {
		if err == nil {
			i.recoverConductorAfterSpawn()
		}
	}()
	// Registered AFTER the gate and BEFORE the tmux work, so it runs on every
	// exit of this function — including each per-tool respawn-pane fast path —
	// while the spawn lock is still held (deferred release() was registered
	// first, so it runs last). A replacement pane that kept the previous
	// receipt would be an unowned tree.
	ownershipCommand := i.Command
	defer func() { i.commitOwnershipAfterRestart(ownershipCommand) }()

	// #1775: supersede the fast-death watcher from the PREVIOUS spawn here, at
	// the single entry point, rather than deeper down. restart() has several
	// early-returning fast paths (the claude respawn-pane branch and its
	// per-tool siblings) that never reach the fallback-recreate block, so a
	// bump placed there left a restart inside the 15s window watching the
	// REPLACEMENT pane with the old spawn's command and start time — and
	// recording a failure against it if that pane died.
	i.bumpSpawnGenAndBarrier()

	if len(env) > 0 {
		i.restartEnv = make(map[string]string, len(env))
		for key, value := range env {
			i.restartEnv[key] = value
		}
		defer func() { i.restartEnv = nil }()
	}
	// Generation publication is the restart invalidation boundary. Commit it
	// before any signal/kill reaches the old Hermes process, so its delayed
	// hooks cannot overwrite the replacement's starting seed.
	if i.Tool == "hermes" {
		if _, err := i.seedHermesHookGeneration("starting", false); err != nil {
			return fmt.Errorf("seed hermes restart generation: %w", err)
		}
	}

	mcpLog.Debug(
		"restart_called",
		slog.String("tool", i.Tool),
		slog.String("claude_session_id", i.ClaudeSessionID),
		slog.Bool("tmux_session", i.tmuxSession != nil),
		slog.Bool("tmux_exists", i.tmuxSession != nil && i.tmuxSession.Exists()),
	)

	// Regenerate .mcp.json before restart to use socket pool if available.
	// Skip if MCP dialog just wrote the config (avoids race condition).
	i.prepareRestartMCPConfig()

	// Regenerate worker-scratch CLAUDE_CONFIG_DIR before restart so
	// changes to Instance.Plugins (added/removed via TUI Plugin Manager
	// or `agent-deck plugin attach/detach`) propagate into the scratch
	// settings.json before claude re-reads it. Without this, the
	// respawn-pane fast path below uses the OLD scratch and claude
	// sees the plugin enablement state from session creation, not the
	// current state. Same call as Start()/recreate paths — idempotent
	// per (sourceProfileDir, plugins-set) and best-effort on failure.
	i.prepareWorkerScratchConfigDirForSpawn()

	// Issue #956: custom-command Claude sessions whose hooks never fired
	// (or whose wrapper script overrode CLAUDE_CONFIG_DIR) arrive at
	// Restart() with empty ClaudeSessionID even when the live conversation
	// wrote a JSONL to disk. Without this prelude the fallback recreate
	// path below dispatches through buildClaudeCommand(i.Command), re-runs
	// the wrapper fresh, and silently drops chat history. Discovery here
	// populates ClaudeSessionID so the respawn-pane fast path
	// (buildClaudeResumeCommand) engages and emits `claude --resume <uuid>`.
	// Mirrors Start()'s ensureClaudeSessionIDFromDisk but bypasses the
	// #608 brand-new-session gate — Restart() implies the instance ran.
	if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID == "" {
		i.ensureClaudeSessionIDFromDiskForRestart()
	}

	// If Claude session with known ID AND tmux session exists, use respawn-pane.
	if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// A known-ID restart resumes straight to `claude --resume <uuid>`
		// without passing through the Start-path prelude. If the live
		// <id>.jsonl was lost to the #1533 account-switch bug but an
		// orphaned <id>.jsonl.bak-<epoch> remains, restore it here so the
		// resume finds its history instead of starting empty.
		if i.Tool == "claude" {
			if restored, rErr := RestoreOrphanedConversationBackup(i, GetClaudeConfigDirForInstance(i)); rErr == nil && restored != "" {
				sessionLog.Info("resume: restored orphaned conversation backup id="+i.ClaudeSessionID+" reason=orphan_bak_restore_restart",
					slog.String("instance_id", i.ID),
					slog.String("claude_session_id", i.ClaudeSessionID),
					slog.String("path", restored),
					slog.String("reason", "orphan_bak_restore_restart"))
			}
		}
		prepInput := i.buildClaudeResumeCommand()
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		ownershipCommand = resumeCmd
		mcpLog.Debug("respawn_pane_claude", slog.String("command", logging.RedactCommandEnv(resumeCmd)))

		// #1822 F2: assert AGENTDECK_PROFILE / CLAUDE_CONFIG_DIR host-side
		// BEFORE respawn-pane, not after. tmux applies the session environment
		// to a process at the moment it starts it, so setting it after
		// RespawnPane only takes effect for the *next* spawn — the process
		// RespawnPane is about to start still inherits whatever was set
		// previously. This branch also returns before the fallback recreate
		// path that would otherwise (re)assert it.
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		// Use respawn-pane for atomic restart
		// This is more reliable than Ctrl+C + wait for shell + send command
		// respawn-pane -k kills the current process and starts the new command atomically
		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			mcpLog.Debug("respawn_pane_claude_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Claude session: %w", err)
		}

		mcpLog.Debug("respawn_pane_claude_succeeded")

		// Persist .sid sidecar so hook events after restart can be correlated
		WriteHookSessionAnchor(i.ID, i.ClaudeSessionID)

		// Issue #666: kill OTHER agentdeck tmux sessions sharing this
		// Claude session id so two `claude --resume` processes don't
		// race the same conversation (and stack two telegram pollers).
		i.sweepDuplicateToolSessions()

		// Re-capture MCPs after restart (they may have changed since session started)
		i.CaptureLoadedMCPs()

		// Start as WAITING - will go GREEN on next tick if Claude shows busy indicator
		i.Status = StatusWaiting
		return nil
	}

	// For Gemini: ALWAYS update session to get the most recent one
	// Krudony fix: don't skip when we already have an ID - the user may have started a NEW session
	if i.Tool == "gemini" {
		i.UpdateGeminiSession(nil)
	}

	// If Gemini session with known ID AND tmux session exists, use respawn-pane.
	if i.Tool == "gemini" && i.GeminiSessionID != "" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		prepInput := i.buildGeminiCommand("gemini")
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		ownershipCommand = resumeCmd
		sessionLog.Info("restart_gemini_respawn", slog.String("command", logging.RedactCommandEnv(resumeCmd)))

		// #1822 F2: gemini's rebuilt resume command carries no inline
		// AGENTDECK_PROFILE prefix, and this branch returns before the
		// fallback recreate path that would otherwise set it. Must run
		// BEFORE RespawnPane — tmux applies session env when it starts the
		// process, so asserting it after RespawnPane only affects the next
		// spawn, not the one just started.
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_gemini_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Gemini session: %w", err)
		}

		sessionLog.Info("restart_gemini_respawn_succeeded")

		// Persist .sid sidecar so hook events after restart can be correlated
		WriteHookSessionAnchor(i.ID, i.GeminiSessionID)

		// Issue #666: sweep cross-tmux duplicates on the respawn path too.
		i.sweepDuplicateToolSessions()

		i.Status = StatusWaiting
		return nil
	}

	// If OpenCode session AND tmux session exists, use respawn-pane
	if i.Tool == "opencode" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// Refresh from OpenCode state before deciding the resume target.
		i.updateOpenCodeSession(true)

		// Try to get session ID from tmux environment if not already set
		// (async detection stores it there but Instance might not have been saved)
		if i.OpenCodeSessionID == "" {
			if envID, err := i.tmuxSession.GetEnvironment("OPENCODE_SESSION_ID"); err == nil && envID != "" {
				i.OpenCodeSessionID = envID
				i.OpenCodeDetectedAt = time.Now()
				sessionLog.Info("restart_opencode_recovered_id", slog.String("session_id", envID))
			}
		}

		if i.OpenCodeSessionID == "" {
			i.OpenCodeStartedAt = time.Now().UnixMilli()
		}
		// Reuse the canonical builder so live-pane respawns retain the same
		// configured command, model, agent, and SSE flags as every other start.
		rawCmd := i.buildOpenCodeCommand("opencode")
		prepInput := rawCmd
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		ownershipCommand = resumeCmd
		sessionLog.Info("restart_opencode_respawn", slog.String("command", logging.RedactCommandEnv(resumeCmd)))

		// #1822 F2: opencode's rebuilt resume command carries no inline
		// AGENTDECK_PROFILE prefix, and this branch returns before the
		// fallback recreate path that would otherwise set it. Must run
		// BEFORE RespawnPane — see the Claude branch above for why.
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_opencode_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart OpenCode session: %w", err)
		}

		// If no session ID, start async detection
		if i.OpenCodeSessionID == "" {
			go i.detectOpenCodeSessionAsync()
		}

		sessionLog.Info("restart_opencode_respawn_succeeded")

		// Persist .sid sidecar so hook events after restart can be correlated
		if i.OpenCodeSessionID != "" {
			WriteHookSessionAnchor(i.ID, i.OpenCodeSessionID)
		}

		// Issue #666: sweep cross-tmux duplicates on the respawn path too.
		i.sweepDuplicateToolSessions()

		i.Status = StatusWaiting
		return nil
	}

	// For Codex: try to update session ID, but only if we don't already have one.
	// When we already have a known session ID (from the database), trust it —
	// the disk scan can return a wrong ID when multiple instances share the same
	// project_path. The process probe is authoritative but only works when the
	// process is running, which it isn't during a restart.
	if IsCodexCompatible(i.Tool) && i.CodexSessionID == "" {
		i.mu.Lock()
		i.pendingCodexRestartWarning = ""
		i.mu.Unlock()
		if missingDep := i.updateCodexSession(nil, true); missingDep != "" {
			i.mu.Lock()
			i.pendingCodexRestartWarning = codexProbeMissingWarning(missingDep)
			i.mu.Unlock()
			sessionLog.Warn("codex_probe_dep_missing_for_restart", slog.String("dependency", missingDep))
		}
	}

	// If Codex session AND tmux session exists, use respawn-pane
	if IsCodexCompatible(i.Tool) && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// Try to get session ID from tmux environment if not already set
		if i.CodexSessionID == "" {
			if envID, err := i.tmuxSession.GetEnvironment("CODEX_SESSION_ID"); err == nil && envID != "" {
				i.CodexSessionID = envID
				i.CodexDetectedAt = time.Now()
				sessionLog.Info("restart_codex_recovered_id", slog.String("session_id", envID))
			}
		}

		if i.CodexSessionID == "" {
			i.CodexStartedAt = time.Now().UnixMilli()
		}
		prepInput := i.buildCodexCommand(i.Command)
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		ownershipCommand = resumeCmd
		sessionLog.Info("restart_codex_respawn", slog.String("command", logging.RedactCommandEnv(resumeCmd)))

		// #1822 F2: belt-and-suspenders to the inline prefix buildCodexCommand
		// already injects; this branch returns before the fallback recreate
		// path that would otherwise set it. Must run BEFORE RespawnPane —
		// see the Claude branch above for why.
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_codex_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Codex session: %w", err)
		}

		// If no session ID, start async detection
		if i.CodexSessionID == "" {
			go i.detectCodexSessionAsync()
		}

		sessionLog.Info("restart_codex_respawn_succeeded")

		// Persist .sid sidecar so hook events after restart can be correlated
		WriteHookSessionAnchor(i.ID, i.CodexSessionID)

		// Issue #666: sweep cross-tmux duplicates on the respawn path too.
		i.sweepDuplicateToolSessions()

		i.Status = StatusWaiting
		return nil
	}

	// If Cursor session AND tmux session exists, use respawn-pane.
	if i.Tool == "cursor" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		prepInput := i.buildCursorCommand(i.Command, true)
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		ownershipCommand = resumeCmd
		sessionLog.Info("restart_cursor_respawn", slog.String("command", logging.RedactCommandEnv(resumeCmd)))

		// #1822 F2: must run BEFORE RespawnPane — see the Claude branch above
		// for why (tmux applies session env at process start, not after).
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_cursor_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Cursor session: %w", err)
		}

		sessionLog.Info("restart_cursor_respawn_succeeded")
		i.sweepDuplicateToolSessions()
		i.CaptureLoadedMCPs()
		i.Status = StatusWaiting
		return nil
	}

	// If custom tool with session resume support AND tmux session exists, use respawn-pane.
	// Resolve id and ToolDef once (avoid double GetGenericSessionID / env races
	// that could produce an empty --resume '' argv). Concurrent config reload
	// can drop the custom entry — never deref nil.
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		toolDef := GetToolDef(i.Tool)
		sessionID := i.GetGenericSessionID()
		if toolDef != nil && toolDef.ResumeFlag != "" && sessionID != "" {
			// The session ID env var is propagated via host-side SetEnvironment after tmux start.
			// Same shape as buildGenericCommand (shared helper) so start/restart cannot drift.
			dangerous := ""
			if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
				dangerous = toolDef.DangerousFlag
			}
			rawCmd := i.buildRestartEnvPrefix() + formatGenericResumeCommand(
				i.Command, toolDef.ResumeFlag, sessionID, dangerous)
			resumeCmd, containerName, err := i.prepareCommand(rawCmd)
			if err != nil {
				// #1924: a restart that fails here left StatusError with no reason.
				i.recordPrepareFailure(rawCmd, err)
				return err
			}
			if containerName != "" {
				i.SandboxContainer = containerName
			}
			ownershipCommand = resumeCmd

			// The resume command embeds the conversation id, so it is not
			// logged; the fingerprint identifies the binding without
			// disclosing it (see fingerprintSessionID).
			sessionLog.Info("restart_generic_respawn",
				slog.String("tool", logging.SanitizeValue(i.Tool)),
				slog.String("session_id_fingerprint", fingerprintSessionID(sessionID)))

			// #1822 F2: the generic resume command is a bare
			// `<cmd> <resumeFlag> <sid>` with no inline AGENTDECK_PROFILE prefix,
			// and this branch returns before the fallback recreate path. Must run
			// BEFORE RespawnPane — see the Claude branch above for why.
			i.ensureProfileEnv()
			i.ensureClaudeConfigDirEnv()

			if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
				sessionLog.Info(
					"restart_generic_respawn_failed",
					slog.String("tool", i.Tool),
					slog.String("error", err.Error()),
				)
				return fmt.Errorf("failed to restart %s session: %w", i.Tool, err)
			}

			sessionLog.Info("restart_generic_respawn_succeeded", slog.String("tool", i.Tool))

			i.loadCustomPatternsFromConfig() // Reload custom patterns
			i.Status = StatusWaiting
			return nil
		}
		// toolDef vanished or id emptied between checks — fall through to recreate.
	}

	mcpLog.Debug("restart_fallback_recreate")

	// (the watcher was already superseded at the top of restart(), which covers
	// this path and every early-returning respawn fast path alike.)

	// Kill old tmux session to prevent orphans before recreating (#138)
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		mcpLog.Debug("restart_killing_old_session", slog.String("session_name", i.tmuxSession.Name))
		if killErr := i.tmuxSession.Kill(); killErr != nil {
			mcpLog.Warn("restart_kill_old_session_failed", slog.String("error", killErr.Error()))
		}
	}

	// Fallback: recreate tmux session (for dead sessions or unknown ID)
	i.recreateTmuxSession()

	// Prepare scratch CLAUDE_CONFIG_DIR for non-conductor claude workers
	// on the restart path too (issue #59, v1.7.68).
	i.prepareWorkerScratchConfigDirForSpawn() // also runs plugin auto-install per fix C1

	var command string
	if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" {
		command = i.buildClaudeResumeCommand()
	} else if i.Tool == "gemini" && i.GeminiSessionID != "" {
		command = i.buildGeminiCommand("gemini")
	} else if i.Tool == "opencode" && i.OpenCodeSessionID != "" {
		command = i.buildOpenCodeCommand("opencode")
	} else if IsCodexCompatible(i.Tool) && i.CodexSessionID != "" {
		command = i.buildCodexCommand(i.Command)
	} else if i.Tool == "hermes" {
		// Issue #2582: resume only the id this entry's own hooks recorded.
		i.prepareHermesResume()
		// Drop any stale hook state from the previous process, then seed a
		// synthetic "waiting" baseline. Hermes fires on_session_start only at
		// the FIRST TURN of a brand-new session (not at process launch), so
		// after a restart NOTHING writes a hook status until the user types:
		// without the clear a pre-restart on_session_finalize "dead" shows an
		// error ✕ for the whole freshness window, and without the seed the
		// session shows no state at all. The freshly respawned process sits at
		// its prompt, which is exactly "waiting".
		if _, err := i.seedHermesHookGeneration("waiting", false); err != nil {
			return fmt.Errorf("seed hermes restart baseline: %w", err)
		}
		command = i.buildHermesCommand(i.Command)
	} else if i.Tool == "deepseek" {
		// Re-discover the workspace's newest dsh session on EVERY restart, for
		// the same self-healing reason as hermes: a pruned session yields the
		// current newest, or "" — in which case the launch boots fresh instead
		// of resuming a dead ID forever. A no-op when [deepseek].resume_flag is
		// unset (the default), where restart is a plain re-boot and dsh's own
		// persistence under $DSH_HOME keeps the conversation reachable.
		// A headless restart replays the recorded task; every other profile
		// re-boots as before. deepSeekRestartCommand refuses rather than
		// rebuilding a taskless one-shot invocation.
		command = validatedDeepSeekRestartCommand
	} else if i.Tool == "muse" {
		// Re-discover the workspace's newest muse session on EVERY restart,
		// for the same self-healing reason as hermes/deepseek: a pruned
		// session yields the current newest, or "" — in which case the
		// launch boots fresh instead of resuming a dead ID forever. Bounded
		// by the last start so a restart cannot rebind to an older
		// unrelated conversation.
		if sid := i.discoverMuseResumeID(); sid != "" {
			command = i.buildMuseResumeCommand(sid)
		} else {
			command = i.buildMuseCommand(i.Command)
		}
	} else {
		// Route to appropriate command builder based on tool
		switch {
		case IsClaudeCompatible(i.Tool):
			command = i.buildClaudeCommand(i.Command)
		case i.Tool == "gemini":
			command = i.buildGeminiCommand(i.Command)
		case i.Tool == "opencode":
			command = i.buildOpenCodeCommand(i.Command)
			// Record start time for async session ID detection
			i.OpenCodeStartedAt = time.Now().UnixMilli()
		case IsCodexCompatible(i.Tool):
			command = i.buildCodexCommand(i.Command)
			// Record start time for async session ID detection
			i.CodexStartedAt = time.Now().UnixMilli()
		case i.Tool == "pi":
			command = i.buildPiCommand(i.Command)
		case i.Tool == "omp":
			command = i.buildOMPCommand(i.Command)
		case i.Tool == "copilot":
			command = i.buildCopilotCommand(i.Command)
		case i.Tool == "crush":
			command = i.buildCrushCommand(i.Command)
		case i.Tool == "cursor":
			command = i.buildCursorCommand(i.Command, true)
		default:
			// Check if this is a custom tool with session resume config
			if toolDef := GetToolDef(i.Tool); toolDef != nil {
				command = i.buildGenericCommand(i.Command)
			} else {
				command = i.buildRestartEnvPrefix() + i.Command
				if i.Command == "" && command != "" {
					shell := ""
					if !i.IsSandboxed() && i.SSHHost == "" {
						shell = os.Getenv("SHELL")
					}
					if shell == "" {
						shell = "/bin/sh"
					}
					command += "exec " + shellescape.Quote(shell)
				}
			}
		}
	}
	command, containerName, err := i.prepareCommand(command)
	if err != nil {
		i.recordPrepareFailure(command, err) // #1924, sister path
		return err
	}
	ownershipCommand = command
	if containerName != "" {
		i.SandboxContainer = containerName
	}

	// Load custom patterns for status detection (for custom tools).
	i.loadCustomPatternsFromConfig()

	// Build tmux option overrides from config (e.g. allow-passthrough = "all").
	// Sandbox sessions also get remain-on-exit for dead-pane detection.
	i.tmuxSession.OptionOverrides = i.buildTmuxOptionOverrides()
	i.tmuxSession.RunCommandAsInitialProcess = i.IsSandboxed() || i.Tool != "shell" || i.tracksCommandExit()
	i.applyLaunchSettingsFromConfig()

	// Re-assert the declarative skill+mcp loadout before respawn — sister
	// call to Start(); config edits land on the next restart this way.
	ApplyConfiguredLoadout(i)

	mcpLog.Debug("restart_starting_new_session", slog.String("command", logging.RedactCommandEnv(command)))
	command, err = i.wrapTrackedCommandExit(command)
	if err != nil {
		return err
	}

	i.retireFastDeathWatchers() // #2805
	i.setInteractiveShellLaunchEnv()
	if err := i.tmuxSession.Start(command); err != nil {
		mcpLog.Debug("restart_start_failed", slog.String("error", err.Error()))
		// #1924: Start() and its sister path have recorded this since #1580,
		// but restart() never did — so the one case where the user has just
		// changed something and is most likely to ask "why?" was the one that
		// set StatusError with nothing to show for it.
		i.recordTmuxStartFailure(command, err)
		i.Status = StatusError
		return fmt.Errorf("failed to restart tmux session: %w", err)
	}

	mcpLog.Debug("restart_start_succeeded")

	// CFG-07: emit the config-resolution log on restart too — triage must not
	// go dark on the exact scenario most likely to need debugging.
	if IsClaudeCompatible(i.Tool) {
		i.logClaudeConfigResolution()
	}

	// Set AGENTDECK_INSTANCE_ID for Claude hooks to identify this session
	// This enables real-time status updates via Stop/SessionStart hooks
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_INSTANCE_ID", i.ID); err != nil {
		sessionLog.Warn("set_instance_id_failed", slog.String("error", err.Error()))
	}

	// Set AGENTDECK_PROFILE (host-side, tool-agnostic) so a bare `agent-deck`
	// command run inside this session resolves the session's own profile rather
	// than falling back to "default". Covers shells/OpenCode/etc. that have no
	// inline env-prefix injection of their own.
	i.ensureProfileEnv()
	i.ensureInteractiveShellEnv()
	i.ensureClaudeConfigDirEnv()

	// Propagate all known tool session IDs to the tmux environment (host-side).
	// This covers Restart() which uses buildClaudeResumeCommand() and similar
	// builders that no longer embed "tmux set-environment" in the shell string.
	i.SyncSessionIDsToTmux()

	// Kill any other agentdeck tmux session that duplicates this instance.
	// Routed through sweepDuplicateToolSessions so the fallback restart path
	// gets the same tool-session-id guard (#596) AND instance-id guard (#678)
	// as the respawn-pane paths. The instance-id guard is what catches shell
	// / placeholder sessions that have no tool-level session id.
	i.sweepDuplicateToolSessions()

	// Re-capture MCPs after restart
	i.CaptureLoadedMCPs()

	// Start async session ID detection for OpenCode (if no ID yet)
	if i.Tool == "opencode" && i.OpenCodeSessionID == "" {
		go i.detectOpenCodeSessionAsync()
	}

	// Start async session ID detection for Codex (if no ID yet)
	if IsCodexCompatible(i.Tool) && i.CodexSessionID == "" {
		go i.detectCodexSessionAsync()
	}

	// Start as WAITING - will go GREEN on next tick if Claude shows busy indicator
	if command != "" {
		i.Status = StatusWaiting
	} else {
		i.Status = StatusIdle
	}

	// #1870: this is the only path that mints a new tmux session name, so it is
	// the only place that has to make it durable. Callers that never save --
	// mcp/skill/plugin attach|detach --restart -- used to leave the killed
	// session's name on disk, so agent-deck reported a live session as broken
	// and orphaned its tmux session. Status travels with it because the two are
	// one fact about the restart. See restart_tmux_persist.go for why this is a
	// targeted write and why a failure here is recorded rather than returned.
	i.recordRestartOutcome()

	return nil
}

// restartFreshBindingCleared runs right after RestartFresh cleared the
// session binding, before the old process is killed. Tests replace it.
var restartFreshBindingCleared = func(*Instance) {}

// RestartFresh restarts the current tool without resuming the existing tool session.
// This recreates the tmux session and clears the stored tool session binding first,
// so the next start gets a brand-new tool session ID.
func (i *Instance) RestartFresh() error {
	i.prepareRestartMCPConfig()
	i.spawnGen.Add(1)

	// Rotate the Hermes hook generation before the binding is cleared and the
	// old process is killed, as Restart does: a late hook from the old process
	// then fails the generation check instead of writing its id back.
	if i.Tool == "hermes" {
		if _, err := i.seedHermesHookGeneration("starting", false); err != nil {
			return fmt.Errorf("seed hermes fresh restart generation: %w", err)
		}
		i.hermesStartFresh = true
	}

	i.clearSessionBindingForFreshStart()
	restartFreshBindingCleared(i)

	// #1775: same reasoning as the restart-fallback-recreate path above, and
	// likewise unconditional — an old watcher whose session is already gone is
	// the one most likely to write a spurious failure.
	i.bumpSpawnGenAndBarrier()

	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		if killErr := i.tmuxSession.Kill(); killErr != nil {
			mcpLog.Warn("restart_fresh_kill_old_session_failed", slog.String("error", killErr.Error()))
		}
	}

	i.recreateTmuxSession()

	if err := i.Start(); err != nil {
		i.hermesStartFresh = false // a failed start must not make a later one fresh
		i.Status = StatusError
		return fmt.Errorf("failed to restart session fresh: %w", err)
	}

	return nil
}

// SetGeminiModel sets the Gemini model for this session and triggers a restart if running.
func (i *Instance) SetGeminiModel(model string) error {
	i.GeminiModel = model
	sessionLog.Debug(
		"gemini_model_set",
		slog.String("model", model),
		slog.String("session_id", i.ID),
		slog.String("title", i.Title),
	)

	// Restart if the session is running so it picks up the new model
	if i.Exists() {
		return i.Restart()
	}
	return nil
}

// SupportsLaunchModel reports whether a newly-created session can receive an
// explicit model override through Agent Deck's generic session creation path.
func SupportsLaunchModel(tool string) bool {
	tool = CanonicalToolName(tool)
	return IsClaudeCompatible(tool) || tool == "gemini" || tool == "opencode" || tool == "omp" || IsCodexCompatible(tool)
}

// ApplyLaunchModel stores a per-session model override in the tool-specific
// field that the relevant command builder already reads on start/restart.
func (i *Instance) ApplyLaunchModel(model string) error {
	model = strings.TrimSpace(model)
	if i == nil || model == "" {
		return nil
	}

	switch {
	case IsClaudeCompatible(i.Tool):
		opts := i.GetClaudeOptions()
		if opts == nil {
			userConfig, _ := LoadUserConfig()
			opts = NewClaudeOptions(userConfig)
		}
		opts.Model = model
		return i.SetClaudeOptions(opts)
	case i.Tool == "gemini":
		i.GeminiModel = model
		return nil
	case i.Tool == "opencode":
		opts := i.GetOpenCodeOptions()
		if opts == nil {
			userConfig, _ := LoadUserConfig()
			opts = NewOpenCodeOptions(userConfig)
		}
		opts.Model = model
		return i.SetOpenCodeOptions(opts)
	case i.Tool == "omp":
		opts := i.GetOMPOptions()
		if opts == nil {
			userConfig, _ := LoadUserConfig()
			opts = NewOMPOptions(userConfig)
		}
		opts.Model = model
		return i.SetOMPOptions(opts)
	case IsCodexCompatible(i.Tool):
		opts := i.GetCodexOptions()
		if opts == nil {
			userConfig, _ := LoadUserConfig()
			opts = NewCodexOptions(userConfig)
		}
		opts.Model = model
		return i.SetCodexOptions(opts)
	default:
		return fmt.Errorf("model selection is not supported for tool %q", i.Tool)
	}
}

// ClearLaunchModel removes any per-session model override so the session falls
// back to the configured default ([claude].default_model, etc.) on the next
// start/restart (#1436). Mirrors ApplyLaunchModel across tools; a no-op when
// no override is set.
func (i *Instance) ClearLaunchModel() error {
	if i == nil {
		return nil
	}
	switch {
	case IsClaudeCompatible(i.Tool):
		opts := i.GetClaudeOptions()
		if opts == nil {
			return nil
		}
		opts.Model = ""
		return i.SetClaudeOptions(opts)
	case i.Tool == "gemini":
		i.GeminiModel = ""
		return nil
	case i.Tool == "opencode":
		opts := i.GetOpenCodeOptions()
		if opts == nil {
			return nil
		}
		opts.Model = ""
		return i.SetOpenCodeOptions(opts)
	case i.Tool == "omp":
		opts := i.GetOMPOptions()
		if opts == nil {
			return nil
		}
		opts.Model = ""
		return i.SetOMPOptions(opts)
	case IsCodexCompatible(i.Tool):
		opts := i.GetCodexOptions()
		if opts == nil {
			return nil
		}
		opts.Model = ""
		return i.SetCodexOptions(opts)
	default:
		return nil
	}
}

// CanRestart returns true if the session can be restarted
// For Claude sessions with known ID: can always restart (interrupt and resume)
// For Gemini sessions with known ID: can always restart (interrupt and resume)
// For OpenCode sessions with known ID: can always restart (interrupt and resume)
// For Codex sessions with known ID: can always restart (interrupt and resume)
// For custom tools with session resume config: can restart if session ID available
// For other sessions: only if dead/error state
func (i *Instance) CanRestart() bool {
	// Gemini sessions with known session ID can always be restarted
	if i.Tool == "gemini" && i.GeminiSessionID != "" {
		return true
	}

	// Claude sessions with known session ID can always be restarted
	if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" {
		return true
	}

	// Claude sessions without ID can still restart (will start fresh or
	// resume the latest JSONL via ensureClaudeSessionIDFromDisk). REQ-7
	// reopen #911: custom-command Claude sessions (Tool=claude with a
	// wrapper Command) bypass happy-path session-id capture and have an
	// intentionally empty ClaudeSessionID. Without this branch they fall
	// to the dead-or-error fallback below and the registry refuses
	// restart even when the underlying tmux pane is alive — the false-
	// error class this issue tracks. Mirrors the opencode/codex policy.
	if IsClaudeCompatible(i.Tool) {
		return true
	}

	// OpenCode sessions with known session ID can always be restarted
	if i.Tool == "opencode" && i.OpenCodeSessionID != "" {
		return true
	}

	// OpenCode sessions without ID can still restart (will start fresh)
	// This allows restart even before session ID is detected
	if i.Tool == "opencode" {
		return true
	}

	// Codex sessions with known session ID can always be restarted
	if IsCodexCompatible(i.Tool) && i.CodexSessionID != "" {
		return true
	}

	// Codex sessions without ID can still restart (will start fresh)
	// This allows restart even before session ID is detected
	if IsCodexCompatible(i.Tool) {
		return true
	}

	// Pi sessions are scoped to an Agent Deck instance-specific session dir and
	// can always be relaunched with --continue.
	if i.Tool == "pi" {
		return true
	}

	// omp sessions are scoped to an Agent Deck instance-specific session dir
	// and can always be relaunched with --continue (verified live: --continue
	// on a fresh empty --session-dir starts a new session gracefully).
	if i.Tool == "omp" {
		return true
	}

	// Cursor sessions resume via --continue on restart (workspace-scoped chat).
	if i.Tool == "cursor" {
		return true
	}

	// Hermes resumes via `--resume <id>`, the id its own hooks recorded
	// (#2582). Always restartable (resumes when an id is recorded, else starts
	// fresh) rather than only when dead/errored,
	// which left a live hermes session silently un-restartable.
	if i.Tool == "hermes" {
		return true
	}

	// DeepSeek Harness is restartable: a restart re-boots the same profile in
	// the same workspace against the same $DSH_HOME, so every conversation dsh
	// persisted stays reachable whether or not the configured profile's app
	// accepts a resume flag. Gating this on "dead or errored" would leave a live
	// dsh session silently un-restartable, the exact hermes bug above.
	//
	// The headless profile is the exception, and it is a REAL one rather than a
	// conservative guess: a one-shot's task is its whole invocation, so without
	// a recorded task there is nothing to restart INTO — the rebuild would be
	// `dsh --profile headless`, which dsh rejects. Claiming restartability there
	// promises an action that can only fail (PR #1942 review, P1c).
	if i.Tool == "deepseek" {
		if i.deepSeekPromptDelivery() == DeepSeekPromptCommandLine {
			return strings.TrimSpace(i.DeepSeekTask) != ""
		}
		return true
	}

	// Custom tools: check if they have session resume support
	if i.CanRestartGeneric() {
		return true
	}

	// Other sessions: only if dead or error
	return i.Status == StatusError || i.tmuxSession == nil || !i.tmuxSession.Exists()
}

// CanRestartFresh returns true when the session has a known tool session binding
// that can be intentionally discarded to start with a new session ID.
func (i *Instance) CanRestartFresh() bool {
	if IsClaudeCompatible(i.Tool) {
		return i.ClaudeSessionID != ""
	}
	if i.Tool == "gemini" {
		return i.GeminiSessionID != ""
	}
	if i.Tool == "opencode" {
		return i.OpenCodeSessionID != ""
	}
	if i.Tool == "codex" {
		return i.CodexSessionID != ""
	}
	// Hermes fresh-restart discards the resume binding and starts a new session.
	if i.Tool == "hermes" {
		return true
	}
	// DeepSeek fresh-restart is only meaningful when there is a resume binding
	// to discard. With [deepseek].resume_flag unset (the default) every restart
	// is already fresh, so offering "restart fresh" as a distinct action would
	// promise a difference that does not exist.
	if i.Tool == "deepseek" {
		return DeepSeekSupportsResume()
	}
	return i.CanRestartGeneric()
}

// regenerateMCPConfig regenerates .mcp.json with current pool status
// If socket pool is running, MCPs will use socket configs (nc -U /tmp/...)
// Otherwise, MCPs will use stdio configs (npx ...)
// Returns error if .mcp.json write fails
func (i *Instance) regenerateMCPConfig() error {
	if IsCodexCompatible(i.Tool) {
		ClearCodexMCPCache(i.getCodexHomeDir())
		mcpInfo := i.GetMCPInfo()
		if mcpInfo == nil || len(mcpInfo.Global) == 0 {
			return nil
		}
		if err := i.WriteGlobalMCPConfig(mcpInfo.Global); err != nil {
			mcpLog.Debug("regen_codex_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate Codex MCP config: %w", err)
		}
		mcpLog.Debug("regen_codex_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(mcpInfo.Global)))
		return nil
	}

	if i.Tool == "cursor" {
		ClearCursorMCPCache(i.ProjectPath)
		mcpInfo := i.GetMCPInfo()
		if mcpInfo == nil || !mcpInfo.HasAny() {
			return nil
		}

		switch GetMCPDefaultScope() {
		case "global", "user":
			globalMCPs := mcpInfo.Global
			if len(globalMCPs) == 0 {
				return nil
			}
			if err := i.WriteGlobalMCPConfig(globalMCPs); err != nil {
				mcpLog.Debug("regen_cursor_global_mcp_failed", slog.String("error", err.Error()))
				return fmt.Errorf("failed to regenerate Cursor global MCP config: %w", err)
			}
			mcpLog.Debug("regen_cursor_global_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(globalMCPs)))
		default:
			localMCPs := mcpInfo.Local()
			if len(localMCPs) == 0 {
				return nil
			}
			if err := i.WriteLocalMCPConfig(localMCPs); err != nil {
				mcpLog.Debug("regen_cursor_project_mcp_failed", slog.String("error", err.Error()))
				return fmt.Errorf("failed to regenerate .cursor/mcp.json: %w", err)
			}
			mcpLog.Debug("regen_cursor_project_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(localMCPs)))
		}
		return nil
	}

	ClearMCPCache(i.ProjectPath) // Force fresh read from disk (not stale 30s cache)
	mcpInfo := GetMCPInfo(i.ProjectPath)
	if mcpInfo == nil {
		return nil // No MCP info, nothing to regenerate
	}

	switch GetMCPDefaultScope() {
	case "global":
		globalMCPs := mcpInfo.Global
		if len(globalMCPs) == 0 {
			return nil
		}
		if err := WriteGlobalMCP(globalMCPs); err != nil {
			mcpLog.Debug("regen_global_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate global MCP config: %w", err)
		}
		mcpLog.Debug(
			"regen_global_mcp_succeeded",
			slog.String("title", i.Title),
			slog.Int("mcp_count", len(globalMCPs)),
		)
	case "user":
		userMCPs := GetUserMCPNames()
		if len(userMCPs) == 0 {
			return nil
		}
		if err := WriteUserMCP(userMCPs); err != nil {
			mcpLog.Debug("regen_user_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate user MCP config: %w", err)
		}
		mcpLog.Debug("regen_user_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(userMCPs)))
	default:
		localMCPs := mcpInfo.Local()
		if len(localMCPs) == 0 {
			return nil
		}
		// WriteMCPJsonFromConfig checks pool status and writes socket configs if pool is running
		if err := WriteMCPJsonFromConfig(i.ProjectPath, localMCPs); err != nil {
			mcpLog.Debug("regen_local_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate .mcp.json: %w", err)
		}
		mcpLog.Debug("regen_local_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(localMCPs)))
	}
	return nil
}
