package session

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #1349 (High-severity data-integrity bug): the notify-daemon rebinds
// STOPPED/REMOVED sessions' Claude session-ids every poll cycle from a stale
// SessionEnd hook file, causing session-id collisions that corrupt routing.
//
// These three regression tests pin the three guards of the fix:
//   1. TestSyncOnce_DoesNotRebindStoppedSession  — liveness gate in the daemon.
//   2. TestUpdateHookStatus_IgnoresSessionEndEvent — terminal-event guard.
//   3. TestGetJSONLPath_NoCollisionAcrossInstances — collision guard.
//
// Written test-first: each must FAIL on pre-#1349 main.

// seedHookStatusFile writes a <instanceID>.json hook status file into the
// hooks dir that readHookStatusFile / hookStatusForInstance reads, modelling
// the stale SessionEnd record that lingers for 24h after a session ends.
func seedHookStatusFile(t *testing.T, instanceID, event, sessionID, status string) {
	t.Helper()
	dir := GetHooksDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	payload := map[string]any{
		"status":     status,
		"session_id": sessionID,
		"event":      event,
		"ts":         time.Now().Unix(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, instanceID+".json"), data, 0o644); err != nil {
		t.Fatalf("write hook file: %v", err)
	}
}

// readLifecycleEventsFor returns every session-id-lifecycle event recorded for
// the given instance id.
func readLifecycleEventsFor(t *testing.T, instanceID string) []SessionIDLifecycleEvent {
	t.Helper()
	path := GetSessionIDLifecycleLogPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open lifecycle log: %v", err)
	}
	defer f.Close()

	var out []SessionIDLifecycleEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev SessionIDLifecycleEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.InstanceID == instanceID {
			out = append(out, ev)
		}
	}
	return out
}

// bootstrapDaemonProfile sets up an isolated HOME + agent-deck profile and
// returns a daemon wired to that profile's storage, plus the storage.
func bootstrapDaemonProfile(t *testing.T, profile string) (*TransitionDaemon, *Storage) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENT_DECK_PROFILE", profile)
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(filepath.Join(home, ".agent-deck"), 0o700); err != nil {
		t.Fatalf("mkdir agent-deck: %v", err)
	}

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	// Wire the global DB so bindClaudeSessionFromHook's WriteClaudeSessionBinding
	// persists into the same DB we read back from.
	statedb.SetGlobal(storage.GetDB())
	t.Cleanup(func() { statedb.SetGlobal(nil) })

	d := NewTransitionDaemon()
	d.storages[profile] = storage
	return d, storage
}

// writeTranscript1349 materializes a Claude transcript jsonl for the given instance
// + session id with `n` conversation records (each containing a "sessionId"
// field, which is what sessionHasConversationData detects). Larger n => larger
// byte size, so it can win the v1.7.23 size guard during a rebind.
func writeTranscript1349(t *testing.T, inst *Instance, sessionID string, n int) {
	t.Helper()
	configDir := GetClaudeConfigDirForInstance(inst)
	projDir := filepath.Join(configDir, "projects", ConvertToClaudeDirName(inst.EffectiveWorkingDir()))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	f, err := os.Create(filepath.Join(projDir, sessionID+".jsonl"))
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	defer f.Close()
	for i := 0; i < n; i++ {
		rec := map[string]any{
			"sessionId": sessionID,
			"type":      "user",
			"message":   map[string]any{"role": "user", "content": "hello world record padding"},
		}
		b, _ := json.Marshal(rec)
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatalf("write transcript record: %v", err)
		}
	}
}

