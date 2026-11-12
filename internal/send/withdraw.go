package send

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// BackspaceSender is the optional transport surface WithdrawTyped needs.
// *tmux.Session implements it. Backspace is the only key withdrawal ever
// sends: never Enter, Ctrl+C, Ctrl+U or Escape.
type BackspaceSender interface {
	SendBackspaces(n int) error
}

// WithdrawOutcome says what WithdrawTyped left in the composer.
type WithdrawOutcome int

const (
	// WithdrawNotNeeded: the composer showed nothing (empty), so there was
	// nothing to remove.
	WithdrawNotNeeded WithdrawOutcome = iota
	// WithdrawRemoved: our text was there, we removed it, and the composer is
	// now empty.
	WithdrawRemoved
	// WithdrawLeft: our text, or text we cannot attribute, is still in the
	// composer.
	WithdrawLeft
)

// Withdraw reasons (WithdrawResult.Reason, only for WithdrawLeft).
const (
	WithdrawReasonForeign      = "composer holds text that is not this send's"
	WithdrawReasonUnreadable   = "composer not readable"
	WithdrawReasonNoFullBody   = "composer body not fully readable"
	WithdrawReasonNoBackspace  = "target cannot send backspace"
	WithdrawReasonBackspaceErr = "backspace send failed"
	WithdrawReasonNoProgress   = "backspace made no progress"
	WithdrawReasonBudget       = "removal budget exhausted"
	WithdrawReasonNotEmpty     = "composer was not empty before the send"
	WithdrawReasonEnterFailed  = "enter could not be sent"
	// WithdrawReasonAttachments: the composer shows image placeholders
	// ("[Image #8]"), which Claude makes from file paths in a pasted body.
	// They cannot be matched against the typed text, and whether Backspace
	// removes the attachment or only its label is not visible in the pane,
	// so nothing is removed.
	WithdrawReasonAttachments = "composer shows image placeholders (attachments) in place of the typed text, which agent-deck cannot match to this send and never removes"
)

// WithdrawResult is what WithdrawTyped did and what it left behind.
type WithdrawResult struct {
	Outcome WithdrawOutcome
	// Verified (WithdrawLeft only): what is left was positively verified,
	// on the last readable frame, as only our own typed text.
	Verified bool
	// Draft is the normalized FULL composer body (ComposerFullBodyReader)
	// at the moment withdrawal stopped, "" when it could not be read. It
	// feeds the residue record's hash and is never logged.
	Draft string
	// Reason is a short lowercase reason when Outcome is WithdrawLeft.
	Reason string
}

const (
	// withdrawBatch is the most Backspaces sent between two verifying
	// captures.
	withdrawBatch = 32
	// withdrawSlack is the total Backspace budget beyond the message's own
	// rune count (paste chips, wrapped rows).
	withdrawSlack = 64
	// withdrawPolls/withdrawPollInterval bound the wait for the composer
	// body to change (and settle) after a batch.
	withdrawPolls        = 10
	withdrawPollInterval = 30 * time.Millisecond
	// withdrawSettleDelay is the wait before the one extra frame read after
	// a batch showed no progress.
	withdrawSettleDelay = 100 * time.Millisecond
)

// pasteChipRe matches a composer draft that is exactly one collapsed paste
// marker, Claude's "[Pasted text #1 +3 lines]".
var pasteChipRe = regexp.MustCompile(`^\[Pasted (text|Content) [^\]]*\]$`)

// imageChipRe matches a Claude image placeholder anywhere in a draft.
var imageChipRe = regexp.MustCompile(`\[Image #\d+\]`)

// withdrawBudget is the most Backspaces WithdrawTyped may send for message:
// its rune count as typed (CRLF and CR read as one line break, whitespace
// NOT collapsed, so an indented body is fully covered) plus withdrawSlack.
func withdrawBudget(message string) int {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\r", "\n")
	return utf8.RuneCountInString(message) + withdrawSlack
}

