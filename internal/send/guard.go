package send

import (
	"strings"
	"time"
)

// IsComposerPlaceholder reports whether the visible composer text is Claude's
// idle-suggestion placeholder rather than operator input. Claude renders hint
// suggestions in the empty composer, e.g.:
//
//	❯ Try "write a test for <filepath>"
//
// Treating these as operator drafts would make every automated send hold and
// Ctrl+C an actually-empty composer (issue #1409).
func IsComposerPlaceholder(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, `Try "`) && strings.HasSuffix(t, `"`)
}

// ComposerDraft returns the normalized operator draft sitting in the visible
// composer, and whether a composer is visible at all.
//
// raw must be the pane capture with ANSI attributes INTACT (tmux capture-pane
// -e, which CapturePaneFresh already requests). The SGR dim attribute is the
// only thing distinguishing Claude's prompt autosuggestion from real operator
// input, so stripping before this call loses the discriminator. strip removes
// ANSI for text extraction only (pass tmux.StripANSI; nil means identity).
//
// Both of Claude's non-input composer states report an empty draft:
//
//	❯ Try "write a test for <filepath>"     idle hint (plain text)
//	❯ <ESC>[2mrun the tests again<ESC>[0m   autosuggestion (dim)
func ComposerDraft(raw string, strip func(string) string) (draft string, composerVisible bool) {
	if strip == nil {
		strip = func(s string) string { return s }
	}
	// Checked against the raw bytes: a suggestion is not content, so it is
	// never saved, cleared or restored.
	if ComposerBodyIsSuggestion(raw) {
		return "", true
	}
	body, ok := CurrentComposerPrompt(strip(raw))
	if !ok {
		return "", false
	}
	body = NormalizePromptText(body)
	if IsComposerPlaceholder(body) {
		return "", true
	}
	return body, true
}

// ComposerHasDraft reports whether the visible composer holds operator input.
// This is the shared "is the composer busy?" check automated senders must run
// before injecting keystrokes into the pane (issue #1409). Same raw/strip
// contract as ComposerDraft.
func ComposerHasDraft(raw string, strip func(string) string) bool {
	draft, visible := ComposerDraft(raw, strip)
	return visible && draft != ""
}

// ComposerGuardTarget is the minimal pane surface GuardComposerDraft needs to
// hold an automated send while an operator draft occupies the composer.
// *tmux.Session satisfies it.
type ComposerGuardTarget interface {
	CapturePaneFresh() (string, error)
}

// ComposerGuardOptions tunes GuardComposerDraft. All bounds are mandatory so
// the guard can never hold a delivery indefinitely.
type ComposerGuardOptions struct {
	// HoldWait is the maximum time to wait for an operator draft to clear on
	// its own (operator submits or erases it) before refusing delivery.
	HoldWait time.Duration
	// PollInterval is the capture cadence during the hold phase.
	// Defaults to 250ms when <= 0.
	PollInterval time.Duration
	// ClearWait is retained for caller compatibility. The guard never clears
	// operator input, so this value is ignored.
	ClearWait time.Duration
	// Strip is applied to raw captured pane content before composer
	// introspection (pass tmux.StripANSI). nil means identity.
	Strip func(string) string
}

// ComposerGuardResult reports what the guard did.
type ComposerGuardResult struct {
	// Held is the total wall-clock time the guard spent before returning.
	Held time.Duration
	// Refused means the composer remains occupied or could not be captured.
	// Callers must return without typing or pressing Enter.
	Refused bool
	// Legacy delivery metadata remains zero: operator input is preserved in
	// place, never saved, cleared, or restored by automated delivery.
	SavedDraft   string
	DraftCleared bool
	ClearFailed  bool
	// ComposerPasteMarkerFree is true when the guard's LAST successful
	// capture showed a composer holding no "[Pasted text …]" marker. It is
	// the pre-send provenance evidence the attribution gate needs to tell
	// agent-deck's own collapsed paste apart from a foreign one parked in the
	// composer (issue #1777): with no marker there before the send, a marker
	// seen afterwards can only be the one our own paste created. False
	// whenever the guard could not establish that (capture failure, or a
	// marker still present), which fails safe — the gate then withholds the
	// Enter nudge.
	ComposerPasteMarkerFree bool
}

// saveReconfirmDelay is the settle time before the save-step re-capture. A
// suggestion sampled in the sub-frame where its text is painted but the dim
// SGR has not landed reads as an operator draft (issue #1777); one frame of
// settle is enough for the attribute to land before re-classification.
const saveReconfirmDelay = 50 * time.Millisecond

// composerProvenanceFree reports whether raw is safe pre-send evidence that
// no foreign paste marker is parked in the composer — the same guarantee
// ComposerPasteMarkerFree promises callers (issue #1777 provenance).
//
// A VISIBLE, EMPTY composer proves it directly. An UNSCOPABLE pane
// (!visible — codex/cursor, or a transiently unreadable Claude pane) must
// NOT be folded into that case: ComposerHoldsPasteMarker makes the OPPOSITE
// call on purpose for !visible, falling back to a whole-pane scan, because
// "no composer to scope to" yields no usable provenance on its own. Before
// this fix GuardComposerDraft (the producer for the highest-volume send
// path) granted provenance on any !visible read regardless, disagreeing
// with ComposerHoldsPasteMarker on the identical pane state and letting a
// foreign marker that renders later be misattributed as ours (#1778 review
// finding 2). Mirror ComposerHoldsPasteMarker's choice here so the two
// producers of this evidence never disagree.
func composerProvenanceFree(raw string, strip func(string) string) bool {
	draft, visible := ComposerDraft(raw, strip)
	if visible {
		return draft == ""
	}
	return !ComposerHoldsPasteMarker(raw, strip)
}

// GuardComposerDraft holds an automated send while operator input occupies
// the composer. At the bound it rechecks once for an autosuggestion redraw,
// then refuses delivery without changing the draft. Capture failure also
// refuses delivery. It never sends an interrupt or other keystroke.
func GuardComposerDraft(t ComposerGuardTarget, opts ComposerGuardOptions) ComposerGuardResult {
	strip := opts.Strip
	if strip == nil {
		strip = func(s string) string { return s }
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}

	start := time.Now()
	deadline := start.Add(opts.HoldWait)

	for {
		raw, err := t.CapturePaneFresh()
		if err != nil {
			// An unreadable pane cannot authorize input.
			return ComposerGuardResult{Held: time.Since(start), Refused: true}
		}
		if composerProvenanceFree(raw, strip) {
			return ComposerGuardResult{Held: time.Since(start), ComposerPasteMarkerFree: true}
		}
		if !time.Now().Before(deadline) {
			break
		}
		sleepFor := poll
		if remaining := time.Until(deadline); remaining < sleepFor {
			sleepFor = remaining
		}
		if sleepFor > 0 {
			time.Sleep(sleepFor)
		}
	}

	// A redraw can paint suggestion text before its dim attributes arrive.
	// Reclassify one settled frame without modifying the composer.
	time.Sleep(saveReconfirmDelay)
	raw, err := t.CapturePaneFresh()
	if err == nil && composerProvenanceFree(raw, strip) {
		return ComposerGuardResult{Held: time.Since(start), ComposerPasteMarkerFree: true}
	}
	return ComposerGuardResult{Held: time.Since(start), Refused: true}
}
