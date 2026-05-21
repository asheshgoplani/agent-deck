package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Regression for issue #1139 — Codex variant of the persist gap fixed
// for Claude in #1140 (PR 304d05a7). #1138 / #1140 documents the root
// cause in full; the short version is that `bindCodexSessionFromHook`
// mutated `i.CodexSessionID` in memory and propagated the new ID into
// the tmux env, but none of the `UpdateHookStatus` callers (TUI tick,
// web refresh, CLI status refresh) called `Save` afterwards. So
// `instances.tool_data.codex_session_id` in `state.db` stayed pinned at
// the pre-/clear UUID indefinitely. DB-direct consumers saw the stale
// ID, and peer agent-deck processes kept reloading the stale row from
// disk and clobbering each other's in-memory bind — producing a runaway
// loop of fresh `rebind` lifecycle entries until something else triggered
// a full save.
//
// These tests pin the fix: both the cold `bind` branch and the `rebind`
// branch must leave `tool_data.codex_session_id` matching the new UUID
// the instant `UpdateHookStatus` returns.

// readCodexSessionIDFromDB returns tool_data.codex_session_id for the
// given instance, reading via a fresh query so the assertion reflects
// what a DB-direct consumer would observe (no in-memory caching).
func readCodexSessionIDFromDB(t *testing.T, db *statedb.StateDB, id string) string {
	t.Helper()
	rows, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		var blob struct {
			CodexSessionID string `json:"codex_session_id"`
		}
		if len(row.ToolData) == 0 {
			return ""
		}
		if err := json.Unmarshal(row.ToolData, &blob); err != nil {
			t.Fatalf("unmarshal tool_data for %s: %v (raw=%s)", id, err, string(row.ToolData))
		}
		return blob.CodexSessionID
	}
	t.Fatalf("instance %q not found in DB", id)
	return ""
}

// TestRebindPersistsCodexSessionIDToDB exercises the rebind shape:
// instance already bound to OLD, UpdateHookStatus fires for NEW. After
// the call, tool_data.codex_session_id in the database must equal NEW.
//
// Pre-fix this test fails: in-memory CodexSessionID advances but the
// row in the DB stays at OLD.
func TestRebindPersistsCodexSessionIDToDB(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	db := withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-codex-rebind-persist", projectPath, "codex")

	oldID := "5ea244ce-0000-0000-0000-000000000a01"
	newID := "2266314c-0000-0000-0000-000000000a02"

	now := time.Now()
	seedRow := &statedb.InstanceRow{
		ID:          inst.ID,
		Title:       inst.Title,
		ProjectPath: inst.ProjectPath,
		GroupPath:   inst.GroupPath,
		Command:     inst.Command,
		Tool:        "codex",
		Status:      "idle",
		CreatedAt:   now,
		ToolData:    json.RawMessage(`{"codex_session_id":"` + oldID + `"}`),
	}
	if err := db.SaveInstance(seedRow); err != nil {
		t.Fatalf("SaveInstance seed: %v", err)
	}

	inst.CodexSessionID = oldID
	if got := readCodexSessionIDFromDB(t, db, inst.ID); got != oldID {
		t.Fatalf("pre-condition: DB codex_session_id = %q, want %q", got, oldID)
	}

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	// Post-condition (in-memory) — the existing behavior pre-fix.
	if inst.CodexSessionID != newID {
		t.Fatalf("in-memory CodexSessionID = %q, want %q (rebind path didn't fire)",
			inst.CodexSessionID, newID)
	}

	// Post-condition (DB) — the regression we are pinning. Pre-fix this
	// stays at OLD because the rebind never reached SQLite.
	if got := readCodexSessionIDFromDB(t, db, inst.ID); got != newID {
		t.Fatalf("post-rebind DB codex_session_id = %q, want %q. "+
			"bindCodexSessionFromHook mutated in-memory state but did not "+
			"persist to tool_data — DB-direct consumers will see the stale UUID "+
			"until something else triggers a save.", got, newID)
	}
}

// TestBindPersistsCodexSessionIDToDB covers the cold-start branch —
// when an instance has no CodexSessionID yet and the very first hook
// event arrives. Must persist just as eagerly as the rebind path.
func TestBindPersistsCodexSessionIDToDB(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	db := withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-codex-bind-persist", projectPath, "codex")

	newID := "7a3b9c10-0000-0000-0000-000000000a03"

	seedRow := &statedb.InstanceRow{
		ID:          inst.ID,
		Title:       inst.Title,
		ProjectPath: inst.ProjectPath,
		GroupPath:   inst.GroupPath,
		Command:     inst.Command,
		Tool:        "codex",
		Status:      "idle",
		CreatedAt:   time.Now(),
		ToolData:    json.RawMessage(`{}`),
	}
	if err := db.SaveInstance(seedRow); err != nil {
		t.Fatalf("SaveInstance seed: %v", err)
	}

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "SessionStart",
		UpdatedAt: time.Now(),
	})

	if inst.CodexSessionID != newID {
		t.Fatalf("in-memory CodexSessionID = %q, want %q", inst.CodexSessionID, newID)
	}
	if got := readCodexSessionIDFromDB(t, db, inst.ID); got != newID {
		t.Fatalf("post-bind DB codex_session_id = %q, want %q — cold-start bind "+
			"did not persist to tool_data", got, newID)
	}
}

