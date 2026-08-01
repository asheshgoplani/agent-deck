package send

import "testing"

// Issue #1777: EnterWouldSubmitForeignDraft is the attribution gate every bare
// Enter nudge must consult. Fixtures shared with suggestion_test.go.

func TestEnterWouldSubmitForeignDraft_MaterializedSuggestionBlocksEnter(t *testing.T) {
	// The defect case: a suggestion materialized as real, normal-coloured
	// (\e[39m) unsubmitted input. agent-deck did not place it there, so a
	// bare Enter would submit an instruction nobody authored.
	if !EnterWouldSubmitForeignDraft(pane(fixtureMaterializedComposer), stripANSI, "our automated message") {
		t.Fatal("normal-coloured foreign composer content must block a bare Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_OperatorDraftBlocksEnter(t *testing.T) {
	if !EnterWouldSubmitForeignDraft(pane(fixtureRealDraftComposer), stripANSI, "our automated message") {
		t.Fatal("an operator draft must block a bare Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_OwnMessageIsAttributable(t *testing.T) {
	// The composer holding the payload agent-deck itself typed is exactly the
	// state the recovery Enter exists for.
	line := "\x1b[39m❯ our automated message"
	if EnterWouldSubmitForeignDraft(pane(line), stripANSI, "our automated message") {
		t.Fatal("agent-deck's own parked message must not block the recovery Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_EmptyAndGhostComposersAreSafe(t *testing.T) {
	for name, fixture := range map[string]string{
		"empty":     fixtureEmptyComposer,
		"dim ghost": fixtureGhostComposer,
		"grey 90":   fixtureGreyBrightBlackComposer,
		"grey 256":  fixtureGrey256Composer,
	} {
		if EnterWouldSubmitForeignDraft(pane(fixture), stripANSI, "our automated message") {
			t.Fatalf("%s composer must not block a nudge — Enter submits nothing", name)
		}
	}
}

func TestEnterWouldSubmitForeignDraft_NoComposerIsSafe(t *testing.T) {
	// Panes without composer introspection (codex/cursor) keep their existing
	// bounded blind-Enter behavior.
	if EnterWouldSubmitForeignDraft("codex>\nplain output\n", stripANSI, "msg") {
		t.Fatal("a pane without a composer must not block the nudge paths")
	}
}

// Issue #1777 follow-up: the pasted-text branch of the send-verify loops used
// to bypass the gate entirely — a whole-pane "[pasted text" match short-
// circuited it to false and pressed Enter unconditionally. Claude collapses a
// bulk paste behind that marker, so the composer body cannot be matched
// against our payload by content; attribution is by provenance instead.

func TestEnterWouldSubmitForeignDraft_ForeignPasteMarkerBlocksEnter(t *testing.T) {
	// No pre-send evidence: the marker may be a paste the operator (or
	// anything else) parked in the composer, so Enter must be withheld.
	composer := "\x1b[39m❯ [Pasted text #1 +89 lines]"
	if !EnterWouldSubmitForeignDraft(pane(composer), stripANSI, "our automated message") {
		t.Fatal("a composer paste marker with no provenance evidence must block a bare Enter")
	}
	if !(EnterAttribution{Message: "our automated message"}).EnterWouldSubmitForeignDraft(pane(composer), stripANSI) {
		t.Fatal("EnterAttribution zero value must fail safe on a composer paste marker")
	}
}

func TestEnterWouldSubmitForeignDraft_OwnPasteMarkerIsAttributable(t *testing.T) {
	// The sender observed an unmarked composer immediately before typing, so
	// the marker is the collapsed form of its own payload: the recovery Enter
	// for a swallowed submit must still fire.
	attrib := EnterAttribution{Message: "a very long automated message", OwnPasteMarker: true}
	if attrib.EnterWouldSubmitForeignDraft(pane("\x1b[39m❯ [Pasted text #1 +89 lines]"), stripANSI) {
		t.Fatal("a paste marker created by our own send must not block the recovery Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_OwnPasteEvidenceDoesNotExcuseTypedForeignText(t *testing.T) {
	// Provenance evidence is scoped to the paste marker only: a materialized
	// suggestion sitting in the composer stays blocked.
	attrib := EnterAttribution{Message: "our automated message", OwnPasteMarker: true}
	if !attrib.EnterWouldSubmitForeignDraft(pane(fixtureMaterializedComposer), stripANSI) {
		t.Fatal("paste provenance must not unblock non-paste foreign composer content")
	}
}

func TestComposerHoldsPasteMarker_IsComposerScopedNotWholePane(t *testing.T) {
	// A marker in submitted scrollback is history, not a parked draft: it must
	// not poison the pre-send provenance probe (that would permanently disable
	// nudges for panes that ever received a large paste).
	history := "\x1b[38;5;239m\x1b[48;5;237m❯ \x1b[38;5;231m[Pasted text #1 +89 lines]\x1b[39m"
	raw := "some prior output\n" + history + "\n\x1b[39m❯ "
	if ComposerHoldsPasteMarker(raw, stripANSI) {
		t.Fatal("a paste marker in scrollback must not count as a parked composer marker")
	}
	if !ComposerHoldsPasteMarker(pane("\x1b[39m❯ [Pasted text #2 +12 lines]"), stripANSI) {
		t.Fatal("a paste marker in the visible composer must be reported")
	}
	if !HasUnsentPastedPrompt(stripANSI(raw)) {
		t.Fatal("fixture guard: the whole-pane check is expected to match here — that is the imprecision being avoided")
	}
}

func TestNudgeEnter_PressesOnlyWhenAttributable(t *testing.T) {
	presses := 0
	presser := enterPresserFunc(func() error { presses++; return nil })

	attrib := EnterAttribution{Message: "our automated message"}
	if attrib.NudgeEnter(presser, pane(fixtureMaterializedComposer), stripANSI) {
		t.Fatal("NudgeEnter must not press into foreign composer content")
	}
	if attrib.NudgeEnter(presser, pane("\x1b[39m❯ [Pasted text #1 +89 lines]"), stripANSI) {
		t.Fatal("NudgeEnter must not press into an unattributable paste marker")
	}
	if presses != 0 {
		t.Fatalf("expected no Enter presses, got %d", presses)
	}
	if !attrib.NudgeEnter(presser, pane(fixtureEmptyComposer), stripANSI) || presses != 1 {
		t.Fatalf("NudgeEnter must press when the composer is empty (presses=%d)", presses)
	}
}

type enterPresserFunc func() error

func (f enterPresserFunc) SendEnter() error { return f() }
