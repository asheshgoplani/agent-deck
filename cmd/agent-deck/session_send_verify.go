package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// The `session send --no-wait` semantics for the CLI live in executeSend
// (called with noWait=true and noWaitSendTuning()). The historical issue
// #616 fix is preserved there as three layers, applied in order:
//
//  1. Preflight readiness barrier (capped at 5s): polls the pane for a
//     visible Claude composer `❯`. Without this, the initial paste
//     lands in the TTY before Claude's Ink TUI has rendered the input
//     surface — the keystrokes are discarded by pre-mount handlers.
//
//  2. Post-composer settle delay (500ms): Claude's composer glyph can
//     render BEFORE React completes mounting the input handler. Without
//     this delay, the paste can still be partially swallowed by the
//     mount transition (observed live: message vanished entirely, no
//     unsent prompt to retry on). 500ms is empirically enough.
//
//  3. Extended verification budget via noWaitSendOptions() (6s, 30×200ms):
//     after the initial send, keeps detecting unsent-prompt markers and
//     re-firing SendEnter if the composer still holds our message.
//
// Full-body resend recovery stays disabled (#479). Non-Claude tools skip
// the preflight; they have their
// own readiness shapes and upstream gating. Issue #1409 added a fourth
// layer between 2 and 3: the composer-draft guard.

type sendRetryTarget interface {
	SendKeysAndEnter(string) error
	SendKeysAndEnterChecked(keys string, capture func() (string, error), check tmux.PostPasteCheck) error
	GetStatus() (string, error)
	SendEnter() error
	SendCtrlC() error
	SendKeysChunked(string) error
	CapturePaneFresh() (string, error)
}

type sendRetryOptions struct {
	promptGuard    *promptGuardTarget
	maxRetries     int
	checkDelay     time.Duration
	maxFullResends int // Legacy option; full-body interrupt/resend recovery is disabled.
	tool           string

	// expectedPasteBreaks, when > 0, is send.ExpectedPasteMarkerLineBreaks
	// for this message: the number of hard line breaks Claude's composer
	// declares in its "[Pasted text #N +M lines]" marker for an intact
	// delivery. When set, the initial send withholds Enter unless the
	// declared count meets it (issue #2079's guard, previously wired only
	// into the launch/instance send path, not `session send`; #2148 item 4).
	// Zero (the default) sends unchecked, matching pre-#2148 behavior for
	// every non-Claude target and every single-line message.
	expectedPasteBreaks int

	// verifyDelivery, when true, requires the verification loop to observe at
	// least one positive signal that the message reached the inner agent (an
	// "active" status transition, an unsent-prompt composer marker, or the
	// message body appearing in the captured pane). If the
	// budget is exhausted without any such signal, the function returns an
	// error instead of the prior best-effort `nil`. Closes the silent-drop
	// path reported in issue #876.
	verifyDelivery bool

	// composerPasteFreeBeforeSend is the pre-send provenance evidence for the
	// #1777 attribution gate: the caller positively observed, immediately
	// before this send, a composer holding no "[Pasted text …]" marker. Only
	// then can a marker seen during the verify loop be attributed to the
	// collapse of our own payload and receive an Enter nudge. Left false, a
	// composer paste marker counts as foreign content and no nudge fires —
	// the fail-safe default for callers that cannot establish provenance.
	composerPasteFreeBeforeSend bool

	// suggestionShownBeforeSend mirrors send.ComposerGuardResult.SuggestionShown
	// for the initial submit (send.TypeAndSubmit).
	suggestionShownBeforeSend bool

	// targetBusyByHook, when non-nil, reports the target's hook-driven busy
	// state (the #1578 `--defer-if-busy` signal) as (busy, known), read fresh
	// from the hook file on every call. It is sampled once before the send
	// and then on every verification pass (issues #1978, #2033): known &&
	// busy, combined with token movement against the pre-send baseline,
	// classifies a landed message as deliveryQueued (busy before the send:
	// the message sits behind a live turn) or deliverySubmitted (busy only
	// after: the target took it up). known == false (hooks absent, not
	// firing, or stale) changes no verdict. nil means no hook signal is
	// wired for this caller, which can then never report queued.
	targetBusyByHook func() (busy, known bool)

	// turnAdvanced, when non-nil, reports whether the harness's own
	// transcript already holds this send's user record after the pre-send
	// cursor (session.TurnAdvanced). It is the authoritative "the target
	// took this message up" signal — turn advancement, not pane inference —
	// and wins over every heuristic below. nil for callers without a
	// transcript (non-Claude tools, slash commands, unknown path).
	turnAdvanced func() bool
}

// verificationChecks is how many post-send checks the verify loop runs for
// this tool: the full budget on the Claude path, the capped arrival poll
// otherwise (arrivalVerifyChecks). verifyContentArrival sizes its loop by
// it, and executeSend words the verification window with it.
func (o sendRetryOptions) verificationChecks(claudeLike bool) int {
	checks := o.maxRetries
	if checks < 1 {
		checks = 1
	}
	if !claudeLike && checks > arrivalVerifyChecks {
		checks = arrivalVerifyChecks
	}
	return checks
}

