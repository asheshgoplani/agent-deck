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

func saveKimiRow(t *testing.T, db *statedb.StateDB, inst *Instance) {
	t.Helper()
	if err := db.SaveInstance(&statedb.InstanceRow{
		ID: inst.ID, Title: inst.Title, ProjectPath: inst.ProjectPath,
		Tool: "kimi", Status: "idle", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
}

func persistedKimiBinding(t *testing.T, db *statedb.StateDB, id string) (string, time.Time) {
	t.Helper()
	rows, err := db.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	for _, row := range rows {
		if row.ID == id {
			return ReadKimiSessionIDFromToolData(row.ToolData), ReadKimiDetectedAtFromToolData(row.ToolData)
		}
	}
	t.Fatalf("instance %q not found", id)
	return "", time.Time{}
}

func TestKimiToolDataRoundTrip(t *testing.T) {
	td := WriteKimiSessionIDToToolData(json.RawMessage(`{"unrelated":true}`), " session-1 ")
	if got := ReadKimiSessionIDFromToolData(td); got != "session-1" {
		t.Fatalf("ReadKimiSessionIDFromToolData() = %q", got)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(td, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["unrelated"]; !ok {
		t.Fatal("existing tool data was dropped")
	}
	unchanged := WriteKimiSessionIDToToolData(td, "")
	if got := ReadKimiSessionIDFromToolData(unchanged); got != "session-1" {
		t.Fatalf("empty unaware write changed binding to %q", got)
	}
}

func TestKimiUpdateHookStatusBindingContract(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := withTempGlobalStateDB(t)
	inst := NewInstanceWithTool("kimi-binding", t.TempDir(), "kimi")
	saveKimiRow(t, db, inst)
	base := time.Now().Add(-time.Minute).Truncate(time.Second)

	inst.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "first", Event: "SessionStart", ClientType: "kimi_code_cli", UpdatedAt: base})
	if inst.KimiSessionID != "first" {
		t.Fatalf("initial binding = %q, want first", inst.KimiSessionID)
	}
	if got, at := persistedKimiBinding(t, db, inst.ID); got != "first" || !at.Equal(base) {
		t.Fatalf("persisted binding = %q at %v, want first at %v", got, at, base)
	}

	for _, tc := range []struct {
		name       string
		sessionID  string
		event      string
		clientType string
	}{
		{name: "wrong event", sessionID: "wrong-event", event: "TurnStarted", clientType: "kimi_code_cli"},
		{name: "wrong client", sessionID: "wrong-client", event: "SessionStart", clientType: "other"},
		{name: "missing client", sessionID: "missing-client", event: "SessionStart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst.UpdateHookStatus(&HookStatus{Status: "running", SessionID: tc.sessionID, Event: tc.event, ClientType: tc.clientType, UpdatedAt: base.Add(time.Second)})
			if inst.KimiSessionID != "first" {
				t.Fatalf("binding changed to %q", inst.KimiSessionID)
			}
			if got := inst.StatusEvidence().HookStatus; got != "running" {
				t.Fatalf("status update was rejected with identity: %q", got)
			}
		})
	}

	WriteHookSessionAnchor(inst.ID, "anchor-only")
	inst.UpdateHookStatus(&HookStatus{Status: "waiting", Event: "SessionStart", ClientType: "kimi_code_cli", UpdatedAt: base.Add(2 * time.Second)})
	if inst.KimiSessionID != "first" {
		t.Fatalf("anchor rebound identity to %q", inst.KimiSessionID)
	}

	inst.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "rotated", Event: "SessionStart", ClientType: "kimi_code_cli", UpdatedAt: base.Add(3 * time.Second)})
	if inst.KimiSessionID != "rotated" {
		t.Fatalf("newer SessionStart did not rotate binding: %q", inst.KimiSessionID)
	}
	if got, at := persistedKimiBinding(t, db, inst.ID); got != "rotated" || !at.Equal(base.Add(3*time.Second)) {
		t.Fatalf("rotated persisted binding = %q at %v", got, at)
	}
	inst.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "stale", Event: "SessionStart", ClientType: "kimi_code_cli", UpdatedAt: base.Add(2 * time.Second)})
	if inst.KimiSessionID != "rotated" {
		t.Fatalf("older SessionStart rebound identity to %q", inst.KimiSessionID)
	}
	if got, _ := persistedKimiBinding(t, db, inst.ID); got != "rotated" {
		t.Fatalf("stale SessionStart changed persisted binding to %q", got)
	}
}

