package send

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ErrComposerNotOurMessage is returned (wrapped) by the TypedComposerCheck
// pre-Enter check when, after agent-deck typed its message, the composer
// holds text that is not the typed message. Enter is withheld: pressing it
// would submit someone else's text. This is a hard stop.
var ErrComposerNotOurMessage = errors.New("composer does not hold the typed message")

// ErrSubmitDeferred is returned (wrapped) by the TypedComposerCheck pre-Enter
// check when, after agent-deck typed its message, the composer still paints a
// Claude prompt suggestion (or the "Try ..." placeholder, drawn by the same
// renderer) because the repaint is slow. It is NOT a failure: the message was
// typed and only the Enter is held back. Callers continue with
// SubmitWhenTyped and their verify loops, which press Enter once the composer
// holds the typed message and never while a suggestion is shown.
var ErrSubmitDeferred = errors.New("submit deferred: composer still shows a prompt suggestion")

// typedCheckPolls and typedCheckInterval bound how long the pre-Enter check
// waits for the composer to repaint with the typed text before deferring the
// submit. The first capture normally shows the typed text already.
const (
	typedCheckPolls    = 10
	typedCheckInterval = 50 * time.Millisecond
)

// deferredSubmitPolls and deferredSubmitInterval bound SubmitWhenTyped for a
// deferred submit: up to 6s for a slow (cold start) repaint, on top of the
// pre-Enter check's own wait.
const (
	deferredSubmitPolls    = 120
	deferredSubmitInterval = 50 * time.Millisecond
)

// typedPrefixLen is how much of the message's start a composer body must
// share to count as our typed text when the stricter matches miss (a wrapped
// or reflowed body).
const typedPrefixLen = 16

// TypedComposerCheck builds the pre-Enter check for a send that has just
// TYPED message into a Claude composer: Enter is pressed only once the
// composer holds the typed message, not a prompt suggestion and not other
// text. A suggestion still painted after the bounded wait defers the submit
// (ErrSubmitDeferred); other text in the composer is a hard stop
// (ErrComposerNotOurMessage). Typing replaces a suggestion, so a normal send
// passes on the first capture; the check exists for the frame where the
// suggestion is still painted (the typed text has not landed) and for a
// composer that changed under us.
//
// capture re-reads the pane while the check waits for a repaint. inner, when
// non-nil, runs after this check passes (for example PasteTruncationCheck);
// its verdict is returned unchanged. The returned function has
// tmux.PostPasteCheck's shape.
//
// A failed capture is "unknown, not unsafe", as in PasteTruncationCheck: the
// text was typed and the send proceeds (the callers' verify loops still
// gate every later bare Enter). An EMPTY visible composer proceeds as well:
// an Enter there submits nothing foreign.
func TypedComposerCheck(capture func() (string, error), message string, strip func(string) string, inner func(string, error) (bool, error)) func(string, error) (bool, error) {
	strip = orIdentity(strip)
	return func(pane string, captureErr error) (bool, error) {
		for poll := 0; captureErr == nil; poll++ {
			problem := typedComposerProblem(pane, message, strip)
			if problem == nil {
				break
			}
			if poll >= typedCheckPolls || capture == nil {
				return false, problem
			}
			time.Sleep(typedCheckInterval)
			pane, captureErr = capture()
		}
		if inner != nil {
			return inner(pane, captureErr)
		}
		return true, nil
	}
}

// errComposerHoldsOtherText is TypedComposerCheck's hard stop.
var errComposerHoldsOtherText = fmt.Errorf("%w (composer holds other text); withholding Enter", ErrComposerNotOurMessage)

// typedComposerProblem returns ErrSubmitDeferred while the composer in raw
// still shows a suggestion, errComposerHoldsOtherText when it holds text that
// is not the typed message, and nil when it holds the message (or is empty,
// or not introspectable).
func typedComposerProblem(raw, message string, strip func(string) string) error {
	if ComposerBodyIsSuggestion(raw) {
		return ErrSubmitDeferred
	}
	draft, visible := ComposerDraft(raw, strip)
	if !visible || draft == "" || HasUnsentPastedPrompt(draft) || HasUnsentComposerPrompt(strip(raw), message) {
		return nil
	}
	prefix := draft[:min(typedPrefixLen, len(draft))]
	if strings.HasPrefix(NormalizePromptText(message), prefix) {
		return nil
	}
	return errComposerHoldsOtherText
}

