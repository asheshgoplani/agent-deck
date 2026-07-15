package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for the codex subagent-thread rebind gate and restart safety net
// (incident 2026-07-15). See codex_subagent_gate.go for the failure story.

// seedCodexRollout writes a minimal rollout JSONL under
// codexHome/sessions/2026/07/15 with the requested thread pedigree.
// finalized appends a final_answer-phase message followed by task_complete,
// matching what codex writes when a subagent delivers its answer.
func seedCodexRolloutWithMeta(t *testing.T, codexHome, sid, threadSource, parentID string, finalized bool) string {
	t.Helper()

	dir := filepath.Join(codexHome, "sessions", "2026", "07", "15")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}

	payload := map[string]any{
		"id":  sid,
		"cwd": "/tmp/project",
	}
	if threadSource != "" {
		payload["thread_source"] = threadSource
	}
	if parentID != "" {
		payload["parent_thread_id"] = parentID
	}
	lines := []map[string]any{
		{"timestamp": "2026-07-15T20:00:00.000Z", "type": "session_meta", "payload": payload},
		{"timestamp": "2026-07-15T20:00:01.000Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": "working"}},
		}},
	}
	if finalized {
		lines = append(lines,
			map[string]any{"timestamp": "2026-07-15T21:00:00.000Z", "type": "response_item", "payload": map[string]any{
				"type": "message", "role": "assistant", "phase": "final_answer",
				"content": []map[string]any{{"type": "output_text", "text": "CLEAN."}},
			}},
			map[string]any{"timestamp": "2026-07-15T21:00:00.100Z", "type": "event_msg", "payload": map[string]any{
				"type": "task_complete", "last_agent_message": "CLEAN.",
			}},
		)
	}

	var b strings.Builder
	for _, l := range lines {
		enc, err := json.Marshal(l)
		if err != nil {
			t.Fatalf("marshal rollout line: %v", err)
		}
		b.Write(enc)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, "rollout-2026-07-15T20-00-00-"+sid+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	return path
}

// newCodexGateInstance builds a codex instance rooted in an isolated
// CODEX_HOME. Session ids are uniqued per test because the thread-meta cache
// is keyed by id for the life of the process.
func newCodexGateInstance(t *testing.T) (*Instance, string) {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	codexHome := filepath.Join(tmpHome, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	return NewInstanceWithTool("codex-gate", projectPath, "codex"), codexHome
}

// uniqueSID returns a UUID-shaped session id unique across the test binary,
// so the package-level thread-meta cache cannot leak state between tests.
var sidCounter int

func uniqueSID(t *testing.T) string {
	t.Helper()
	sidCounter++
	return fmt.Sprintf("019f0000-0000-7000-8000-%012d", sidCounter)
}

func TestCodexHookRebind_RejectsSubagentThread(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	mainSID := uniqueSID(t)
	childSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "", "", false)
	seedCodexRolloutWithMeta(t, codexHome, childSID, "subagent", mainSID, true)

	inst.CodexSessionID = mainSID
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: childSID,
		Event:     "agent-turn-complete",
		UpdatedAt: time.Now(),
	})

	if inst.CodexSessionID != mainSID {
		t.Fatalf("subagent turn-complete hook must not usurp the binding: "+
			"got %q, want %q (the 2026-07-15 poisoning)", inst.CodexSessionID, mainSID)
	}
}

func TestCodexHookRebind_ColdStartRejectsSubagentThread(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	childSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, childSID, "subagent", uniqueSID(t), false)

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: childSID,
		Event:     "agent-turn-complete",
		UpdatedAt: time.Now(),
	})

	if inst.CodexSessionID != "" {
		t.Fatalf("cold start must not bind a subagent thread (the probe finds "+
			"the owning thread instead): got %q, want empty", inst.CodexSessionID)
	}
}

func TestCodexHookRebind_AllowsUserThread(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	oldSID := uniqueSID(t)
	newSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, oldSID, "", "", false)
	seedCodexRolloutWithMeta(t, codexHome, newSID, "user", "", false)

	inst.CodexSessionID = oldSID
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newSID,
		Event:     "agent-turn-complete",
		UpdatedAt: time.Now(),
	})

	if inst.CodexSessionID != newSID {
		t.Fatalf("user-thread rebind (e.g. /new rotation) must still work: got %q, want %q",
			inst.CodexSessionID, newSID)
	}
}

func TestCodexHookRebind_AllowsUnflushedCandidate(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	oldSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, oldSID, "", "", false)
	newSID := uniqueSID(t) // no rollout on disk yet

	inst.CodexSessionID = oldSID
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newSID,
		Event:     "agent-turn-complete",
		UpdatedAt: time.Now(),
	})

	if inst.CodexSessionID != newSID {
		t.Fatalf("candidate without a flushed rollout must bind (fail-open, "+
			"pre-gate behavior): got %q, want %q", inst.CodexSessionID, newSID)
	}
}

