package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Regression for issue #1139 — Gemini variant of the persist gap fixed
// for Claude in #1140 (PR 304d05a7). See the Codex sibling test file
// and the issue threads for the full RCA. Short version: hook-driven
// rebind mutated `i.GeminiSessionID` in memory but never reached
// SQLite. DB-direct consumers and peer agent-deck processes saw the
// stale UUID indefinitely, and the lifecycle log accumulated fresh
// `rebind` entries forever.

// readGeminiSessionIDFromDB returns tool_data.gemini_session_id for the
// given instance, reading via a fresh query (no in-memory caching).
func readGeminiSessionIDFromDB(t *testing.T, db *statedb.StateDB, id string) string {
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
			GeminiSessionID string `json:"gemini_session_id"`
		}
		if len(row.ToolData) == 0 {
			return ""
		}
		if err := json.Unmarshal(row.ToolData, &blob); err != nil {
			t.Fatalf("unmarshal tool_data for %s: %v (raw=%s)", id, err, string(row.ToolData))
		}
		return blob.GeminiSessionID
	}
	t.Fatalf("instance %q not found in DB", id)
	return ""
}

// seedGeminiSessionFile writes a minimal Gemini session JSON the
// quality-gate (geminiSessionHasConversationData) accepts. The file
// name pattern session-*-<first8>.json is required by the disk glob.
func seedGeminiSessionFile(t *testing.T, projectPath, sessionID string) {
	t.Helper()
	sessionsDir := GetGeminiSessionsDir(projectPath)
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir gemini sessions dir: %v", err)
	}
	filePath := filepath.Join(sessionsDir, "session-2026-05-21T10-00-"+sessionID[:8]+".json")
	content := `{"sessionId":"` + sessionID + `","messages":[{"type":"user","content":"hi"}]}`
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatalf("write gemini session file: %v", err)
	}
}

// TestRebindPersistsGeminiSessionIDToDB exercises the rebind shape:
// instance bound to OLD, UpdateHookStatus fires for NEW with a valid
// on-disk session file (required by the gemini quality gate). After
// the call, tool_data.gemini_session_id in the database must equal NEW.
//
// Pre-fix this test fails: in-memory GeminiSessionID advances but the
// row in the DB stays at OLD.
func TestRebindPersistsGeminiSessionIDToDB(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	db := withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-gemini-rebind-persist", projectPath, "gemini")

	oldID := "5ea244ce-0000-0000-0000-000000000b01"
	newID := "2266314c-0000-0000-0000-000000000b02"

	// The rebind path's quality gate (geminiSessionHasConversationData)
	// rejects the candidate when GeminiSessionID is already set unless
	// the candidate's session file exists on disk with at least one
	// message — seed it so we exercise the rebind branch.
	seedGeminiSessionFile(t, projectPath, newID)

	now := time.Now()
	seedRow := &statedb.InstanceRow{
		ID:          inst.ID,
		Title:       inst.Title,
		ProjectPath: inst.ProjectPath,
		GroupPath:   inst.GroupPath,
		Command:     inst.Command,
		Tool:        "gemini",
		Status:      "idle",
		CreatedAt:   now,
		ToolData:    json.RawMessage(`{"gemini_session_id":"` + oldID + `"}`),
	}
	if err := db.SaveInstance(seedRow); err != nil {
		t.Fatalf("SaveInstance seed: %v", err)
	}

	inst.GeminiSessionID = oldID
	if got := readGeminiSessionIDFromDB(t, db, inst.ID); got != oldID {
		t.Fatalf("pre-condition: DB gemini_session_id = %q, want %q", got, oldID)
	}

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	if inst.GeminiSessionID != newID {
		t.Fatalf("in-memory GeminiSessionID = %q, want %q (rebind path didn't fire — "+
			"check geminiSessionHasConversationData seed)", inst.GeminiSessionID, newID)
	}

	if got := readGeminiSessionIDFromDB(t, db, inst.ID); got != newID {
		t.Fatalf("post-rebind DB gemini_session_id = %q, want %q. "+
			"bindGeminiSessionFromHook mutated in-memory state but did not "+
			"persist to tool_data — DB-direct consumers will see the stale UUID.",
			got, newID)
	}
}

// TestBindPersistsGeminiSessionIDToDB covers the cold-start branch —
// GeminiSessionID == "" lets the quality gate through without a
// session file on disk. Must persist as eagerly as the rebind path.
func TestBindPersistsGeminiSessionIDToDB(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	db := withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-gemini-bind-persist", projectPath, "gemini")

	newID := "7a3b9c10-0000-0000-0000-000000000b03"

	seedRow := &statedb.InstanceRow{
		ID:          inst.ID,
		Title:       inst.Title,
		ProjectPath: inst.ProjectPath,
		GroupPath:   inst.GroupPath,
		Command:     inst.Command,
		Tool:        "gemini",
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

	if inst.GeminiSessionID != newID {
		t.Fatalf("in-memory GeminiSessionID = %q, want %q", inst.GeminiSessionID, newID)
	}
	if got := readGeminiSessionIDFromDB(t, db, inst.ID); got != newID {
		t.Fatalf("post-bind DB gemini_session_id = %q, want %q — cold-start bind "+
			"did not persist to tool_data", got, newID)
	}
}

// TestGeminiRebindNoOpWhenStateDBUnset confirms the persistence call
// is optional: when no global StateDB is wired, the rebind must still
// mutate in-memory state and not panic.
func TestGeminiRebindNoOpWhenStateDBUnset(t *testing.T) {
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
	inst := NewInstanceWithTool("hook-gemini-rebind-nodb", projectPath, "gemini")

	newID := "9f5e1d80-0000-0000-0000-000000000b04"
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "SessionStart",
		UpdatedAt: time.Now(),
	})

	if inst.GeminiSessionID != newID {
		t.Fatalf("in-memory GeminiSessionID = %q, want %q (rebind path must work "+
			"even without a global StateDB)", inst.GeminiSessionID, newID)
	}
}

// TestGeminiRebindPreservesUnrelatedToolDataKeys pins json_set
// semantics: only $.gemini_session_id and $.gemini_detected_at may
// be rewritten — every other key in tool_data must survive untouched.
func TestGeminiRebindPreservesUnrelatedToolDataKeys(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	db := withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-gemini-rebind-extras", projectPath, "gemini")

	newID := "2266314c-0000-0000-0000-000000000b06"

	// Use the cold-start bind branch (GeminiSessionID == "") so we
	// don't need to seed a gemini session file on disk — the
	// persistence path is identical to the rebind branch (both call
	// bindGeminiSessionFromHook).
	seedJSON := `{
		"latest_prompt": "summarize the README",
		"notes": "user prefers concise answers",
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
		Tool:        "gemini",
		Status:      "idle",
		CreatedAt:   time.Now(),
		ToolData:    json.RawMessage(seedJSON),
	}
	if err := db.SaveInstance(seedRow); err != nil {
		t.Fatalf("SaveInstance seed: %v", err)
	}

	inst.GeminiSessionID = ""
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "SessionStart",
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

	if got, _ := blob["gemini_session_id"].(string); got != newID {
		t.Errorf("gemini_session_id = %q, want %q (rebind didn't persist)", got, newID)
	}
	if _, ok := blob["gemini_detected_at"]; !ok {
		t.Errorf("gemini_detected_at missing — rebind should have written it")
	}

	preserved := map[string]any{
		"latest_prompt":    "summarize the README",
		"notes":            "user prefers concise answers",
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
