package main

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Round-2 regression tests for issue #1855 on the launch recovery path.
//
// verifyPromptConsumedAfterLaunch is the v1.7.64 safety net for the
// welcome-screen race where claude eats the initial Enter of
// `agent-deck launch -m "…" --no-wait`. It exists because the first-layer
// budget on the launch path is only 8×150ms — far too short to observe that
// race on a cold start with MCPs.
//
// The #1855 transport change frames every multi-line body as a bracketed
// paste, so claude collapses the launch prompt behind a "[Pasted text …]"
// marker as its NORMAL delivered form. The recovery pass built its #1777
// attribution gate with no provenance:
//
//	attrib := send.EnterAttribution{Message: message}   // OwnPasteMarker false
//
// so the gate read agent-deck's own collapse as a foreign draft and withheld
// the one retry this function exists to make — permanently, for the common
// multi-line prompt shape. The caller had the provenance all along
// (composerPasteFree, launch_cmd.go) and simply did not pass it.
//
// Second half of the fix: when the composer holds our own marker the body has
// already arrived and only the Enter was lost, so the recovery must be a BARE
// Enter. Retyping the message would append a second copy of the prompt behind
// the marker and submit both.

// paneMarkerComposer renders a composer whose input line holds claude's
// collapsed-paste marker — the delivered-but-unsubmitted shape of a framed
// multi-line launch prompt.
func paneMarkerComposer() string {
	div := strings.Repeat("─", 25)
	return "welcome to claude\n" + div + "\n❯ [Pasted text #1 +3 lines]\n" + div + "\n"
}

// multiLineLaunchPrompt is the shape of every real `launch -m` prompt once the
// completion-sentinel instruction is appended.
const multiLineLaunchPrompt = "/superpowers:writing-skills\n" +
	"Write a skill for seeded data.\n\n## Final step\nPrint the sentinel when done."

// TestIssue1855_LaunchRecoveryNudgesItsOwnPasteMarker: with the pre-send
// provenance passed through, our own collapse marker is attributable, so the
// recovery fires — as a bare Enter, not a retype.
func TestIssue1855_LaunchRecoveryNudgesItsOwnPasteMarker(t *testing.T) {
	mock := &mockSendRetryTarget{panes: []string{paneMarkerComposer()}}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunchAttributed(
		mock, multiLineLaunchPrompt, true,
		5*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 1 {
		t.Fatalf("issue #1855: an attributable paste marker must be recovered with exactly one bare Enter, got %d", got)
	}
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 0 {
		t.Fatalf("issue #1855: the body already arrived behind the marker — retyping it would submit two copies of the prompt; got %d retypes", got)
	}
	if strings.Contains(warn.String(), "unattributable") {
		t.Fatalf("our own paste marker must not be reported as unattributable content: %q", warn.String())
	}
}

// TestIssue1855_LaunchRecoveryWithholdsForeignPasteMarker is the other
// direction, and the reason the provenance is a parameter rather than an
// assumption: with no pre-send evidence, a marker in the composer may be a
// foreign paste, and #1777 requires the retry to be withheld rather than
// submit text nobody authored.
func TestIssue1855_LaunchRecoveryWithholdsForeignPasteMarker(t *testing.T) {
	mock := &mockSendRetryTarget{panes: []string{paneMarkerComposer()}}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunchAttributed(
		mock, multiLineLaunchPrompt, false,
		5*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("#1777: an unattributable paste marker must never be nudged, got %d Enter presses", got)
	}
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 0 {
		t.Fatalf("#1777: an unattributable composer must not be retyped into, got %d retypes", got)
	}
	if !strings.Contains(warn.String(), "unattributable") {
		t.Fatalf("withholding the retry must be surfaced to the operator, got %q", warn.String())
	}
}

// TestIssue1855_LaunchRecoveryStillRetypesAnUnsentPlainPrompt pins that the
// bare-Enter branch is scoped to the marker case only: when the composer holds
// the prompt as plain text (a single-line prompt, which still takes the
// unframed send-keys path), the historical retype-and-submit recovery is
// unchanged.
func TestIssue1855_LaunchRecoveryStillRetypesAnUnsentPlainPrompt(t *testing.T) {
	const prompt = "explain the transport fork in detail"
	mock := &mockSendRetryTarget{panes: []string{paneUnsent(prompt)}}
	var warn bytes.Buffer

	verifyPromptConsumedAfterLaunchAttributed(
		mock, prompt, true,
		5*time.Millisecond, time.Millisecond, &warn)

	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("a plainly-visible unsent prompt must still be recovered by one retype, got %d", got)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("the bare-Enter branch must be scoped to the paste-marker case, got %d bare Enters", got)
	}
}
