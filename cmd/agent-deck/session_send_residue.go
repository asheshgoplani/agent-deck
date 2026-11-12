package main

import (
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A `session send` that types its message and then cannot submit it must not
// leave that text in the target's composer without saying so (11-12 Nov
// incident: the CLI said "not sent", the ~1 kB payload sat typed for 19 hours
// and every later send was refused as "composer is occupied"). This file
// holds the reporting and the self-heal; send.WithdrawTyped does the removal.

var sendResidueLog = logging.ForComponent(logging.CompSession)

// composer_residue values: what this send left in the target's composer.
const (
	// composerResidueNone: nothing of this send is in the composer (nothing
	// was typed, or the composer was found empty).
	composerResidueNone = "none"
	// composerResidueRemoved: the send typed, could not submit, and removed
	// exactly what it typed.
	composerResidueRemoved = "removed"
	// composerResidueLeft: the send's text is still typed, unsubmitted, in
	// the composer; a human (or a self-healing later send) must deal with it.
	composerResidueLeft = "left"
)

// residueMinClearRunes is the shortest recorded residue (normalized draft,
// in runes) a later send may remove on the record's say-so. Short text
// ("continue", "yes") is too common: a human could have typed the very same
// words after the residue was gone, so it is refused with its origin named
// instead.
const residueMinClearRunes = 16

// composerHoldsAgentDeckResidue is the --json composer_holds value for a
// refusal caused by text a previous failed agent-deck send left behind.
const composerHoldsAgentDeckResidue = "agent_deck_residue"

// errComposerOccupied is the refusal when the composer guard finds a draft
// before anything was typed. The words "composer is occupied or unreadable;
// existing draft preserved" are matched by callers and tests; keep them.
var errComposerOccupied = errors.New("message not sent: composer is occupied or unreadable; existing draft preserved, nothing was typed")

// composerResidueError carries what a send that typed and did not submit left
// in the composer. Error() is the full report line.
type composerResidueError struct {
	residue  string
	withdraw send.WithdrawResult
	err      error
}

func (e *composerResidueError) Error() string { return e.err.Error() }

func (e *composerResidueError) Unwrap() error { return e.err }

// notSubmittedOutcome maps a send that typed its message and did not submit
// it (cause) plus what send.WithdrawTyped (or send.ObserveLeft) found to the
// delivery status and report:
//
//   - removed: agent-deck deleted exactly its own text -> typed_withdrawn,
//     the only outcome a queue may retry as "nothing happened";
//   - not needed: the composer was already empty, but agent-deck removed
//     nothing, so something else emptied it and may have submitted it ->
//     typed_not_submitted, composer_residue none (never typed_withdrawn: a
//     retry could deliver the message twice);
//   - left -> typed_not_submitted, naming the reason.
func notSubmittedOutcome(cause error, w send.WithdrawResult) (string, error) {
	switch w.Outcome {
	case send.WithdrawRemoved:
		return deliveryWithdrawn, &composerResidueError{residue: composerResidueRemoved, withdraw: w,
			err: fmt.Errorf("message not sent: %w; the typed text was removed from the composer, which is as it was before the send", cause)}
	case send.WithdrawNotNeeded:
		return deliveryTypedNotSubmitted, &composerResidueError{residue: composerResidueNone, withdraw: w,
			err: fmt.Errorf("message not confirmed: %w; the composer was emptied by something other than this send; the message may have been submitted", cause)}
	}
	mixed := ""
	if !w.Verified {
		mixed = " (it may be mixed with other text)"
	}
	return deliveryTypedNotSubmitted, &composerResidueError{residue: composerResidueLeft, withdraw: w,
		err: fmt.Errorf("message not sent and left in the composer: %w; %s; this send's text is still typed, unsubmitted, in the target's composer%s: a human must submit or clear it", cause, w.Reason, mixed)}
}

// guardRefusalOutcome is the input-prompt guard's refusal once the send may
// have typed. Before any keystroke it is the guard's own verdict. After
// typing, the typed text is reported left in the composer (unverified), and
// recorded when the composer can be read, unless one fresh capture proves the
// composer empty. Nothing is deleted: the guard refused because a menu is in
// the way, where Backspace is not editing.
func guardRefusalOutcome(target sendRetryTarget, message string, opts sendRetryOptions) (string, error) {
	delivery, err := opts.promptGuard.refusal()
	if delivery != deliveryTypedNotSubmitted || err == nil {
		return delivery, err
	}
	w := send.ObserveLeft(target, message, opts.composerBody, tmux.StripANSI)
	if w.Outcome == send.WithdrawLeft {
		w.Reason = "the input prompt was taken over before the send could finish"
	}
	return notSubmittedOutcome(err, w)
}

// transportFailureOutcome is a send whose transport failed with a plain
// error (cause), possibly after typing began: a chunk's send-keys failed
// mid-paste, or the final Enter failed. One fresh capture says what is in the
// composer; nothing is ever deleted here (an unknown amount landed, the Enter
// may have half-landed). Text in the composer is reported left, unverified,
// and recorded; an empty composer is none. A tool without a full-body reader
// keeps the plain send_failed report.
func transportFailureOutcome(target sendRetryTarget, message string, opts sendRetryOptions, cause error) (string, error) {
	if opts.composerBody == nil {
		return deliverySendFailed, cause
	}
	w := send.ObserveLeft(target, message, opts.composerBody, tmux.StripANSI)
	if w.Outcome == send.WithdrawNotNeeded {
		return deliverySendFailed, &composerResidueError{residue: composerResidueNone, withdraw: w,
			err: fmt.Errorf("%w; the composer is empty, nothing of this send is left in it", cause)}
	}
	w.Verified = false
	if w.Reason == send.WithdrawReasonEnterFailed {
		w.Reason = "the send failed after typing may have begun"
	}
	return notSubmittedOutcome(cause, w)
}

// forgetOwnedChipIfEmptied drops the owned paste chip label once any
// post-Enter frame shows the composer empty: our chip left the composer
// (submitted or queued), so a chip with the same label seen later is a new
// paste, quite possibly a human's (Claude may restart paste numbering for
// each prompt), and must never be removed as ours.
func (o *sendRetryOptions) forgetOwnedChipIfEmptied(raw string, captureErr error) {
	if o.ownedPasteChip == "" || captureErr != nil || o.composerBody == nil {
		return
	}
	if body, ok := o.composerBody(raw, tmux.StripANSI); ok && send.FullBodyEmpty(body) {
		o.ownedPasteChip = ""
	}
}

// withdrawIfStuck runs at the end of the verify budget. A typed_not_submitted
// verdict means the composer still holds our message after every bounded
// Enter retry; take it back where that is provably safe and report what is
// left. Every other verdict passes through unchanged.
func withdrawIfStuck(target sendRetryTarget, message string, opts sendRetryOptions, delivery string, err error) (string, error) {
	if delivery != deliveryTypedNotSubmitted || err == nil {
		return delivery, err
	}
	// Only the exact chip label this send's own paste showed before Enter
	// is ours; a chip the composer shows now may be a human's own paste
	// that arrived after our message was submitted.
	w := send.WithdrawTyped(target, message, opts.ownedPasteChip, opts.composerBody, tmux.StripANSI, opts.composerEmptyBeforeSend)
	if w.Outcome == send.WithdrawNotNeeded {
		// The composer emptied without us between the verdict and now: the
		// message may have been taken up late, or a human cleared it. Keep
		// the verdict; say the composer is clear.
		return delivery, &composerResidueError{residue: composerResidueNone, withdraw: w,
			err: fmt.Errorf("%w; the composer was emptied by something other than this send; the message may have been submitted late", err)}
	}
	return notSubmittedOutcome(err, w)
}

// sendResidueStore keeps the per-target record of text a failed send left in
// the composer (session.FileSendResidueStore in production). Keyed by the
// target instance id.
type sendResidueStore interface {
	Load(instanceID string) (session.SendResidue, bool, error)
	Save(instanceID string, r session.SendResidue) error
	Clear(instanceID string) error
}

// recordResidue writes (or, when nothing is left, clears) the residue record
// for res's outcome. Best effort: a store failure is logged, never fails the
// send.
func recordResidue(tun sendExecTuning, message string, cr *composerResidueError) {
	if tun.residue == nil || tun.targetID == "" || cr == nil {
		return
	}
	if cr.residue != composerResidueLeft || cr.withdraw.Draft == "" {
		return
	}
	rec := session.SendResidue{
		DraftSHA256:   session.SendResidueHash(cr.withdraw.Draft),
		DraftLen:      len(cr.withdraw.Draft),
		MessageSHA256: session.SendResidueHash(message),
		// A residue that is only a paste chip is never verified: the label
		// ("[Pasted text #1 +20 lines]") is generic, not unique to this
		// send's text, so it must never authorise a later removal.
		Verified:  cr.withdraw.Verified && !send.IsPasteChip(cr.withdraw.Draft),
		Sender:    tun.residueSender,
		CreatedAt: time.Now(),
	}
	if err := tun.residue.Save(tun.targetID, rec); err != nil {
		sendResidueLog.Warn("send_residue_save_failed", slog.String("instance_id", tun.targetID), slog.String("error", err.Error()))
	}
}

// clearResidue drops the target's residue record: the composer was seen
// empty, so nothing a failed send left is there any more.
func clearResidue(tun sendExecTuning) {
	if tun.residue == nil || tun.targetID == "" {
		return
	}
	if err := tun.residue.Clear(tun.targetID); err != nil {
		sendResidueLog.Warn("send_residue_clear_failed", slog.String("instance_id", tun.targetID), slog.String("error", err.Error()))
	}
}

// residueCheck is what healComposerResidue found behind a guard refusal.
type residueCheck struct {
	// cleared: the composer held exactly a verified residue and it was
	// removed; the caller re-runs the guard and sends.
	cleared bool
	// named: the composer holds a recorded residue that was not removed;
	// err names it.
	named bool
	age   time.Duration
	err   error
}

// healComposerResidue runs when the composer guard refused. If the target's
// residue record is retained (session.SendResidueKeepAge) and the composer's
// normalized FULL body hashes exactly to it, the body is text a previous
// agent-deck send typed and could not submit:
//
//   - verified (it was only that send's text, typed into a composer verified
//     empty) and recorded within session.SendResidueMaxAge: remove it, never
//     submit it. That sender was told "not sent" and may have retried or
//     re-routed, so submitting would deliver a stale, possibly duplicate
//     message; removing it restores what it was told.
//   - unverified, older than SendResidueMaxAge, or the removal stopped: keep
//     refusing, but name the residue's origin and age.
//
// Anything else (no record, a dropped record, a body that does not hash to
// it, a tool without a full-body reader) is a human's draft as far as
// agent-deck can tell: untouched, generic refusal. A human draft cannot
// match: matching needs the exact hash of text agent-deck recorded.
func healComposerResidue(target sendRetryTarget, read send.ComposerFullBodyReader, tun sendExecTuning) residueCheck {
	if tun.residue == nil || tun.targetID == "" || read == nil {
		return residueCheck{}
	}
	rec, ok, err := tun.residue.Load(tun.targetID)
	if err != nil {
		sendResidueLog.Warn("send_residue_load_failed", slog.String("instance_id", tun.targetID), slog.String("error", err.Error()))
		return residueCheck{}
	}
	if !ok {
		return residueCheck{}
	}
	now := time.Now()
	if !rec.Retained(now) {
		clearResidue(tun)
		return residueCheck{}
	}
	raw, err := target.CapturePaneFresh()
	if err != nil {
		return residueCheck{}
	}
	body, visible := read(raw, tmux.StripANSI)
	draft := send.NormalizePromptText(body)
	if !visible || draft == "" || session.SendResidueHash(draft) != rec.DraftSHA256 {
		return residueCheck{}
	}
	age := now.Sub(rec.CreatedAt)
	why := "it was not verified as agent-deck's own text only (it may include other text), so it is neither submitted nor removed"
	switch {
	case rec.Verified && !rec.AuthorizesClear(now):
		why = fmt.Sprintf("it is older than %s, too old to be removed automatically, so it is neither submitted nor removed", session.SendResidueMaxAge)
	case rec.Verified && utf8.RuneCountInString(draft) < residueMinClearRunes:
		why = fmt.Sprintf("it is shorter than %d characters, too common to be told apart from text a human typed, so it is neither submitted nor removed", residueMinClearRunes)
	case rec.Verified:
		// The recorded body is the whole of what may be removed: pass it
		// (the current full body, which hashes to the record) as the
		// message, so ownership is "a prefix of that exact body". No chip
		// is owned: a chip-only residue is never recorded verified.
		w := send.WithdrawTyped(target, body, "", read, tmux.StripANSI, true)
		switch {
		case w.Outcome != send.WithdrawLeft:
			clearResidue(tun)
			return residueCheck{cleared: true}
		case w.Verified && w.Draft != "":
			// Partly removed: the record follows what is left.
			rec.DraftSHA256, rec.DraftLen = session.SendResidueHash(w.Draft), len(w.Draft)
			if err := tun.residue.Save(tun.targetID, rec); err != nil {
				sendResidueLog.Warn("send_residue_save_failed", slog.String("instance_id", tun.targetID), slog.String("error", err.Error()))
			}
			why = fmt.Sprintf("it is agent-deck's own text but could not be removed (%s), so it is neither submitted nor removed", w.Reason)
		default:
			// Someone typed into it while it was being removed (or it became
			// unreadable): it is no longer provably only agent-deck's text.
			clearResidue(tun)
			why = fmt.Sprintf("removing it stopped (%s), so what is left is neither submitted nor removed", w.Reason)
		}
	}
	from := ""
	if rec.Sender != "" {
		from = fmt.Sprintf(" (from %s)", rec.Sender)
	}
	return residueCheck{named: true, age: age, err: fmt.Errorf(
		"message not sent: the composer holds text left by a failed agent-deck send %s ago%s, unsubmitted; %s: a human must submit or clear it; nothing was typed, existing draft preserved",
		age.Round(time.Second), from, why)}
}

// SendBackspaces lets send.WithdrawTyped take back typed text through the
// input-prompt guard: never into an open picker, where Backspace is not
// editing.
func (g *promptGuardTarget) SendBackspaces(n int) error {
	bs, ok := g.sendRetryTarget.(send.BackspaceSender)
	if !ok {
		return errors.New("target cannot send backspace")
	}
	raw, err := g.CapturePaneFresh()
	if guardErr := g.checkAfterTyping(raw, err); guardErr != nil {
		return guardErr
	}
	return bs.SendBackspaces(n)
}
