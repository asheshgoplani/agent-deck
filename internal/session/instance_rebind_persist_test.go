package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Regression for the "rebind logged but never persisted" gap.
//
// Symptom on a real install: after `/clear` inside a Claude tile, the
// `.sid` sidecar and the in-memory Instance both advance to the new
// session UUID — but `instances.tool_data.claude_session_id` in
// `state.db` stays pinned at the pre-/clear UUID indefinitely. The
// lifecycle log fills with fresh `rebind` entries on every poll because
// peer agent-deck processes keep reloading the stale row from disk and
// clobbering the in-memory mutation.
//
// Root cause: `bindClaudeSessionFromHook` mutated `i.ClaudeSessionID`
// in memory and emitted the lifecycle event, but no `UpdateHookStatus`
// caller (TUI tick, web refresh, CLI status refresh) called `Save`
// afterwards. The PERSIST-12 contract above the function assumed an
// external save cycle would; in practice none ran. The fix added a
// targeted `WriteClaudeSessionBinding` UPDATE inside the bind path so
// the row is current the instant the bind decision is made.
//
// These tests pin the fix: both the cold `bind` branch and the
// `rebind` branch must leave `tool_data.claude_session_id` matching
// the new UUID after `UpdateHookStatus` returns.

// withTempGlobalStateDB swaps the package-global StateDB for the test's
// duration. Returns the test DB so the caller can seed rows and assert
// against them. Restores the previous global on cleanup so tests run in
// any order without contaminating each other (or production code that
// might be loaded into the same test binary).
func withTempGlobalStateDB(t *testing.T) *statedb.StateDB {
	t.Helper()
	tmpDir := t.TempDir()
	db, err := statedb.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("statedb.Open: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("statedb.Migrate: %v", err)
	}
	prev := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() {
		statedb.SetGlobal(prev)
		_ = db.Close()
	})
	return db
}

// readClaudeSessionIDFromDB returns tool_data.claude_session_id for the
// given instance, reading via a fresh query (no in-memory caching) so
// the assertion reflects what a DB-direct consumer like claudopticon
// would observe.
func readClaudeSessionIDFromDB(t *testing.T, db *statedb.StateDB, id string) string {
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
			ClaudeSessionID string `json:"claude_session_id"`
		}
		if len(row.ToolData) == 0 {
			return ""
		}
		if err := json.Unmarshal(row.ToolData, &blob); err != nil {
			t.Fatalf("unmarshal tool_data for %s: %v (raw=%s)", id, err, string(row.ToolData))
		}
		return blob.ClaudeSessionID
	}
	t.Fatalf("instance %q not found in DB", id)
	return ""
}

