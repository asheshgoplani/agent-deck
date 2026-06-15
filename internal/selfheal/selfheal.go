// Package selfheal is the deterministic "Go floor" of the self-heal supervision
// design (SELF-HEAL-DESIGN.md §4.3). It detects a TRULY stuck session, classifies
// the cause, and — gated by a release stage — decides on a bounded, idempotent,
// fully-logged recovery.
//
// STAGE 1 (this release, v1.9.67) is OBSERVE-ONLY. The engine evaluates the §1.3
// stuck predicate, exercises the caps / backoff / circuit-breaker / flicker
// state-machine, and LOGS what it WOULD do — but takes ZERO action. Observe mode
// holds no action executor at all, so "no recovery primitive is ever called" is a
// structural property, not a runtime check (see Engine.Observe and the
// no-action test). Modes single_action / full are DEFINED but GUARDED: they
// refuse to act until Stages 2-3 are re-approved (the three §9 gap-fixes).
//
// The module is pure: it consumes a Candidate snapshot (already-read data) and a
// monotonic clock, and emits a structured Event to an EventSink. It never reaches
// into tmux, never calls SaveInstances, never touches the rebind path. That
// keeps the predicate and the safety machine fully unit-testable and guarantees
// the observe-only invariant.
package selfheal

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Mode is the global self-heal authority level (config [selfheal] mode).
type Mode string

const (
	// ModeObserve logs would_have for every candidate and takes NO action. The
	// default and the only mode that does anything in v1.9.67.
	ModeObserve Mode = "observe"
	// ModeSingleAction is Stage 2 (model_unavailable restart+Opus only). DEFINED
	// but GUARDED — refuses to act until Ashesh re-approves + the §9 gaps close.
	ModeSingleAction Mode = "single_action"
	// ModeFull is Stage 3 (all classes, auto within caps). DEFINED but GUARDED.
	ModeFull Mode = "full"
)

// Action is what self-heal would do for a candidate. It is recorded in the audit
// event; in observe mode it is the would_have value and is never executed.
type Action string

const (
	ActionNone Action = ""
	// ActionRestartModelSwitch — restart pinned to a safe model + re-issue last
	// intent once (model_unavailable, §2.1).
	ActionRestartModelSwitch Action = "restart_model_switch"
	// ActionRestart — one bounded restart via the resume path (wedged_pane, §2.3).
	ActionRestart Action = "restart"
	// ActionResend — re-send the last intent once before any restart
	// (idle_at_empty_prompt, §2.3).
	ActionResend Action = "resend"
	// ActionRestartReassertCreds — one restart that reasserts the credential
	// symlink, the known fix for the scratch-clobber 401 class (auth_401, §2.2).
	ActionRestartReassertCreds Action = "restart_reassert_creds"
	// ActionEscalate — surface one consolidated alert, take no recovery
	// (cap hit, breaker open, 401 that survived a restart).
	ActionEscalate Action = "escalate"
)

// Decision is the verdict the policy reached for a candidate this cycle.
type Decision string

const (
	DecisionAct          Decision = "act"          // would act (observe: would_have set)
	DecisionSkipHealthy  Decision = "skip_healthy"  // substate not a stuck class
	DecisionSkipBusy     Decision = "skip_busy"     // live busy indicator (authoritative)
	DecisionSkipMidTurn  Decision = "skip_midturn"  // fresh hook-running / output moved
	DecisionSkipDwell    Decision = "skip_dwell"    // not dwelled past threshold yet
	DecisionSkipConfirm  Decision = "skip_confirm"  // second read disagreed (2-read drop)
	DecisionSkipStopped  Decision = "skip_stopped"  // session stopped (user-intentional)
	DecisionSkipOptOut   Decision = "skip_optout"   // per-session/group opt-out
	DecisionCapHit       Decision = "cap_hit"       // per-session or global cap exceeded
	DecisionBreakerOpen  Decision = "breaker_open"  // circuit breaker / flicker quarantine
)

// stuckDwellThresholds are the §1.3 cause-specific dwell windows. usage_limit is
// intentionally absent (never auto-restart, §2). A substate not present here is
// not a self-heal-actionable stuck class.
var stuckDwellThresholds = map[tmux.Substate]time.Duration{
	tmux.SubstateModelUnavailable:  90 * time.Second,
	tmux.SubstateAuth401:           60 * time.Second,
	tmux.SubstateIdleAtEmptyPrompt: 5 * time.Minute,
}

// actionForSubstate maps a confirmed stuck substate to the action self-heal
// would take FIRST (§2.4). Observe mode records this as would_have.
var actionForSubstate = map[tmux.Substate]Action{
	tmux.SubstateModelUnavailable:  ActionRestartModelSwitch,
	tmux.SubstateAuth401:           ActionRestartReassertCreds,
	tmux.SubstateIdleAtEmptyPrompt: ActionResend,
}

// IsStuckSubstate reports whether a substate is a self-heal-actionable stuck
// class (§1.3 condition 1). SubstateRunning / SubstateNone are never stuck.
func IsStuckSubstate(s tmux.Substate) bool {
	_, ok := stuckDwellThresholds[s]
	return ok
}

// DwellThreshold returns the cause-specific dwell window for a stuck substate,
// or 0 (and ok=false) for a non-stuck class.
func DwellThreshold(s tmux.Substate) (time.Duration, bool) {
	d, ok := stuckDwellThresholds[s]
	return d, ok
}

// WouldHaveAction returns the action self-heal would take for a confirmed stuck
// candidate (used to populate would_have in observe mode).
func WouldHaveAction(s tmux.Substate) Action {
	if a, ok := actionForSubstate[s]; ok {
		return a
	}
	return ActionEscalate
}