// WithdrawTyped removes from the composer exactly the text a send typed into
// it and then had to withhold Enter on, and only that text, so a failed send
// never leaves its payload behind for a human (or the next send) to trip on.
//
// read must return the composer's WHOLE body (ComposerFullBodyReader). A nil
// read never deletes: the result is WithdrawLeft, unverified, reason
// WithdrawReasonNoFullBody. A reader that stops at the first paragraph would
// let text after a blank line (a human's note) be deleted unchecked.
//
// It deletes nothing unless composerEmptyBefore: the caller saw the composer
// empty (a prompt suggestion counts as empty) immediately before typing, so
// anything in it now that matches what we typed is ours. Ownership is checked
// on every frame: the normalized full body must be a prefix of the normalized
// message, or a prefix of ownedChip. ownedChip is the exact collapsed paste
// label ("[Pasted text #1 +3 lines]", #N included) THIS send's own paste
// showed on its post-paste, pre-Enter frame (SubmitReport.OwnedChip); "" when
// no chip is known to be ours. A chip is never inferred from what the
// composer shows now: a human's own paste (#2) arriving after our message
// was submitted would otherwise be deleted as ours. Anything else stops the
// loop at once and is reported unverified; if nothing was sent yet, nothing
// was touched.
//
// A body is empty when it reads empty (a dim prompt suggestion reads empty,
// ComposerBodyIsSuggestion), or when it is shaped like Claude's idle
// placeholder or queued hint (IsComposerPlaceholder) AND is not a prefix of
// our message: ownership is decided before any placeholder mapping, so a
// message that happens to read `Try "..."` is still ours.
//
// Each batch is at most withdrawBatch Backspaces and never more than the
// visible body's rune count, and the total is bounded by withdrawBudget.
// Progress is the composer body changing after a batch; the rest of the pane
// (a spinner, a clock) does not count. When a batch shows no progress, one
// more frame is read after a settle delay and the result records what that
// frame holds.
//
// Once Backspaces were sent, a frame that cannot be read, or a Backspace
// send that fails, ends the loop unverified: an unknown number of keys
// landed, so the last good draft is stale.
//
// TOCTOU, stated plainly: a key a human types between the capture a batch is
// sized from and the send-keys that delivers it lands at the cursor and can
// be consumed by that batch. If what remains is still a prefix of our
// message, the next frame looks like ours and the loop does not notice;
// the human's keystrokes are lost. This cannot be closed from outside the
// TUI. It is bounded by the batch size: at most withdrawBatch (32) keys per
// round can be eaten this way, and a human key that survives a batch makes
// the next frame foreign, which stops the loop.
func WithdrawTyped(t ComposerGuardTarget, message, ownedChip string, read ComposerFullBodyReader, strip func(string) string, composerEmptyBefore bool) WithdrawResult {
	strip = orIdentity(strip)
	left := func(reason string, v bool, draft string) WithdrawResult {
		return WithdrawResult{Outcome: WithdrawLeft, Verified: v, Draft: draft, Reason: reason}
	}
	if read == nil {
		return left(WithdrawReasonNoFullBody, false, "")
	}
	want := NormalizePromptText(message)
	ownedChip = NormalizePromptText(ownedChip)
	if !pasteChipRe.MatchString(ownedChip) {
		ownedChip = ""
	}
	owned := func(draft string) bool {
		return strings.HasPrefix(want, draft) || (ownedChip != "" && strings.HasPrefix(ownedChip, draft))
	}
	// frame reads one capture: the full body, its normalized draft ("" for
	// an empty composer, placeholder mapping applied after ownership), and
	// whether it could be read at all.
	frame := func(raw string, err error) (body, draft string, ok bool) {
		if err != nil {
			return "", "", false
		}
		body, ok = read(raw, strip)
		if !ok {
			return "", "", false
		}
		draft = NormalizePromptText(body)
		if draft != "" && !owned(draft) && IsComposerPlaceholder(draft) {
			draft = ""
		}
		return body, draft, true
	}
	budget := withdrawBudget(message)
	bs, canBackspace := t.(BackspaceSender)

	sent := 0
	// settled marks the frame read after a no-progress batch: it is
	// recorded, never acted on.
	settled := false
	raw, err := t.CapturePaneFresh()
	for {
		body, draft, ok := frame(raw, err)
		if !ok {
			// Before any Backspace, nothing changed since the caller's own
			// look; after one, the last good draft is stale.
			return left(WithdrawReasonUnreadable, false, "")
		}
		if draft == "" {
			if sent > 0 {
				return WithdrawResult{Outcome: WithdrawRemoved}
			}
			return WithdrawResult{Outcome: WithdrawNotNeeded}
		}
		if !composerEmptyBefore {
			return left(WithdrawReasonNotEmpty, false, draft)
		}
		if !owned(draft) {
			if imageChipRe.MatchString(draft) {
				return left(WithdrawReasonAttachments, false, draft)
			}
			return left(WithdrawReasonForeign, false, draft)
		}
		if settled {
			return left(WithdrawReasonNoProgress, true, draft)
		}
		if !canBackspace {
			return left(WithdrawReasonNoBackspace, true, draft)
		}
		n := min(withdrawBatch, utf8.RuneCountInString(body), budget-sent)
		if pasteChipRe.MatchString(draft) {
			// Claude deletes a whole collapsed paste chip with one
			// Backspace; the rest of a full batch would land in an empty
			// composer. A chip that erases rune by rune still matches the
			// owned chip's prefix and continues in full batches.
			n = min(n, 1)
		}
		if n <= 0 {
			return left(WithdrawReasonBudget, true, draft)
		}
		if sendErr := bs.SendBackspaces(n); sendErr != nil {
			if sent == 0 {
				return left(WithdrawReasonBackspaceErr, true, draft)
			}
			// Earlier batches landed and this one may have partly: record
			// what one fresh frame shows, unverified.
			_, now, nowOK := frame(t.CapturePaneFresh())
			if !nowOK {
				now = ""
			}
			return left(WithdrawReasonBackspaceErr, false, now)
		}
		sent += n
		var changed bool
		raw, changed, err = awaitBodyChange(t, body, read, strip)
		if err == nil && !changed {
			// No change within the poll: read one settled frame and record
			// what it actually holds (a slow repaint may land late).
			time.Sleep(withdrawSettleDelay)
			raw, err = t.CapturePaneFresh()
			settled = true
		}
	}
}

