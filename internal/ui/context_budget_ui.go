package ui

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/safego"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// shouldNotifyBudgetCrossing reports whether a one-time notification should fire
// for an upward transition into BudgetHigh or BudgetOver. Warn is intentionally
// bar/badge-only; dropping back never notifies.
func shouldNotifyBudgetCrossing(prev, cur session.BudgetLevel) bool {
	if cur <= prev {
		return false
	}
	return cur == session.BudgetHigh || cur == session.BudgetOver
}

// budgetWarnState returns a session's budget level and whether a usable context
// token signal exists (Claude-compatible tool + cached analytics). When ok is
// false, callers must not warn or act.
func (h *Home) budgetWarnState(inst *session.Instance, cfg session.ContextBudgetSettings) (session.BudgetLevel, bool) {
	if !session.IsClaudeCompatible(inst.Tool) {
		return session.BudgetNormal, false
	}
	a := h.getAnalyticsForSession(inst)
	if a == nil {
		return session.BudgetNormal, false
	}
	return a.BudgetLevel(cfg), true
}

// evaluateContextBudgetWarnings runs once per background tick over all sessions,
// firing a debounced one-shot notification on each upward crossing into
// high/over. Visual treatments (bar/badge) are handled in render.
func (h *Home) evaluateContextBudgetWarnings(instances []*session.Instance) {
	cfg := session.GetContextBudgetSettings()
	if !cfg.GetEnabled() {
		return
	}
	// Callers pass the active (non-archived) subset; archived sessions are torn
	// down and display-frozen, so they must never emit budget crossings.
	for _, inst := range instances {
		level, ok := h.budgetWarnState(inst, cfg)
		if !ok {
			continue
		}
		prev := h.budgetLastLevel[inst.ID] // zero value = BudgetNormal
		if shouldNotifyBudgetCrossing(prev, level) {
			h.notifyBudgetCrossing(inst, level)
		}
		h.budgetLastLevel[inst.ID] = level
	}
}

// notifyBudgetCrossing emits the one-time alert for a high/over crossing. It
// logs at WARN always; visual feedback is provided by the budget bar/badge.
// Debounce is handled by the caller's per-session last-level map.
func (h *Home) notifyBudgetCrossing(inst *session.Instance, level session.BudgetLevel) {
	uiLog.Warn("context_budget_crossing",
		slog.String("session", inst.Title),
		slog.String("id", inst.ID),
		slog.String("level", level.String()))
}

// isAutonomousSession reports whether agent-deck launched this session non-
// interactively: a conductor, or a parented/fleet child. Only autonomous
// sessions get the auto wrap-up/fork; interactive sessions get warnings only.
func isAutonomousSession(inst *session.Instance) bool {
	if inst.IsConductor || inst.GroupPath == "conductor" {
		return true
	}
	return inst.ParentSessionID != ""
}

// handoffAgentIdle reports whether the agent is idle/waiting (safe to fork).
// Both StatusWaiting (stopped, awaiting input) and StatusIdle qualify; an
// actively generating session (StatusRunning) does not.
func handoffAgentIdle(inst *session.Instance) bool {
	return inst.Status == session.StatusWaiting || inst.Status == session.StatusIdle
}

// handoffDir returns the per-session handoff directory <data-dir>/handoff/<id>.
// The "handoff" marker keeps a pre-XDG ~/.agent-deck/handoff in use when it
// exists, so sessions mid-handoff across the layout migration still resolve.
//
// The PROMPT.md path inside it is derived by session.HandoffPromptPath, which
// the CLI also uses; keep this in agreement with it.
func handoffDir(id string) string {
	dir, err := agentpaths.EffectiveDataPath(filepath.Join("handoff", id), "handoff")
	if err != nil {
		return ""
	}
	return dir
}

// fileExists reports whether the path exists (used to poll for PROMPT.md).
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// effectiveHandoffTargetTool returns the tool the continuation session should
// run. An unset target, one naming the source's own tool, or one that would not
// resolve to a real tool all fall back to the source's tool.
//
// The fallback matters: the spawn path maps an unrecognized name to "shell", so
// an invalid target would silently replace an agent handoff with a dead shell
// pane. Config load rejects bad values loudly; this is the belt-and-braces stop
// for anything that reaches here anyway.
func effectiveHandoffTargetTool(inst *session.Instance, cfg session.ContextBudgetSettings) string {
	target := strings.TrimSpace(cfg.HandoffTargetTool)
	if target == "" || strings.EqualFold(target, inst.Tool) {
		return inst.Tool
	}
	if err := session.ValidateHandoffTargetTool(target); err != nil {
		uiLog.Warn("handoff_target_tool_invalid",
			slog.String("target", target),
			slog.String("fallback", inst.Tool),
			slog.Any("err", err))
		return inst.Tool
	}
	return target
}