// hookBusyNow reads targetBusyByHook once and reports whether the hook
// signal is fresh AND busy. Idle, unknown (hooks absent, not firing, or
// stale) and "no probe wired" all read false, so no caller can mistake
// silence for busy.
func (o sendRetryOptions) hookBusyNow() bool {
	if o.targetBusyByHook == nil {
		return false
	}
	busy, known := o.targetBusyByHook()
	return known && busy
}

// sendInitialKeysChecked is the initial submit of `session send`:
// send.TypeAndSubmit, shared with the launch path (#2148 item 4b).
func sendInitialKeysChecked(target sendRetryTarget, message string, expectedBreaks int, suggestionShown bool) error {
	return send.TypeAndSubmit(target, message, expectedBreaks, suggestionShown)
}

// sendWithRetryTarget sends the message and runs the bounded submit
// verification loop. It returns a delivery status (one of the delivery*
// constants) alongside the error so callers can expose a machine-checkable
// outcome (issue #1413).
func sendWithRetryTarget(target sendRetryTarget, message string, skipVerify bool, opts sendRetryOptions) (string, error) {
	if opts.maxRetries <= 0 {
		opts.maxRetries = 1
	}
	if opts.checkDelay < 0 {
		opts.checkDelay = 0
	}

	// Baseline for the arrival check below, taken BEFORE the send. Neither
	// signal the check uses means anything as a snapshot — only as a change:
	//
	//   - "the body is on screen": re-sending an identical message (a
	//     heartbeat, an inbox nudge, a retry) would match the previous copy
	//     still sitting in the pane and certify a send that vanished.
	//   - "the agent is active": a pane that was ALREADY busy is still busy a
	//     moment later whether or not it received anything.
	//
	// Both would hand back a success for a message that never arrived, which
	// is the exact phantom this is here to kill. Only a transition away from
	// this baseline counts. Costs one pane capture plus one status read, and
	// only on the path that needs them.
	//
	// Issue #1978: the queued verdict needs the same baseline. "Hook says
	// busy" plus "the body is on screen" is a snapshot; only "hook says busy"
	// plus "a NEW copy of the body appeared" is attributable to this send. So
	// the baseline is also taken whenever a hook probe is wired. Callers that
	// wire none can never receive a queued verdict and pay nothing extra.
	var arrivalBaseline sendArrivalBaseline
	if skipVerify || opts.targetBusyByHook != nil {
		arrivalBaseline = captureArrivalBaseline(target, message)
	}
	// hookBusyBeforeSend distinguishes the two ways the hook can read busy
	// after a send that landed: the target was ALREADY mid-turn, so the
	// message sits behind that turn (queued); or it went busy only now, which
	// is the target taking this message up (submitted).
	hookBusyBeforeSend := opts.hookBusyNow()

	if err := sendInitialKeysChecked(target, message, opts.expectedPasteBreaks, opts.suggestionShownBeforeSend); err != nil {
		if opts.promptGuard != nil && opts.promptGuard.refused {
			return opts.promptGuard.refusal()
		}
		// A refused over-long line is a distinct, actionable outcome: the
		// transport typed nothing, so the composer is untouched and the
		// caller must not retry the same body against the same pane
		// (issue #1793).
		if errors.Is(err, tmux.ErrCanonicalLineOverflow) {
			return deliveryLineTooLong, fmt.Errorf("message not delivered: %w", err)
		}
		return deliverySendFailed, fmt.Errorf("failed to send message: %w", err)
	}

	if skipVerify {
		// Issue #1793: "the tmux command returned" is not delivery. This
		// path used to return success on transport alone, which is exactly
		// how a 4095-byte payload that never reached the agent was reported
		// as `{"success":true,"delivery":"unverified"}`. Confirm the body
		// actually reached the pane before claiming anything.
		return verifyContentArrival(target, message, opts, arrivalBaseline, hookBusyBeforeSend)
	}

	// Verify the agent accepted Enter and began processing.
	// Strategy:
	// - If unsent prompt is visible, press Enter again immediately.
	// - Consider success only after sustained post-send activity ("active").
	// - If we never observe active and remain in waiting/idle, keep a periodic
	//   fallback Enter cadence instead of returning early (handles late unsent
	//   prompt rendering races seen in Claude startup).
	const activeSuccessThreshold = 2
	const waitingAfterActiveThreshold = 2
	waitingNoMarkerChecks := 0
	activeChecks := 0
	sawActiveAfterSend := false

	// sawDeliveryEvidence flips true on any positive signal that the message
	// reached the agent: an "active" status transition, an unsent-prompt
	// composer marker, or the message body appearing verbatim in the pane.
	// When opts.verifyDelivery is set and this stays false for the entire
	// budget, the function returns an error instead of silently succeeding
	// (issue #876).
	//
	// ARRIVAL IS NOT SUBMISSION. Two of those three signals — body text in the
	// pane, and an unsent-prompt marker — say the bytes got there and say
	// nothing about the agent accepting them. The unsent-prompt marker
	// literally means the opposite. Only sawActiveAfterSend below is
	// submission evidence, and conflating the two is what let this function
	// return deliverySubmitted for a message still sitting in a composer:
	// the exact phantom success of issue #1793, on the Claude path.
	sawDeliveryEvidence := false
	// The composer positively HOLDING this message at some point, combined
	// with it being clear at the end of the budget (held-then-cleared), is
	// genuine submission evidence: the agent took the message out of the
	// composer. That latch lives in obs (send.Observer.NoteComposerHeld); body
	// text merely being visible is not the same thing.
	// Snippet of the message body to look for in captured pane content. Some
	// TUI frameworks (and non-Claude tools) won't render a "[Pasted text …]"
	// or "❯ <msg>" marker, so direct verbatim content is the only signal.
	// Take the first run of non-whitespace content, capped, to avoid false
	// positives from matching common short strings.
	deliveryToken := messageDeliveryToken(message)
	// attrib is the #1777 attribution gate. EVERY bare Enter in this loop —
	// including the unsent-prompt branch, which used to press unconditionally
	// whenever a "[Pasted text …]" marker appeared anywhere in the pane —
	// goes through attrib.NudgeEnter, so no branch can submit composer
	// content agent-deck cannot attribute to its own delivery.
	attrib := send.EnterAttribution{
		Message:        message,
		OwnPasteMarker: opts.composerPasteFreeBeforeSend,
	}
	// sawTokenMovement latches once a NEW copy of the body is observed
	// relative to the pre-send baseline (issue #1978). Unlike
	// sawDeliveryEvidence's "the token is somewhere on screen", this is
	// attributable to THIS send, and it stays latched so a body that later
	// scrolls out of the visible pane (#2033) is not un-delivered by a later
	// capture. Only ever set when a baseline exists. A composer paste marker
	// is deliberately NOT movement here: a marker the composer newly holds is
	// the shape of bytes sitting unsent, the opposite of a delivery.
	sawTokenMovement := false
	// obs folds every frame below into the evidence the end-of-budget verdict
	// rests on (issue #1793): arrival, the composer holding then releasing
	// the body, an open menu, a gone pane. The early returns in the loop are
	// the positive submission signals; obs decides what an exhausted budget
	// means.
	obs := newSendObserver(opts.tool, message, arrivalBaseline, true, hookBusyBeforeSend)
	for retry := 0; retry < opts.maxRetries; retry++ {
		if opts.promptGuard != nil && opts.promptGuard.refused {
			return opts.promptGuard.refusal()
		}
		time.Sleep(opts.checkDelay)

		// Turn advancement in the harness's own transcript is the one
		// authoritative submission signal and needs no pane at all.
		if opts.turnAdvanced != nil && opts.turnAdvanced() {
			return deliverySubmitted, nil
		}

		unsentPromptDetected := false
		queueAcknowledged := false
		// paneNow is this iteration's observation (raw ANSI + whether the
		// capture succeeded at all), and is what the attribution gate reads.
		captured, captureErr := target.CapturePaneFresh()
		paneNow := send.CaptureOutcome(captured, captureErr)
		obs.Observe(paneNow)
		if paneNow.OK {
			content := tmux.StripANSI(captured)
			unsentPromptDetected = send.ComposerHoldsPasteMarker(captured, tmux.StripANSI) || send.ComposerHoldsUnsentMessage(captured, message, tmux.StripANSI)
			queueAcknowledged = claudeQueueAcknowledged(content)
			if !sawDeliveryEvidence && deliveryToken != "" && strings.Contains(content, deliveryToken) {
				sawDeliveryEvidence = true
			}
			if !sawTokenMovement && arrivalBaseline.paneOK {
				if n, _, ok := paneArrivalCounts(captured, message); ok && n > arrivalBaseline.occurrences {
					sawTokenMovement = true
				}
			}
		}

		// Issue #1978 / #2033: the hook-driven busy signal (the one
		// --defer-if-busy polls, #1578) is read FRESH every iteration and is
		// the only thing that may say "busy" — the spike-filtered
		// GetStatus decays through waiting to idle while a target is in
		// fact generating, which is how a queued message was reported "NOT
		// delivered" for the whole budget. Busy alone proves nothing about
		// this message, though, and neither does the body being on screen.
		// `queued` is only ever reported on the harness's own
		// acknowledgement: the target was mid-turn BEFORE the send, a new
		// copy of the body landed, the composer is not holding it, and the
		// pane shows Claude's queued-messages affordance in the same frame.
		// A target that was NOT busy before the send and reads busy once the
		// body has landed took this message up (the hook edge is written by
		// its UserPromptSubmit hook). Classification does not depend on any
		// retry threshold or resend budget, so the --no-wait path reports it
		// identically. known == false (hooks absent, not firing, or stale)
		// leaves every other verdict exactly as it was.
		if sawTokenMovement && !unsentPromptDetected && opts.hookBusyNow() {
			if !hookBusyBeforeSend {
				return deliverySubmitted, nil
			}
			// The hint must have appeared for THIS send. One left from
			// earlier queued input proves nothing, so the frames are read
			// as if no hint were shown (ASK-95 lander review).
			if queueAcknowledged && !arrivalBaseline.queueHint {
				return deliveryQueued, nil
			}
		}
		status, err := target.GetStatus()

		if unsentPromptDetected {
			sawDeliveryEvidence = true
			obs.NoteComposerHeld()
			waitingNoMarkerChecks = 0
			activeChecks = 0
			attrib.NudgeEnter(target, paneNow, tmux.StripANSI)
			continue
		}

		// A target the hook reported mid-turn BEFORE the send is active on
		// that turn; its activity is not evidence about this message (the
		// same "was already busy" rule verifyContentArrival applies), so
		// the active shortcut is disabled and only turn advancement, the
		// hook edge or the queue acknowledgement above can settle it.
		if err == nil && status == "active" && !hookBusyBeforeSend {
			sawActiveAfterSend = true
			sawDeliveryEvidence = true
			waitingNoMarkerChecks = 0
			activeChecks++
			if activeChecks >= activeSuccessThreshold {
				return deliverySubmitted, nil
			}
			continue
		}
		activeChecks = 0

		if err == nil && (status == "waiting" || status == "idle") {
			if sawActiveAfterSend {
				waitingNoMarkerChecks++
				if waitingNoMarkerChecks >= waitingAfterActiveThreshold {
					return deliverySubmitted, nil
				}
			} else {
				waitingNoMarkerChecks = 0

				// Never clear or resend a body to force progress. Automatic Ctrl-C
				// can erase operator input or become a session-exit gesture.
				// Only the attribution gate may authorize an Enter retry.
				//
				// #2033: a target that is in fact generating satisfies every
				// condition here, because the spike-filtered status decays
				// through waiting to idle without ever reporting active. The
				// hook-driven busy probe at the top of the loop is what tells
				// that target apart from one that never received the message.

				// We haven't observed any post-send activity yet. Nudge Enter
				// aggressively in the early window (every iteration for first 5
				// retries) then every 2nd iteration. This addresses bracketed
				// paste timing failures that are most likely early on.
				if retry < 5 || retry%2 == 0 {
					attrib.NudgeEnter(target, paneNow, tmux.StripANSI)
				}
			}
			continue
		}
		waitingNoMarkerChecks = 0

		// Ambiguous state: keep a best-effort Enter retry budget.
		// Increased from 2 to 4 because some TUI frameworks take longer
		// to process and reflect state.
		if retry < 4 {
			attrib.NudgeEnter(target, paneNow, tmux.StripANSI)
		}
	}
	if opts.promptGuard != nil && opts.promptGuard.refused {
		return opts.promptGuard.refusal()
	}

	// Budget exhausted without a confirmed submit. The verdict follows the
	// evidence, never its absence (issue #1793): one last fresh frame goes to
	// the observer, then it classifies.
	if opts.verifyDelivery {
		if sawActiveAfterSend {
			// The agent went active after the send: it took the message up.
			obs.NoteTurnStarted()
		}
		if sawDeliveryEvidence && !arrivalBaseline.paneOK {
			// With no pre-send baseline the token being on screen at all is
			// the only arrival evidence there is. With a baseline the
			// observer's delta is the truth: a copy that predates the send
			// is not this send arriving (#1978).
			obs.NoteBodyArrived()
		}
		final, finalErr := target.CapturePaneFresh()
		obs.Observe(send.CaptureOutcome(final, finalErr))
		v := obs.Verdict(opts.maxRetries, verificationWindow(opts.maxRetries, opts.checkDelay))
		switch v.Outcome {
		case send.OutcomeConfirmed:
			// Active edge, or held-then-cleared on a target that was not
			// already mid-turn (issue #1978: on a busy target the body
			// moving out of view is not this message's turn).
			return deliverySubmitted, nil
		case send.OutcomeFailed:
			return deliveryFailure(v, opts.maxRetries)
		case send.OutcomeDeliveredUnconfirmed:
			// The body reached the pane and Enter was sent; nothing showed
			// the harness taking it, nothing showed it failing. Delivered,
			// confirmation unknown: exit 0, and the CLI says so.
			return deliveryDelivered, nil
		}
		// A target known busy before the send may keep the submitted line off
		// screen until its next turn. No visible echo is an unknown outcome.
		if hookBusyBeforeSend {
			return deliveryUnverified, nil
		}
		// Idle targets still retain the startup silent-drop guard (#876).
		return deliveryNoEvidence, fmt.Errorf("send dropped silently: no evidence of delivery after %d checks (issue #876). "+
			"The agent never transitioned to 'active', no composer/unsent-paste marker appeared, "+
			"and the message body was not visible in the pane. Verify the inner agent is reading from "+
			"its TTY before retrying", opts.maxRetries)
	}

	// Legacy best-effort contract for paths that gate verification elsewhere.
	return deliveryUnverified, nil
}

