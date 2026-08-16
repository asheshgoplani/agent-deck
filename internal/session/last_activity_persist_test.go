// Issue #1846: the TUI's two "ago" surfaces (row badge, preview ⏱ line)
// disagreed and both went stale because every trace of real agent activity
// is ephemeral — the hook status file is deleted on attach-return, the tmux
// tracker's confirmed activity is process-local, and MarkAccessed only
// reaches SQLite on the next full save cycle (which may never come).
//
// These tests pin the durable last-activity record that closes the gap:
// tool_data.last_activity_at (same extras-zone mechanism as #1704's
// last_started_at), advanced whenever hook evidence is observed, flushed
// before ClearHookStatus destroys that evidence, and never rewound.
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// readLastActivityAtFromDB returns tool_data.last_activity_at for the given
// instance via a fresh LoadInstances query — what a restarted TUI or a
// DB-direct consumer would observe.
func readLastActivityAtFromDB(t *testing.T, db *statedb.StateDB, id string) time.Time {
	t.Helper()
	rows, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		return ReadLastActivityAtFromToolData(row.ToolData)
	}
	t.Fatalf("instance %q not found in DB", id)
	return time.Time{}
}

func seedInstanceRow(t *testing.T, db *statedb.StateDB, inst *Instance, toolData string) {
	t.Helper()
	row := &statedb.InstanceRow{
		ID:          inst.ID,
		Title:       inst.Title,
		ProjectPath: inst.ProjectPath,
		GroupPath:   inst.GroupPath,
		Command:     inst.Command,
		Tool:        inst.Tool,
		Status:      "idle",
		CreatedAt:   time.Now(),
		ToolData:    json.RawMessage(toolData),
	}
	if err := db.SaveInstance(row); err != nil {
		t.Fatalf("SaveInstance seed: %v", err)
	}
}

// TestLastActivityAt_ToolDataPersistenceRoundTrip mirrors
// TestLastStartedAt_ToolDataPersistenceRoundTrip: the extras-zone helpers
// round-trip, preserve unrelated keys, and a zero time clears the key so
// legacy rows stay indistinguishable from never-active ones.
func TestLastActivityAt_ToolDataPersistenceRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 2, 21, 55, 0, 0, time.UTC)

	td := WriteLastActivityAtToToolData(nil, at)
	if got := ReadLastActivityAtFromToolData(td); !got.Equal(at) {
		t.Fatalf("ReadLastActivityAtFromToolData after Write = %v, want %v", got, at)
	}

	cleared := WriteLastActivityAtToToolData(td, time.Time{})
	if got := ReadLastActivityAtFromToolData(cleared); !got.IsZero() {
		t.Fatalf("Write(td, zero) should clear, got %v", got)
	}

	mixed := []byte(`{"color":"#ff00aa","claude_session_id":"abc"}`)
	out := WriteLastActivityAtToToolData(mixed, at)
	if got := ReadLastActivityAtFromToolData(out); !got.Equal(at) {
		t.Fatalf("round-trip with extras lost last_activity_at: got %v, want %v", got, at)
	}
	if !strings.Contains(string(out), `"color":"#ff00aa"`) {
		t.Fatalf("round-trip dropped color: %s", string(out))
	}
	if !strings.Contains(string(out), `"claude_session_id":"abc"`) {
		t.Fatalf("round-trip dropped claude_session_id: %s", string(out))
	}
}

// TestLastActivityAt_SQLiteRoundTrip proves the value survives a real
// SaveWithGroups -> LoadWithGroups cycle — the boundary a TUI restart
// crosses, which is exactly where the pre-fix badge collapsed back to
// CreatedAt.
func TestLastActivityAt_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("last-activity-roundtrip", "/tmp")
	inst.Tool = "shell"
	at := time.Now().Add(-90 * time.Minute).Truncate(time.Second)
	inst.lastActivityAt = at

	groupTree := NewGroupTreeWithGroups([]*Instance{inst}, nil)
	if err := storage.SaveWithGroups([]*Instance{inst}, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(loaded))
	}
	if got := loaded[0].LastActivityAt(); !got.Equal(at) {
		t.Fatalf("LastActivityAt not preserved across SQLite round-trip: got %v, want %v", got, at)
	}

	// Never-active sessions must keep reading as zero.
	fresh := NewInstance("never-active-roundtrip", "/tmp")
	fresh.Tool = "shell"
	groupTree2 := NewGroupTreeWithGroups([]*Instance{fresh}, nil)
	if err := storage.SaveWithGroups([]*Instance{fresh}, groupTree2); err != nil {
		t.Fatalf("SaveWithGroups (never-active): %v", err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups (never-active): %v", err)
	}
	for _, i := range loaded2 {
		if i.ID == fresh.ID && !i.LastActivityAt().IsZero() {
			t.Fatalf("never-active instance got non-zero LastActivityAt after round-trip: %v", i.LastActivityAt())
		}
	}
}

