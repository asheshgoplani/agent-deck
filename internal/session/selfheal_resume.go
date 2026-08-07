package session

import (
	"errors"
	"fmt"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/selfheal"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// The self-heal resume executor: the FIRST real selfheal.ActionExecutor.
//
// It delivers exactly one continuation prompt to a session wedged by a transport
// error or an exhausted usage window, through the same verified send path that
// backs `session nudge` — composer-draft guard, submit verification, and the
// Escape+Enter escalation that is the only thing which ungates a wedged composer.
// It does not reimplement send, and it never restarts anything.
//
// Why the send path arrives by registration rather than by import: executeSend
// lives in package main (cmd/agent-deck/session_cmd.go) and internal/session
// cannot import it. cmd/agent-deck registers a wrapper at init, so the one
// verified implementation is still what runs.

// SelfHealSendFunc delivers a message to a session and reports the `session send`
// delivery verdict — one of "submitted", "typed", "typed_not_submitted",
// "unverified", "line_too_long", "no_evidence", "send_failed". The error is the
// send path's own; a non-nil error alongside a delivery string is normal (the
// send path fails loudly for every delivery except "submitted").
type SelfHealSendFunc func(inst *Instance, message string) (delivery string, err error)

var (
	selfHealSenderMu sync.RWMutex
	selfHealSender   SelfHealSendFunc
)

// SetSelfHealSender registers the process-wide send implementation. Called once
// from cmd/agent-deck's init. Passing nil unregisters (tests).
func SetSelfHealSender(fn SelfHealSendFunc) {
	selfHealSenderMu.Lock()
	defer selfHealSenderMu.Unlock()
	selfHealSender = fn
}

func registeredSelfHealSender() SelfHealSendFunc {
	selfHealSenderMu.RLock()
	defer selfHealSenderMu.RUnlock()
	return selfHealSender
}

// Sentinel errors. They are the only three conditions under which a resume
// produces no delivery verdict at all; everything else reports the verdict it got.
var (
	// ErrSelfHealNoSender means no send implementation was registered. In a
	// correctly linked binary this is unreachable — it means the daemon is
	// running a build whose cmd/agent-deck init never ran.
	ErrSelfHealNoSender = errors.New("selfheal: no send implementation registered")
	// ErrSelfHealUnknownSession means the candidate's session id is not in the
	// executor's current view (the session vanished between the poll that built
	// the candidate and the action).
	ErrSelfHealUnknownSession = errors.New("selfheal: candidate session not found")
	// ErrSelfHealWrongAction means the engine asked for something other than a
	// resume. The chokepoint should make this unreachable; it is a mis-wire alarm.
	ErrSelfHealWrongAction = errors.New("selfheal: executor only performs resume")
)

// Resume prompt text. It differs by reason because the two wedges need different
// things said.
const (
	// resumePromptTransport is the transport-error continuation. Field evidence
	// 2026-08-07: one prompt of this shape resumed all three wedged sessions on
	// the first attempt.
	resumePromptTransport = "[agent-deck self-heal] Your previous turn ended on an API transport error " +
		"(the API was unreachable). The connection has since recovered and nothing else has changed. " +
		"Continue exactly where you left off: re-run the step that failed, then carry on."

	// resumePromptUsageLimit is the usage-limit continuation. The subagent warning
	// is mandatory, not decorative: "Agent terminated early due to an API error"
	// means a SUBAGENT hit the limit, and resuming the parent does not restore the
	// subagent's work — without this sentence a parent silently loses a child's
	// output and reports the task done.
	resumePromptUsageLimit = "[agent-deck self-heal] Your previous turn was rejected because the plan usage " +
		"window was exhausted. The window has since reset. Continue exactly where you left off. " +
		"IMPORTANT: if you had dispatched a subagent when the limit hit, that subagent was terminated " +
		"by the limit and its work was NOT saved — re-dispatch it rather than assuming it finished."
)

// resumePromptFor picks the prompt for a candidate's substate. It reads the
// substate rather than the audit's action_params so the executor cannot be
// steered by a malformed params map.
func resumePromptFor(s tmux.Substate) string {
	if s == tmux.SubstateUsageLimit {
		return resumePromptUsageLimit
	}
	return resumePromptTransport
}

// ResumeExecutor is the selfheal.ActionExecutor for ActionResume.
//
// It holds a per-cycle view of the profile's instances because the engine is
// long-lived (the two-read confirm and the cap windows must accumulate across
// polls) while the instance slice is rebuilt every poll. SetInstances refreshes
// the view at the top of each pass.
type ResumeExecutor struct {
	mu   sync.RWMutex
	byID map[string]*Instance
	// send overrides the registered process-wide sender. Non-nil only in tests.
	send SelfHealSendFunc
}

// NewResumeExecutor returns an executor with an empty instance view. Call
// SetInstances before the first Execute.
func NewResumeExecutor() *ResumeExecutor {
	return &ResumeExecutor{byID: map[string]*Instance{}}
}

// SetInstances replaces the id→instance view with this cycle's instances.
func (x *ResumeExecutor) SetInstances(instances []*Instance) {
	view := make(map[string]*Instance, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.ID == "" {
			continue
		}
		view[inst.ID] = inst
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.byID = view
}

func (x *ResumeExecutor) instance(id string) *Instance {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.byID[id]
}

func (x *ResumeExecutor) sender() SelfHealSendFunc {
	x.mu.RLock()
	override := x.send
	x.mu.RUnlock()
	if override != nil {
		return override
	}
	return registeredSelfHealSender()
}

// Execute delivers ONE continuation prompt and reports the real delivery verdict.
//
// The outcome is "resumed:<delivery>", matching the engine's
// outcomeDeliveredPrefix contract: only "resumed:submitted" counts as a healthy
// recovery, so a message typed into a composer that never accepted Enter is
// recorded as the failure it is and feeds the circuit breaker.
//
// A delivery verdict is returned with a NIL error even when the send path itself
// errored — it errors for every delivery except "submitted", and collapsing that
// into an error string would discard the machine-checkable value the audit exists
// to carry. The send error is logged instead. Only the three conditions that
// produce no verdict at all return an error.
func (x *ResumeExecutor) Execute(c selfheal.Candidate, a selfheal.Action) (string, error) {
	if a != selfheal.ActionResume {
		return "", fmt.Errorf("%w: got %q", ErrSelfHealWrongAction, a)
	}
	inst := x.instance(c.SessionID)
	if inst == nil {
		return "", fmt.Errorf("%w: %s", ErrSelfHealUnknownSession, c.SessionID)
	}
	send := x.sender()
	if send == nil {
		return "", ErrSelfHealNoSender
	}

	prompt := resumePromptFor(c.Substate)
	delivery, sendErr := send(inst, prompt)
	if delivery == "" {
		// The send path always sets a delivery on any path that touched the pane.
		// An empty one means it failed before that, which is a verdict of its own.
		delivery = "no_evidence"
	}
	logging.ForComponent(logging.CompNotif).Info("selfheal_resume_delivered",
		"session_id", c.SessionID,
		"title", inst.Title,
		"substate", string(c.Substate),
		"delivery", delivery,
		"send_error", errString(sendErr),
	)
	return "resumed:" + delivery, nil
}