func TestKimiStatusFileFlowsThroughWatcherAndBinding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("kimi-watcher", t.TempDir(), "kimi")
	watcher := &StatusFileWatcher{hooksDir: hooksDir, statuses: make(map[string]*HookStatus)}
	at := time.Now().Truncate(time.Second)
	data, err := json.Marshal(map[string]any{
		"status": "waiting", "session_id": "native-id", "event": "SessionStart",
		"client_type": "kimi_code_cli", "ts": at.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(hooksDir, inst.ID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	watcher.processFile(path)
	status := watcher.GetHookStatus(inst.ID)
	if status == nil || status.ClientType != "kimi_code_cli" {
		t.Fatalf("decoded status = %+v, want client_type", status)
	}
	if direct := readHookStatusFile(inst.ID); direct == nil || direct.ClientType != "kimi_code_cli" {
		t.Fatalf("direct status-file decode = %+v, want client_type", direct)
	}
	inst.UpdateHookStatus(status)
	if inst.KimiSessionID != "native-id" {
		t.Fatalf("watcher-fed binding = %q, want native-id", inst.KimiSessionID)
	}
}

func TestKimiFreshStartClearsPersistedBinding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := withTempGlobalStateDB(t)
	inst := NewInstanceWithTool("kimi-fresh", t.TempDir(), "kimi")
	saveKimiRow(t, db, inst)
	inst.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "old-native", Event: "SessionStart", ClientType: "kimi_code_cli", UpdatedAt: time.Now()})
	inst.clearSessionBindingForFreshStart()
	if inst.KimiSessionID != "" || !inst.kimiBoundAt.IsZero() {
		t.Fatalf("fresh binding = %q at %v, want cleared", inst.KimiSessionID, inst.kimiBoundAt)
	}
	if got, at := persistedKimiBinding(t, db, inst.ID); got != "" || !at.IsZero() {
		t.Fatalf("persisted fresh binding = %q at %v, want cleared", got, at)
	}
	if !inst.CanRestart() || !inst.CanRestartFresh() {
		t.Fatal("Kimi must support restart and fresh restart")
	}
	if got := inst.buildKimiCommand("kimi"); strings.Contains(got, "--session") {
		t.Fatalf("fresh launch retained resume flag: %q", got)
	}
}

func TestKimiBindingColdStorageReloadAndResume(t *testing.T) {
	s := newTestStorage(t)
	inst := NewInstanceWithTool("kimi-cold", t.TempDir(), "kimi")
	if err := s.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1790000000, 0)
	if err := s.db.WriteKimiSessionBinding(inst.ID, "native-cold-id", at); err != nil {
		t.Fatal(err)
	}
	lite, _, err := s.LoadLite()
	if err != nil {
		t.Fatal(err)
	}
	if len(lite) != 1 || lite[0].KimiSessionID != "native-cold-id" || !lite[0].KimiDetectedAt.Equal(at) {
		t.Fatalf("LoadLite binding = %+v", lite)
	}
	loaded, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].KimiSessionID != "native-cold-id" || !loaded[0].kimiBoundAt.Equal(at) {
		t.Fatalf("LoadWithGroups binding = %+v", loaded)
	}
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{DefaultModel: "fresh-only", PermissionMode: "ask-when-needed"}})
	defer restore()
	command := loaded[0].buildKimiCommand("kimi")
	if !strings.HasSuffix(command, "kimi --session native-cold-id --yolo") || strings.Contains(command, "--model") {
		t.Fatalf("cold reload resume command = %q", command)
	}
}