// evaluateContextBudgetHandoff drives the per-session handoff state machine for
// autonomous sessions. State is persisted via statedb and resumed across
// restarts. Runs once per background tick. Interactive sessions are skipped
// (they receive warnings only, never auto-action).
func (h *Home) evaluateContextBudgetHandoff(instances []*session.Instance) {
	cfg := session.GetContextBudgetSettings()
	if !cfg.GetEnabled() || !cfg.GetAutonomousHandoff() {
		return
	}
	db := statedb.GetGlobal()
	if db == nil {
		return
	}
	for _, inst := range instances {
		if !isAutonomousSession(inst) {
			continue
		}
		// Gate on a usable context-token signal (Claude-compatible tool + cached
		// analytics). Without it there is nothing to act on.
		if _, ok := h.budgetWarnState(inst, cfg); !ok {
			continue
		}
		a := h.getAnalyticsForSession(inst)
		if a == nil {
			continue
		}

		// Resume persisted state lazily the first time we see this session
		// (survives a restart mid-wrap).
		cur := h.handoffState[inst.ID]
		trig := h.handoffTriggeredAt[inst.ID]
		if _, seen := h.handoffState[inst.ID]; !seen {
			if pState, pAt, err := db.ReadHandoffState(inst.ID); err == nil {
				cur = session.HandoffState(pState)
				trig = pAt
				h.handoffState[inst.ID] = cur
				h.handoffTriggeredAt[inst.ID] = pAt
			}
		}

		in := session.HandoffInputs{
			Tokens:      a.CurrentContextTokens,
			PromptReady: fileExists(filepath.Join(handoffDir(inst.ID), "PROMPT.md")),
			AgentIdle:   handoffAgentIdle(inst),
			Now:         time.Now(),
			TriggeredAt: trig,
		}
		dec := session.NextHandoffState(cur, in, cfg)
		if dec.Next == cur && dec.Action == session.ActionNone {
			continue // no change this tick
		}

		switch dec.Action {
		case session.ActionRequestWrap:
			now := time.Now()
			h.handoffTriggeredAt[inst.ID] = now
			h.requestWrap(inst)
			_ = db.WriteHandoffState(inst.ID, string(dec.Next), now)
		case session.ActionFork:
			h.forkContinuation(inst, "fork")
			_ = db.WriteHandoffState(inst.ID, string(dec.Next), h.handoffTriggeredAt[inst.ID])
		case session.ActionFailsafe:
			// The ceiling/timeout path still attempts a continuation: the
			// transcript fallback needs nothing from the (by definition
			// unresponsive) agent, and forkContinuation falls through to
			// failsafePause when neither prompt source is usable or the chain
			// cap is reached.
			h.forkContinuation(inst, "failsafe")
			_ = db.WriteHandoffState(inst.ID, string(dec.Next), h.handoffTriggeredAt[inst.ID])
		default:
			_ = db.WriteHandoffState(inst.ID, string(dec.Next), h.handoffTriggeredAt[inst.ID])
		}
		h.handoffState[inst.ID] = dec.Next
	}
}

// requestWrap creates the handoff dir and injects the wrap-up instruction,
// telling the agent to finish, persist, and write a continuation PROMPT.md.
func (h *Home) requestWrap(inst *session.Instance) {
	dir := handoffDir(inst.ID)
	_ = os.MkdirAll(dir, 0o755)
	ts := inst.GetTmuxSession()
	if ts == nil {
		return
	}
	prompt := filepath.Join(dir, "PROMPT.md")
	msg := "Context budget reached. Finish and save your current work now, then write a continuation prompt for a fresh session to " +
		prompt + " (and any work notes alongside it). Do not start new work. When PROMPT.md is written, stop and wait."
	safego.Go(uiLog, "context_budget_wrapup", func() {
		time.Sleep(500 * time.Millisecond)
		_ = ts.SendKeysAndEnter(msg)
	})
}