// TestRebindPersistsClaudeSessionIDToDB exercises the /clear shape from
// the bug report: an instance bound to OLD, then UpdateHookStatus fires
// for NEW (smaller-but-fresher candidate, the canonical /clear pattern
// that #856's mtime-gap exception lets through). After the call,
// tool_data.claude_session_id in the database must equal NEW.
//
// Pre-fix this test fails: in-memory ClaudeSessionID advances but the
// row in the DB stays at OLD.
func TestRebindPersistsClaudeSessionIDToDB(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmpHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	db := withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-rebind-persist", projectPath, "claude")

	oldID := "5ea244ce-0000-0000-0000-0000000000aa"
	newID := "2266314c-0000-0000-0000-0000000000bb"

	// Seed the DB row that production code would have written when the
	// instance was first created — including the pre-/clear binding.
	now := time.Now()
	seedRow := &statedb.InstanceRow{
		ID:          inst.ID,
		Title:       inst.Title,
		ProjectPath: inst.ProjectPath,
		GroupPath:   inst.GroupPath,
		Command:     inst.Command,
		Tool:        "claude",
		Status:      "idle",
		CreatedAt:   now,
		ToolData:    json.RawMessage(`{"claude_session_id":"` + oldID + `"}`),
	}
	if err := db.SaveInstance(seedRow); err != nil {
		t.Fatalf("SaveInstance seed: %v", err)
	}

	// /clear shape: old rich session, smaller-but-fresh candidate.
	// Mtime gap >= clearRebindMtimeGrace makes the rebind branch fire
	// despite the size loss (issue #856).
	oldPath := seedClaudeJSONL(t, inst, oldID, 200, 1024)
	newPath := seedClaudeJSONL(t, inst, newID, 1, 8)
	mtimeNow := time.Now()
	oldMtime := mtimeNow.Add(-clearRebindMtimeGrace - time.Second)
	if err := os.Chtimes(oldPath, oldMtime, oldMtime); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(newPath, mtimeNow, mtimeNow); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}

	// Pre-condition: DB and instance both reflect OLD.
	inst.ClaudeSessionID = oldID
	if got := readClaudeSessionIDFromDB(t, db, inst.ID); got != oldID {
		t.Fatalf("pre-condition: DB claude_session_id = %q, want %q", got, oldID)
	}

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
	})

	// Post-condition (in-memory) — the existing behavior pre-fix.
	if inst.ClaudeSessionID != newID {
		t.Fatalf("in-memory ClaudeSessionID = %q, want %q (rebind path didn't fire — "+
			"check clearRebindMtimeGrace boundary or sessionHasConversationData)",
			inst.ClaudeSessionID, newID)
	}

	// Post-condition (DB) — the regression we are pinning. Pre-fix this
	// stays at OLD because the rebind never reached SQLite.
	if got := readClaudeSessionIDFromDB(t, db, inst.ID); got != newID {
		t.Fatalf("post-rebind DB claude_session_id = %q, want %q. "+
			"bindClaudeSessionFromHook mutated in-memory state but did not "+
			"persist to tool_data — DB-direct consumers (claudopticon, etc.) "+
			"will see the pre-/clear UUID until something else triggers a save.",
			got, newID)
	}
}

// TestBindPersistsClaudeSessionIDToDB covers the cold-start branch at
// instance.go:3599 — when an instance has no ClaudeSessionID yet and
// the very first hook event arrives. This branch is `action: bind`
// (not rebind) and skips the size-guard entirely, so it must persist
// just as eagerly as the rebind path.
func TestBindPersistsClaudeSessionIDToDB(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmpHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	db := withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-bind-persist", projectPath, "claude")

	newID := "7a3b9c10-0000-0000-0000-0000000000cc"

	seedRow := &statedb.InstanceRow{
		ID:          inst.ID,
		Title:       inst.Title,
		ProjectPath: inst.ProjectPath,
		GroupPath:   inst.GroupPath,
		Command:     inst.Command,
		Tool:        "claude",
		Status:      "idle",
		CreatedAt:   time.Now(),
		// No claude_session_id yet — cold start.
		ToolData: json.RawMessage(`{}`),
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

	if inst.ClaudeSessionID != newID {
		t.Fatalf("in-memory ClaudeSessionID = %q, want %q", inst.ClaudeSessionID, newID)
	}
	if got := readClaudeSessionIDFromDB(t, db, inst.ID); got != newID {
		t.Fatalf("post-bind DB claude_session_id = %q, want %q — cold-start bind "+
			"did not persist to tool_data", got, newID)
	}
}

// TestRebindNoOpWhenStateDBUnset confirms the persistence call is
// optional: when no global StateDB is wired (CLI processes that haven't
// initialized statedb yet, certain test paths), the rebind must still
// mutate in-memory state and not panic. This protects the existing
// contract that UpdateHookStatus is callable from any context.
func TestRebindNoOpWhenStateDBUnset(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmpHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	// Explicitly null out the global, restoring whatever was there before
	// (likely nil already, but be defensive against test-order coupling).
	prev := statedb.GetGlobal()
	statedb.SetGlobal(nil)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-rebind-nodb", projectPath, "claude")

	newID := "9f5e1d80-0000-0000-0000-0000000000dd"
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "SessionStart",
		UpdatedAt: time.Now(),
	})

	if inst.ClaudeSessionID != newID {
		t.Fatalf("in-memory ClaudeSessionID = %q, want %q (rebind path must work "+
			"even without a global StateDB)", inst.ClaudeSessionID, newID)
	}
}