// awaitBodyChange re-captures until the composer's full body differs from
// before and then holds still for one more capture, so the next batch is
// sized from a frame that has absorbed the previous one. Only the composer
// body is compared: a spinner or clock elsewhere in the pane is not
// progress. A frame whose body cannot be read counts as changed and is
// returned for the caller to report.
func awaitBodyChange(t ComposerGuardTarget, before string, read ComposerFullBodyReader, strip func(string) string) (raw string, changed bool, err error) {
	prev := before
	for poll := 0; poll < withdrawPolls; poll++ {
		if poll > 0 {
			time.Sleep(withdrawPollInterval)
		}
		raw, err = t.CapturePaneFresh()
		if err != nil {
			return "", changed, err
		}
		cur, ok := read(raw, strip)
		if !ok {
			return raw, true, nil
		}
		if changed && cur == prev {
			return raw, true, nil
		}
		if cur != before {
			changed = true
		}
		prev = cur
	}
	return raw, changed, nil
}

// ObserveLeft reports, without deleting anything, what a send whose
// transport failed after typing may have begun left in the composer: one
// capture through read. An empty composer is WithdrawNotNeeded (nothing of
// ours is there; something may have emptied it, possibly by submitting it);
// anything else is WithdrawLeft, unverified, reason WithdrawReasonEnterFailed
// (or unreadable). Nothing is attributed: the keys may have half-landed.
// message decides, as in WithdrawTyped, that a placeholder-shaped body which
// is a prefix of it is our text, not an empty composer.
func ObserveLeft(t ComposerGuardTarget, message string, read ComposerFullBodyReader, strip func(string) string) WithdrawResult {
	if read == nil {
		return WithdrawResult{Outcome: WithdrawLeft, Reason: WithdrawReasonNoFullBody}
	}
	raw, err := t.CapturePaneFresh()
	if err != nil {
		return WithdrawResult{Outcome: WithdrawLeft, Reason: WithdrawReasonUnreadable}
	}
	body, ok := read(raw, orIdentity(strip))
	if !ok {
		return WithdrawResult{Outcome: WithdrawLeft, Reason: WithdrawReasonUnreadable}
	}
	draft := NormalizePromptText(body)
	if draft == "" || (IsComposerPlaceholder(draft) && !strings.HasPrefix(NormalizePromptText(message), draft)) {
		return WithdrawResult{Outcome: WithdrawNotNeeded}
	}
	return WithdrawResult{Outcome: WithdrawLeft, Draft: draft, Reason: WithdrawReasonEnterFailed}
}

// Describe says, for an error message, what is left of a send's typed text
// in the composer.
func (w WithdrawResult) Describe() string {
	switch w.Outcome {
	case WithdrawRemoved:
		return "the typed text was removed from the composer"
	case WithdrawNotNeeded:
		return "the composer was emptied by something other than this send; the message may have been submitted"
	}
	if w.Verified {
		return "the typed text is still in the composer (" + w.Reason + ")"
	}
	return "the typed text may still be in the composer, possibly mixed with other text (" + w.Reason + ")"
}

// NotSubmittedError is TypeAndSubmitReport's error for a send that typed its
// message and then withheld Enter on a pre-Enter check. Withdraw says what
// was done about the typed text. errors.As-able; Unwrap returns Cause.
type NotSubmittedError struct {
	Cause    error
	Withdraw WithdrawResult
}

func (e *NotSubmittedError) Error() string { return e.Cause.Error() }

func (e *NotSubmittedError) Unwrap() error { return e.Cause }