// TestSyncOnce_DoesNotRebindStoppedSession is the PRIMARY regression test for
// #1349. A stopped Claude instance bound to session "X" has a lingering stale
// SessionEnd hook file naming session "Y". Session "Y" even has richer
// conversation data than "X" (so the size-based rebind guard would NOT block
// it). The daemon poll must STILL NOT rebind it, because a stopped session is
// not live — its session id must never change from a stale terminal hook.
func TestSyncOnce_DoesNotRebindStoppedSession(t *testing.T) {
	const profile = "_test_issue1349_stopped"
	d, storage := bootstrapDaemonProfile(t, profile)

	const sessX = "11111111-1111-1111-1111-111111111111"
	const sessY = "22222222-2222-2222-2222-222222222222"

	inst := &Instance{
		ID:              "stopped-inst-1349",
		Title:           "stopped",
		ProjectPath:     filepath.Join(os.Getenv("HOME"), "issue1349-stopped"),
		GroupPath:       DefaultGroupPath,
		Tool:            "claude",
		Status:          StatusStopped,
		ClaudeSessionID: sessX,
		CreatedAt:       time.Now(),
	}
	if err := os.MkdirAll(inst.ProjectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	// X is the bound session; Y is the stale-hook candidate with MORE data, so
	// the size/data guards in UpdateHookStatus would otherwise allow the rebind.
	writeTranscript1349(t, inst, sessX, 1)
	writeTranscript1349(t, inst, sessY, 50)

	// Lingering stale terminal hook record naming the DIFFERENT session id.
	seedHookStatusFile(t, inst.ID, "SessionEnd", sessY, "dead")

	// Run several poll cycles.
	for i := 0; i < 3; i++ {
		d.syncProfile(profile)
	}

	// Assert: no bind/rebind lifecycle event for this instance.
	for _, ev := range readLifecycleEventsFor(t, inst.ID) {
		if ev.Action == "bind" || ev.Action == "rebind" {
			t.Fatalf("stopped session was rebound: got lifecycle action %q (old=%q new=%q event=%q)",
				ev.Action, ev.OldID, ev.NewID, ev.HookEvent)
		}
	}

	// Assert: DB claude_session_id stays "X".
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	var got string
	for _, in := range instances {
		if in.ID == inst.ID {
			got = in.ClaudeSessionID
		}
	}
	if got != sessX {
		t.Fatalf("stopped session claude_session_id changed: want %q, got %q", sessX, got)
	}
}

// TestUpdateHookStatus_IgnoresSessionEndEvent is DEFENSE-IN-DEPTH #1: even when
// UpdateHookStatus is invoked directly with a terminal SessionEnd payload, it
// must never bind the session id from that payload.
func TestUpdateHookStatus_IgnoresSessionEndEvent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	const sessX = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const sessY = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	inst := &Instance{
		ID:              "sessionend-inst-1349",
		Title:           "ended",
		ProjectPath:     filepath.Join(home, "issue1349-ended"),
		GroupPath:       DefaultGroupPath,
		Tool:            "claude",
		Status:          StatusStopped,
		ClaudeSessionID: sessX,
		CreatedAt:       time.Now(),
	}
	if err := os.MkdirAll(inst.ProjectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	// Give the SessionEnd candidate (Y) richer data than the bound session (X)
	// so the size/data guards in UpdateHookStatus would otherwise permit the
	// rebind. The terminal-event guard must reject it regardless.
	writeTranscript1349(t, inst, sessX, 1)
	writeTranscript1349(t, inst, sessY, 50)

	inst.UpdateHookStatus(&HookStatus{
		Event:     "SessionEnd",
		SessionID: sessY,
		Status:    "dead",
		UpdatedAt: time.Now(),
	})

	if inst.ClaudeSessionID != sessX {
		t.Fatalf("SessionEnd payload rebound the session id: want %q, got %q", sessX, inst.ClaudeSessionID)
	}
}

// TestGetJSONLPath_NoCollisionAcrossInstances is DEFENSE-IN-DEPTH #2: two live
// instances forced to share the same claude_session_id (and project path) must
// not silently resolve to the same transcript path — the resolver must refuse
// or flag the collision.
func TestGetJSONLPath_NoCollisionAcrossInstances(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	projectPath := filepath.Join(home, "proj1349")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	// Materialize a transcript file so a naive GetJSONLPath would return it.
	configDir := GetClaudeConfigDir()
	projDir := filepath.Join(configDir, "projects", ConvertToClaudeDirName(projectPath))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir claude project dir: %v", err)
	}
	sharedID := "SHARED-SESSION-ID"
	jsonl := filepath.Join(projDir, sharedID+".jsonl")
	if err := os.WriteFile(jsonl, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	mk := func(id string) *Instance {
		return &Instance{
			ID:              id,
			Title:           id,
			ProjectPath:     projectPath,
			GroupPath:       DefaultGroupPath,
			Tool:            "claude",
			Status:          StatusRunning,
			ClaudeSessionID: sharedID,
			CreatedAt:       time.Now(),
		}
	}
	a := mk("inst-a-1349")
	b := mk("inst-b-1349")

	// The collision-aware resolver must refuse rather than silently return the
	// same transcript path for two distinct live instances.
	pathA, errA := a.GetJSONLPathChecked([]*Instance{a, b})
	if errA == nil {
		t.Fatalf("expected collision error for instance a, got path %q", pathA)
	}
	pathB, errB := b.GetJSONLPathChecked([]*Instance{a, b})
	if errB == nil {
		t.Fatalf("expected collision error for instance b, got path %q", pathB)
	}
}

// TestGetJSONLPathChecked_NoCollisionResolvesNormally guards against the
// collision check over-blocking: a unique session id (no other live instance
// shares it) must resolve to its real transcript path, identical to GetJSONLPath.
func TestGetJSONLPathChecked_NoCollisionResolvesNormally(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	projectPath := filepath.Join(home, "proj1349-unique")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := &Instance{
		ID:              "inst-unique-1349",
		Title:           "unique",
		ProjectPath:     projectPath,
		GroupPath:       DefaultGroupPath,
		Tool:            "claude",
		Status:          StatusRunning,
		ClaudeSessionID: "UNIQUE-SESSION-ID",
		CreatedAt:       time.Now(),
	}
	writeTranscript1349(t, inst, inst.ClaudeSessionID, 3)

	// A sibling with a DIFFERENT session id must not trip the collision guard.
	sibling := &Instance{
		ID:              "sibling-1349",
		ProjectPath:     projectPath,
		GroupPath:       DefaultGroupPath,
		Tool:            "claude",
		Status:          StatusRunning,
		ClaudeSessionID: "OTHER-SESSION-ID",
		CreatedAt:       time.Now(),
	}

	got, err := inst.GetJSONLPathChecked([]*Instance{inst, sibling})
	if err != nil {
		t.Fatalf("unique session id was wrongly flagged as a collision: %v", err)
	}
	if want := inst.GetJSONLPath(); got != want || got == "" {
		t.Fatalf("checked resolve mismatch: got %q, want %q", got, want)
	}
}

// TestGetJSONLPathChecked_SameIDDifferentProjectIsNotCollision is the Codex
// medium-finding guard: two live instances that share a session id but resolve
// to DIFFERENT transcript directories (different project paths) do not collide
// on one transcript file, so the guard must not block either.
func TestGetJSONLPathChecked_SameIDDifferentProjectIsNotCollision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	const sharedID = "SAME-ID-DIFFERENT-PROJECT"

	mk := func(id, sub string) *Instance {
		pp := filepath.Join(home, sub)
		if err := os.MkdirAll(pp, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
		inst := &Instance{
			ID:              id,
			Title:           id,
			ProjectPath:     pp,
			GroupPath:       DefaultGroupPath,
			Tool:            "claude",
			Status:          StatusRunning,
			ClaudeSessionID: sharedID,
			CreatedAt:       time.Now(),
		}
		writeTranscript1349(t, inst, sharedID, 2)
		return inst
	}
	a := mk("inst-a-diffproj", "projA")
	b := mk("inst-b-diffproj", "projB")

	gotA, errA := a.GetJSONLPathChecked([]*Instance{a, b})
	if errA != nil {
		t.Fatalf("same id but different project wrongly flagged as collision for a: %v", errA)
	}
	gotB, errB := b.GetJSONLPathChecked([]*Instance{a, b})
	if errB != nil {
		t.Fatalf("same id but different project wrongly flagged as collision for b: %v", errB)
	}
	if gotA == "" || gotB == "" || gotA == gotB {
		t.Fatalf("expected two distinct non-empty transcript paths, got a=%q b=%q", gotA, gotB)
	}
}

// TestSyncOnce_StillRebindsLiveSession guards against the liveness gate
// over-blocking: a genuinely LIVE Claude session (running status + a real tmux
// session) MUST still rebind from a fresh non-terminal hook (e.g. SessionStart
// after /clear). This is the legitimate path #1349's fix must preserve.
func TestSyncOnce_StillRebindsLiveSession(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const profile = "_test_issue1349_live"
	d, storage := bootstrapDaemonProfile(t, profile)

	const sessOld = "33333333-3333-3333-3333-333333333333"
	const sessNew = "44444444-4444-4444-4444-444444444444"

	// Start a real tmux session so inst.Exists() is true.
	sessName := tmux.SessionPrefix + "issue1349-live-" + strconv.Itoa(int(time.Now().UnixNano()))
	if err := exec.Command("tmux", "new-session", "-d", "-s", sessName, "sh", "-c", "sleep 3600").Run(); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", sessName).Run() })

	projectPath := filepath.Join(os.Getenv("HOME"), "issue1349-live")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := &Instance{
		ID:              "live-inst-1349",
		Title:           "live",
		ProjectPath:     projectPath,
		GroupPath:       DefaultGroupPath,
		Tool:            "claude",
		Status:          StatusRunning,
		ClaudeSessionID: sessOld,
		CreatedAt:       time.Now(),
	}
	// Attach the live tmux session the same way the TUI does on cold start, so
	// inst.Exists() resolves true against the real tmux server.
	inst.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(sessName, inst.ID, projectPath, "claude", "running"))
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Fresh /clear: new session has strictly more data, non-terminal event.
	writeTranscript1349(t, inst, sessOld, 1)
	writeTranscript1349(t, inst, sessNew, 50)
	seedHookStatusFile(t, inst.ID, "SessionStart", sessNew, "running")

	for i := 0; i < 2; i++ {
		d.syncProfile(profile)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	var got string
	for _, in := range instances {
		if in.ID == inst.ID {
			got = in.ClaudeSessionID
		}
	}
	if got != sessNew {
		t.Fatalf("live session failed to rebind: want %q, got %q (the liveness gate over-blocked a legit rebind)", sessNew, got)
	}
}
