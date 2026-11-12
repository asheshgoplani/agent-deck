package main

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A `session send` that types its message and then cannot submit it must not
// leave that text in the target's input box without saying so (11-12 Nov
// incident: the CLI said "not sent", the ~1 kB payload sat typed for 19 hours
// and every later send was refused as "composer is occupied").
//
// This file only REPORTS. agent-deck never deletes or submits anything in
// another session's input box: no Backspace, no clearing, no "self-heal". A
// missed cleanup costs a person one keypress; a wrong deletion or a double
// delivery costs their text or their trust (PR 3123, second review: a
// Backspace withdraw deleted human pastes and whitespace, and reported a
// submitted message as withdrawn so the queue delivered it twice). A safe way
// to clear agent-deck's own leftover is a separate design whose test list is
// that review's findings.

var sendResidueLog = logging.ForComponent(logging.CompSession)

// composer_residue values: what this send left in the target's input box.
const (
	// composerResidueNone: nothing was typed. Only a refusal before any
	// keystroke (the composer guard's composer_blocked, the input-prompt
	// guard's pre-type refusal) reports it.
	composerResidueNone = "none"
	// composerResidueLeft: the send typed (or may have) and the input box
	// shows text: this send's text is still typed there, unsubmitted, and a
	// person must submit or clear it. composer_residue_verified says whether
	// it was verified as only this send's text.
	composerResidueLeft = "left"
	// composerResidueAbsent: the send typed (or may have) but the input box
	// now reads empty: something other than this send emptied it and the
	// message may have been submitted. Do not blindly resend.
	composerResidueAbsent = "absent"
)

// composer_holds values on a composer_blocked refusal: what the input box
// holds, as far as the leftover record can tell.
const (
	// composerHoldsAgentDeckResidue: the input box matches (record hash)
	// the text an earlier failed agent-deck send left there, verified then
	// as only that send's own text. A hash match is not authorship: a
	// person may have typed or recalled the same text since.
	composerHoldsAgentDeckResidue = "agent_deck_residue"
	// composerHoldsAgentDeckResidueUnverified: the input box matches
	// (record hash) what an earlier failed send saw there, but that was
	// never verified as agent-deck's: it may be partly or wholly a person's
	// draft (the 11-12 Nov incident's box held only other text).
	composerHoldsAgentDeckResidueUnverified = "agent_deck_residue_unverified"
	// composerHoldsUnknownText: an earlier send left a message there, but
	// the input box has changed since (the hash does not match).
	composerHoldsUnknownText = "unknown_text"
	// composerHoldsOtherText: no leftover is on record: a person's draft?
	composerHoldsOtherText = "other_text"
	// composerHoldsUnreadable: the input box could not be read.
	composerHoldsUnreadable = "unreadable"
)

// errComposerOccupied is the refusal when the composer guard finds a draft
// before anything was typed. The words "composer is occupied or unreadable;
// existing draft preserved" are matched by callers and tests; keep them.
var errComposerOccupied = errors.New("message not sent: composer is occupied or unreadable; existing draft preserved, nothing was typed")

// composerResidueError carries what a send left in the input box. Error() is
// the full report line.
type composerResidueError struct {
	residue  string
	observed send.ComposerObservation
	err      error
}

func (e *composerResidueError) Error() string { return e.err.Error() }

func (e *composerResidueError) Unwrap() error { return e.err }

// nothingTyped marks a refusal before any keystroke as composer_residue none.
func nothingTyped(err error) error {
	if err == nil {
		return nil
	}
	return &composerResidueError{residue: composerResidueNone, err: err}
}

// reportTyped maps a send that typed its message (or may have) and did not
// submit it (cause), plus one look at the input box (o), to the delivery and
// the report. leftDelivery is the delivery while text is (or may be) there;
// absentDelivery when the box reads empty. A tool without a full-body reader
// gets no composer_residue (unknown), never none. Nothing is ever deleted.
func reportTyped(cause error, o send.ComposerObservation, leftDelivery, absentDelivery string) (string, error) {
	switch {
	case o.State == send.ComposerEmpty:
		return absentDelivery, &composerResidueError{residue: composerResidueAbsent, observed: o,
			err: fmt.Errorf("message not confirmed: %w; %s", cause, o.Describe())}
	case o.State == send.ComposerUnreadable && o.Reason == send.ObserveReasonNoFullBody:
		return leftDelivery, fmt.Errorf("%w; %s", cause, o.Describe())
	}
	if o.State != send.ComposerLeft {
		o.Verified = false
	}
	return leftDelivery, &composerResidueError{residue: composerResidueLeft, observed: o,
		err: fmt.Errorf("message not sent: %w; %s", cause, o.Describe())}
}