// deliveryFailure maps a Failed verdict's positive evidence to the delivery
// status and error line the CLI reports (exit 1).
func deliveryFailure(v send.Verdict, checks int) (string, error) {
	switch v.Failure {
	case send.FailurePaneGone:
		return deliveryPaneGone, errors.New(v.Message)
	case send.FailureInteractiveMenu:
		return deliveryMenuOpen, errors.New(v.Message)
	default:
		return composerHoldsFailure(checks)
	}
}

// composerHoldsFailure is the typed_not_submitted outcome (issue #1413): the
// composer still holds the message after every bounded Enter retry.
func composerHoldsFailure(checks int) (string, error) {
	return deliveryTypedNotSubmitted, fmt.Errorf(
		"message typed but not submitted after %d verification checks (issue #1413): "+
			"the composer still holds the message despite bounded Enter retries. "+
			"The recipient agent's input handler is not accepting Enter", checks)
}

// sendArrivalBaseline is the pre-send state the arrival check measures change
// against. Every field here is meaningless on its own and meaningful only as a
// delta (see the comment at the capture site).
type sendArrivalBaseline struct {
	// occurrences is how many copies of the message body were already
	// visible in the pane before the send.
	occurrences int
	// paneOK reports that the pre-send capture actually succeeded. When it
	// did not there is NO baseline, and the content signal must be switched
	// off rather than defaulted to zero: a failed look would otherwise make
	// a pre-existing copy of a repeated message read as a new arrival.
	paneOK bool
	// pasteMarkers is how many "[Pasted text …]" collapse markers the
	// composer already held before the send. Only meaningful when paneOK is
	// true — it comes from the same capture. Needed because the transport
	// frames multi-line bodies as bracketed pastes (issue #1855), which a
	// composer renders as that marker instead of the verbatim body.
	//
	// A COUNT, exactly like occurrences above, and for the same reason: a
	// submitted paste leaves its marker on screen permanently, so a boolean
	// "a marker was already there" is armed forever after the first
	// multi-line send to a pane and kills the signal for every later one.
	// Only "one more than before" is attributable to THIS send.
	pasteMarkers int
	// raw is the pre-send capture itself (only meaningful when paneOK), the
	// baseline the send.Observer measures arrival against.
	raw string
	// wasActive reports whether the agent was already working before the
	// send, in which case "it is active now" proves nothing.
	wasActive bool
	// statusOK reports that the pre-send status read succeeded. Same reason:
	// a failed read defaulting to "was not active" would turn a
	// continuously-busy agent into a fake not-active-to-active transition.
	statusOK bool
	// queueHint reports that the composer already showed Claude's
	// queued-messages hint before the send (earlier queued input, ASK-95).
	// A hint that was already there acknowledges nothing about this send.
	queueHint bool
}

