package ui

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

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

// handoffDir returns the per-session handoff directory ~/.agent-deck/handoff/<id>.
func handoffDir(id string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agent-deck", "handoff", id)
}

// fileExists reports whether the path exists (used to poll for PROMPT.md).
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
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
			h.forkContinuation(inst)
			_ = db.WriteHandoffState(inst.ID, string(dec.Next), h.handoffTriggeredAt[inst.ID])
		case session.ActionFailsafe:
			h.failsafePause(inst)
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

// forkContinuation reads PROMPT.md, forks a continuation session inheriting the
// old session's tool/profile/path/group/parent/worktree, seeds it with a short
// preamble + the handoff prompt, registers it, and archives the old session.
// On any failure it falls back to failsafePause (no silent data loss).
func (h *Home) forkContinuation(inst *session.Instance) {
	promptPath := filepath.Join(handoffDir(inst.ID), "PROMPT.md")

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
		data, err := os.ReadFile(promptPath)
		if err != nil {
			uiLog.Warn("handoff_prompt_read_failed", slog.String("id", inst.ID), slog.Any("err", err))
			h.failsafePause(inst)
			return
		}
		seed := "You are a continuation of a previous session that reached its context budget. " +
			"Resume from this handoff prompt:\n\n" + string(data)

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

		// Register the new (already-started-in-tmux) session via the same
		// persist+reload path the reload branch uses to inject a forked session
		// from a non-UI goroutine. The reload rebuilds the tree on the UI thread.
		h.instancesMu.Lock()
		h.instances = append(h.instances, fm.instance)
		h.instanceByID[fm.instance.ID] = fm.instance
		h.instancesMu.Unlock()
		h.forceSaveInstances()
		if h.storageWatcher != nil {
			h.storageWatcher.TriggerReload()
		}

		// Seed the continuation prompt once the new pane is live.
		time.Sleep(2 * time.Second)
		if ts := fm.instance.GetTmuxSession(); ts != nil {
			_ = ts.SendKeysAndEnter(seed)
		}

		// Archive (pause) the old session for history — targeted, idempotent.
		_ = inst.Kill()
		inst.ArchivedAt = time.Now().UTC()
		if db := statedb.GetGlobal(); db != nil {
			_ = db.SetArchived(inst.ID, inst.ArchivedAt)
		}
	})
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
