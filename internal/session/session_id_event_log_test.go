package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestWriteSessionIDLifecycleEvent_AppendsJSONL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first := SessionIDLifecycleEvent{
		InstanceID: "inst-1",
		Tool:       "claude",
		Action:     "bind",
		Source:     "tmux_env",
		NewID:      "session-a",
	}
	second := SessionIDLifecycleEvent{
		InstanceID: "inst-1",
		Tool:       "claude",
		Action:     "rebind",
		Source:     "hook_payload",
		OldID:      "session-a",
		NewID:      "session-b",
	}

	if err := WriteSessionIDLifecycleEvent(first); err != nil {
		t.Fatalf("WriteSessionIDLifecycleEvent(first) error: %v", err)
	}
	if err := WriteSessionIDLifecycleEvent(second); err != nil {
		t.Fatalf("WriteSessionIDLifecycleEvent(second) error: %v", err)
	}

	data, err := os.ReadFile(GetSessionIDLifecycleLogPath())
	if err != nil {
		t.Fatalf("read lifecycle log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}

	var gotFirst, gotSecond SessionIDLifecycleEvent
	if err := json.Unmarshal([]byte(lines[0]), &gotFirst); err != nil {
		t.Fatalf("unmarshal first line: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &gotSecond); err != nil {
		t.Fatalf("unmarshal second line: %v", err)
	}
	if gotFirst.Action != "bind" || gotSecond.Action != "rebind" {
		t.Fatalf("actions = %q/%q, want bind/rebind", gotFirst.Action, gotSecond.Action)
	}
	if gotFirst.Timestamp == 0 || gotSecond.Timestamp == 0 {
		t.Fatal("timestamps should be auto-populated")
	}
}
