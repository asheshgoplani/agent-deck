package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"gopkg.in/yaml.v3"
)

func TestInjectHermesHooks_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	installed, err := session.InjectHermesHooks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !installed {
		t.Fatal("expected installed=true on fresh install")
	}
	if !session.CheckHermesHooksInstalled(dir) {
		t.Fatal("CheckHermesHooksInstalled returned false after install")
	}
}

func TestInjectHermesHooks_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := session.InjectHermesHooks(dir); err != nil {
		t.Fatalf("first install: %v", err)
	}
	installed, err := session.InjectHermesHooks(dir)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if installed {
		t.Fatal("expected installed=false on second call (already present)")
	}
}

func TestInjectHermesHooks_AllEventsPresent(t *testing.T) {
	dir := t.TempDir()
	if _, err := session.InjectHermesHooks(dir); err != nil {
		t.Fatalf("install: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}

	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse config.yaml: %v", err)
	}

	hooksSection, _ := raw["hooks"].(map[string]interface{})
	if hooksSection == nil {
		t.Fatal("no hooks section in config.yaml")
	}

	for _, event := range []string{"pre_tool_call", "post_tool_call", "on_session_start", "on_session_end"} {
		entries, _ := hooksSection[event].([]interface{})
		found := false
		for _, e := range entries {
			em, _ := e.(map[string]interface{})
			if cmd, _ := em["command"].(string); strings.Contains(cmd, "agent-deck hook-handler") {
				found = true
			}
		}
		if !found {
			t.Errorf("event %q missing agent-deck hook-handler entry", event)
		}
	}
}

func TestInjectHermesHooks_PreservesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	existing := []byte("model: hermes-3-70b\ntemperature: 0.7\n")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), existing, 0644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	if _, err := session.InjectHermesHooks(dir); err != nil {
		t.Fatalf("inject: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	content := string(data)
	if !strings.Contains(content, "hermes-3-70b") {
		t.Error("model key was lost after injection")
	}
	if !strings.Contains(content, "0.7") {
		t.Error("temperature key was lost after injection")
	}
	if !strings.Contains(content, "agent-deck hook-handler") {
		t.Error("hook command not found after injection")
	}
}

func TestInjectHermesHooks_PreservesExistingHooks(t *testing.T) {
	dir := t.TempDir()
	existing := []byte(`hooks:
  pre_tool_call:
    - command: /usr/local/bin/my-hook.sh
`)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), existing, 0644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	if _, err := session.InjectHermesHooks(dir); err != nil {
		t.Fatalf("inject: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	content := string(data)
	if !strings.Contains(content, "/usr/local/bin/my-hook.sh") {
		t.Error("existing user hook was removed")
	}
	if !strings.Contains(content, "agent-deck hook-handler") {
		t.Error("agent-deck hook not added")
	}
}

func TestRemoveHermesHooks_Removes(t *testing.T) {
	dir := t.TempDir()
	if _, err := session.InjectHermesHooks(dir); err != nil {
		t.Fatalf("inject: %v", err)
	}

	removed, err := session.RemoveHermesHooks(dir)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Fatal("expected removed=true")
	}
	if session.CheckHermesHooksInstalled(dir) {
		t.Fatal("hooks still detected after removal")
	}
}

func TestRemoveHermesHooks_NoFile(t *testing.T) {
	dir := t.TempDir()
	removed, err := session.RemoveHermesHooks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed {
		t.Fatal("expected removed=false when file doesn't exist")
	}
}

func TestRemoveHermesHooks_PreservesUserHooks(t *testing.T) {
	dir := t.TempDir()
	existing := []byte(`hooks:
  pre_tool_call:
    - command: /usr/local/bin/my-hook.sh
`)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), existing, 0644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	if _, err := session.InjectHermesHooks(dir); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, err := session.RemoveHermesHooks(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(data), "/usr/local/bin/my-hook.sh") {
		t.Error("user hook was removed along with agent-deck hook")
	}
}

func TestCheckHermesHooksInstalled_NotPresent(t *testing.T) {
	dir := t.TempDir()
	if session.CheckHermesHooksInstalled(dir) {
		t.Fatal("expected false for empty dir")
	}
}

func TestGetHermesConfigDir_ReturnsPath(t *testing.T) {
	dir := session.GetHermesConfigDir()
	if dir == "" {
		t.Fatal("GetHermesConfigDir returned empty string")
	}
	if !strings.Contains(dir, ".hermes") {
		t.Errorf("expected .hermes in path, got %q", dir)
	}
}
