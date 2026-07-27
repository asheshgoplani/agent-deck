package send

// EnterWouldSubmitForeignDraft reports whether a bare Enter pressed into the
// pane right now could submit composer content that agent-deck cannot
// positively attribute to its own in-flight delivery (issue #1777).
//
// The invariant it enforces: agent-deck must never cause text it did not
// receive from the operator to be submitted. A Claude autosuggestion can
// materialize in the composer as REAL, normal-coloured (`\e[39m`) unsubmitted
// input — indistinguishable from an operator draft by colour alone — and the
// send-verify loops' fallback "nudge Enter" presses would submit it as an
// instruction nobody authored. Before any bare Enter that is not a direct
// response to seeing agent-deck's own message parked in the composer, callers
// must consult this check and skip the press when it returns true.
//
// raw is the pane capture with ANSI attributes INTACT (tmux capture-pane -e —
// CapturePaneFresh already requests it); a plain capture strips the dim/grey
// attribute and makes ghost text look typed, so detection on stripped content
// is unreliable by construction. strip removes ANSI for text extraction (pass
// tmux.StripANSI; nil means identity). message is the payload agent-deck
// itself just typed — content matching it is attributable and safe to nudge.
//
// Failure semantics are deliberately asymmetric: returning true only skips a
// recovery nudge (the delivery verify then reports the send unconfirmed —
// recoverable), while a wrong false submits foreign text (unrecoverable). A
// pane with no introspectable composer returns false: there is nothing
// attributable to protect and the nudge paths exist precisely for those
// agents.
func EnterWouldSubmitForeignDraft(raw string, strip func(string) string, message string) bool {
	if strip == nil {
		strip = func(s string) string { return s }
	}
	draft, visible := ComposerDraft(raw, strip)
	if !visible || draft == "" {
		// Empty composer, suggestion, or placeholder: Enter submits nothing.
		return false
	}
	if message != "" && HasUnsentComposerPrompt(strip(raw), message) {
		// The composer holds agent-deck's own payload: attributable.
		return false
	}
	return true
}