// captureArrivalBaseline snapshots the pane and status before a send. Each
// signal records whether it was actually observed; a signal without a valid
// baseline is disabled, never guessed.
//
// The capture is taken even for a message too short to yield a token: the
// observer still needs the pre-send frame for the paste-marker delta.
func captureArrivalBaseline(target sendRetryTarget, message string) sendArrivalBaseline {
	base := sendArrivalBaseline{}
	if raw, err := target.CapturePaneFresh(); err == nil {
		base.raw, base.paneOK = raw, true
		base.occurrences, base.pasteMarkers, _ = paneArrivalCounts(raw, message)
		base.queueHint = claudeQueueAcknowledged(tmux.StripANSI(raw))
	}
	if status, err := target.GetStatus(); err == nil {
		base.wasActive, base.statusOK = status == "active", true
	}
	return base
}

// newSendObserver starts the send.Observer for one send over the pre-send
// baseline. It is the one reader of the pane for the end-of-budget verdict
// (issue #1793): every frame the verification loops capture is fed to it.
func newSendObserver(tool, message string, baseline sendArrivalBaseline, claudeLike, hookBusyBeforeSend bool) *send.Observer {
	obs := send.NewObserver(tool, message, send.PaneCapture{Raw: baseline.raw, OK: baseline.paneOK})
	obs.ClaudeLike = claudeLike
	obs.CodexLike = session.IsCodexCompatible(tool)
	obs.BusyBeforeSend = hookBusyBeforeSend
	return obs
}

