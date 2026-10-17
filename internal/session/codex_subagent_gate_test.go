package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
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
	if threadSource == "guardian_review" {
		payload["source"] = map[string]any{"subagent": map[string]any{"other": "guardian"}}
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

func TestCodexThreadMetaIncompleteHeadIsRetried(t *testing.T) {
	_, codexHome := newCodexGateInstance(t)
	sid := uniqueSID(t)
	path := seedCodexRolloutWithMeta(t, codexHome, sid, "subagent", uniqueSID(t), false)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if meta, ok := codexThreadMetaForSession(sid, codexHome); !ok || meta.ThreadSource != "" {
		t.Fatalf("incomplete rollout should have no readable pedigree: %+v, ok=%t", meta, ok)
	}
	seedCodexRolloutWithMeta(t, codexHome, sid, "subagent", uniqueSID(t), false)
	if meta, ok := codexThreadMetaForSession(sid, codexHome); !ok || meta.ThreadSource != "subagent" {
		t.Fatalf("completed rollout pedigree was not reread: %+v, ok=%t", meta, ok)
	}
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

func TestCodexHookRebind_RejectsGuardianReviewThread(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)

	mainSID := uniqueSID(t)
	guardianSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, guardianSID, "guardian_review", mainSID, true)

	inst.CodexSessionID = mainSID
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: guardianSID,
		Event:     "agent-turn-complete",
		UpdatedAt: time.Now(),
	})

	if inst.CodexSessionID != mainSID {
		t.Fatalf("guardian review hook replaced the user thread: got %q, want %q", inst.CodexSessionID, mainSID)
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

	// Only events that can precede the rollout keep the fail-open binding; a
	// turn-end without a rollout is an ephemeral helper thread (see
	// codex_title_thread_rebind_test.go).
	inst.CodexSessionID = oldSID
	inst.UpdateHookStatus(&HookStatus{
		Status:    "waiting",
		SessionID: newSID,
		Event:     "thread.started",
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

func TestBuildCodexCommand_ResumesGuardianReviewParent(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	db := withTempGlobalStateDB(t)

	mainSID := uniqueSID(t)
	guardianSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, guardianSID, "guardian_review", mainSID, true)
	if err := db.SaveInstance(&statedb.InstanceRow{
		ID: inst.ID, Title: inst.Title, ProjectPath: inst.ProjectPath,
		GroupPath: inst.GroupPath, Command: inst.Command, Tool: "codex",
		Status: "idle", CreatedAt: time.Now(),
		ToolData: json.RawMessage(`{"codex_session_id":"` + guardianSID + `"}`),
	}); err != nil {
		t.Fatal(err)
	}

	inst.CodexSessionID = guardianSID
	cmd := inst.buildCodexCommand("codex")

	if !strings.Contains(cmd, "resume "+mainSID) || strings.Contains(cmd, "fork "+guardianSID) {
		t.Fatalf("guardian review binding must resume its user parent: got %q", cmd)
	}
	if inst.CodexSessionID != mainSID {
		t.Fatalf("guardian review binding was not repaired: got %q, want %q", inst.CodexSessionID, mainSID)
	}
	if got := readCodexSessionIDFromDB(t, db, inst.ID); got != mainSID {
		t.Fatalf("guardian review binding persisted %q, want parent %q", got, mainSID)
	}
}

func TestBuildCodexCommand_RepairsGuardianBindingInOwningDB(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	ownerDB := withTempGlobalStateDB(t)

	mainSID := uniqueSID(t)
	guardianSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, guardianSID, "guardian_review", mainSID, true)
	if err := ownerDB.SaveInstance(&statedb.InstanceRow{
		ID: inst.ID, Title: inst.Title, ProjectPath: inst.ProjectPath,
		GroupPath: inst.GroupPath, Command: inst.Command, Tool: "codex",
		Status: "idle", CreatedAt: time.Now(),
		ToolData: json.RawMessage(`{"codex_session_id":"` + guardianSID + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	inst.restartDB.Store(ownerDB)
	_ = withTempGlobalStateDB(t) // An unrelated profile is the process-wide database.

	inst.CodexSessionID = guardianSID
	if cmd := inst.buildCodexCommand("codex"); !strings.Contains(cmd, "resume "+mainSID) {
		t.Fatalf("guardian binding must resume its user parent: got %q", cmd)
	}
	if got := readCodexSessionIDFromDB(t, ownerDB, inst.ID); got != mainSID {
		t.Fatalf("owning database persisted %q, want parent %q", got, mainSID)
	}
}

func TestBuildCodexCommand_ForksWhenGuardianRepairWriteFails(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	mainSID := uniqueSID(t)
	guardianSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, guardianSID, "guardian_review", mainSID, true)

	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveInstance(&statedb.InstanceRow{
		ID: inst.ID, Title: inst.Title, ProjectPath: inst.ProjectPath,
		GroupPath: inst.GroupPath, Command: inst.Command, Tool: "codex",
		Status: "idle", CreatedAt: time.Now(),
		ToolData: json.RawMessage(`{"codex_session_id":"` + guardianSID + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	inst.restartDB.Store(db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_ = withTempGlobalStateDB(t) // The owning database is present but cannot accept writes.

	inst.CodexSessionID = guardianSID
	cmd := inst.buildCodexCommand("codex")
	if !strings.Contains(cmd, "fork "+guardianSID) || strings.Contains(cmd, "resume "+mainSID) {
		t.Fatalf("failed repair must use the existing fork fallback: got %q", cmd)
	}
	if inst.CodexSessionID != guardianSID {
		t.Fatalf("failed repair changed in-memory binding to %q", inst.CodexSessionID)
	}

	reopened, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := readCodexSessionIDFromDB(t, reopened, inst.ID); got != guardianSID {
		t.Fatalf("failed repair saved %q, want original guardian ID", got)
	}
}

func TestCodexBindingAfterShortLivedStorageCloses(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		name := "hook"
		if recovery {
			name = "guardian_recovery"
		}
		t.Run(name, func(t *testing.T) {
			inst, codexHome := newCodexGateInstance(t)
			owner := newTestStorage(t)
			_ = withTempGlobalStateDB(t) // A different profile must not receive the repair.
			mainSID, oldSID := uniqueSID(t), uniqueSID(t)
			seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
			if recovery {
				seedCodexRolloutWithMeta(t, codexHome, oldSID, "guardian_review", mainSID, true)
			}
			inst.CodexSessionID = oldSID
			if err := owner.Save([]*Instance{inst}); err != nil {
				t.Fatal(err)
			}
			webDB, err := statedb.Open(owner.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			webStorage := &Storage{db: webDB, dbPath: owner.dbPath, profile: owner.profile}
			if err := webStorage.Save([]*Instance{inst}); err != nil {
				t.Fatal(err)
			}
			if err := webStorage.Close(); err != nil {
				t.Fatal(err)
			}
			if recovery {
				if cmd := inst.buildCodexCommand("codex"); !strings.Contains(cmd, "resume "+mainSID) {
					t.Fatalf("closed storage must not prevent guardian recovery: got %q", cmd)
				}
			} else {
				inst.UpdateHookStatus(&HookStatus{
					Status: "waiting", SessionID: mainSID,
					Event: "agent-turn-complete", UpdatedAt: time.Now(),
				})
			}
			if inst.CodexSessionID != mainSID {
				t.Fatalf("closed storage prevented rebind: got %q, want %q", inst.CodexSessionID, mainSID)
			}
			if got := readCodexSessionIDFromDB(t, owner.db, inst.ID); got != mainSID {
				t.Fatalf("owning database saved %q, want %q", got, mainSID)
			}
		})
	}
}

func TestBuildCodexCommand_DoesNotResumeGuardianExecParent(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	execSID, guardianSID := uniqueSID(t), uniqueSID(t)
	path := seedCodexRolloutWithMeta(t, codexHome, execSID, "user", "", false)
	head := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":"/tmp/project","thread_source":"user","source":"exec"}}`, execSID)
	if err := os.WriteFile(path, []byte(head+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedCodexRolloutWithMeta(t, codexHome, guardianSID, "guardian_review", execSID, true)
	inst.CodexSessionID = guardianSID
	if cmd := inst.buildCodexCommand("codex"); !strings.Contains(cmd, "fork "+guardianSID) || strings.Contains(cmd, "resume "+execSID) {
		t.Fatalf("guardian recovery must not resume a nested exec thread: got %q", cmd)
	}
}

func TestBuildCodexCommand_DoesNotBypassReadOnlyOwningDB(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	owner := newTestStorage(t)
	mainSID, guardianSID := uniqueSID(t), uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, guardianSID, "guardian_review", mainSID, true)
	inst.CodexSessionID = guardianSID
	if err := owner.Save([]*Instance{inst}); err != nil {
		t.Fatal(err)
	}
	readOnly, err := statedb.OpenReadOnlyLive(owner.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	inst.restartDB.Store(readOnly)
	if cmd := inst.buildCodexCommand("codex"); !strings.Contains(cmd, "fork "+guardianSID) {
		t.Fatalf("read-only storage must retain the failed-write fallback: got %q", cmd)
	}
	if got := readCodexSessionIDFromDB(t, owner.db, inst.ID); got != guardianSID {
		t.Fatalf("read-only repair changed the saved binding to %q", got)
	}
}

func TestBuildCodexCommand_ResumesGuardianReviewUserAncestor(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	mainSID := uniqueSID(t)
	subagentSID := uniqueSID(t)
	firstGuardianSID := uniqueSID(t)
	guardianSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, subagentSID, "subagent", mainSID, false)
	seedCodexRolloutWithMeta(t, codexHome, firstGuardianSID, "guardian_review", subagentSID, true)
	seedCodexRolloutWithMeta(t, codexHome, guardianSID, "guardian_review", firstGuardianSID, true)

	inst.CodexSessionID = guardianSID
	if cmd := inst.buildCodexCommand("codex"); !strings.Contains(cmd, "resume "+mainSID) {
		t.Fatalf("guardian review chain must resume its user ancestor: got %q", cmd)
	}
	if inst.CodexSessionID != mainSID {
		t.Fatalf("guardian review chain bound %q, want user thread %q", inst.CodexSessionID, mainSID)
	}
}

func TestBuildCodexCommand_ResumesLegacyCLIParent(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	mainSID := uniqueSID(t)
	guardianSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "cli", "", false)
	seedCodexRolloutWithMeta(t, codexHome, guardianSID, "guardian_review", mainSID, true)

	inst.CodexSessionID = guardianSID
	if cmd := inst.buildCodexCommand("codex"); !strings.Contains(cmd, "resume "+mainSID) {
		t.Fatalf("guardian review must resume its legacy CLI parent: got %q", cmd)
	}
}

func TestBuildCodexCommand_DoesNotFollowGuardianParentCycle(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	firstGuardianSID := uniqueSID(t)
	secondGuardianSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, firstGuardianSID, "guardian_review", secondGuardianSID, true)
	seedCodexRolloutWithMeta(t, codexHome, secondGuardianSID, "guardian_review", firstGuardianSID, true)

	inst.CodexSessionID = firstGuardianSID
	if cmd := inst.buildCodexCommand("codex"); !strings.Contains(cmd, "fork "+firstGuardianSID) {
		t.Fatalf("guardian parent cycle must use the existing fallback: got %q", cmd)
	}
}

func TestBuildCodexCommand_DoesNotResumeMissingGuardianParent(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	guardianSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, guardianSID, "guardian_review", uniqueSID(t), true)

	inst.CodexSessionID = guardianSID
	cmd := inst.buildCodexCommand("codex")

	if !strings.Contains(cmd, "fork "+guardianSID) {
		t.Fatalf("a missing guardian parent must not become a resume target: got %q", cmd)
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

func TestResolveCodexDetectionCandidateRejectsSubagent(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	userSID := uniqueSID(t)
	subSID := uniqueSID(t)
	seedCodexRolloutCwd(t, codexHome, userSID, "user", inst.ProjectPath)
	seedCodexRolloutCwd(t, codexHome, subSID, "subagent", inst.ProjectPath)

	if got := inst.resolveCodexDetectionCandidate(subSID, nil); got != userSID {
		t.Fatalf("async candidate resolution = %q, want user thread %q", got, userSID)
	}
}

func TestResolveCodexDetectionCandidateRejectsGuardianReview(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	userSID := uniqueSID(t)
	guardianSID := uniqueSID(t)
	seedCodexRolloutCwd(t, codexHome, userSID, "user", inst.ProjectPath)
	seedCodexRolloutCwd(t, codexHome, guardianSID, "guardian_review", inst.ProjectPath)

	if got := inst.resolveCodexDetectionCandidate(guardianSID, nil); got != userSID {
		t.Fatalf("guardian probe candidate resolved to %q, want user thread %q", got, userSID)
	}
}

func TestRejectedSubagentProbePreservesIncompleteResult(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	userSID := uniqueSID(t)
	subSID := uniqueSID(t)
	seedCodexRolloutCwd(t, codexHome, userSID, "user", inst.ProjectPath)
	seedCodexRolloutCwd(t, codexHome, subSID, "subagent", inst.ProjectPath)
	subPath := codexRolloutPathInHome(subSID, codexHome)

	restore := procfdOpenVnodePaths
	t.Cleanup(func() { procfdOpenVnodePaths = restore })
	procfdOpenVnodePaths = func(int) ([]string, error) {
		return []string{subPath}, os.ErrPermission
	}
	sessionID, probeErr := inst.queryCodexSessionFromHostNative([]int{123})
	if sessionID != "" || probeErr == nil {
		t.Fatalf("native subagent partial probe = (%q, %v), want (empty, error)", sessionID, probeErr)
	}
	if got := inst.resolveCodexDetectionCandidate(sessionID, probeErr); got != "" {
		t.Fatalf("undetermined async probe fell through to disk ID %q", got)
	}

	dir := t.TempDir()
	fakeDocker := filepath.Join(dir, "docker")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\nexit 2\n", subPath)
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	inst.SandboxContainer = "test"
	sessionID, _, probeErr = inst.queryCodexSessionFromDockerProcFD()
	if sessionID != "" || probeErr == nil {
		t.Fatalf("docker subagent partial probe = (%q, %v), want (empty, error)", sessionID, probeErr)
	}
}

func TestUpdateCodexSession_DiskScan_PrefersUserOverSubagent(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	inst.lastCodexProbeAt = time.Now().Add(time.Hour) // This test isolates disk selection.
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
	inst.lastCodexProbeAt = time.Now().Add(time.Hour) // This test isolates disk selection.
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
