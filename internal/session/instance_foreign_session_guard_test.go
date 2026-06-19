package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression coverage for the `claude -p` env-pollution class: a foreign
// ephemeral Claude session (a `claude -p` child that inherited this instance's
// AGENTDECK_INSTANCE_ID via env and fired its own hooks under our id) must not
// flip THIS instance's status. The bind path already rejected the foreign
// session id (candidate_has_no_conversation_data); these tests pin that the
// STATUS application is now gated by the same ownership check.
//
// Setup mirrors instance_clear_rebind_boundary_test.go (seedClaudeJSONL +
// isolated HOME/CLAUDE_CONFIG_DIR).

func newGuardTestInstance(t *testing.T, title string) *Instance {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmpHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	return NewInstanceWithTool(title, projectPath, "claude")
}

// Foreign ephemeral (different id, NO conversation data) on an ESTABLISHED
// instance: status must NOT be applied, bind must NOT change, and a reject is
// logged with the foreign-session reason.
func TestUpdateHookStatus_ForeignEphemeral_StatusSuppressed(t *testing.T) {
	inst := newGuardTestInstance(t, "foreign-guard-suppress")

	ownID := "5ea244ce-0000-0000-0000-0000000000a0"
	foreignID := "2266314c-0000-0000-0000-0000000000b0" // no jsonl seeded → no conversation data
	seedClaudeJSONL(t, inst, ownID, 50, 512)            // our real session has history
	inst.ClaudeSessionID = ownID

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: foreignID,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	if inst.hookStatus == "running" {
		t.Fatalf("foreign ephemeral claude -p hook flipped our hookStatus to %q; "+
			"the ownership guard must suppress a no-conversation-data foreign session", inst.hookStatus)
	}
	if inst.ClaudeSessionID != ownID {
		t.Fatalf("foreign session must not rebind; got %q want %q", inst.ClaudeSessionID, ownID)
	}
	events := readLifecycleEvents(t)
	if !hasRejectReason(events, "candidate_has_no_conversation_data") {
		t.Fatalf("expected reject reason=candidate_has_no_conversation_data; events=%+v", events)
	}
}

// The owning session's own hook (SessionID == bound id) must apply normally.
func TestUpdateHookStatus_OwningSession_StatusApplied(t *testing.T) {
	inst := newGuardTestInstance(t, "foreign-guard-owning")

	ownID := "5ea244ce-0000-0000-0000-0000000000a1"
	seedClaudeJSONL(t, inst, ownID, 50, 512)
	inst.ClaudeSessionID = ownID

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: ownID,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	if inst.hookStatus != "running" {
		t.Fatalf("owning session hook must apply status; got hookStatus=%q want running", inst.hookStatus)
	}
}

// Cold start (no bound id yet) must NOT be suppressed — the first real session
// has to be free to bind and report status.
func TestUpdateHookStatus_ColdStart_NotSuppressed(t *testing.T) {
	inst := newGuardTestInstance(t, "foreign-guard-coldstart")

	firstID := "2266314c-0000-0000-0000-0000000000c0" // no jsonl needed: cold start binds unconditionally
	inst.ClaudeSessionID = ""

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: firstID,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	if inst.hookStatus != "running" {
		t.Fatalf("cold-start hook must apply status; got hookStatus=%q want running", inst.hookStatus)
	}
	if inst.ClaudeSessionID != firstID {
		t.Fatalf("cold-start must bind first candidate; got %q want %q", inst.ClaudeSessionID, firstID)
	}
}

// A LEGITIMATE rebind (different id but WITH conversation data, /clear shape)
// must NOT be flagged as foreign — status applies and the rebind proceeds. This
// pins that the guard never starves a real session change.
func TestUpdateHookStatus_LegitRebindWithData_NotSuppressed(t *testing.T) {
	inst := newGuardTestInstance(t, "foreign-guard-rebind")

	oldID := "5ea244ce-0000-0000-0000-0000000000a2"
	newID := "2266314c-0000-0000-0000-0000000000d0"
	oldPath := seedClaudeJSONL(t, inst, oldID, 200, 1024)
	newPath := seedClaudeJSONL(t, inst, newID, 1, 8) // smaller-but-fresh: the /clear shape

	now := time.Now()
	oldMtime := now.Add(-clearRebindMtimeGrace - time.Second)
	if err := os.Chtimes(oldPath, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(newPath, now, now); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}

	inst.ClaudeSessionID = oldID
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	if inst.hookStatus != "running" {
		t.Fatalf("legit rebind (candidate has conversation data) must apply status; got %q", inst.hookStatus)
	}
	if inst.ClaudeSessionID != newID {
		t.Fatalf("legit /clear rebind must proceed; got ClaudeSessionID=%q want %q", inst.ClaudeSessionID, newID)
	}
}