// verificationWindow is the wording for how long a submission signal was
// awaited: checks × delay, e.g. "6s".
func verificationWindow(checks int, delay time.Duration) string {
	return (time.Duration(checks) * delay).Round(100 * time.Millisecond).String()
}

// verifyContentArrival confirms that message reached the target pane, for
// tools whose TUI exposes no Claude-shaped submit signal (issue #1793).
//
// Evidence is a TRANSITION from the pre-send baseline, never a snapshot:
// either a new copy of the body appearing in the pane, or the agent going
// active when it was not active before the send — an idle agent that starts
// working necessarily received what it started working on. An agent that was
// already busy stays busy regardless, so that case proves nothing and is not
// accepted.
//
// The two signals are not equal in strength, and the result says which one
// was found. An idle agent going active is attributable to this send, so that
// is deliverySubmitted. The body appearing is only deliveryTyped: bytes in a
// composer are not an accepted turn — Enter can still have been swallowed,
// which is the failure #1413 and #1793 are both about.
//
// The pane comparison is whitespace-insensitive because a pane wraps long
// lines at its width and capture-pane returns those wraps as newlines, so a
// byte-exact search for a 64-character token fails on any message wider than
// the remaining columns. Stripping whitespace from both sides restores the
// contiguity the terminal broke.
//
// A signal whose pre-send baseline could not be read is switched OFF, not
// defaulted: without a baseline there is no transition to measure, and
// guessing one is how a failed capture would quietly become fake evidence.
//
// Issue #1978: tools on this path (codex, gemini) emit hook status too. A body
// that newly appears as the hook flips from not-busy to busy is the target
// taking it up; hookBusyBeforeSend is the pre-send reading of
// opts.targetBusyByHook. A composer paste marker never feeds that verdict.
func verifyContentArrival(target sendRetryTarget, message string, opts sendRetryOptions, baseline sendArrivalBaseline, hookBusyBeforeSend bool) (string, error) {
	// Whether an unverified outcome is a failure depends on the longest LINE,
	// not on the total payload. Canonical buffering is per line — that is the
	// whole finding this fix rests on — so a 20 KB body of 80-byte lines is
	// as deliverable as a one-liner, and failing it for its total size would
	// contradict the transport in the same commit.
	//
	// The comparison is against the pane's own capacity where that can be
	// measured, and only against the universal floor when it cannot. The
	// floor is what EVERY pane can take, not what THIS pane can take: a
	// raw-mode pane has no line limit at all, so judging it by the floor
	// would condemn perfectly deliverable sends.
	longestLine := longestMessageLineBytes(message)
	riskyLine := false
	if longestLine > arrivalSafeLineBytes {
		riskyLine = longestLine > maxDeliverableLineBytes(target)
	}

	token := collapseWhitespace(messageDeliveryToken(message))
	if token == "" {
		// Nothing distinctive enough to look for. Verification is impossible
		// rather than failed — but "impossible" must not become an exit 0 for
		// a payload with a line big enough to be silently eaten, which would
		// leave the reported bug wide open through the token-less door.
		if riskyLine {
			return deliveryNoEvidence, fmt.Errorf(
				"send could not be verified: this message has a %d-byte line, long enough that a "+
					"canonical-mode reader can discard it along with the submitting Enter, and it carries no "+
					"content distinctive enough to look for in the pane (issue #1793). Refusing to report "+
					"success for a send nothing can confirm",
				longestLine)
		}
		return deliveryUnverified, nil
	}

	checks := opts.verificationChecks(false)
	retryCodexSubmit := opts.tool == "codex" && os.Getenv("AGENTDECK_CODEX_SUBMIT_RETRY") == "1"
	attrib := send.EnterAttribution{Message: message}

	// obs is the reader of record for the verdict (issue #1793): it sees
	// every frame this loop captures and decides what an exhausted budget
	// means. For a shell-like tool it also supplies the only confirmation
	// such a tool has: the sent line consumed, new output or a fresh prompt
	// below it (send.ShellProgressed).
	obs := newSendObserver(opts.tool, message, baseline, false, hookBusyBeforeSend)
	sawBody := false
	lastContent := ""
	for i := 0; i < checks; i++ {
		// Turn advancement in the harness's own transcript is authoritative.
		if opts.turnAdvanced != nil && opts.turnAdvanced() {
			return deliverySubmitted, nil
		}
		// Strongest pane signal: an idle agent that starts working received
		// what it started working on, which is submission, not just arrival.
		if baseline.statusOK && !baseline.wasActive {
			if status, err := target.GetStatus(); err == nil && status == "active" {
				return deliverySubmitted, nil
			}
		}
		if baseline.paneOK {
			raw, captureErr := target.CapturePaneFresh()
			obs.Observe(send.CaptureOutcome(raw, captureErr))
			if retryCodexSubmit && captureErr == nil && i < checks-1 &&
				send.ComposerHoldsUnsentMessage(raw, message, tmux.StripANSI) {
				guard := &promptGuardTarget{sendRetryTarget: target, tool: "codex"}
				if guard.check(raw, nil) == nil {
					attrib.NudgeEnter(guard, send.Captured(raw), tmux.StripANSI)
				}
			}
			if obs.TurnStarted() {
				return deliverySubmitted, nil
			}
			if n, markers, ok := paneArrivalCounts(raw, message); captureErr == nil && ok {
				content := tmux.StripANSI(raw)
				lastContent = content
				if n > baseline.occurrences {
					if opts.tool == "pi" && piComposerEmpty(content, message) {
						return deliverySubmitted, nil
					}
					// Issue #1978: the hook edge is the harness acknowledging
					// the submission (codex/gemini write "running" from their
					// prompt-submit hooks). A target that was NOT busy before
					// the send, reads busy now, and is not holding the body in
					// its composer took this message up. A target that was
					// already busy can only be inferred queued, and this path
					// has no queue acknowledgement to read, so it keeps the
					// #1793 verdict below.
					if !hookBusyBeforeSend && !send.HasUnsentComposerPrompt(content, message) && opts.hookBusyNow() {
						return deliverySubmitted, nil
					}
					// Keep polling: the body is in, but the turn may still
					// start within the budget and upgrade this to submitted.
					sawBody = true
				}
				// A paste marker the COMPOSER did not hold before the send is
				// the collapsed rendering of this send's own framed body
				// (issue #1855) — codex-style composers render agent-deck's
				// multi-line paste as "[Pasted text …]" too, so the verbatim
				// token may never become visible. Same evidentiary strength
				// as the body itself: bytes reached the composer, and nothing
				// about the agent accepting them.
				//
				// Measured as `markers > baseline.pasteMarkers`, the same
				// delta idiom as the body count above and for the same
				// reason. A boolean here would be wrong twice over: a
				// submitted paste leaves its marker on screen, so the
				// baseline arms permanently after the first multi-line send
				// to the pane, and a marker sitting in the TRANSCRIPT (the
				// shape of a send that SUCCEEDED) is not a composer holding
				// unsent bytes.
				if markers > baseline.pasteMarkers {
					sawBody = true
				}
			}
		}
		if i < checks-1 {
			time.Sleep(opts.checkDelay)
		}
	}

	if sawBody {
		obs.NoteBodyArrived()
		// The bytes demonstrably reached the pane. What that means is the
		// observer's call (issue #1793): a composer still holding the body
		// after Enter is a failure (the reported bug: text sitting unsent
		// must never be exit 0); a gone pane is a failure; a shell whose
		// prompt moved on is confirmed; and arrival with no signal either
		// way is delivered, confirmation unknown — exit 0, never "NOT
		// delivered" on the absence of a signal this tool may not even have.
		v := obs.Verdict(checks, verificationWindow(checks, opts.checkDelay))
		switch v.Outcome {
		case send.OutcomeConfirmed:
			return deliverySubmitted, nil
		case send.OutcomeFailed:
			return deliveryFailure(v, checks)
		}
		if opts.tool == "pi" && piComposerHoldsMessage(lastContent, message) {
			// Pi's editor is border-framed rather than glyph-led, so the
			// generic composer check cannot see it holding the body.
			return composerHoldsFailure(checks)
		}
		return deliveryDelivered, nil
	}

	if riskyLine {
		return deliveryNoEvidence, fmt.Errorf(
			"send could not be confirmed: a message with a %d-byte line never appeared in the pane and the "+
				"agent showed no new activity after %d checks (issue #1793). A line that long is discarded "+
				"outright — together with the submitting Enter — by a canonical-mode reader, so this is "+
				"reported as a failure rather than as an unverified success",
			longestLine, checks)
	}
	return deliveryUnverified, nil
}

