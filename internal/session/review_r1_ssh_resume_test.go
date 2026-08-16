package session

import (
	"strings"
	"testing"
	"time"
)

// Review round 1, finding F1: SSH Claude sessions could no longer resume a
// remote conversation.
//
// The #1851 transcript boundary makes sessionHasConversationData return false
// for an --ssh session, which is the correct answer to the question it asks
// ("is there conversation data on THIS machine?"). But buildClaudeResumeCommand
// used that answer to choose between `--resume <id>` and `--session-id <id>`,
// so every remote restart asked claude to CREATE a session under an
// already-used UUID: the conversation is lost, or claude rejects the id.
//
// conversationIsResumable separates the two questions. For a remote session the
// controller's evidence is the id it recorded; the remote claude resolves it
// against the only disk that can answer.

// remoteClaudeSessionForResume builds an --ssh claude instance that previously
// ran and carries a conversation id, with no local transcript anywhere.
func remoteClaudeSessionForResume(t *testing.T, sessionID string) *Instance {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home+"/.claude")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	inst := NewInstanceWithTool("remote-resume", t.TempDir(), "claude")
	inst.SSHHost = "cloudlydr@remote-host"
	inst.SSHRemotePath = "/srv/app"
	inst.ClaudeSessionID = sessionID
	inst.ClaudeDetectedAt = time.Now()
	return inst
}

// TestBuildClaudeResumeCommand_RemoteSessionResumesItsConversation is the F1
// regression itself.
func TestBuildClaudeResumeCommand_RemoteSessionResumesItsConversation(t *testing.T) {
	const sessionID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	inst := remoteClaudeSessionForResume(t, sessionID)

	cmd := inst.buildClaudeResumeCommand()

	if !strings.Contains(cmd, "--resume "+sessionID) {
		t.Fatalf("a remote session with a recorded conversation id must resume it, not create a fresh session under an already-used UUID.\ngot: %s", cmd)
	}
	if strings.Contains(cmd, "--session-id "+sessionID) {
		t.Fatalf("remote restart emitted --session-id for an id that is already in use:\n%s", cmd)
	}
}

// TestBuildClaudeResumeCommand_RemoteWithNoConversationIDStartsFresh: the
// evidence is the recorded id, so with no id there is nothing to resume.
func TestBuildClaudeResumeCommand_RemoteWithNoConversationIDStartsFresh(t *testing.T) {
	inst := remoteClaudeSessionForResume(t, "")

	cmd := inst.buildClaudeResumeCommand()
	if strings.Contains(cmd, "--resume") {
		t.Fatalf("a remote session with no recorded conversation id has nothing to resume:\n%s", cmd)
	}
}

// TestBuildClaudeResumeCommand_LocalBehaviourUnchanged guards the local half:
// the fix must not turn "no conversation on disk" into a resume for a local
// session, which is the #662 / v1.7.6 behaviour the original check protects.
func TestBuildClaudeResumeCommand_LocalBehaviourUnchanged(t *testing.T) {
	const sessionID = "11111111-2222-3333-4444-555555555555"

	// A local session whose transcript does NOT exist must still start fresh.
	local, _, _, _ := remoteBoundaryFixture(t)
	local.ClaudeSessionID = "99999999-8888-7777-6666-555555555555" // no file for this id
	if conversationIsResumable(local, local.ClaudeSessionID) {
		t.Error("a LOCAL session with no conversation data on disk must not resume")
	}

	// ...and one whose transcript DOES exist must resume.
	local.ClaudeSessionID = sessionID
	if !conversationIsResumable(local, sessionID) {
		t.Error("a LOCAL session with conversation data on disk must resume")
	}
}

// TestConversationIsResumable_SeparatesTheTwoQuestions pins the distinction the
// fix rests on: the local-disk predicate stays honest (false for remote — the
// #1851 boundary) while the resume decision gets the SSH-aware answer.
func TestConversationIsResumable_SeparatesTheTwoQuestions(t *testing.T) {
	const sessionID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	remote := remoteClaudeSessionForResume(t, sessionID)

	if sessionHasConversationData(remote, sessionID) {
		t.Error("sessionHasConversationData must stay false for a remote session: it answers a LOCAL-disk question (#1851)")
	}
	if !conversationIsResumable(remote, sessionID) {
		t.Error("conversationIsResumable must be true for a remote session with a recorded id (F1)")
	}
	if conversationIsResumable(remote, "") {
		t.Error("no recorded id means nothing to resume, remote or not")
	}
	if conversationIsResumable(nil, "") {
		t.Error("nil instance with no id must not be resumable")
	}
}

// TestBuildClaudeCommandWithMessage_RemoteResumeModeResumes covers the sibling
// call site: `SessionMode == "resume"` had the identical bug.
func TestBuildClaudeCommandWithMessage_RemoteResumeModeResumes(t *testing.T) {
	const sessionID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	inst := remoteClaudeSessionForResume(t, sessionID)

	opts := inst.GetClaudeOptions()
	if opts == nil {
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}
	opts.SessionMode = "resume"
	opts.ResumeSessionID = sessionID
	if err := inst.SetClaudeOptions(opts); err != nil {
		t.Fatalf("SetClaudeOptions: %v", err)
	}

	cmd := inst.buildClaudeCommandWithMessage("claude", "")
	if !strings.Contains(cmd, "--resume "+sessionID) {
		t.Fatalf("resume-mode spawn for a remote session did not resume its conversation:\n%s", cmd)
	}
}

// TestSessionHasConversationData_HookGuardsStayConservative: the OTHER callers
// of sessionHasConversationData are "is this candidate id trustworthy?" guards
// protecting an established instance from a foreign ephemeral. A local-disk
// "no" is the correct conservative answer there and must NOT become "yes"
// through the resume-side fix.
func TestSessionHasConversationData_HookGuardsStayConservative(t *testing.T) {
	remote := remoteClaudeSessionForResume(t, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")

	if sessionHasConversationData(remote, "ffffffff-ffff-ffff-ffff-ffffffffffff") {
		t.Error("an unknown candidate id must not be credited with conversation data for a remote session")
	}
	if got := sessionConversationByteSize(remote, "ffffffff-ffff-ffff-ffff-ffffffffffff"); got != 0 {
		t.Errorf("sessionConversationByteSize = %d for a remote session, want 0", got)
	}
}