func TestBuildCodexCommand_ForksFinalizedSubagentBinding(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	mainSID := uniqueSID(t)
	childSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, childSID, "subagent", mainSID, true)

	inst.CodexSessionID = childSID
	cmd := inst.buildCodexCommand("codex")

	if !strings.Contains(cmd, "fork "+childSID) {
		t.Fatalf("a subagent-sourced binding must launch with `codex fork` "+
			"(resume loads it but the first typed message dies with "+
			"\"turn/start failed in TUI\"); got command %q", cmd)
	}
}

func TestBuildCodexCommand_ForksUnfinalizedSubagentBinding(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	mainSID := uniqueSID(t)
	childSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, childSID, "subagent", mainSID, false)

	inst.CodexSessionID = childSID
	cmd := inst.buildCodexCommand("codex")

	if !strings.Contains(cmd, "fork "+childSID) {
		t.Fatalf("finalization is irrelevant — codex refuses user turns on any "+
			"subagent-sourced thread (ares-attn died on send while its adopted "+
			"thread was mid-flight), so fork is required; got %q", cmd)
	}
}

func TestBuildCodexCommand_ResumesUserThreadBinding(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	mainSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)

	inst.CodexSessionID = mainSID
	cmd := inst.buildCodexCommand("codex")

	if !strings.Contains(cmd, "resume "+mainSID) {
		t.Fatalf("user-thread bindings must keep plain resume: got %q", cmd)
	}
	if strings.Contains(cmd, "fork ") {
		t.Fatalf("user-thread bindings must not be forked: got %q", cmd)
	}
}

func TestShouldRejectCodexSubagentRebind(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	subSID := uniqueSID(t)
	userSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, subSID, "subagent", uniqueSID(t), false)
	seedCodexRolloutWithMeta(t, codexHome, userSID, "user", "", false)
	unflushedSID := uniqueSID(t) // no rollout on disk

	if !inst.shouldRejectCodexSubagentRebind(subSID) {
		t.Fatalf("subagent-sourced candidate must be rejected")
	}
	if inst.shouldRejectCodexSubagentRebind(userSID) {
		t.Fatalf("user-sourced candidate must be allowed")
	}
	if inst.shouldRejectCodexSubagentRebind(unflushedSID) {
		t.Fatalf("candidate without a flushed rollout must be allowed (fail-open)")
	}
}

// seedCodexRolloutCwd writes a realistic rollout-<ts>-<sid>.jsonl whose
// session_meta head carries both cwd (for the disk scan's project match) and
// thread_source (for the subagent gate). Distinct from seedCodexRolloutWithMeta,
// which hard-codes cwd — the disk-scan gate needs the cwd to match ProjectPath.
func seedCodexRolloutCwd(t *testing.T, codexHome, sid, threadSource, cwd string) {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "07", "15")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	head := map[string]any{
		"timestamp": "2026-07-15T20:00:00.000Z",
		"type":      "session_meta",
		"payload":   map[string]any{"id": sid, "cwd": cwd, "thread_source": threadSource},
	}
	enc, err := json.Marshal(head)
	if err != nil {
		t.Fatalf("marshal head: %v", err)
	}
	path := filepath.Join(dir, "rollout-2026-07-15T20-00-00-"+sid+".jsonl")
	if err := os.WriteFile(path, append(enc, '\n'), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
}

func TestUpdateCodexSession_DiskScan_PrefersUserOverSubagent(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	if err := os.MkdirAll(inst.ProjectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	userSID := uniqueSID(t)
	subSID := uniqueSID(t)
	// Both scoped to the instance's project; the subagent rollout is written
	// last so a naive most-recent-wins scan would prefer it.
	seedCodexRolloutCwd(t, codexHome, userSID, "user", inst.ProjectPath)
	seedCodexRolloutCwd(t, codexHome, subSID, "subagent", inst.ProjectPath)

	inst.UpdateCodexSession(nil)

	if inst.CodexSessionID == subSID {
		t.Fatalf("disk scan adopted the subagent thread %q — codex refuses user "+
			"turns on it", subSID)
	}
	if inst.CodexSessionID != userSID {
		t.Fatalf("disk scan should bind the user thread %q, got %q", userSID, inst.CodexSessionID)
	}
}

func TestUpdateCodexSession_DiskScan_RejectsLoneSubagent(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	if err := os.MkdirAll(inst.ProjectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	subSID := uniqueSID(t)
	seedCodexRolloutCwd(t, codexHome, subSID, "subagent", inst.ProjectPath)

	inst.UpdateCodexSession(nil)

	if inst.CodexSessionID != "" {
		t.Fatalf("disk scan must leave the session unbound when only a subagent "+
			"rollout matches; got %q", inst.CodexSessionID)
	}
}
