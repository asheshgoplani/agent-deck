package send

import (
	"errors"
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Delivery has three outcomes, and the exit code follows the evidence, never
// the absence of it (issue #1793, the false-negative half: #1978, #2071).
//
//   - Confirmed: positive evidence the harness took the message (a turn
//     started, the hook edge flipped, the composer was seen holding the body
//     and then cleared, or — for a shell — the sent line was consumed and new
//     output or a fresh prompt appeared). Exit 0.
//   - DeliveredUnconfirmed: the text reached the pane and Enter was sent, but
//     this tool offers no submission signal, or the signal did not arrive in
//     the window. Exit 0, and the caller says so out loud. It is never "NOT
//     delivered": nothing showed the message failing.
//   - Failed: positive evidence it did not go through — the pane is gone, the
//     body is still sitting in the composer after Enter with no turn started,
//     or an interactive menu swallowed the keystrokes. Exit 1 with the
//     evidence line.
type Outcome string

const (
	OutcomeConfirmed            Outcome = "confirmed"
	OutcomeDeliveredUnconfirmed Outcome = "delivered"
	OutcomeFailed               Outcome = "failed"
	OutcomeNoEvidence           Outcome = "no_evidence"
)

// Confirmation is the `--json` "confirmation" value: what the tool could
// establish about the harness taking the message up.
const (
	ConfirmationConfirmed = "confirmed"
	ConfirmationUnknown   = "unknown"
	ConfirmationFailed    = "failed"
)

// FailureKind names the positive evidence a Failed verdict rests on.
type FailureKind string

const (
	FailurePaneGone        FailureKind = "pane_gone"
	FailureComposerHolds   FailureKind = "composer_holds_body"
	FailureInteractiveMenu FailureKind = "interactive_menu"
)

// DeliveryEvidence is everything the post-send observation window established.
// Each field is positive evidence of one thing; a field left false means "not
// observed", never "observed to be false".
type DeliveryEvidence struct {
	// Tool is the target's tool name, for the verdict's wording.
	Tool string
	// TurnStarted is any positive submission signal: turn advancement in the
	// harness transcript, an idle-to-active transition, the hook edge, the
	// composer held-then-cleared, or shell progress (see ShellProgressed).
	TurnStarted bool
	// BodyArrived is a NEW copy of the body (or a new composer paste marker)
	// observed in the pane after the send.
	BodyArrived bool
	// PaneGone is the pane disappearing during the observation window.
	PaneGone bool
	// ComposerHoldsBody is the FINAL frame still showing the message in the
	// composer after every bounded Enter retry.
	ComposerHoldsBody bool
	// MenuOpen is the FINAL frame showing an open AskUserQuestion picker or
	// permission dialog (internal/tmux substate) with the message not taken.
	MenuOpen bool
	// Checks and Window describe the observation budget, for the wording.
	Checks int
	Window string
}

// Verdict is the classification of one send.
type Verdict struct {
	Outcome Outcome
	// Confirmation is the --json value (ConfirmationConfirmed / Unknown / Failed).
	Confirmation string
	// Failure names the positive evidence behind a Failed verdict.
	Failure FailureKind
	// Message is the human line: the evidence, not a guess.
	Message string
}

// Classify turns the evidence into a verdict. Positive evidence of failure
// outranks arrival, arrival outranks silence, and a started turn outranks
// everything: once the harness demonstrably took the message, a later menu or
// a later dead pane is the turn's business, not the delivery's.
func Classify(ev DeliveryEvidence) Verdict {
	if ev.TurnStarted {
		return Verdict{Outcome: OutcomeConfirmed, Confirmation: ConfirmationConfirmed}
	}
	if ev.PaneGone {
		return Verdict{
			Outcome: OutcomeFailed, Confirmation: ConfirmationFailed, Failure: FailurePaneGone,
			Message: "target pane is gone; message not submitted",
		}
	}
	if ev.MenuOpen {
		return Verdict{
			Outcome: OutcomeFailed, Confirmation: ConfirmationFailed, Failure: FailureInteractiveMenu,
			Message: "AskUserQuestion/permission menu open; message not submitted",
		}
	}
	if ev.ComposerHoldsBody {
		return Verdict{
			Outcome: OutcomeFailed, Confirmation: ConfirmationFailed, Failure: FailureComposerHolds,
			Message: fmt.Sprintf("the composer still holds the message after %d Enter checks; message not submitted", ev.Checks),
		}
	}
	if ev.BodyArrived {
		return Verdict{
			Outcome: OutcomeDeliveredUnconfirmed, Confirmation: ConfirmationUnknown,
			Message: UnconfirmedMessage(ev.Tool, ev.Window),
		}
	}
	return Verdict{Outcome: OutcomeNoEvidence, Confirmation: ConfirmationUnknown}
}

// UnconfirmedMessage is the wording for a delivered-but-unconfirmed send: a
// tool with no submission signal is told apart from one whose signal simply
// did not arrive in the window.
func UnconfirmedMessage(tool, window string) string {
	if !ToolHasSubmissionSignal(tool) {
		return fmt.Sprintf("delivered; submission not confirmable for tool %s", toolLabel(tool))
	}
	if window == "" {
		return "delivered; submission not confirmed"
	}
	return fmt.Sprintf("delivered; submission not confirmed within %s", window)
}

func toolLabel(tool string) string {
	if strings.TrimSpace(tool) == "" {
		return "unknown"
	}
	return tool
}

// shellLikeTools have no composer and no hooks: a line typed at their prompt
// gives no submission signal beyond the pane itself, and the pane itself is
// their confirmation (ShellProgressed).
var shellLikeTools = map[string]bool{
	"shell": true, "sh": true, "bash": true, "zsh": true, "fish": true,
	"dash": true, "ksh": true, "nu": true, "nushell": true, "pwsh": true, "powershell": true,
}

// ToolHasSubmissionSignal reports whether tool exposes any signal that a
// message was taken up (a composer that clears, a hook edge, an activity
// transition). Shells and an unknown (empty) tool do not; for them
// "delivered" is the strongest honest claim short of ShellProgressed.
func ToolHasSubmissionSignal(tool string) bool {
	name := strings.ToLower(strings.TrimSpace(tool))
	return name != "" && !shellLikeTools[name]
}

// IsShellLikeTool reports whether tool is a plain shell (see shellLikeTools).
// An unknown tool is not a shell: its pane is not read as a shell prompt.
func IsShellLikeTool(tool string) bool {
	return shellLikeTools[strings.ToLower(strings.TrimSpace(tool))]
}

// collapse removes every run of whitespace so pane wrapping and tmux reflow
// (#2071) cannot break a substring match.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// shellAnchor is the fragment of message a shell echoes on its last input
// line: the last non-empty line, whitespace-collapsed, capped to its final 64
// bytes so a wrapped line still ends where the anchor ends.
func shellAnchor(message string) string {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		anchor := collapse(lines[i])
		if anchor == "" {
			continue
		}
		if len(anchor) > 64 {
			anchor = anchor[len(anchor)-64:]
		}
		return anchor
	}
	return ""
}