// notSubmittedOutcome reports a *send.NotSubmittedError from the initial
// submit. A pre-Enter refusal (the body was typed, Enter was withheld) is
// typed_not_submitted whatever the box shows. A failed Enter after a full
// type is a transport failure: typed_not_submitted while text is there,
// send_failed (its old retry semantics) when the box reads empty.
func notSubmittedOutcome(ns *send.NotSubmittedError) (string, error) {
	if errors.Is(ns.Cause, send.ErrComposerNotOurMessage) || errors.Is(ns.Cause, send.ErrPasteTruncated) {
		return reportTyped(ns.Cause, ns.Observed, deliveryTypedNotSubmitted, deliveryTypedNotSubmitted)
	}
	return reportTyped(fmt.Errorf("failed to send message: %w", ns.Cause), ns.Observed, deliveryTypedNotSubmitted, deliverySendFailed)
}

// guardRefusalOutcome is the input-prompt guard's refusal. Before any
// keystroke it is the guard's own verdict, composer_residue none. After
// typing, one capture says what the input box holds, reported left
// (unverified: a menu took the prompt over) or absent.
func guardRefusalOutcome(target sendRetryTarget, message string, opts sendRetryOptions) (string, error) {
	delivery, err := opts.promptGuard.refusal()
	if delivery != deliveryTypedNotSubmitted {
		return delivery, nothingTyped(err)
	}
	o := send.ObserveComposer(target, message, opts.ownedPasteChip, opts.composerBody, tmux.StripANSI, opts.composerEmptyBeforeSend)
	if o.State == send.ComposerLeft {
		o.Verified = false
		o.Reason = "the input prompt was taken over before the send could finish"
	}
	return reportTyped(err, o, deliveryTypedNotSubmitted, deliveryTypedNotSubmitted)
}

// transportFailureOutcome is a send whose transport failed with a plain
// error (cause), possibly after typing began: a chunk's send-keys failed
// mid-paste, or the final Enter failed. One capture says what is in the
// input box: text is reported left (unverified, typed_not_submitted), an
// empty box absent (send_failed, as before). A tool without a full-body
// reader keeps the plain send_failed report.
func transportFailureOutcome(target sendRetryTarget, message string, opts sendRetryOptions, cause error) (string, error) {
	if opts.composerBody == nil {
		return deliverySendFailed, cause
	}
	o := send.ObserveComposer(target, message, opts.ownedPasteChip, opts.composerBody, tmux.StripANSI, opts.composerEmptyBeforeSend)
	if o.State == send.ComposerLeft {
		o.Verified = false
		o.Reason = "the send failed after typing may have begun"
	}
	return reportTyped(cause, o, deliveryTypedNotSubmitted, deliverySendFailed)
}

// reportIfStuck runs at the end of the verify budget. A typed_not_submitted
// verdict means the input box still held our message after every bounded
// Enter retry: one more capture says what it holds now, and the report says
// so. Every other verdict passes through unchanged. Nothing is deleted.
func reportIfStuck(target sendRetryTarget, message string, opts sendRetryOptions, delivery string, err error) (string, error) {
	if delivery != deliveryTypedNotSubmitted || err == nil {
		return delivery, err
	}
	o := send.ObserveComposer(target, message, opts.ownedPasteChip, opts.composerBody, tmux.StripANSI, opts.composerEmptyBeforeSend)
	return reportTyped(err, o, deliveryTypedNotSubmitted, deliveryTypedNotSubmitted)
}

// forgetOwnedChipIfEmptied drops the owned paste chip label once any
// post-Enter frame shows the input box empty: our chip left it (submitted
// or queued), so a chip with the same label seen later is a new paste,
// quite possibly a person's, and must never be reported as verified ours.
func (o *sendRetryOptions) forgetOwnedChipIfEmptied(raw string, captureErr error) {
	if o.ownedPasteChip == "" || captureErr != nil || o.composerBody == nil {
		return
	}
	if body, ok := o.composerBody(raw, tmux.StripANSI); ok && send.FullBodyEmpty(body) {
		o.ownedPasteChip = ""
	}
}

// sendResidueStore keeps the per-target record of text a failed send left in
// the input box (session.FileSendResidueStore in production). Keyed by the
// target instance id. The record only makes later refusals informative; it
// never authorises a key press.
type sendResidueStore interface {
	Load(instanceID string) (session.SendResidue, bool, error)
	Save(instanceID string, r session.SendResidue) error
	Clear(instanceID string) error
}

