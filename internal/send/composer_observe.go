package send

import (
	"regexp"
	"strings"
)

// A send that typed its message and then could not submit it leaves that
// text in the target's input box (11-12 Nov incident: the CLI said "not
// sent" and the ~1 kB payload sat typed for 19 hours). agent-deck REPORTS
// that; it never deletes or submits anything in another session's input box.
// A missed cleanup costs a person one keypress; a wrong deletion or a double
// delivery costs their text or their trust (PR 3123, second review). The
// functions here only capture the pane; none of them sends a key.

// ComposerState is what ObserveComposer saw in the input box.
type ComposerState int

const (
	// ComposerEmpty: the input box reads empty (a prompt suggestion or
	// Claude's idle placeholder counts as empty). Nothing of this send is
	// there, but something other than agent-deck emptied it, possibly by
	// submitting it.
	ComposerEmpty ComposerState = iota
	// ComposerLeft: the input box shows text; this send's text is (or may
	// be) still typed there, unsubmitted.
	ComposerLeft
	// ComposerUnreadable: the input box could not be read (capture failed,
	// no bounded composer on screen, or no full-body reader for the tool).
	ComposerUnreadable
)

// Observation reasons (ComposerObservation.Reason).
const (
	ObserveReasonNoFullBody  = "input box not fully readable for this tool"
	ObserveReasonUnreadable  = "input box not readable"
	ObserveReasonNotEmpty    = "the input box was not empty before the send"
	ObserveReasonForeign     = "the input box holds text that is not only this send's"
	ObserveReasonEnterFailed = "enter could not be sent"
	// ObserveReasonAttachments: the input box shows image placeholders
	// ("[Image #8]"), which Claude makes from file paths in a pasted body.
	// They cannot be matched against the typed text.
	ObserveReasonAttachments = "the input box shows image placeholders (attachments) in place of the typed text, which agent-deck cannot match to this send"
)

// ComposerObservation is one look at the input box after a send typed.
type ComposerObservation struct {
	State ComposerState
	// Verified (ComposerLeft only): the whole normalized body is a prefix
	// of the normalized message, or exactly the paste chip label this send's
	// own paste showed, and the box was empty before the send. Otherwise
	// what is there may be mixed with other text.
	Verified bool
	// Draft is the normalized full body ("" when empty or unreadable). It
	// feeds the leftover record's hash and is never logged.
	Draft string
	// Reason is a short lowercase reason, "" when there is nothing to add.
	Reason string
}

// pasteChipRe matches a body that is exactly one collapsed paste marker,
// Claude's "[Pasted text #1 +3 lines]".
var pasteChipRe = regexp.MustCompile(`^\[Pasted (text|Content) [^\]]*\]$`)

// imageChipRe matches a Claude image placeholder anywhere in a body.
var imageChipRe = regexp.MustCompile(`\[Image #\d+\]`)

// ObserveComposer reads the input box once through read and says what a send
// that typed message (and did not submit it) left there. It only captures:
// it never sends a key of any kind.
//
// read must return the box's WHOLE body (ComposerFullBodyReader); nil reports
// ComposerUnreadable. ownedChip is the exact paste chip label this send's own
// paste showed before Enter (SubmitReport.OwnedChip); it only lets a chip-only
// body count as Verified. composerEmptyBefore is the caller's look just
// before typing; without it nothing is Verified.
//
// A placeholder-shaped body (Claude's idle hint) is empty unless it is a
// prefix of message: ownership is decided first, so a message that happens to
// read `Try "..."` is still reported left.
func ObserveComposer(t ComposerGuardTarget, message, ownedChip string, read ComposerFullBodyReader, strip func(string) string, composerEmptyBefore bool) ComposerObservation {
	if read == nil {
		return ComposerObservation{State: ComposerUnreadable, Reason: ObserveReasonNoFullBody}
	}
	raw, err := t.CapturePaneFresh()
	if err != nil {
		return ComposerObservation{State: ComposerUnreadable, Reason: ObserveReasonUnreadable}
	}
	body, ok := read(raw, orIdentity(strip))
	if !ok {
		return ComposerObservation{State: ComposerUnreadable, Reason: ObserveReasonUnreadable}
	}
	want := NormalizePromptText(message)
	draft := NormalizePromptText(body)
	ownedChip = NormalizePromptText(ownedChip)
	if !pasteChipRe.MatchString(ownedChip) {
		ownedChip = ""
	}
	ours := draft != "" && (strings.HasPrefix(want, draft) || (ownedChip != "" && draft == ownedChip))
	if draft == "" || (!ours && IsComposerPlaceholder(draft)) {
		return ComposerObservation{State: ComposerEmpty}
	}
	o := ComposerObservation{State: ComposerLeft, Draft: draft}
	switch {
	case imageChipRe.MatchString(draft):
		o.Reason = ObserveReasonAttachments
	case !composerEmptyBefore:
		o.Reason = ObserveReasonNotEmpty
	case !ours:
		o.Reason = ObserveReasonForeign
	default:
		o.Verified = true
	}
	return o
}

// Describe says, for an error or warning line, what the input box holds.
// Every state but ComposerEmpty carries StillTypedNotice.
func (o ComposerObservation) Describe() string {
	switch o.State {
	case ComposerEmpty:
		return "the input box now reads empty: something other than this send emptied it and the message may have been submitted; do not blindly resend"
	case ComposerUnreadable:
		return "the input box could not be read (" + o.Reason + "); assume " + StillTypedNotice + MixedNotice
	}
	if o.Verified {
		return StillTypedNotice
	}
	if o.Reason != "" {
		return o.Reason + "; " + StillTypedNotice + MixedNotice
	}
	return StillTypedNotice + MixedNotice
}

// MixedNotice follows StillTypedNotice when the leftover was not verified as
// only this send's text.
const MixedNotice = "; it may be mixed with other text"

// StillTypedNotice is the plain statement every report of a send whose text
// stayed in the input box carries (callers grep it).
const StillTypedNotice = "this send's text is still typed, unsubmitted, in the target's input box; a person must submit or clear it"

// NotSubmittedError is TypeAndSubmitReport's error for a send that typed its
// message and then did not submit it: a pre-Enter check withheld Enter, or
// the Enter itself failed after a full type. Observed is one look at the
// input box afterwards. errors.As-able; Unwrap returns Cause.
type NotSubmittedError struct {
	Cause    error
	Observed ComposerObservation
}

func (e *NotSubmittedError) Error() string { return e.Cause.Error() }

func (e *NotSubmittedError) Unwrap() error { return e.Cause }