// TestRebindPreservesUnrelatedToolDataKeys pins the json_set semantics
// of WriteClaudeSessionBinding: only $.claude_session_id and
// $.claude_detected_at may be rewritten — every other key in tool_data
// must survive untouched. This is the contract that prevents a future
// "let's just do tool_data = ?" simplification from silently dropping
// latest_prompt, notes, MCP attachments, sandbox config, plugins,
// auto-linked channels, or any user-managed unmodeled keys (e.g.
// clear_on_compact) on every Claude /clear.
func TestRebindPreservesUnrelatedToolDataKeys(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmpHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	db := withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-rebind-extras", projectPath, "claude")

	oldID := "5ea244ce-0000-0000-0000-0000000000ee"
	newID := "2266314c-0000-0000-0000-0000000000ff"

	// Seed a tool_data row carrying a representative spread of keys
	// across all three classes:
	//   - typed schema, will be overwritten by the rebind:
	//       claude_session_id, claude_detected_at
	//   - typed schema, unrelated (must survive):
	//       latest_prompt, notes, loaded_mcp_names, ssh_host
	//   - unmodeled extras (must survive via tool_data_extras.go merge
	//     semantics, even though we bypass MergeToolDataExtras with a
	//     direct json_set — they live at top-level keys json_set leaves
	//     alone):
	//       clear_on_compact
	seedJSON := `{
		"claude_session_id": "` + oldID + `",
		"claude_detected_at": 1700000000,
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
		Tool:        "claude",
		Status:      "idle",
		CreatedAt:   time.Now(),
		ToolData:    json.RawMessage(seedJSON),
	}
	if err := db.SaveInstance(seedRow); err != nil {
		t.Fatalf("SaveInstance seed: %v", err)
	}

	// Trigger the cold-start bind branch (simpler than the rebind
	// branch — no JSONL/mtime setup needed — and the persistence path
	// is identical, both routes call bindClaudeSessionFromHook).
	inst.ClaudeSessionID = ""
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: newID,
		Event:     "SessionStart",
		UpdatedAt: time.Now(),
	})

	// Read the merged tool_data back and decode liberally so we can
	// assert against every key regardless of typed-vs-extras classification.
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

	// Rewritten keys.
	if got, _ := blob["claude_session_id"].(string); got != newID {
		t.Errorf("claude_session_id = %q, want %q (rebind didn't persist)", got, newID)
	}
	if v, ok := blob["claude_detected_at"]; !ok {
		t.Errorf("claude_detected_at missing — rebind should have written it")
	} else if f, _ := v.(float64); int64(f) == 1700000000 {
		t.Errorf("claude_detected_at = %v, want a fresh timestamp (rebind didn't refresh it)", v)
	}

	// Preserved keys — the load-bearing assertions for this test.
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
				"replace the whole column. If you simplified to `tool_data = ?` "+
				"this is the regression you introduced.", k)
			continue
		}
		if got != want {
			t.Errorf("tool_data[%q] = %v, want %v", k, got, want)
		}
	}
	// Array preservation needs a slice equality check.
	mcps, ok := blob["loaded_mcp_names"].([]any)
	if !ok {
		t.Errorf("tool_data lost loaded_mcp_names after rebind (got %T)", blob["loaded_mcp_names"])
	} else if len(mcps) != 2 || mcps[0] != "github" || mcps[1] != "linear" {
		t.Errorf("loaded_mcp_names = %v, want [github linear]", mcps)
	}
}