// longestMessageLineBytes is the length of the longest line of message.
// Mirrors the quantity the tmux transport measures, because the terminal
// limit this whole fix is about is per line, not per payload. Both \n and \r
// end a line: with ICRNL set (the tty default) an incoming CR becomes NL
// before the line discipline sees it, so counting only \n would read a
// CR-delimited body as one enormous line.
func longestMessageLineBytes(message string) int {
	longest := 0
	for _, line := range strings.FieldsFunc(message, func(r rune) bool {
		return r == '\n' || r == '\r'
	}) {
		if len(line) > longest {
			longest = len(line)
		}
	}
	return longest
}

// paneLineCapacityReporter is implemented by *tmux.Session. It is an optional
// capability, discovered by type assertion, so the interface sendRetryTarget
// stays small and existing fakes keep working: a target that cannot report a
// capacity simply falls back to the universal floor.
type paneLineCapacityReporter interface {
	PaneLineCapacity() (int, bool)
}

// maxDeliverableLineBytes returns the longest single line this target can
// accept. It prefers the pane's DETECTED capacity — a raw-mode pane has no
// line limit and a Linux canonical pane holds four times what the floor
// assumes — and only falls back to arrivalSafeLineBytes when the pane cannot
// be probed. Without this, a 2000-byte line that delivers perfectly to any
// raw-mode agent TUI would be reported as lost purely because 2000 > 1023.
//
// Only consulted when a line already exceeds the floor, so ordinary sends
// never pay for the probe.
func maxDeliverableLineBytes(target sendRetryTarget) int {
	if reporter, ok := target.(paneLineCapacityReporter); ok {
		if capacity, known := reporter.PaneLineCapacity(); known && capacity > 0 {
			return capacity
		}
	}
	return arrivalSafeLineBytes
}