// ShellProgressed is the shell-like confirmation: the line carrying the sent
// message is no longer the last thing on screen — new output or a fresh prompt
// appeared below it. A shell that has taken a line and moved on has submitted
// it; a line still sitting at the prompt with nothing after it is unknown
// (Enter may have been swallowed, or the command may simply still be running).
//
// The pane is compared whitespace-collapsed line by line, accumulating, so a
// message wrapped over the pane width still anchors on the line where its
// last byte landed. Only the LAST occurrence counts: a repeat of an identical
// command earlier in the scrollback is followed by output too, and must not
// certify this send.
func ShellProgressed(raw, message string) bool {
	anchor := shellAnchor(message)
	if len(anchor) < 4 {
		return false
	}
	lines := strings.Split(tmux.StripANSI(raw), "\n")
	acc := ""
	lastEnd := -1
	for i, line := range lines {
		before := len(acc)
		acc += collapse(line)
		if idx := strings.LastIndex(acc, anchor); idx >= 0 && idx+len(anchor) > before {
			lastEnd = i
		}
	}
	if lastEnd < 0 {
		return false
	}
	for _, line := range lines[lastEnd+1:] {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

// MenuSwallowedMessage reports whether a Claude pane shows an open
// AskUserQuestion picker or permission dialog: keystrokes typed into a menu
// select options instead of composing a message, so a body that is not in
// the transcript and not in the composer was swallowed. Only meaningful for
// Claude-shaped panes; other tools report false.
func MenuSwallowedMessage(content string) bool {
	return tmux.NewPromptDetector("claude").ClassifySubstate(content) == tmux.SubstateInteractiveMenu
}

// PaneGone reports whether a capture error means the target pane or server
// no longer exists (as opposed to a transient timeout).
func PaneGone(err error) bool {
	return errors.Is(err, tmux.ErrCaptureGone)
}

// DeliveryToken is the content-bearing slice of message the arrival check
// looks for in the pane: the first 64 bytes of the trimmed body, or "" when
// the body is too short (under 12 bytes) to be distinctive.
func DeliveryToken(message string) string {
	const minTokenLen, maxTokenLen = 12, 64
	trimmed := strings.TrimSpace(message)
	if len(trimmed) < minTokenLen {
		return ""
	}
	if len(trimmed) > maxTokenLen {
		trimmed = trimmed[:maxTokenLen]
	}
	return trimmed
}

// Observer folds a sequence of post-send pane captures into DeliveryEvidence.
// It is the one place the pane is read for the verdict, so a recorded
// sequence of frames fully determines the outcome and can be replayed in a
// test. Positive submission signals that do not come from the pane (hook
// edges, transcript turn records, status transitions) are reported to it
// through NoteTurnStarted.
type Observer struct {
	// Tool is the target's tool name; ClaudeLike enables the Claude-shaped
	// final-frame checks (composer, interactive menu).
	Tool       string
	ClaudeLike bool
	// BusyBeforeSend is the hook-driven busy reading taken before the send.
	// A composer that held the body and then cleared is submission on an
	// idle target; on a target already mid-turn it only means the input
	// moved out of view (#1978), which is not evidence this verdict may use.
	BusyBeforeSend bool

	message string
	token   string

	baselineOK      bool
	baselineCount   int
	baselineMarkers int
	// baselineHeld: the composer already showed the body verbatim before
	// the send (a foreign draft the guard let through); then a composer
	// showing it afterwards is not this send's doing.
	baselineHeld bool

	ev      DeliveryEvidence
	last    string
	hasLast bool
	// held latches once the composer was positively observed holding the
	// body (paste marker or verbatim text at the prompt).
	held bool
}

// NewObserver starts observing message on tool. baseline is the pre-send
// capture; when it failed there is no baseline, and arrival is then "the body
// is visible" rather than "one more copy than before".
func NewObserver(tool, message string, baseline PaneCapture) *Observer {
	o := &Observer{Tool: tool, message: message, token: collapse(DeliveryToken(message))}
	if baseline.OK {
		o.baselineOK = true
		o.baselineCount, o.baselineMarkers = o.counts(baseline.Raw)
		o.baselineHeld = HasUnsentComposerPrompt(tmux.StripANSI(baseline.Raw), message)
	}
	return o
}

// counts reports the two arrival signals of one frame: token occurrences
// across the whole pane (0 when the message yields no token) and paste
// markers the composer holds.
func (o *Observer) counts(raw string) (int, int) {
	markers := ComposerPasteMarkerCount(raw, tmux.StripANSI)
	if o.token == "" {
		return 0, markers
	}
	return strings.Count(collapse(tmux.StripANSI(raw)), o.token), markers
}

// Observe records one post-send capture.
func (o *Observer) Observe(c PaneCapture) {
	if !c.OK {
		if PaneGone(c.Err) {
			o.ev.PaneGone = true
		}
		return
	}
	// The pane answered: whatever an earlier capture said, it is here now.
	o.ev.PaneGone = false
	o.last, o.hasLast = c.Raw, true
	if !o.ev.BodyArrived {
		// With a baseline, arrival is one more copy (or composer marker)
		// than before the send. Without one, only the token being visible
		// at all counts: a marker with nothing to compare against is not
		// attributable to this send. A token-less message counts 0 copies.
		n, markers := o.counts(c.Raw)
		if o.baselineOK {
			o.ev.BodyArrived = n > o.baselineCount || markers > o.baselineMarkers
		} else {
			o.ev.BodyArrived = n > 0
		}
	}
	// A shell that has taken the line and moved on has submitted it.
	if o.ev.BodyArrived && !o.ev.TurnStarted && IsShellLikeTool(o.Tool) && ShellProgressed(c.Raw, o.message) {
		o.ev.TurnStarted = true
	}
	if !o.held && o.hasComposer() && o.composerHolds(c.Raw) {
		o.held = true
		o.ev.BodyArrived = true
	}
}

// hasComposer reports whether the final-frame composer checks apply: a
// Claude-shaped pane, or a tool with a submission signal of its own. A shell
// line still at its prompt may be a command that is simply running, which is
// unknown, not failed; an unknown tool is given the same benefit.
func (o *Observer) hasComposer() bool {
	return o.ClaudeLike || ToolHasSubmissionSignal(o.Tool)
}

// composerHolds reports whether the composer in raw is holding THIS send's
// body: a paste marker the composer did not hold before the send, or the
// verbatim text at the prompt when it was not there before. A marker or
// draft that predates the send is foreign and never counts (#1855, #1777).
//
// On a non-Claude pane the marker only counts when a composer is
// introspectable: with none, the count falls back to the whole pane, where
// one more marker is arrival (the scrollback of a submitted paste), not a
// composer holding unsent bytes. A Claude pane keeps the whole-pane fallback
// the verification loop has always used for its own unsent-marker check.
func (o *Observer) composerHolds(raw string) bool {
	_, visible := ComposerDraft(raw, tmux.StripANSI)
	if (visible || o.ClaudeLike) && ComposerPasteMarkerCount(raw, tmux.StripANSI) > o.baselineMarkers {
		return true
	}
	content := tmux.StripANSI(raw)
	return !o.baselineHeld && (HasUnsentComposerPrompt(content, o.message) || composerHoldsWrappedBody(content, o.message))
}

// composerHoldsWrappedBody catches the body a tall composer wraps past the
// window CurrentComposerPrompt scans: everything from the LAST prompt-marker
// line to the bottom of the pane, whitespace-collapsed, still contains the
// whole message. An empty marker line at the bottom (a cleared composer
// below a submitted transcript copy) is the last one and holds nothing.
func composerHoldsWrappedBody(content, message string) bool {
	want := collapse(message)
	if len(want) < 16 {
		return false
	}
	lines := strings.Split(content, "\n")
	last := -1
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "❯") || strings.HasPrefix(trimmed, "›") {
			last = i
		}
	}
	if last < 0 {
		return false
	}
	return strings.Contains(collapse(strings.Join(lines[last:], "\n")), want)
}

