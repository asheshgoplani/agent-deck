package session

import "testing"

// The "Resume from summary" picker only ever appears when claude is invoked
// with --resume on a long-running conversation. A brand-new session cannot
// show it, so polling for it there burns the full autoResumeOptions.Timeout
// (3s of CapturePaneFresh subprocesses) on every single `agent-deck launch`
// for nothing. These tests pin the gate that skips the poll on fresh starts.

func TestShouldAutoConfirmResumePicker_FreshStartSkipsPoll(t *testing.T) {
	i := &Instance{Tool: "claude"}
	// A fresh start never routed through buildClaudeResumeCommand.
	if i.shouldAutoConfirmResumePicker(true) {
		t.Fatal("fresh start must not poll for the resume picker: it cannot appear")
	}
}

func TestShouldAutoConfirmResumePicker_ResumedStartPolls(t *testing.T) {
	i := &Instance{Tool: "claude"}
	i.startedWithResume = true
	if !i.shouldAutoConfirmResumePicker(true) {
		t.Fatal("a resumed start must still poll: issue #67 depends on it")
	}
}

func TestShouldAutoConfirmResumePicker_ConfigOptOutWins(t *testing.T) {
	i := &Instance{Tool: "claude"}
	i.startedWithResume = true
	if i.shouldAutoConfirmResumePicker(false) {
		t.Fatal("[claude].auto_resume_summary=false must disable the poll even on resume")
	}
}

// buildClaudeResumeCommand runs for EVERY agent-deck launch, not just
// resuming ones: launch mints a UUID up front, so the helper emits a bare
// `claude --session-id <uuid>` when no transcript exists yet. Only the
// --resume form can raise the picker, so the marker tracks that, not the
// helper having run. This distinction is the whole fix — gating on "the
// resume helper ran" leaves the poll firing on every fresh launch.
func TestMarkResumeIntent_TracksActualResumeNotHelperRun(t *testing.T) {
	i := &Instance{Tool: "claude"}

	i.markResumeIntent(false) // `claude --session-id <uuid>`, no JSONL yet
	if i.shouldAutoConfirmResumePicker(true) {
		t.Fatal("a minted --session-id with no transcript cannot show the picker")
	}

	i.markResumeIntent(true) // `claude --resume <uuid>` over real history
	if !i.shouldAutoConfirmResumePicker(true) {
		t.Fatal("resuming an existing transcript must still poll (issue #67)")
	}
}

func TestBuildClaudeCommand_LeavesFreshStartUnmarked(t *testing.T) {
	i := &Instance{Tool: "claude", ProjectPath: t.TempDir()}
	_ = i.buildClaudeCommand("claude")
	if i.startedWithResume {
		t.Fatal("a fresh buildClaudeCommand must not mark the start as resuming")
	}
}

// A launch into a directory with no Claude history must leave the marker
// clear even though it routes through buildClaudeResumeCommand.
func TestBuildClaudeResumeCommand_NoTranscriptLeavesMarkerClear(t *testing.T) {
	i := &Instance{
		Tool:            "claude",
		ProjectPath:     t.TempDir(),
		ClaudeSessionID: "11111111-2222-3333-4444-555555555555",
	}
	_ = i.buildClaudeResumeCommand()
	if i.startedWithResume {
		t.Fatal("no JSONL on disk means no --resume and no possible picker")
	}
}

// consumeForkStartCommand emits `claude --session-id <new> --resume <parent>
// --fork-session`, which can show the picker, so it must mark too.
func TestConsumeForkStartCommand_MarksStartedWithResume(t *testing.T) {
	i := &Instance{
		Tool:                "claude",
		ProjectPath:         t.TempDir(),
		IsForkAwaitingStart: true,
		ForkStartCommand:    "claude --session-id new --resume parent --fork-session",
	}
	_ = i.consumeForkStartCommand()
	if !i.startedWithResume {
		t.Fatal("a fork start resumes the parent transcript and must mark as resuming")
	}
}

// A restart of a session that resumed once must not leave the marker set for
// a later fresh start of the same in-memory Instance.
func TestStartedWithResume_ClearedByFreshRebuild(t *testing.T) {
	i := &Instance{
		Tool:            "claude",
		ProjectPath:     t.TempDir(),
		ClaudeSessionID: "11111111-2222-3333-4444-555555555555",
	}
	_ = i.buildClaudeResumeCommand()
	i.resetResumeMarker()
	if i.startedWithResume {
		t.Fatal("resetResumeMarker must clear the marker before a new start decides")
	}
}
