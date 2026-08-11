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

func TestGenericSessionID_ToolDataRoundTrip(t *testing.T) {
	started := time.Unix(1_700_000_000, 0).UTC()
	td := WriteGenericSessionIDToToolData(nil, "sid-abc", started)
	if got := ReadGenericSessionIDFromToolData(td); got != "sid-abc" {
		t.Fatalf("ReadGenericSessionIDFromToolData = %q, want sid-abc", got)
	}
	if got := ReadGenericDetectedAtFromToolData(td); !got.Equal(started) {
		t.Fatalf("detected_at = %v, want %v", got, started)
	}

	mixed := json.RawMessage(`{"color":"#fff","last_started_at":123}`)
	out := WriteGenericSessionIDToToolData(mixed, "sid-xyz", started)
	if got := ReadGenericSessionIDFromToolData(out); got != "sid-xyz" {
		t.Fatalf("round-trip lost id: %q", got)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["color"]) != `"#fff"` {
		t.Fatalf("lost color key: %s", out)
	}

	cleared := WriteGenericSessionIDToToolData(out, "", time.Time{})
	if got := ReadGenericSessionIDFromToolData(cleared); got != "" {
		t.Fatalf("clear left id %q", got)
	}
}

func TestGenericSessionID_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	wantID := "019f683f-260d-7ae1-a84d-205234ea3184"
	detected := time.Unix(1_700_000_100, 0).UTC()
	inst := NewInstance("sysadmin-roundtrip", "/tmp")
	inst.Tool = "shell"
	inst.GenericSessionID = wantID
	inst.GenericDetectedAt = detected

	groupTree := NewGroupTreeWithGroups([]*Instance{inst}, nil)
	if err := storage.SaveWithGroups([]*Instance{inst}, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len(loaded)=%d", len(loaded))
	}
	if got := loaded[0].GenericSessionID; got != wantID {
		t.Fatalf("persisted GenericSessionID = %q, want %q", got, wantID)
	}

	// Targeted write (live capture path) on the underlying StateDB.
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "new-sid", time.Now()); err != nil {
		t.Fatalf("WriteGenericSessionBinding: %v", err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded2[0].GenericSessionID; got != "new-sid" {
		t.Fatalf("after WriteGenericSessionBinding = %q", got)
	}
}

func TestStickyMerge_PreservesGenericSessionID(t *testing.T) {
	old := json.RawMessage(`{"generic_session_id":"keep-me","color":"#abc"}`)
	new_ := json.RawMessage(`{"color":"#abc"}`)
	merged := statedb.MergeToolDataExtras(old, new_)
	if got := ReadGenericSessionIDFromToolData(merged); got != "keep-me" {
		t.Fatalf("sticky merge dropped generic_session_id: %s", merged)
	}
}

func TestSetField_ToolSessionID(t *testing.T) {
	inst := &Instance{ID: "i1", Tool: "shell", Title: "t"}
	old, _, err := SetField(inst, FieldToolSessionID, "new-sid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if old != "" {
		t.Fatalf("old = %q", old)
	}
	if inst.GenericSessionID != "new-sid" {
		t.Fatalf("GenericSessionID = %q", inst.GenericSessionID)
	}
	if inst.GenericDetectedAt.IsZero() {
		t.Fatal("GenericDetectedAt should be set")
	}
	_, _, err = SetField(inst, FieldToolSessionID, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst.GenericSessionID != "" {
		t.Fatalf("clear failed: %q", inst.GenericSessionID)
	}
}

func TestBuildGenericCommand_ResumesFromPersistedID(t *testing.T) {
	// Isolate config so [tools.fake-tool] with resume_flag is visible.
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	t.Cleanup(func() {
		_ = os.Setenv("HOME", oldHome)
		ClearUserConfigCache()
	})
	ClearUserConfigCache()

	cfgDir := filepath.Join(tmpHome, ".config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Also write legacy path used by some resolvers.
	legacy := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `
[tools.fake-tool]
command = "fake-tool"
resume_flag = "--resume"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()

	if def := GetToolDef("fake-tool"); def == nil || def.ResumeFlag != "--resume" {
		t.Fatalf("GetToolDef(fake-tool) = %+v, want resume_flag=--resume (config not loaded?)", def)
	}

	inst := &Instance{
		ID:               "i1",
		Tool:             "fake-tool",
		Command:          "fake-tool",
		GenericSessionID: "persisted-uuid",
	}
	if got := inst.GetGenericSessionID(); got != "persisted-uuid" {
		t.Fatalf("GetGenericSessionID = %q", got)
	}
	if !inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric should be true with resume_flag + persisted id")
	}
	cmd := inst.buildGenericCommand("fake-tool")
	if !strings.Contains(cmd, "--resume") || !strings.Contains(cmd, "persisted-uuid") {
		t.Fatalf("buildGenericCommand = %q, want --resume persisted-uuid", cmd)
	}
}

func TestCanRestartGeneric_RequiresResumeFlag(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	t.Cleanup(ClearUserConfigCache)
	ClearUserConfigCache()

	cfgDir := filepath.Join(tmpHome, ".config", "agent-deck")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".agent-deck"), 0o700)
	content := `
[tools.no-resume]
command = "no-resume"
`
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o600)
	_ = os.WriteFile(filepath.Join(tmpHome, ".agent-deck", "config.toml"), []byte(content), 0o600)
	ClearUserConfigCache()

	inst := &Instance{
		Tool:             "no-resume",
		Command:          "no-resume",
		GenericSessionID: "should-not-resume",
	}
	if inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric must be false without resume_flag")
	}
}

// writeResumeToolConfig installs a minimal [tools.<name>] block with resume_flag
// under both XDG and legacy config paths (agentpaths resolvers differ by version).
func writeResumeToolConfig(t *testing.T, home, name, resumeFlag string) {
	t.Helper()
	content := fmt.Sprintf(`
[tools.%s]
command = "%s"
resume_flag = "%s"
`, name, name, resumeFlag)
	for _, dir := range []string{
		filepath.Join(home, ".config", "agent-deck"),
		filepath.Join(home, ".agent-deck"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ClearUserConfigCache()
}

func TestBuildGenericCommand_NoResumeWhenIDEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Cleanup(ClearUserConfigCache)
	writeResumeToolConfig(t, home, "fake-tool", "--resume")

	inst := &Instance{Tool: "fake-tool", Command: "fake-tool"}
	cmd := inst.buildGenericCommand("fake-tool")
	if strings.Contains(cmd, "--resume") {
		t.Fatalf("fresh start must not inject --resume: %q", cmd)
	}
	if inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric with empty id must be false")
	}
}

func TestClearSessionBindingForFreshStart_DropsGenericID(t *testing.T) {
	inst := &Instance{
		ID:                "i1",
		GenericSessionID:  "old-sid",
		GenericDetectedAt: time.Now(),
	}
	inst.clearSessionBindingForFreshStart()
	if inst.GenericSessionID != "" || !inst.GenericDetectedAt.IsZero() {
		t.Fatalf("expected cleared generic id, got %q / %v", inst.GenericSessionID, inst.GenericDetectedAt)
	}
}

func TestRestartPolicy_ToolSessionIDRequiresRestart(t *testing.T) {
	if got := RestartPolicyFor(FieldToolSessionID); got != FieldRestartRequired {
		t.Fatalf("RestartPolicyFor(tool-session-id) = %v, want FieldRestartRequired", got)
	}
}

func TestStickyMerge_ExplicitEmptyClearsGenericSessionID(t *testing.T) {
	old := json.RawMessage(`{"generic_session_id":"keep-me"}`)
	// Explicit empty in the new blob is an intentional clear (sticky only on omission).
	new_ := json.RawMessage(`{"generic_session_id":""}`)
	merged := statedb.MergeToolDataExtras(old, new_)
	if got := ReadGenericSessionIDFromToolData(merged); got != "" {
		t.Fatalf("explicit empty should clear, got %q from %s", got, merged)
	}
}

func TestWriteGenericSessionBinding_Clear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	inst := NewInstance("bind-clear", "/tmp")
	inst.Tool = "shell"
	inst.GenericSessionID = "sid-to-clear"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "" {
		t.Fatalf("clear binding left %q", loaded[0].GenericSessionID)
	}
}

func TestRebootResume_Simulated(t *testing.T) {
	// Save with id, load into a fresh Instance with no tmux → build resume command.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Cleanup(ClearUserConfigCache)
	writeResumeToolConfig(t, home, "fake-tool", "--resume")

	storage := newTestStorage(t)
	inst := NewInstance("reboot-sim", "/tmp")
	inst.Tool = "fake-tool"
	inst.Command = "fake-tool"
	inst.GenericSessionID = "conversation-after-reboot"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	cold := loaded[0]
	cold.tmuxSession = nil // reboot: no tmux yet
	if cold.GenericSessionID != "conversation-after-reboot" {
		t.Fatalf("cold load lost id: %q", cold.GenericSessionID)
	}
	if !cold.CanRestartGeneric() {
		t.Fatal("cold instance should be restartable with resume")
	}
	cmd := cold.buildGenericCommand("fake-tool")
	if !strings.Contains(cmd, "--resume") || !strings.Contains(cmd, "conversation-after-reboot") {
		t.Fatalf("post-reboot command = %q", cmd)
	}
}
