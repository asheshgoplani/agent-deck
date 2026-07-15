package session

import "time"

// HandoffState is the persisted per-session position in the autonomous handoff
// state machine. The zero value ("") is HandoffNormal so an unset session is
// implicitly normal.
type HandoffState string

const (
	HandoffNormal        HandoffState = ""
	HandoffWrapRequested HandoffState = "wrap_requested"
	HandoffWaiting       HandoffState = "wait_handoff"
	HandoffDone          HandoffState = "done"
	HandoffFailsafe      HandoffState = "failsafe"
)

// HandoffAction is the side effect the caller must perform on a transition.
type HandoffAction int

const (
	ActionNone HandoffAction = iota
	// ActionRequestWrap: create the handoff dir, inject the wrap-up instruction,
	// pause new work, and record TriggeredAt = now.
	ActionRequestWrap
	// ActionFork: read PROMPT.md, fork the continuation session, archive the old one.
	ActionFork
	// ActionFailsafe: pause/stop the old session and raise the loudest alert.
	ActionFailsafe
)

// HandoffInputs are the injected observations the pure state machine reasons
// over. No I/O happens inside the machine.
type HandoffInputs struct {
	Tokens      int       // CurrentContextTokens
	PromptReady bool      // handoff/<id>/PROMPT.md exists
	AgentIdle   bool      // session is waiting/idle (not actively generating)
	Now         time.Time // current clock
	TriggeredAt time.Time // when WRAP_REQUESTED was entered (zero before that)
}

// HandoffDecision is the machine's output for one tick.
type HandoffDecision struct {
	Next   HandoffState
	Action HandoffAction
}

func timeoutElapsed(in HandoffInputs, cfg ContextBudgetSettings) bool {
	if in.TriggeredAt.IsZero() || cfg.HandoffTimeoutSeconds <= 0 {
		return false
	}
	return in.Now.Sub(in.TriggeredAt) >= time.Duration(cfg.HandoffTimeoutSeconds)*time.Second
}

// NextHandoffState advances the handoff state machine by one tick. Pure.
func NextHandoffState(cur HandoffState, in HandoffInputs, cfg ContextBudgetSettings) HandoffDecision {
	switch cur {
	case HandoffDone, HandoffFailsafe:
		return HandoffDecision{Next: cur, Action: ActionNone}

	case HandoffWrapRequested, HandoffWaiting:
		if in.Tokens >= cfg.CeilingTokens || timeoutElapsed(in, cfg) {
			return HandoffDecision{Next: HandoffFailsafe, Action: ActionFailsafe}
		}
		if in.PromptReady && in.AgentIdle {
			return HandoffDecision{Next: HandoffDone, Action: ActionFork}
		}
		return HandoffDecision{Next: HandoffWaiting, Action: ActionNone}

	default: // HandoffNormal
		if in.Tokens >= cfg.HighTokens {
			return HandoffDecision{Next: HandoffWrapRequested, Action: ActionRequestWrap}
		}
		return HandoffDecision{Next: HandoffNormal, Action: ActionNone}
	}
}