// SubmitWhenTyped finishes a deferred submit (ErrSubmitDeferred): it re-reads
// the pane up to polls times, interval apart, and presses Enter once, through
// the NudgeEnter gate, as soon as the composer holds message. It never
// presses while a suggestion is shown or while the composer holds anything
// else; it reports whether Enter was pressed. If the typed text never shows
// up, nothing is pressed and the caller's verify loop reports the outcome.
//
// inner is the pre-Enter check TypedComposerCheck wrapped (for example
// PasteTruncationCheck, issue #2079; nil for none). It runs on the frame
// that would be submitted, so a deferred submit keeps the guard a direct one
// has: a refusal is returned unchanged and nothing is pressed. A composer
// holding a paste marker that inner accepts is our message collapsed behind
// it; that Enter is left to the caller's verify loop, whose gate knows the
// marker's provenance.
func SubmitWhenTyped(p EnterPresser, capture func() (string, error), message string, strip func(string) string, inner func(string, error) (bool, error), polls int, interval time.Duration) (bool, error) {
	if p == nil || capture == nil {
		return false, nil
	}
	attrib := EnterAttribution{Message: message}
	for poll := 0; poll < polls; poll++ {
		if poll > 0 {
			time.Sleep(interval)
		}
		raw, err := capture()
		if err != nil {
			continue
		}
		holdsMarker := ComposerHoldsPasteMarker(raw, strip)
		if !holdsMarker && !ComposerHoldsUnsentMessage(raw, message, strip) {
			continue
		}
		if inner != nil {
			if ok, err := inner(raw, nil); !ok {
				return false, err
			}
		}
		if holdsMarker {
			return false, nil
		}
		return attrib.NudgeEnter(p, Captured(raw), strip), nil
	}
	return false, nil
}

// TypedSender is the transport TypeAndSubmit drives. *tmux.Session and the
// CLI's send targets satisfy it.
type TypedSender interface {
	EnterPresser
	SendKeysAndEnter(keys string) error
	SendKeysAndEnterChecked(keys string, capture func() (string, error), check tmux.PostPasteCheck) error
	CapturePaneFresh() (string, error)
}

// TypeAndSubmit types message and presses Enter: the initial submit shared
// by `session send`, the launch path and the launch prompt retry.
//
// expectedBreaks > 0 (ExpectedPasteMarkerLineBreaks; 0 for a single-line
// message or a non-Claude target) adds PasteTruncationCheck: a paste cut
// short in transit withholds Enter (issue #2079). suggestionShown (the
// pre-send capture showed a Claude prompt suggestion) puts
// TypedComposerCheck in front of it, so Enter is pressed only once the
// composer holds the typed message. A slow repaint that still shows the
// suggestion defers the Enter instead of failing: SubmitWhenTyped presses it
// once the typed message is painted, still running the paste check on that
// frame. With neither check the send is a plain SendKeysAndEnter.
func TypeAndSubmit(t TypedSender, message string, expectedBreaks int, suggestionShown bool) error {
	_, err := TypeAndSubmitReport(t, message, expectedBreaks, suggestionShown, false, nil)
	return err
}

// SubmitReport is what TypeAndSubmitReport observed while typing, for the
// caller's later report about the input box.
type SubmitReport struct {
	// OwnedChip is the exact collapsed paste label ("[Pasted text #1 +3
	// lines]", #N included) the input box showed on THIS send's post-paste,
	// pre-Enter frame, read through the full-body reader, when the box was
	// empty before the send and that frame held exactly one chip. It is used
	// only to mark a chip-only leftover as Verified (ObserveComposer); ""
	// when none is known (no chip, no reader, or no empty-before
	// provenance). It never authorises a key press.
	OwnedChip string
}

// TypeAndSubmitReport is TypeAndSubmit for a caller that can say whether the
// input box was empty immediately before the send (composerEmptyBefore: a
// visible composer with an empty full body; a prompt suggestion counts as
// empty) and how to read the target's whole input box (read; nil for a tool
// without a ComposerFullBodyReader).
//
// It never deletes or submits anything beyond what TypeAndSubmit does: when
// a pre-Enter check refuses Enter after the message was typed
// (ErrComposerNotOurMessage or ErrPasteTruncated), or the whole message was
// typed and passed the pre-Enter check but the Enter itself could not be
// sent, the typed text stays where it is. The error is then a
// *NotSubmittedError whose Observed is ObserveComposer's one-capture report
// of what the input box holds (Verified forced false after a failed Enter,
// which may have half-landed). A plain SendKeysAndEnter failure, like any
// other transport error (send-keys failed, ErrCanonicalLineOverflow), is
// returned unchanged: nothing, or an unknown amount, was typed, and the
// caller decides how to observe it.
func TypeAndSubmitReport(t TypedSender, message string, expectedBreaks int, suggestionShown, composerEmptyBefore bool, read ComposerFullBodyReader) (SubmitReport, error) {
	var rep SubmitReport
	noteChip := func(pane string, captureErr error) {
		if rep.OwnedChip != "" || captureErr != nil || read == nil || !composerEmptyBefore {
			return
		}
		if body, ok := read(pane, tmux.StripANSI); ok {
			if d := NormalizePromptText(body); pasteChipRe.MatchString(d) {
				rep.OwnedChip = d
			}
		}
	}
	typedFully, err := typeAndSubmit(t, message, expectedBreaks, suggestionShown, noteChip)
	switch {
	case err == nil:
		return rep, nil
	case errors.Is(err, ErrComposerNotOurMessage) || errors.Is(err, ErrPasteTruncated):
		return rep, &NotSubmittedError{Cause: err, Observed: ObserveComposer(t, message, rep.OwnedChip, read, tmux.StripANSI, composerEmptyBefore)}
	case typedFully:
		o := ObserveComposer(t, message, rep.OwnedChip, read, tmux.StripANSI, composerEmptyBefore)
		if o.State == ComposerLeft {
			o.Verified = false
			o.Reason = ObserveReasonEnterFailed
		}
		return rep, &NotSubmittedError{Cause: err, Observed: o}
	}
	return rep, err
}