// claudeQueueAcknowledged reports whether the captured pane's COMPOSER — the
// block between the last two divider lines, parsed the same way the unsent
// prompt checks parse it — holds exactly Claude Code's queued-messages
// placeholder. That element is the harness's own acknowledgement that a
// message is queued, and the only thing the queued verdict is allowed to
// rest on (issue #1978). On a second send the hint may already be on screen
// from earlier queued input (ASK-95), so it never stands alone: the queued
// verdict also needs a new copy of the body and a composer that does not hold
// our message. The words appearing anywhere else in the pane (a
// prompt or an assistant reply that mentions queued messages) do not count,
// and neither does a pane without a divider-framed composer.
func claudeQueueAcknowledged(content string) bool {
	lines := strings.Split(content, "\n")
	prev, last := send.ComposerBounds(lines)
	if prev < 0 || prev+1 >= last {
		return false
	}
	body, ok := send.ParsePromptFromComposerBlock(lines[prev+1 : last])
	return ok && strings.EqualFold(body, claudeQueuePlaceholder)
}

// paneArrivalCounts reports both arrival signals for one pane frame: how
// many times the message's distinctive token is visible in the pane, and how
// many "[Pasted text …]" collapse markers the COMPOSER holds. ok is false
// when the message carries no usable token, so a short body can never
// register as movement. Both counts are raw observations; the caller
// compares them to its baseline.
//
// The two signals are scoped differently on purpose. The body token is looked
// for across the WHOLE pane, because a body that scrolled out of the composer
// into the transcript still arrived. The marker is scoped to the COMPOSER
// (send.ComposerPasteMarkerCount), because a marker in the TRANSCRIPT is the
// ordinary trace of a SUCCESSFUL multi-line send: reading it as this send's
// unsent bytes turns a delivered message into a failure, and a false negative
// is the input to the double-delivery class (#876). Only a composer holding
// one more marker than before is unsubmitted payload.
func paneArrivalCounts(raw, message string) (int, int, bool) {
	token := collapseWhitespace(messageDeliveryToken(message))
	if token == "" {
		return 0, 0, false
	}
	content := tmux.StripANSI(raw)
	return strings.Count(collapseWhitespace(content), token),
		send.ComposerPasteMarkerCount(raw, tmux.StripANSI), true
}

