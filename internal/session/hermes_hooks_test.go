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

// TestRemoveHermesHooks_PreservesUserHookContainingSubstring guards against
// the substring-match false positive that the prior `strings.Contains` check
// suffered. A user hook whose command merely mentions "agent-deck hook-handler"
// (e.g. a wrapper script that logs or forwards to it) is NOT agent-deck-owned
// and must survive RemoveHermesHooks.
func TestRemoveHermesHooks_PreservesUserHookContainingSubstring(t *testing.T) {
	dir := t.TempDir()
	// User hook command happens to contain the agent-deck phrase as a
	// substring. With the prior substring matcher this entry would be
	// silently deleted by RemoveHermesHooks.
	existing := []byte(`hooks:
  pre_tool_call:
    - command: /usr/local/bin/wrap.sh agent-deck hook-handler --debug
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
	if !strings.Contains(string(data), "/usr/local/bin/wrap.sh agent-deck hook-handler --debug") {
		t.Errorf("user hook containing agent-deck substring was clobbered; config: %s", data)
	}
}

// TestInjectRemoveHermesHooks_ConcurrentSafe runs many parallel inject/remove
// cycles against a single config path to exercise the in-process keyed mutex.
// Under -race this catches read-modify-write tears and shared-tmp clobbers.
func TestInjectRemoveHermesHooks_ConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	// Seed with one user-owned hook so we can verify it survives every cycle.
	existing := []byte(`hooks:
  pre_tool_call:
    - command: /opt/user/sentinel.sh
`)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), existing, 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const goroutines = 8
	const cycles = 5
	done := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			for c := 0; c < cycles; c++ {
				if _, err := session.InjectHermesHooks(dir); err != nil {
					done <- err
					return
				}
				if _, err := session.RemoveHermesHooks(dir); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	for g := 0; g < goroutines; g++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent inject/remove: %v", err)
		}
	}

	// After the storm, the user-owned sentinel hook must still be there.
	data, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(data), "/opt/user/sentinel.sh") {
		t.Errorf("user sentinel hook lost after concurrent inject/remove storm; config: %s", data)
	}
}

// TestRemoveHermesHooks_NoOpDoesNotCreateArtifacts guards against a regression
// where acquireHermesConfigLock would MkdirAll + create config.yaml.lock even
// when config.yaml itself didn't exist, so `agent-deck hermes-hooks uninstall`
// on a fresh machine left behind an unwanted ~/.hermes/ directory.
func TestRemoveHermesHooks_NoOpDoesNotCreateArtifacts(t *testing.T) {
	parent := t.TempDir()
	configDir := filepath.Join(parent, "never-existed")
	// configDir does NOT exist yet. Remove should be a clean no-op.

	removed, err := session.RemoveHermesHooks(configDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed {
		t.Error("expected removed=false for non-existent config")
	}

	// Critical: configDir and the .lock file must NOT have been created.
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Errorf("configDir was created by no-op uninstall (err=%v); should be untouched", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.yaml.lock")); !os.IsNotExist(err) {
		t.Errorf("config.yaml.lock was created by no-op uninstall (err=%v); should not exist", err)
	}
}

// TestRemoveHermesHooks_PreservesMalformedSiblingEvent guards against the
// shared-flag bug where, once any event lost an agent-deck hook, a subsequent
// event whose value wasn't a list would be silently deleted from user config.
func TestRemoveHermesHooks_PreservesMalformedSiblingEvent(t *testing.T) {
	dir := t.TempDir()
	// post_tool_call holds a string instead of a list — odd but valid user
	// config; we must not destroy it just because we removed our hook from
	// pre_tool_call.
	existing := []byte(`hooks:
  pre_tool_call:
    - command: agent-deck hook-handler
  post_tool_call: keep-this-string
`)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), existing, 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := session.RemoveHermesHooks(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(data), "keep-this-string") {
		t.Errorf("malformed sibling event was clobbered; config: %s", data)
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

func TestInjectHermesHooks_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := session.InjectHermesHooks(dir); err != nil {
		t.Fatalf("inject: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("stat config.yaml: %v", err)
	}
	// Config contains secrets (model keys, hook commands) — must not be world-readable.
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("config.yaml permissions = %04o, want 0600", perm)
	}
}

func TestRemoveHermesHooks_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := session.InjectHermesHooks(dir); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, err := session.RemoveHermesHooks(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// File still exists (user hooks may have been kept); permissions must be 0600.
	info, err := os.Stat(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return // file removed entirely — nothing to check
		}
		t.Fatalf("stat config.yaml: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("config.yaml permissions after remove = %04o, want 0600", perm)
	}
}
