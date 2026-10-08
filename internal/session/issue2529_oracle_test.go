package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Oracle for #2529, identical on origin/main and the PR head. It writes the
// rollout header from the issue verbatim (placeholders replaced by ids).
func writeIssue2529Rollout(t *testing.T, codexHome, sid, header string) {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "10", "07")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(header)) {
		t.Fatalf("bad header %s", header)
	}
	p := filepath.Join(dir, "rollout-2026-10-07T10-00-00-"+sid+".jsonl")
	if err := os.WriteFile(p, []byte(header+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func issue2529Fixture(t *testing.T) (*Instance, string, string) {
	inst, codexHome := newCodexGateInstance(t)
	mainSID, guardianSID := uniqueSID(t), uniqueSID(t)
	writeIssue2529Rollout(t, codexHome, mainSID,
		`{"type":"session_meta","payload":{"id":"`+mainSID+`","thread_source":"user","source":"cli","cwd":"`+inst.ProjectPath+`"}}`)
	writeIssue2529Rollout(t, codexHome, guardianSID,
		`{"type":"session_meta","payload":{"id":"`+guardianSID+`","thread_source":"guardian_review","source":{"subagent":{"other":"guardian"}},"parent_thread_id":"`+mainSID+`","cwd":"`+inst.ProjectPath+`"}}`)
	return inst, mainSID, guardianSID
}

// Symptom 1: a guardian review hook replaces the saved user thread id.
func TestIssue2529Oracle_HookKeepsUserThread(t *testing.T) {
	inst, mainSID, guardianSID := issue2529Fixture(t)
	inst.CodexSessionID = mainSID
	inst.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: guardianSID,
		Event: "agent-turn-complete", UpdatedAt: time.Now()})
	if inst.CodexSessionID != mainSID {
		t.Fatalf("ORACLE RED: guardian hook rebound codex_session_id to the guardian thread %q (want user %q)", inst.CodexSessionID, mainSID)
	}
}

// Symptom 2: restart resumes the saved guardian thread.
func TestIssue2529Oracle_RestartDoesNotResumeGuardian(t *testing.T) {
	inst, mainSID, guardianSID := issue2529Fixture(t)
	_ = withTempGlobalStateDB(t)
	inst.CodexSessionID = guardianSID
	cmd := inst.buildCodexCommand("codex")
	t.Logf("restart command: %s", cmd)
	if strings.Contains(cmd, "resume "+guardianSID) {
		t.Fatalf("ORACLE RED: restart resumes the guardian review thread: %q", cmd)
	}
	if !strings.Contains(cmd, "resume "+mainSID) {
		t.Fatalf("ORACLE: restart should resume the user conversation %q: %q", mainSID, cmd)
	}
}