// TestCodexRebindNoOpWhenStateDBUnset confirms the persistence call is
// optional: when no global StateDB is wired (CLI processes that haven't
// initialized statedb yet, certain test paths), the rebind must still
// mutate in-memory state and not panic. Protects the existing contract
// that UpdateHookStatus is callable from any context.
func TestCodexRebindNoOpWhenStateDBUnset(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	prev := statedb.GetGlobal()
	statedb.SetGlobal(nil)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-codex-rebind-nodb", projectPath, "codex")

	newID := "9f5e1d80-0000-0000-0000-000000000a04"
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "SessionStart",
		UpdatedAt: time.Now(),
	})

	if inst.CodexSessionID != newID {
		t.Fatalf("in-memory CodexSessionID = %q, want %q (rebind path must work "+
			"even without a global StateDB)", inst.CodexSessionID, newID)
	}
}

// TestCodexRebindPreservesUnrelatedToolDataKeys pins the json_set
// semantics of WriteCodexSessionBinding: only $.codex_session_id and
// $.codex_detected_at may be rewritten — every other key in tool_data
// must survive untouched. Prevents a future "let's just do
// tool_data = ?" simplification from silently dropping unrelated state
// on every Codex rebind.
func TestCodexRebindPreservesUnrelatedToolDataKeys(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	db := withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-codex-rebind-extras", projectPath, "codex")

	oldID := "5ea244ce-0000-0000-0000-000000000a05"
	newID := "2266314c-0000-0000-0000-000000000a06"

	seedJSON := `{
		"codex_session_id": "` + oldID + `",
		"codex_detected_at": 1700000000,
		"latest_prompt": "what does this code do?",
		"notes": "user prefers terse responses",
		"loaded_mcp_names": ["github", "linear"],
		"ssh_host": "dev01.coder",
		"clear_on_compact": true
	}`
	seedRow := &statedb.InstanceRow{
		ID:          inst.ID,
		Title:       inst.Title,
		ProjectPath: inst.ProjectPath,
		GroupPath:   inst.GroupPath,
		Command:     inst.Command,
		Tool:        "codex",
		Status:      "idle",
		CreatedAt:   time.Now(),
		ToolData:    json.RawMessage(seedJSON),
	}
	if err := db.SaveInstance(seedRow); err != nil {
		t.Fatalf("SaveInstance seed: %v", err)
	}

	inst.CodexSessionID = oldID
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	rows, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	var found *statedb.InstanceRow
	for _, row := range rows {
		if row.ID == inst.ID {
			found = row
			break
		}
	}
	if found == nil {
		t.Fatalf("instance %q missing from DB", inst.ID)
	}
	var blob map[string]any
	if err := json.Unmarshal(found.ToolData, &blob); err != nil {
		t.Fatalf("unmarshal tool_data: %v (raw=%s)", err, string(found.ToolData))
	}

	if got, _ := blob["codex_session_id"].(string); got != newID {
		t.Errorf("codex_session_id = %q, want %q (rebind didn't persist)", got, newID)
	}
	if v, ok := blob["codex_detected_at"]; !ok {
		t.Errorf("codex_detected_at missing — rebind should have written it")
	} else if f, _ := v.(float64); int64(f) == 1700000000 {
		t.Errorf("codex_detected_at = %v, want a fresh timestamp", v)
	}

	preserved := map[string]any{
		"latest_prompt":    "what does this code do?",
		"notes":            "user prefers terse responses",
		"ssh_host":         "dev01.coder",
		"clear_on_compact": true,
	}
	for k, want := range preserved {
		got, ok := blob[k]
		if !ok {
			t.Errorf("tool_data lost key %q after rebind — json_set must not "+
				"replace the whole column.", k)
			continue
		}
		if got != want {
			t.Errorf("tool_data[%q] = %v, want %v", k, got, want)
		}
	}
	mcps, ok := blob["loaded_mcp_names"].([]any)
	if !ok {
		t.Errorf("tool_data lost loaded_mcp_names after rebind (got %T)", blob["loaded_mcp_names"])
	} else if len(mcps) != 2 || mcps[0] != "github" || mcps[1] != "linear" {
		t.Errorf("loaded_mcp_names = %v, want [github linear]", mcps)
	}
}
