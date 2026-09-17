package session

// Hook lag (status-light audit 2026-09-17, defect B).
//
// Claude's lifecycle hooks are the primary truth for a Claude session's light,
// and a "running" hook within its freshness window short-circuits pane
// inspection. The audit caught the cost: the Stop hook landed 8 minutes after
// the pane showed "✻ Sautéed for 3m 4s · done" at an empty prompt, and for the
// whole window the light stayed green while the substate — read live from the
// pane — said idle-at-empty-prompt, a pair the substate contract forbids.
//
// The rule: the pane is newer evidence than a lagging hook, but a single frame
// never overrules a fresh hook. Two consecutive independent samples of a
// completed turn at an idle prompt (no spinner, no interrupt hint, no open
// menu, no background work — tmux.PromptDetector.CompletedTurnAtIdlePrompt)
// flip the light to waiting; the substate says hook-lag from the first sample
// so the disagreement is visible before the light moves. Any busy cue resets
// the count, so a live spinner is never contradicted, and a new hook event
// (the Stop finally landing, or a new prompt) starts the count over.

// hookLagConfirmPasses is the number of consecutive completed-turn samples
// needed before the light flips away from the hook's "running".
const hookLagConfirmPasses = 2

// noteHookLagSampleLocked records one pane sample taken while the hook says
// running and reports whether the lag is now confirmed. sampled=false (the
// probe served its cached verdict) leaves the count untouched, so two calls
// within one sample interval cannot count as two passes. Caller holds i.mu.
func (i *Instance) noteHookLagSampleLocked(paneIdle, sampled bool) bool {
	if !i.hookLagHookUpdate.Equal(i.hookLastUpdate) {
		// A newer hook event: whatever was observed under the old one is moot.
		i.hookLagHookUpdate = i.hookLastUpdate
		i.hookLagPasses = 0
	}
	if !sampled {
		return i.hookLagPasses >= hookLagConfirmPasses
	}
	if !paneIdle {
		i.hookLagPasses = 0
		return false
	}
	i.hookLagPasses++
	return i.hookLagPasses >= hookLagConfirmPasses
}

// resetHookLagLocked drops the evidence. Caller holds i.mu.
func (i *Instance) resetHookLagLocked() {
	i.hookLagPasses = 0
}

// reconcileSubstate applies reconcileSubstateWithStatus to sub under the
// instance's current status and hook-lag evidence. Takes i.mu briefly.
func (i *Instance) reconcileSubstate(sub Substate) Substate {
	i.mu.Lock()
	status, lagged := i.Status, i.hookLagPasses > 0
	i.mu.Unlock()
	return reconcileSubstateWithStatus(status, sub, lagged)
}

// reconcileSubstateWithStatus closes the contradictory pair at the accessor:
// idle-at-empty-prompt pairs with idle/waiting only. Beside a running status
// it is either hook lag (when the lag probe has seen the finished frame) or
// simply unknown — never a claim that the session is both working and idle.
// A confirmed lag keeps its name after the light flips to waiting, so the
// reason for the waiting light stays visible until the hook catches up.
func reconcileSubstateWithStatus(status Status, sub Substate, lagged bool) Substate {
	if sub != SubstateIdleAtEmptyPrompt {
		return sub
	}
	if lagged {
		return SubstateHookLag
	}
	if status == StatusRunning {
		return SubstateNone
	}
	return sub
}