// TestUpdateHookStatus_AdvancesLastActivityAt: an accepted hook event (the
// instance's own bound session id reporting from its own cwd) must advance
// the durable record — both in memory and in the DB row, so a later TUI
// restart still sees it.
func TestUpdateHookStatus_AdvancesLastActivityAt(t *testing.T) {
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
	inst := NewInstanceWithTool("hook-activity-advance", projectPath, "claude")
	sessionID := "5ea244ce-0000-0000-0000-0000000000aa"
	inst.ClaudeSessionID = sessionID
	seedInstanceRow(t, db, inst, `{"claude_session_id":"`+sessionID+`"}`)

	eventAt := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	inst.UpdateHookStatus(&HookStatus{
		Status:    "waiting",
		Event:     "Stop",
		SessionID: sessionID,
		UpdatedAt: eventAt,
		Cwd:       projectPath,
	})

	if got := inst.LastActivityAt(); !got.Equal(eventAt) {
		t.Fatalf("LastActivityAt after accepted hook event = %v, want %v", got, eventAt)
	}
	if got := readLastActivityAtFromDB(t, db, inst.ID); !got.Equal(eventAt) {
		t.Fatalf("DB last_activity_at after accepted hook event = %v, want %v", got, eventAt)
	}
}

// TestUpdateHookStatus_StaleReplayDoesNotRewind: the watcher re-applies the
// same on-disk file on rescans; an event older than what we already know
// must never pull the durable record backwards.
func TestUpdateHookStatus_StaleReplayDoesNotRewind(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmpHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("hook-activity-rewind", projectPath, "claude")
	sessionID := "5ea244ce-0000-0000-0000-0000000000ab"
	inst.ClaudeSessionID = sessionID

	newer := time.Now().Add(-1 * time.Minute).Truncate(time.Second)
	older := newer.Add(-30 * time.Minute)

	inst.UpdateHookStatus(&HookStatus{Status: "waiting", Event: "Stop", SessionID: sessionID, UpdatedAt: newer, Cwd: projectPath})
	inst.UpdateHookStatus(&HookStatus{Status: "waiting", Event: "Stop", SessionID: sessionID, UpdatedAt: older, Cwd: projectPath})

	if got := inst.LastActivityAt(); !got.Equal(newer) {
		t.Fatalf("stale replay rewound LastActivityAt to %v, want %v", got, newer)
	}
}

// TestUpdateHookStatus_ForeignRejectDoesNotAdvanceLastActivityAt: a foreign
// ephemeral (a `claude -p` child that inherited AGENTDECK_INSTANCE_ID, cwd
// provably outside the instance's paths) is restored to a no-op by the
// #1729 guard — its activity must not be credited to this instance either.
func TestUpdateHookStatus_ForeignRejectDoesNotAdvanceLastActivityAt(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmpHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	withTempGlobalStateDB(t)

	projectPath := filepath.Join(tmpHome, "project")
	foreignCwd := filepath.Join(tmpHome, "elsewhere")
	for _, d := range []string{projectPath, foreignCwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	inst := NewInstanceWithTool("hook-activity-foreign", projectPath, "claude")
	inst.ClaudeSessionID = "5ea244ce-0000-0000-0000-0000000000ac"

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		Event:     "UserPromptSubmit",
		SessionID: "99999999-0000-0000-0000-0000000000ff", // foreign candidate
		UpdatedAt: time.Now(),
		Cwd:       foreignCwd,
	})

	if got := inst.LastActivityAt(); !got.IsZero() {
		t.Fatalf("rejected foreign hook event advanced LastActivityAt to %v, want zero", got)
	}
}

