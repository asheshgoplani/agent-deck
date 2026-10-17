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
	var pasteCheck func(string, error) (bool, error)
	if expectedBreaks > 0 {
		pasteCheck = PasteTruncationCheck(expectedBreaks)
	}
	check := pasteCheck
	if suggestionShown {
		check = TypedComposerCheck(t.CapturePaneFresh, message, tmux.StripANSI, pasteCheck)
	}
	if check == nil {
		return t.SendKeysAndEnter(message)
	}
	err := t.SendKeysAndEnterChecked(message, t.CapturePaneFresh, check)
	if errors.Is(err, ErrSubmitDeferred) {
		_, err = SubmitWhenTyped(t, t.CapturePaneFresh, message, tmux.StripANSI, pasteCheck, deferredSubmitPolls, deferredSubmitInterval)
	}
	return err
}

// ProbeComposerBeforeSend reads one pane capture taken immediately before an
// initial submit and returns the two facts TypeAndSubmit and its verify loop
// need from it: pasteMarkerFree (no "[Pasted text …]" marker, so a marker seen
// later is our own collapsed payload, issue #1777) and suggestionShown (a
// Claude prompt suggestion is painted, so Enter waits until our typed text
// replaced it). claude says the target is a Claude composer; the suggestion
// rule applies to nothing else. A capture failure reports both false.
func ProbeComposerBeforeSend(capture func() (string, error), claude bool) (pasteMarkerFree, suggestionShown bool) {
	raw, err := capture()
	if err != nil {
		return false, false
	}
	return !ComposerHoldsPasteMarker(raw, tmux.StripANSI), claude && ComposerBodyIsSuggestion(raw)
}
