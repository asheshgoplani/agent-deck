package send

// EnterAttribution is the single chokepoint every automated bare-Enter press
// must route through (issue #1777).
//
// The invariant it enforces: agent-deck must never cause text it did not
// receive from the operator to be submitted. A Claude autosuggestion can
// materialize in the composer as REAL, normal-coloured (`\e[39m`) unsubmitted
// input — indistinguishable from an operator draft by colour alone — and the
// send-verify loops' fallback "nudge Enter" presses would submit it as an
// instruction nobody authored. So the rule is inverted from "block what looks
// like a ghost" to "press only what is positively attributable": an empty
// composer, a suggestion/placeholder, the message agent-deck itself just
// typed, or that message collapsed behind Claude's paste marker with
// provenance evidence (OwnPasteMarker) that the collapse is ours.
//
// Failure semantics are deliberately asymmetric: withholding a nudge only
// leaves a delivery unconfirmed (the verify then classifies it — #1413
// semantics — recoverable), while a wrong press submits foreign text
// (unrecoverable). Every field therefore fails safe at its zero value.
type EnterAttribution struct {
	// Message is the payload agent-deck itself typed into this pane. Composer
	// content matching it is attributable and safe to nudge.
	Message string

	// OwnPasteMarker records that a "[Pasted text #N +M lines]" marker found
	// in the composer AFTER the send is the collapsed rendering of Message.
	// Claude hides a bulk paste behind that marker, so the composer body
	// cannot be matched against Message by content and provenance is the only
	// available evidence. Callers set it true only when they positively
	// observed, immediately before typing, a composer holding no paste marker
	// (see ComposerHoldsPasteMarker): a marker seen afterwards can then only
	// be the one their own paste created.
	//
	// Left false (unknown provenance, capture failure, no plumbing) a
	// composer paste marker counts as foreign and the Enter is withheld.
	OwnPasteMarker bool
}

// EnterPresser is the minimal pane surface NudgeEnter needs. Both
// *tmux.Session and the send-verify loops' targets satisfy it.
type EnterPresser interface {
	SendEnter() error
}

// NudgeEnter presses Enter only when the pane's current composer content is
// attributable to agent-deck's own in-flight delivery. It reports whether the
// press was made.
//
// This is the chokepoint: automated recovery paths must call NudgeEnter
// instead of SendEnter directly, so no future branch can reintroduce an
// ungated press. raw is this iteration's pane capture with ANSI attributes
// INTACT ("" when the capture failed); strip removes ANSI for text extraction
// (pass tmux.StripANSI; nil means identity).
func (a EnterAttribution) NudgeEnter(p EnterPresser, raw string, strip func(string) string) bool {
	if p == nil || a.EnterWouldSubmitForeignDraft(raw, strip) {
		return false
	}
	return p.SendEnter() == nil
}

// EnterWouldSubmitForeignDraft reports whether a bare Enter pressed into the
// pane right now could submit composer content that agent-deck cannot
// positively attribute to its own in-flight delivery.
//
// raw must be the pane capture with ANSI attributes INTACT (tmux capture-pane
// -e — CapturePaneFresh already requests it); a plain capture strips the
// dim/grey attribute and makes ghost text look typed, so colour classification
// must happen pre-strip. The gate additionally treats even normal-coloured
// foreign content as unsubmittable, so colour is no longer load-bearing for
// safety. strip removes ANSI for text extraction (nil means identity).
//
// A pane with no introspectable composer returns false: there is nothing
// attributable to protect and the nudge paths exist precisely for those
// agents.
func (a EnterAttribution) EnterWouldSubmitForeignDraft(raw string, strip func(string) string) bool {
	strip = orIdentity(strip)
	draft, visible := ComposerDraft(raw, strip)
	if !visible || draft == "" {
		// Empty composer, suggestion, or placeholder: Enter submits nothing.
		return false
	}
	if a.Message != "" && HasUnsentComposerPrompt(strip(raw), a.Message) {
		// The composer holds agent-deck's own payload verbatim: attributable.
		return false
	}
	if a.OwnPasteMarker && HasUnsentPastedPrompt(draft) {
		// The composer holds a paste marker and the caller established that
		// no marker was parked there before it typed: the collapse is ours.
		return false
	}
	return true
}

// EnterWouldSubmitForeignDraft is the provenance-free form of the gate: with
// no evidence about who created a composer paste marker, the marker counts as
// foreign. Callers that can supply provenance use EnterAttribution directly.
func EnterWouldSubmitForeignDraft(raw string, strip func(string) string, message string) bool {
	return EnterAttribution{Message: message}.EnterWouldSubmitForeignDraft(raw, strip)
}

// ComposerHoldsPasteMarker reports whether the VISIBLE COMPOSER — not the
// pane's scrollback, which the whole-pane HasUnsentPastedPrompt would also
// match — currently holds Claude's "[Pasted text #N +M lines]" marker.
// Automated senders call it on a pre-send capture to establish
// EnterAttribution.OwnPasteMarker: a composer with no marker before the send
// means any marker afterwards is the collapsed form of their own payload.
func ComposerHoldsPasteMarker(raw string, strip func(string) string) bool {
	strip = orIdentity(strip)
	draft, visible := ComposerDraft(raw, strip)
	return visible && HasUnsentPastedPrompt(draft)
}

func orIdentity(strip func(string) string) func(string) string {
	if strip == nil {
		return func(s string) string { return s }
	}
	return strip
}
