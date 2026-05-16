package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Phase 1 v1.9 regression coverage for issue #856 — case rebind-001.
//
// The /clear-rebind threshold is `clearRebindMtimeGrace = 5 * time.Second`
// (instance.go:2765). Today's TestInstance_UpdateHookStatus_ClearCreatesNewSession_RebindsRegardlessOfSize
// uses a 2-MINUTE gap, which is so far over the threshold that any future
// tweak to clearRebindMtimeGrace (e.g. "make it 30s, users complained
// /clear was too eager") would slip through unnoticed.
//
// This pair pins the exact threshold:
//   - gap == clearRebindMtimeGrace exactly  → rebind (the >= branch).
//   - gap one second below threshold        → reject (sister-flap branch).
//
// If the threshold is bumped to 30s without updating this test, both cases
// invert and the test fails loudly — exactly the signal we want.

func TestInstance_UpdateHookStatus_ClearRebind_BoundaryAtThreshold(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmpHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-rebind-boundary", projectPath, "claude")

	oldID := "5ea244ce-0000-0000-0000-000000000010"
	newID := "2266314c-0000-0000-0000-000000000020"

	// Old rich session, smaller-but-fresh new candidate (the /clear shape).
	oldPath := seedClaudeJSONL(t, inst, oldID, 200, 1024)
	newPath := seedClaudeJSONL(t, inst, newID, 1, 8)

	now := time.Now()
	// EXACT boundary: candidate.mtime - current.mtime == clearRebindMtimeGrace.
	oldMtime := now.Add(-clearRebindMtimeGrace)
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

	if inst.ClaudeSessionID != newID {
		t.Fatalf("at exact threshold (gap = clearRebindMtimeGrace = %v) the rebind "+
			"branch must fire (>= comparison); got ClaudeSessionID = %q, want %q. "+
			"If clearRebindMtimeGrace was tightened to a strict > comparison, this "+
			"reproduces the #856 regression on the boundary tick.",
			clearRebindMtimeGrace, inst.ClaudeSessionID, newID)
	}
}

func TestInstance_UpdateHookStatus_ClearRebind_BoundaryOneSecondBelowRejects(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmpHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-rebind-below-boundary", projectPath, "claude")

	oldID := "5ea244ce-0000-0000-0000-000000000011"
	newID := "2266314c-0000-0000-0000-000000000021"

	oldPath := seedClaudeJSONL(t, inst, oldID, 200, 1024)
	newPath := seedClaudeJSONL(t, inst, newID, 1, 8)

	now := time.Now()
	// Just under threshold: gap = clearRebindMtimeGrace - 1s. This is the
	// canonical #661 flap shape: the user is actively typing into the rich
	// session, the candidate's UserPromptSubmit fires within ~4s of the
	// last record on the old jsonl, and the size-loss must therefore reject.
	gap := clearRebindMtimeGrace - time.Second
	if gap <= 0 {
		t.Skipf("clearRebindMtimeGrace=%v too small to construct sub-threshold gap", clearRebindMtimeGrace)
	}
	oldMtime := now.Add(-gap)
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

	if inst.ClaudeSessionID != oldID {
		t.Fatalf("at gap = clearRebindMtimeGrace - 1s = %v the rebind branch must NOT "+
			"fire (still inside the #661 flap window); got ClaudeSessionID = %q, "+
			"want %q (kept). A regression that loosened the boundary would "+
			"reintroduce the rich-history-overwrite class.",
			gap, inst.ClaudeSessionID, oldID)
	}

	// And the reject must be logged with the correct reason so the user-
	// facing diagnostic remains accurate.
	events := readLifecycleEvents(t)
	if !hasRejectReason(events, "candidate_has_less_conversation_data") {
		t.Fatalf("expected reject event with reason=candidate_has_less_conversation_data; events=%+v", events)
	}
}