// forkContinuation creates the continuation session and archives the old one.
//
// The continuation prompt comes from ResolveContinuationPrompt: the agent's
// curated PROMPT.md when it wrote one, otherwise a prompt rebuilt from the
// on-disk transcript. That fallback is why a wedged agent no longer dead-ends —
// only a session with neither artifact falls through to failsafePause.
//
// Same-tool continuations fork (inheriting tool/profile/path/group/parent/
// worktree); a configured cross-tool target goes through the create path
// instead, since fork is tool-preserving by construction.
//
// reason distinguishes the normal idle fork from the ceiling/timeout failsafe;
// it only affects logging and alert loudness.
func (h *Home) forkContinuation(inst *session.Instance, reason string) {
	promptPath := filepath.Join(handoffDir(inst.ID), "PROMPT.md")
	cfg := session.GetContextBudgetSettings()
	targetTool := effectiveHandoffTargetTool(inst, cfg)

	// The chain cap is the only brake on a session that loops into its own
	// ceiling and forks a successor that does the same. Read the persisted
	// generation so a TUI restart cannot reset the count.
	generation := 0
	if db := statedb.GetGlobal(); db != nil {
		if g, err := db.ReadHandoffGeneration(inst.ID); err == nil {
			generation = g
		}
	}
	if !session.HandoffChainAllows(generation, cfg) {
		uiLog.Error("context_budget_chain_cap",
			slog.String("session", inst.Title),
			slog.String("id", inst.ID),
			slog.Int("generation", generation),
			slog.Int("max_chain", cfg.MaxHandoffChain),
			slog.String("action", "paused; chain cap reached, manual handoff required"))
		h.failsafePause(inst)
		return
	}

	// Inherit worktree fields when the source ran in a worktree.
	var opts *session.ClaudeOptions
	if inst.WorktreePath != "" {
		opts = &session.ClaudeOptions{
			WorktreePath:     inst.WorktreePath,
			WorktreeRepoRoot: inst.WorktreeRepoRoot,
			WorktreeBranch:   inst.WorktreeBranch,
		}
	}

	safego.Go(uiLog, "context_budget_fork", func() {
		resolved, err := session.ResolveContinuationPrompt(inst, targetTool, promptPath, session.DefaultHandoffMaxChars)
		if err != nil {
			uiLog.Warn("handoff_prompt_unresolvable",
				slog.String("id", inst.ID),
				slog.String("reason", reason),
				slog.Any("err", err))
			h.failsafePause(inst)
			return
		}
		// A transcript-sourced prompt means the agent never produced a curated
		// wrap-up: continuity happens, but the human must know it was degraded.
		if resolved.Source == session.ContinuationSourceTranscript {
			uiLog.Warn("handoff_prompt_from_transcript",
				slog.String("session", inst.Title),
				slog.String("id", inst.ID),
				slog.String("reason", reason),
				slog.Int("generation", generation+1),
				slog.Int("max_chain", cfg.MaxHandoffChain),
				slog.Bool("truncated", resolved.Info.Truncated))
			h.notifyBudgetCrossing(inst, session.BudgetOver)
		}
		seed := "You are a continuation of a previous session that reached its context budget. " +
			"Resume from this handoff prompt:\n\n" + resolved.Text

		if !strings.EqualFold(targetTool, inst.Tool) {
			h.spawnCrossToolContinuation(inst, targetTool, seed, generation+1)
			return
		}

		cmd := h.forkSessionCmdWithOptions(
			inst,
			inst.Title+" (cont.)",
			inst.GroupPath,
			forkToggles{},
			opts,
			git.WorktreeStateOptions{},
			inst.ParentSessionID,
			inst.ParentProjectPath,
			"",
		)
		if cmd == nil {
			h.failsafePause(inst)
			return
		}
		msg := cmd() // executes the fork; returns sessionForkedMsg
		fm, ok := msg.(sessionForkedMsg)
		if !ok || fm.err != nil || fm.instance == nil {
			h.failsafePause(inst)
			return
		}

		h.registerContinuation(inst, fm.instance, seed, generation+1)
	})
}

