package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue1830_TUICreateResumeByIDVouchesOwnership pins a review finding on
// #1830: the TUI options panel lets the operator type a conversation UUID
// into the "resume by session ID" field (ClaudeOptions.SessionMode ==
// "resume" / ResumeSessionID). That landed ONLY inside ToolOptionsJSON --
// the create path never assigned it to inst.ClaudeSessionID or vouched for
// it. The resume-time chokepoint (canResumeClaudeSession) never looks
// inside ToolOptionsJSON, so recordedClaudeSessionID() was always empty and
// the operator's explicit choice was silently discarded for a freshly
// minted id.
//
// The fix mirrors the CLI's --resume-session handling (launch_cmd.go /
// main.go): a non-empty operator-supplied ResumeSessionID is an ownership
// declaration, so it must land on ClaudeSessionID AND be vouched via
// session.MarkClaudeSessionIDVerified before the session ever reaches the
// resume chokepoint.
func TestIssue1830_TUICreateResumeByIDVouchesOwnership(t *testing.T) {
	const wantID = "a1a1a1a1-2222-4333-8444-555555555555"

	opts := session.NewClaudeOptions(nil)
	opts.SessionMode = "resume"
	opts.ResumeSessionID = wantID
	toolOptionsJSON, err := session.MarshalToolOptions(opts)
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}

	h := &Home{}
	msg := h.createSessionInGroupWithWorktreeAndOptions(
		"resume-by-id-test",
		t.TempDir(),
		"claude",
		"test",
		"", "", "",
		false,
		false,
		toolOptionsJSON,
		nil,
		"",
		"",
		false,
		nil,
		"", "",
		"",
		false,
	)().(sessionCreatedMsg)
	if msg.err != nil {
		t.Fatalf("create session: %v", msg.err)
	}
	inst := msg.instance
	t.Cleanup(func() {
		if err := inst.KillAndWait(); err != nil {
			t.Errorf("cleanup session: %v", err)
		}
	})

	if inst.ClaudeSessionID != wantID {
		t.Fatalf("ClaudeSessionID = %q, want the operator-typed id %q -- the resume-by-id panel entry was discarded",
			inst.ClaudeSessionID, wantID)
	}
	allow, reason := session.ResumeIdentityAllowed(inst, wantID)
	if !allow {
		t.Fatalf("ResumeIdentityAllowed(%q) = false (reason=%q), want true: an operator-typed resume id must be vouched, not left tainted/unrecorded", wantID, reason)
	}
}
