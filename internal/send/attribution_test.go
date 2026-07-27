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