// NoteComposerHeld records that the caller positively observed the composer
// holding this send's body (the verification loop's own unsent-marker check).
func (o *Observer) NoteComposerHeld() { o.held = true }

// ComposerHeld reports whether the composer was ever observed holding the body.
func (o *Observer) ComposerHeld() bool { return o.held }

// NoteTurnStarted records a positive submission signal from outside the pane.
func (o *Observer) NoteTurnStarted() { o.ev.TurnStarted = true }

// NoteBodyArrived records arrival established by the caller's own check.
func (o *Observer) NoteBodyArrived() { o.ev.BodyArrived = true }

// BodyArrived reports whether a new copy of the body has been observed.
func (o *Observer) BodyArrived() bool { return o.ev.BodyArrived }

// TurnStarted reports whether any positive submission signal was recorded.
func (o *Observer) TurnStarted() bool { return o.ev.TurnStarted }

// Verdict classifies what was observed. The final-frame failure checks
// (composer still holding the body, an open interactive menu) only apply to
// panes with a composer (hasComposer).
func (o *Observer) Verdict(checks int, window string) Verdict {
	ev := o.ev
	ev.Tool, ev.Checks, ev.Window = o.Tool, checks, window
	if o.hasLast && !ev.TurnStarted && o.hasComposer() {
		ev.ComposerHoldsBody = o.composerHolds(o.last)
		if o.ClaudeLike {
			ev.MenuOpen = MenuSwallowedMessage(tmux.StripANSI(o.last))
		}
		// Held-then-cleared: the harness took the body out of its composer.
		// Only on a target that was not already mid-turn (see BusyBeforeSend).
		if o.held && !ev.ComposerHoldsBody && !ev.MenuOpen && !o.BusyBeforeSend {
			ev.TurnStarted = true
		}
	}
	return Classify(ev)
}