// TestClearHookStatus_PreservesActivityEvidence is the heart of #1846's
// repro: attach-return calls ClearHookStatus to force live status polling
// (a fresh-looking "waiting" file would mask a pane killed by /q), and
// pre-fix that also destroyed the only durable record of real activity —
// the badge collapsed to CreatedAt ("2d 9h ago") while the preview showed
// LastAccessedAt ("7h 43m ago"). Clearing the hook STATUS must flush the
// activity timestamp to the DB before the evidence is gone, even when the
// throttled write in UpdateHookStatus skipped it.
func TestClearHookStatus_PreservesActivityEvidence(t *testing.T) {
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
	inst := NewInstanceWithTool("hook-activity-clear", projectPath, "claude")
	sessionID := "5ea244ce-0000-0000-0000-0000000000ad"
	inst.ClaudeSessionID = sessionID
	seedInstanceRow(t, db, inst, `{"claude_session_id":"`+sessionID+`"}`)

	// Two events inside the persist-throttle window: the first persists,
	// the second stays memory-only — the exact state attach-return hits.
	first := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	second := first.Add(10 * time.Second)
	inst.UpdateHookStatus(&HookStatus{Status: "running", Event: "UserPromptSubmit", SessionID: sessionID, UpdatedAt: first, Cwd: projectPath})
	inst.UpdateHookStatus(&HookStatus{Status: "waiting", Event: "Stop", SessionID: sessionID, UpdatedAt: second, Cwd: projectPath})

	inst.ClearHookStatus()

	if got := inst.LastActivityAt(); !got.Equal(second) {
		t.Fatalf("ClearHookStatus lost in-memory activity evidence: got %v, want %v", got, second)
	}
	if got := readLastActivityAtFromDB(t, db, inst.ID); !got.Equal(second) {
		t.Fatalf("ClearHookStatus did not flush activity evidence to DB: got %v, want %v", got, second)
	}
	// The status itself must still be cleared — that is ClearHookStatus's job.
	if status, _ := inst.GetHookStatus(); status != "" {
		t.Fatalf("ClearHookStatus left hook status %q, want empty", status)
	}
}

// TestMarkAccessed_PersistsLastAccessedDurably: pre-fix, MarkAccessed only
// mutated memory and reached SQLite on the next full save cycle — which may
// never come before the TUI exits, leaving the preview's fallback hours
// stale (the "7h 43m ago" half of #1846). The targeted write makes each
// attach/detach durable on its own.
func TestMarkAccessed_PersistsLastAccessedDurably(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := withTempGlobalStateDB(t)

	inst := NewInstance("mark-accessed-durable", "/tmp")
	inst.Tool = "shell"
	seedInstanceRow(t, db, inst, `{}`)

	before := time.Now().Add(-time.Second)
	inst.MarkAccessed()

	rows, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	for _, row := range rows {
		if row.ID != inst.ID {
			continue
		}
		if row.LastAccessed.Before(before) {
			t.Fatalf("MarkAccessed not persisted: DB last_accessed = %v, want >= %v", row.LastAccessed, before)
		}
		return
	}
	t.Fatalf("instance %q not found in DB", inst.ID)
}

// TestDisplayLastActivityTime_PrefersPersistedActivity: the preview (and
// staleActivityEvidence in status --stale) must see durable activity that
// outlived the hook file, instead of falling through to the older
// LastAccessedAt.
func TestDisplayLastActivityTime_PrefersPersistedActivity(t *testing.T) {
	inst := NewInstance("display-persisted-activity", "/tmp")
	inst.Tool = "shell"
	inst.LastAccessedAt = time.Now().Add(-8 * time.Hour)
	activity := time.Now().Add(-90 * time.Minute).Truncate(time.Second)
	inst.lastActivityAt = activity

	if got := inst.DisplayLastActivityTime(); !got.Equal(activity) {
		t.Fatalf("DisplayLastActivityTime = %v, want persisted activity %v", got, activity)
	}

	// An attach NEWER than the last real activity is a peek, not activity —
	// the persisted evidence still wins (matches the badge's semantics).
	inst.LastAccessedAt = time.Now()
	if got := inst.DisplayLastActivityTime(); !got.Equal(activity) {
		t.Fatalf("newer LastAccessedAt overrode real activity: got %v, want %v", got, activity)
	}
}