// registerContinuation publishes a freshly-spawned continuation session, seeds
// it with the handoff prompt, records its generation, and archives the source.
//
// Spawning off the UI loop means the normal sessionCreated/sessionForked
// handlers never run, so registration is done by hand here — the same
// persist+reload path the reload branch uses to inject a session from a non-UI
// goroutine. The reload rebuilds the tree on the UI thread.
func (h *Home) registerContinuation(source, next *session.Instance, seed string, generation int) {
	h.instancesMu.Lock()
	h.instances = append(h.instances, next)
	h.instanceByID[next.ID] = next
	h.instancesMu.Unlock()
	h.forceSaveInstances()
	if h.storageWatcher != nil {
		h.storageWatcher.TriggerReload()
	}

	// Persist the successor's generation before it can ever fork itself, so a
	// crash between here and its own handoff cannot reset the chain bound.
	if db := statedb.GetGlobal(); db != nil {
		_ = db.WriteHandoffGeneration(next.ID, generation)
	}

	// Seed the continuation prompt once the new pane is live.
	time.Sleep(2 * time.Second)
	if ts := next.GetTmuxSession(); ts != nil {
		_ = ts.SendKeysAndEnter(seed)
	}

	// Archive (pause) the old session for history — targeted, idempotent.
	_ = source.Kill()
	source.ArchivedAt = time.Now().UTC()
	if db := statedb.GetGlobal(); db != nil {
		_ = db.SetArchived(source.ID, source.ArchivedAt)
	}
}

// spawnCrossToolContinuation creates the continuation with a DIFFERENT tool.
//
// Fork is tool-preserving by construction, so a tool switch has to go through
// the create path. targetTool is passed as the bare tool name because
// createSessionTool matches commands exactly — it maps "cursor" to the
// "cursor agent" command itself, and anything it does not recognize silently
// becomes a shell, which is why the name is validated before reaching here.
//
// tempID is empty on purpose: there is no placeholder row to reconcile when the
// create runs off the UI loop.
func (h *Home) spawnCrossToolContinuation(inst *session.Instance, targetTool, seed string, generation int) {
	worktreePath, worktreeRepo, worktreeBranch := inst.WorktreePath, inst.WorktreeRepoRoot, inst.WorktreeBranch

	cmd := h.createSessionInGroupWithWorktreeAndOptions(
		inst.Title+" (cont. "+targetTool+")",
		inst.ProjectPath,
		targetTool,
		inst.GroupPath,
		worktreePath, worktreeRepo, worktreeBranch,
		false, // geminiYoloMode
		false, // sandboxEnabled
		nil,   // toolOptionsJSON
		nil,   // claudeExtraArgs
		"",    // claudeStartQuery — the handoff prompt is sent after the pane is live
		"",    // launchModelID
		false, // multiRepoEnabled
		nil,   // additionalPaths
		inst.ParentSessionID,
		inst.ParentProjectPath,
		"", // tempID
		false,
	)
	if cmd == nil {
		h.failsafePause(inst)
		return
	}
	msg := cmd()
	cm, ok := msg.(sessionCreatedMsg)
	if !ok || cm.err != nil || cm.instance == nil {
		uiLog.Warn("handoff_cross_tool_create_failed",
			slog.String("id", inst.ID),
			slog.String("target_tool", targetTool))
		h.failsafePause(inst)
		return
	}
	if cm.instance.Tool != targetTool {
		// The create path degrades an unrecognized tool to "shell"; a shell pane
		// is not a continuation, so refuse rather than hand off into a dead end.
		uiLog.Error("handoff_cross_tool_degraded",
			slog.String("id", inst.ID),
			slog.String("target_tool", targetTool),
			slog.String("actual_tool", cm.instance.Tool))
		h.failsafePause(inst)
		return
	}
	h.registerContinuation(inst, cm.instance, seed, generation)
}

// failsafePause stops the old session (no data loss) and raises the loudest
// alert. It NEVER auto-/clears the context — a human must take over.
func (h *Home) failsafePause(inst *session.Instance) {
	uiLog.Error("context_budget_failsafe",
		slog.String("session", inst.Title),
		slog.String("id", inst.ID),
		slog.String("action", "paused; manual handoff required"))
	safego.Go(uiLog, "context_budget_failsafe_pause", func() {
		_ = inst.Kill()
	})
	h.notifyBudgetCrossing(inst, session.BudgetOver)
}