// piComposerEmpty recognizes Pi's editor between its final two horizontal
// borders. Once this send's body is in the pane and that editor is empty, Enter
// was accepted; an unsent message would still occupy the editor.
func piComposerEmpty(content, message string) bool {
	// User-provided rules can impersonate the editor's top border. A pane
	// capture cannot distinguish those bytes from Pi's own empty editor, so
	// these messages require an activity transition instead of visual inference.
	if strings.Contains(tmux.StripANSI(message), strings.Repeat("─", 20)) {
		return false
	}
	lines := strings.Split(content, "\n")
	borders := make([]int, 0, 2)
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Count(line, "\u2500") >= 20 && strings.Trim(line, "\u2500") == "" {
			borders = append(borders, i)
		}
	}
	if len(borders) < 2 {
		return false
	}
	top, bottom := borders[len(borders)-2], borders[len(borders)-1]
	return strings.TrimSpace(strings.Join(lines[top+1:bottom], "\n")) == ""
}

// piComposerHoldsMessage is piComposerEmpty's positive counterpart: Pi's
// editor (between its final two borders) still shows this send's body, which
// is the text-sitting-unsent state issue #1793 is about. A message that
// contains a border-like line is undecidable and reports false (unknown).
func piComposerHoldsMessage(content, message string) bool {
	if strings.Contains(tmux.StripANSI(message), strings.Repeat("─", 20)) {
		return false
	}
	token := collapseWhitespace(messageDeliveryToken(message))
	if token == "" {
		return false
	}
	lines := strings.Split(content, "\n")
	borders := make([]int, 0, 2)
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Count(line, "\u2500") >= 20 && strings.Trim(line, "\u2500") == "" {
			borders = append(borders, i)
		}
	}
	if len(borders) < 2 {
		return false
	}
	top, bottom := borders[len(borders)-2], borders[len(borders)-1]
	return strings.Contains(collapseWhitespace(strings.Join(lines[top+1:bottom], "\n")), token)
}

// collapseWhitespace removes every whitespace byte, so a comparison survives
// the line wrapping a terminal applies to long content.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func messageDeliveryToken(message string) string {
	const minTokenLen = 12
	const maxTokenLen = 64
	trimmed := strings.TrimSpace(message)
	if len(trimmed) < minTokenLen {
		return ""
	}
	if len(trimmed) > maxTokenLen {
		trimmed = trimmed[:maxTokenLen]
	}
	return trimmed
}

// shouldGateSlashRegistration reports whether a send needs to wait for
// Claude's slash-command parser to finish registering before relaying.
//
// Issue #966: after `session restart`, Claude reaches "waiting" with the
// composer prompt visible *before* its slash-command router is armed. A
// bare `/foo` sent in that window is silently dropped. The gate fires only
// for the trigger condition — Claude tool plus a bare slash payload — so
// conversational text and non-Claude tools don't pay the latency.
func shouldGateSlashRegistration(tool, message string) bool {
	if tool != "claude" {
		return false
	}
	trimmed := strings.TrimLeft(message, " \t")
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "/")
}

// waitForSlashCommandReady polls the pane until the composer prompt has been
// continuously visible for the slash-registration settle window, then returns.
// Callers must have already passed send.WaitForAgentReady; this is an additional
// hold-back specifically for the #966 race.
//
// The function probes (rather than blind-sleeps) so a long-already-ready
// Claude returns near-immediately on retries, while a freshly restarted
// Claude pays the full settle window.
func waitForSlashCommandReady(target send.AgentReadyChecker, tool string, timeout time.Duration) error {
	const pollInterval = 100 * time.Millisecond
	// Eight stable composer observations (~800ms) is the empirical floor
	// for Claude to finish registering its slash-command parser after the
	// composer first renders. Bumping this is a no-op for healthy sessions
	// (we early-return as soon as stability is met); it only delays the
	// first send after a restart.
	const minStableHits = 8

	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)

	stable := 0
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		rawContent, err := target.CapturePaneFresh()
		if err != nil {
			stable = 0
			continue
		}
		content := tmux.StripANSI(rawContent)
		if !send.HasCurrentComposerPrompt(content) {
			stable = 0
			continue
		}
		stable++
		if stable >= minStableHits {
			return nil
		}
	}

	return fmt.Errorf("slash-command registration not ready after %s (tool=%s)", timeout, tool)
}

// statusChecker abstracts tmux status polling so waitForCompletion is testable.
type statusChecker interface {
	GetStatus() (string, error)
}