// typeAndSubmit types message and submits it behind the pre-Enter checks.
// typedFully reports that a pre-Enter check ran and passed on the typed
// message, so a later error came from pressing Enter, not from typing.
// noteFrame (nil for none) sees every post-paste, pre-Enter frame a check
// is handed, in order, before the check judges it.
func typeAndSubmit(t TypedSender, message string, expectedBreaks int, suggestionShown bool, noteFrame func(string, error)) (typedFully bool, err error) {
	if noteFrame == nil {
		noteFrame = func(string, error) {}
	}
	var pasteCheck func(string, error) (bool, error)
	if expectedBreaks > 0 {
		inner := PasteTruncationCheck(expectedBreaks)
		pasteCheck = func(pane string, captureErr error) (bool, error) {
			noteFrame(pane, captureErr)
			return inner(pane, captureErr)
		}
	}
	check := pasteCheck
	if suggestionShown {
		check = TypedComposerCheck(t.CapturePaneFresh, message, tmux.StripANSI, pasteCheck)
	}
	if check == nil {
		return false, t.SendKeysAndEnter(message)
	}
	noted := func(pane string, captureErr error) (bool, error) {
		noteFrame(pane, captureErr)
		ok, checkErr := check(pane, captureErr)
		if ok {
			typedFully = true
		}
		return ok, checkErr
	}
	err = t.SendKeysAndEnterChecked(message, t.CapturePaneFresh, noted)
	if errors.Is(err, ErrSubmitDeferred) {
		typedFully = false
		_, err = SubmitWhenTyped(t, t.CapturePaneFresh, message, tmux.StripANSI, pasteCheck, deferredSubmitPolls, deferredSubmitInterval)
	}
	return typedFully, err
}

// ProbeComposerBeforeSend reads one pane capture taken immediately before an
// initial submit and returns the two facts TypeAndSubmit and its verify loop
// need from it: pasteMarkerFree (no "[Pasted text …]" marker, so a marker seen
// later is our own collapsed payload, issue #1777) and suggestionShown (a
// Claude prompt suggestion is painted, so Enter waits until our typed text
// replaced it). claude says the target is a Claude composer; the suggestion
// rule applies to nothing else. A capture failure reports both false.
func ProbeComposerBeforeSend(capture func() (string, error), claude bool) (pasteMarkerFree, suggestionShown bool) {
	p := ProbeComposer(capture, claude)
	return p.PasteMarkerFree, p.SuggestionShown
}

// ComposerProbe is ProbeComposer's reading of the capture taken immediately
// before an initial submit.
type ComposerProbe struct {
	// PasteMarkerFree: no "[Pasted text …]" marker (issue #1777).
	PasteMarkerFree bool
	// SuggestionShown: a Claude prompt suggestion is painted.
	SuggestionShown bool
	// ComposerEmpty: a Claude composer is visible and its whole body
	// (ClaudeComposerFullBody) is empty; a suggestion counts as empty. It is
	// TypeAndSubmitReport's composerEmptyBefore. Always false for a
	// non-Claude target.
	ComposerEmpty bool
	// FullBody is the reader TypeAndSubmitReport reports the input box
	// through: ClaudeComposerFullBody for a Claude target, nil otherwise.
	FullBody ComposerFullBodyReader
}

// ProbeComposer is ProbeComposerBeforeSend plus the composer-empty
// provenance TypeAndSubmitReport needs to call a leftover verified.
// A capture failure reports every fact false.
func ProbeComposer(capture func() (string, error), claude bool) ComposerProbe {
	p := ComposerProbe{}
	if claude {
		p.FullBody = ClaudeComposerFullBody
	}
	raw, err := capture()
	if err != nil {
		return p
	}
	p.PasteMarkerFree = !ComposerHoldsPasteMarker(raw, tmux.StripANSI)
	if claude {
		p.SuggestionShown = ComposerBodyIsSuggestion(raw)
		body, ok := ClaudeComposerFullBody(raw, tmux.StripANSI)
		p.ComposerEmpty = ok && FullBodyEmpty(body)
	}
	return p
}
