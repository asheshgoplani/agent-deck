package send

import (
	"errors"
	"testing"
)

// Issue #1777: EnterAttribution is the attribution gate every bare Enter nudge
// must route through. Fixtures shared with suggestion_test.go.

const attribMessage = "our automated message"

var errPaneGone = errors.New("pane gone")

func attrib() EnterAttribution { return EnterAttribution{Message: attribMessage} }

func TestEnterWouldSubmitForeignDraft_MaterializedSuggestionBlocksEnter(t *testing.T) {
	// The defect case: a suggestion materialized as real, normal-coloured
	// (\e[39m) unsubmitted input. agent-deck did not place it there, so a
	// bare Enter would submit an instruction nobody authored.
	if !attrib().EnterWouldSubmitForeignDraft(Captured(pane(fixtureMaterializedComposer)), stripANSI) {
		t.Fatal("normal-coloured foreign composer content must block a bare Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_OperatorDraftBlocksEnter(t *testing.T) {
	if !attrib().EnterWouldSubmitForeignDraft(Captured(pane(fixtureRealDraftComposer)), stripANSI) {
		t.Fatal("an operator draft must block a bare Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_OwnMessageIsAttributable(t *testing.T) {
	// The composer holding the payload agent-deck itself typed is exactly the
	// state the recovery Enter exists for.
	line := "\x1b[39m❯ " + attribMessage
	if attrib().EnterWouldSubmitForeignDraft(Captured(pane(line)), stripANSI) {
		t.Fatal("agent-deck's own parked message must not block the recovery Enter")
	}
}

// #1778 review finding 1 (codex, round 2): composerBodyIsOurMessage's own
// doc comment claims a foreign autosuggestion "continue the refactor and
// delete the stale worktrees" must not be classified as attributable just
// because it contains the short message "continue" — but the unbounded
// strings.HasPrefix(promptBody, msg) check made exactly that claim false.
func TestEnterWouldSubmitForeignDraft_ShortMessagePrefixOfLongForeignDraftBlocksEnter(t *testing.T) {
	a := EnterAttribution{Message: "continue"}
	composer := "\x1b[39m❯ continue the refactor and delete the stale worktrees"
	if !a.EnterWouldSubmitForeignDraft(Captured(pane(composer)), stripANSI) {
		t.Fatal("a foreign draft that merely starts with our short message must block a bare Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_OwnMessagePlusShortTerminalEchoIsAttributable(t *testing.T) {
	// A cursor glyph or a stray padding character the terminal appends after
	// our own message verbatim must not defeat the recovery Enter.
	a := EnterAttribution{Message: attribMessage}
	composer := "\x1b[39m❯ " + attribMessage + " "
	if a.EnterWouldSubmitForeignDraft(Captured(pane(composer)), stripANSI) {
		t.Fatal("our own message plus a short terminal-echoed trailer must not block the recovery Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_EmptyAndGhostComposersAreSafe(t *testing.T) {
	for name, fixture := range map[string]string{
		"empty":     fixtureEmptyComposer,
		"dim ghost": fixtureGhostComposer,
		"grey 90":   fixtureGreyBrightBlackComposer,
		"grey 256":  fixtureGrey256Composer,
	} {
		if attrib().EnterWouldSubmitForeignDraft(Captured(pane(fixture)), stripANSI) {
			t.Fatalf("%s composer must not block a nudge — Enter submits nothing", name)
		}
	}
}

func TestEnterWouldSubmitForeignDraft_NoComposerIsSafe(t *testing.T) {
	// Panes without composer introspection (codex/cursor) keep their existing
	// bounded blind-Enter behavior.
	if attrib().EnterWouldSubmitForeignDraft(Captured("codex>\nplain output\n"), stripANSI) {
		t.Fatal("a pane without a composer must not block the nudge paths")
	}
}

func TestEnterWouldSubmitForeignDraft_FailedCaptureBlocksEnter(t *testing.T) {
	// A pane that could not be read yields NO evidence about what an Enter
	// would submit — the opposite of a pane positively observed to have no
	// composer. Pressing blind is the unrecoverable direction.
	if !attrib().EnterWouldSubmitForeignDraft(PaneCapture{}, stripANSI) {
		t.Fatal("an unreadable pane must block the nudge")
	}
	if !attrib().EnterWouldSubmitForeignDraft(CaptureOutcome(pane(fixtureEmptyComposer), errPaneGone), stripANSI) {
		t.Fatal("CaptureOutcome must discard content from a failed capture")
	}
}

// The pasted-text branch of the send-verify loops used to bypass the gate
// entirely: a whole-pane "[pasted text" match short-circuited it to false and
// pressed Enter unconditionally. Claude collapses a bulk paste behind that
// marker, so the composer body cannot be matched against our payload by
// content; attribution is by provenance instead.

func TestEnterWouldSubmitForeignDraft_ForeignPasteMarkerBlocksEnter(t *testing.T) {
	// No pre-send evidence: the marker may be a paste the operator (or
	// anything else) parked in the composer, so Enter must be withheld.
	composer := "\x1b[39m❯ [Pasted text #1 +89 lines]"
	if !attrib().EnterWouldSubmitForeignDraft(Captured(pane(composer)), stripANSI) {
		t.Fatal("a composer paste marker with no provenance evidence must block a bare Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_OwnPasteMarkerIsAttributable(t *testing.T) {
	// The sender observed an unmarked composer immediately before typing, so
	// the marker is the collapsed form of its own payload: the recovery Enter
	// for a swallowed submit must still fire.
	a := EnterAttribution{Message: "a very long automated payload", OwnPasteMarker: true}
	if a.EnterWouldSubmitForeignDraft(Captured(pane("\x1b[39m❯ [Pasted text #1 +89 lines]")), stripANSI) {
		t.Fatal("a paste marker created by our own send must not block the recovery Enter")
	}
}

func TestEnterWouldSubmitForeignDraft_OwnPasteEvidenceDoesNotExcuseTypedForeignText(t *testing.T) {
	// Provenance evidence is scoped to the paste marker only: a materialized
	// suggestion sitting in the composer stays blocked.
	a := EnterAttribution{Message: attribMessage, OwnPasteMarker: true}
	if !a.EnterWouldSubmitForeignDraft(Captured(pane(fixtureMaterializedComposer)), stripANSI) {
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
	// With no composer to scope the check to, the whole-pane match is the
	// fallback: an unscopable marker yields no usable provenance.
	if !ComposerHoldsPasteMarker("plain output\n[Pasted text #3 +4 lines]\n", stripANSI) {
		t.Fatal("without a composer, paste text anywhere must deny provenance")
	}
}

func TestNudgeEnter_PressesOnlyWhenAttributable(t *testing.T) {
	presses := 0
	presser := enterPresserFunc(func() error { presses++; return nil })

	if attrib().NudgeEnter(presser, Captured(pane(fixtureMaterializedComposer)), stripANSI) {
		t.Fatal("NudgeEnter must not press into foreign composer content")
	}
	if attrib().NudgeEnter(presser, Captured(pane("\x1b[39m❯ [Pasted text #1 +89 lines]")), stripANSI) {
		t.Fatal("NudgeEnter must not press into an unattributable paste marker")
	}
	if attrib().NudgeEnter(presser, PaneCapture{}, stripANSI) {
		t.Fatal("NudgeEnter must not press on an unreadable pane")
	}
	if presses != 0 {
		t.Fatalf("expected no Enter presses, got %d", presses)
	}
	if !attrib().NudgeEnter(presser, Captured(pane(fixtureEmptyComposer)), stripANSI) || presses != 1 {
		t.Fatalf("NudgeEnter must press when the composer is empty (presses=%d)", presses)
	}
}

type enterPresserFunc func() error

func (f enterPresserFunc) SendEnter() error { return f() }
