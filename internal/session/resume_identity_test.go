package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Resume-identity guard suite (#1815).
//
// Incident shape being pinned: a session whose own conversation id had been
// lost (an account switch reported "no conversation to migrate, fresh
// session") was restarted. The restart's disk-discovery prelude returned the
// newest transcript filed under the working directory — one belonging to a
// DIFFERENT session — and the restart resumed it. The restarted pane came up
// as a live second instance of that other session, carrying its context and
// its authority.
//
// Contract: at the moment a `--resume` is assembled, the id must match the
// session's OWN recorded conversation id. No recorded id, or a mismatch,
// means start fresh — and the refused id is not reused via --session-id
// either, since it may belong to another session.

// stageConversation writes a conversation jsonl for sessionID under home's
// Claude projects dir for projectPath and returns its path.
func stageConversation(t *testing.T, home, projectPath, sessionID string) string {
	t.Helper()
	dir := claudeProjectDirForTest(t, filepath.Join(home, ".claude"), projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	body := `{"type":"user","sessionId":"` + sessionID + `","text":"hello"}` + "\n" +
		`{"type":"assistant","sessionId":"` + sessionID + `","text":"hi"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	return path
}

func newGuardInstance(t *testing.T, home string) *Instance {
	t.Helper()
	projectPath := filepath.Join(home, "shared-project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("guard-test", projectPath, "claude")
	inst.ClaudeSessionID = ""
	return inst
}

// TestResumeGuard_DiscoveredForeignTranscriptIsNotResumed is the incident
// itself: session A has NO recorded conversation id, and disk discovery offers
// session B's transcript from the shared working directory. The restart must
// NOT resume B — and must not claim B's id via --session-id either.
func TestResumeGuard_DiscoveredForeignTranscriptIsNotResumed(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const foreignID = "bbbbbbbb-2222-4333-8444-555555555555"
	stageConversation(t, home, inst.ProjectPath, foreignID)

	// Restart() implies the session previously ran.
	inst.ClaudeDetectedAt = time.Now()
	inst.ensureClaudeSessionIDFromDiskForRestart()

	if inst.ClaudeSessionID != foreignID {
		t.Fatalf("precondition: discovery should surface the only transcript on disk; got %q", inst.ClaudeSessionID)
	}
	if got := inst.recordedClaudeSessionID(); got != "" {
		t.Fatalf("a discovered id must not count as a RECORDED (owned) id; recordedClaudeSessionID = %q", got)
	}

	cmd := inst.buildClaudeResumeCommand()

	if strings.Contains(cmd, "--resume") {
		t.Fatalf("#1815: restart must NOT --resume a transcript this session does not own.\ncommand: %s", cmd)
	}
	if strings.Contains(cmd, foreignID) {
		t.Fatalf("#1815: the refused id belongs to another session and must not be reused via --session-id either.\ncommand: %s", cmd)
	}
	if !strings.Contains(cmd, "--session-id ") {
		t.Fatalf("refusal must start a fresh session via --session-id.\ncommand: %s", cmd)
	}
	if inst.ClaudeSessionID == foreignID || inst.ClaudeSessionID == "" {
		t.Fatalf("instance must carry a freshly minted id after refusal; got %q", inst.ClaudeSessionID)
	}
	if inst.recordedClaudeSessionID() != inst.ClaudeSessionID {
		t.Fatalf("the freshly minted id is this session's own and must be recorded as verified")
	}
}

// TestResumeGuard_MissingRecordedUUIDRefuses pins the rule directly: with no
// recorded conversation id, no candidate may be resumed.
func TestResumeGuard_MissingRecordedUUIDRefuses(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const candidate = "cccccccc-3333-4444-8555-666666666666"
	stageConversation(t, home, inst.ProjectPath, candidate)
	inst.adoptDiscoveredClaudeSessionID(candidate)

	if decision := inst.resumeIdentityAllowed(candidate); decision.Allow {
		t.Fatalf("resume must be refused when no recorded conversation id exists (reason=%s)", decision.Reason)
	} else if decision.Reason != "no_recorded_session_id" {
		t.Fatalf("reason = %q, want no_recorded_session_id", decision.Reason)
	}
	if canResumeClaudeSession(inst, candidate) {
		t.Fatal("chokepoint must refuse even though the transcript exists and has conversation data")
	}
}

// TestResumeGuard_IdentityMismatchRefuses covers the other refusal: a recorded
// id exists, but the candidate about to be resumed is a different conversation.
func TestResumeGuard_IdentityMismatchRefuses(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const ownID = "11111111-1111-4111-8111-111111111111"
	const otherID = "99999999-9999-4999-8999-999999999999"
	stageConversation(t, home, inst.ProjectPath, ownID)
	stageConversation(t, home, inst.ProjectPath, otherID)
	inst.ClaudeSessionID = ownID

	if canResumeClaudeSession(inst, otherID) {
		t.Fatal("#1815: a candidate that is not this session's recorded conversation must be refused")
	}
	if !canResumeClaudeSession(inst, ownID) {
		t.Fatal("the session's own conversation must still resume normally")
	}
}

// TestResumeGuard_MatchingIDStillResumes is the no-regression case, including
// the prefix spelling the Claude CLI accepts.
func TestResumeGuard_MatchingIDStillResumes(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const ownID = "abcdef01-2345-4678-8abc-def012345678"
	stageConversation(t, home, inst.ProjectPath, ownID)
	inst.ClaudeSessionID = ownID

	if !canResumeClaudeSession(inst, ownID) {
		t.Fatal("exact id match must resume")
	}
	if decision := inst.resumeIdentityAllowed(ownID[:8]); !decision.Allow {
		t.Fatalf("id PREFIX form must be accepted (the CLI resolves --resume by prefix); reason=%s", decision.Reason)
	}

	cmd := inst.buildClaudeResumeCommand()
	if !strings.Contains(cmd, "--resume "+ownID) {
		t.Fatalf("a verified, present conversation must still produce --resume <own id>.\ncommand: %s", cmd)
	}
}

// TestClaudeSessionIDsMatch tables the matching rule, including the short
// prefix that carries too little entropy to be identity evidence.
func TestClaudeSessionIDsMatch(t *testing.T) {
	const full = "abcdef01-2345-4678-8abc-def012345678"
	cases := []struct {
		name           string
		recorded, cand string
		want           bool
	}{
		{"exact", full, full, true},
		{"case insensitive", full, strings.ToUpper(full), true},
		{"whitespace trimmed", full, "  " + full + "\n", true},
		{"candidate is prefix", full, full[:8], true},
		{"recorded is prefix", full[:12], full, true},
		{"prefix too short", full, full[:4], false},
		{"different ids", full, "99999999-9999-4999-8999-999999999999", false},
		{"empty recorded", "", full, false},
		{"empty candidate", full, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeSessionIDsMatch(tc.recorded, tc.cand); got != tc.want {
				t.Fatalf("claudeSessionIDsMatch(%q, %q) = %v, want %v", tc.recorded, tc.cand, got, tc.want)
			}
		})
	}
}

// TestResumeGuard_VerifiedSourcesClearDiscoveryTaint: once a source that
// identifies THIS session confirms the id (own tmux env, own hook payload,
// explicit --session-id in its own command), the id becomes recorded and
// resumable again.
func TestResumeGuard_VerifiedSourcesClearDiscoveryTaint(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const id = "dddddddd-4444-4555-8666-777777777777"
	inst.adoptDiscoveredClaudeSessionID(id)
	if inst.recordedClaudeSessionID() != "" {
		t.Fatal("precondition: a discovered id is not recorded ownership")
	}

	inst.markClaudeSessionIDVerified()
	if inst.recordedClaudeSessionID() != id {
		t.Fatalf("a verified id must be recorded; got %q", inst.recordedClaudeSessionID())
	}

	// A LATER assignment of a different id is verified by default — the taint
	// is bound to the discovered value, so it can never go stale onto an
	// unrelated id and refuse a legitimate resume.
	inst.adoptDiscoveredClaudeSessionID(id)
	const boundByHook = "eeeeeeee-5555-4666-8777-888888888888"
	inst.ClaudeSessionID = boundByHook
	if inst.recordedClaudeSessionID() != boundByHook {
		t.Fatalf("id replaced after a discovery must not inherit the taint; got %q", inst.recordedClaudeSessionID())
	}
}