// recordResidue writes the leftover record for a composer_residue "left"
// report. Best effort: a store failure is logged, never fails the send.
func recordResidue(tun sendExecTuning, message string, cr *composerResidueError) {
	if tun.residue == nil || tun.targetID == "" || cr == nil {
		return
	}
	if cr.residue != composerResidueLeft || cr.observed.Draft == "" {
		return
	}
	rec := session.SendResidue{
		DraftSHA256:   session.SendResidueHash(cr.observed.Draft),
		DraftLen:      len(cr.observed.Draft),
		MessageSHA256: session.SendResidueHash(message),
		// A leftover that is only a paste chip is never verified: the label
		// ("[Pasted text #1 +20 lines]") is generic, not unique to this
		// send's text.
		Verified:  cr.observed.Verified && !send.IsPasteChip(cr.observed.Draft),
		Sender:    tun.residueSender,
		CreatedAt: time.Now(),
	}
	if err := tun.residue.Save(tun.targetID, rec); err != nil {
		sendResidueLog.Warn("send_residue_save_failed", slog.String("instance_id", tun.targetID), slog.String("error", err.Error()))
	}
}

// clearResidue drops the target's leftover record: the input box was seen
// empty, so nothing a failed send left is there any more.
func clearResidue(tun sendExecTuning) {
	if tun.residue == nil || tun.targetID == "" {
		return
	}
	if err := tun.residue.Clear(tun.targetID); err != nil {
		sendResidueLog.Warn("send_residue_clear_failed", slog.String("instance_id", tun.targetID), slog.String("error", err.Error()))
	}
}

// blockedComposer is what describeBlockedComposer found behind a
// composer_blocked refusal.
type blockedComposer struct {
	// holds is the composer_holds value, "" when not established (no
	// record store or no full-body reader for the tool).
	holds  string
	age    time.Duration
	sender string
	err    error
}

// describeBlockedComposer runs when the composer guard refused, before
// anything was typed. With the leftover record store wired and a full-body
// reader for the tool, one capture says what the input box holds:
//
//   - the same text an earlier failed send left (record hash matches):
//     agent_deck_residue when that send verified it as only its own text,
//     agent_deck_residue_unverified otherwise, with its sender and age.
//     A hash match is not authorship, so the wording says "matches" and
//     asks the reader to check before clearing;
//   - a record exists but the hash differs: unknown text;
//   - no record: other text (a person's draft?);
//   - unreadable: the generic occupied-or-unreadable wording.
//
// It only reads the record and the pane. It never sends a key: no code path
// may read the record and then press anything in the target.
func describeBlockedComposer(target sendRetryTarget, read send.ComposerFullBodyReader, tun sendExecTuning) blockedComposer {
	generic := blockedComposer{err: errComposerOccupied}
	if tun.residue == nil || tun.targetID == "" || read == nil {
		return generic
	}
	raw, err := target.CapturePaneFresh()
	if err != nil {
		return blockedComposer{holds: composerHoldsUnreadable, err: errComposerOccupied}
	}
	body, ok := read(raw, tmux.StripANSI)
	if !ok {
		return blockedComposer{holds: composerHoldsUnreadable, err: errComposerOccupied}
	}
	draft := send.NormalizePromptText(body)
	if send.FullBodyEmpty(body) {
		// It emptied between the guard's look and this one: nothing to name.
		return generic
	}
	rec, found, err := tun.residue.Load(tun.targetID)
	if err != nil {
		sendResidueLog.Warn("send_residue_load_failed", slog.String("instance_id", tun.targetID), slog.String("error", err.Error()))
		found = false
	}
	now := time.Now()
	if found && !rec.Retained(now) {
		clearResidue(tun)
		found = false
	}
	if !found {
		return blockedComposer{holds: composerHoldsOtherText, err: errors.New(
			"message not sent: the input box holds other text (a person's draft?); nothing was typed; composer is occupied or unreadable; existing draft preserved")}
	}
	age := now.Sub(rec.CreatedAt).Round(time.Second)
	if session.SendResidueHash(draft) != rec.DraftSHA256 {
		return blockedComposer{holds: composerHoldsUnknownText, age: age, err: fmt.Errorf(
			"message not sent: the input box holds unknown text (agent-deck left a message there %s ago, but the box has changed since); nothing was typed; composer is occupied or unreadable; existing draft preserved", age)}
	}
	from := rec.Sender
	if from == "" {
		from = "an unknown sender"
	}
	// A hash match says the box holds the same text, not who put it there:
	// never call it agent-deck's own when it was not verified, and always
	// tell the reader to check before clearing.
	if !rec.Verified {
		return blockedComposer{holds: composerHoldsAgentDeckResidueUnverified, age: age, sender: rec.Sender, err: fmt.Errorf(
			"message not sent: the input box holds the text it held when an agent-deck send from %s failed %s ago; that text was never verified as agent-deck's and may be partly or wholly a person's draft; nothing was typed; check it before clearing; existing draft preserved", from, age)}
	}
	return blockedComposer{holds: composerHoldsAgentDeckResidue, age: age, sender: rec.Sender, err: fmt.Errorf(
		"message not sent: the input box matches the unsent message an agent-deck send from %s left there %s ago (if a person typed or recalled the same text since, it is theirs); nothing was typed; check it before clearing, then submit or clear it", from, age)}
}
