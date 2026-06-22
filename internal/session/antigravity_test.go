package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInjectAntigravityHooks(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config")
	installed, err := InjectAntigravityHooks(configDir)
	if err != nil {
		t.Fatalf("InjectAntigravityHooks: %v", err)
	}
	if !installed {
		t.Fatal("expected hooks to be newly installed")
	}

	installed, err = InjectAntigravityHooks(configDir)
	if err != nil {
		t.Fatalf("InjectAntigravityHooks second call: %v", err)
	}
	if installed {
		t.Fatal("expected hooks to already be installed")
	}

	if !CheckAntigravityHooksInstalled(configDir) {
		t.Fatal("CheckAntigravityHooksInstalled = false")
	}

	removed, err := RemoveAntigravityHooks(configDir)
	if err != nil {
		t.Fatalf("RemoveAntigravityHooks: %v", err)
	}
	if !removed {
		t.Fatal("expected hooks to be removed")
	}
	if CheckAntigravityHooksInstalled(configDir) {
		t.Fatal("hooks still reported installed after remove")
	}
}

func TestBuildAntigravityCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	inst := NewInstanceWithTool("agy-test", "/tmp/project", "antigravity")
	inst.Command = "agy"
	inst.AntigravityConversationID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	enabled := true
	inst.AntigravityYoloMode = &enabled
	inst.AntigravityModel = "gemini-2.5-flash"

	cmd := inst.buildAntigravityCommand("agy")
	if cmd == "" {
		t.Fatal("empty command")
	}
	for _, want := range []string{
		"--conversation aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"--dangerously-skip-permissions",
		"--model gemini-2.5-flash",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("command %q missing %q", cmd, want)
		}
	}
}

func TestAntigravityToolDataRoundTrip(t *testing.T) {
	enabled := true
	inst := NewInstanceWithTool("agy-persist", "/tmp/project", "antigravity")
	inst.AntigravityConversationID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	inst.AntigravityDetectedAt = time.Unix(1700000000, 0)
	inst.AntigravityYoloMode = &enabled
	inst.AntigravityModel = "gemini-2.5-flash"

	td := WriteAntigravityToToolData(nil, inst)
	id, at, yolo, model := ReadAntigravityFromToolData(td)
	if id != inst.AntigravityConversationID {
		t.Fatalf("conversation id = %q, want %q", id, inst.AntigravityConversationID)
	}
	if !at.Equal(inst.AntigravityDetectedAt) {
		t.Fatalf("detected at = %v, want %v", at, inst.AntigravityDetectedAt)
	}
	if yolo == nil || !*yolo {
		t.Fatalf("yolo = %v, want true", yolo)
	}
	if model != inst.AntigravityModel {
		t.Fatalf("model = %q, want %q", model, inst.AntigravityModel)
	}
}

func TestAntigravityConversationHasData(t *testing.T) {
	tmp := t.TempDir()
	SetAntigravityAppDataDirOverrideForTest(tmp)
	t.Cleanup(func() { SetAntigravityAppDataDirOverrideForTest("") })

	id := "11111111-2222-3333-4444-555555555555"
	brain := filepath.Join(tmp, "brain", id)
	if err := os.MkdirAll(brain, 0755); err != nil {
		t.Fatal(err)
	}
	if !antigravityConversationHasData(id) {
		t.Fatal("expected conversation data")
	}
}

func TestExtractAntigravityConversationIDFromPane(t *testing.T) {
	text := "Thanks for using agy\nResume: agy --conversation=d1d8a55b-cc27-4dd4-bc62-2f73015960d2 (or -c)\n"
	got := ExtractAntigravityConversationIDFromPane(text)
	want := "d1d8a55b-cc27-4dd4-bc62-2f73015960d2"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAntigravityHooksJSONShape(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config")
	if _, err := InjectAntigravityHooks(configDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["agent-deck"]; !ok {
		t.Fatalf("missing agent-deck block: %s", string(data))
	}
}
