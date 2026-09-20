package session

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestStorageRestoresCustomPatterns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if err := SaveUserConfig(&UserConfig{Tools: map[string]ToolDef{
		"commandcode": {Command: "commandcode", BusyPatterns: []string{"BUSY"}, PromptPatterns: []string{"PROMPT"}},
	}}); err != nil {
		t.Fatal(err)
	}
	storage := &Storage{}
	instances, _, err := storage.convertToInstances(&StorageData{Instances: []*InstanceData{{
		ID: "test", Title: "test", Tool: "commandcode", ProjectPath: home, TmuxSession: "test-pattern-restore",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	session := instances[0].GetTmuxSession()
	if session == nil {
		t.Fatal("missing tmux session")
	}
	patterns := reflect.ValueOf(session).Elem().FieldByName("resolvedPatterns")
	if patterns.IsNil() {
		t.Fatal("restored custom tool lost configured patterns")
	}
	if got := patterns.Elem().FieldByName("BusyStrings").Index(0).String(); got != "BUSY" {
		t.Fatalf("busy pattern = %q", got)
	}
	if got := patterns.Elem().FieldByName("PromptStrings").Index(0).String(); got != "PROMPT" {
		t.Fatalf("prompt pattern = %q", got)
	}
}
