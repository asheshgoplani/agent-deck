package ui

import (
	"fmt"
	"testing"
	"time"
)

// A web-created session is started before its first save, so the launch's
// targeted write finds no row: the first save must record what it launched,
// or a tool edit before any restart would change its comms rules while the
// pane still runs the launched harness.
func TestWebMutatorCreateSessionRecordsItsLaunchedTool(t *testing.T) {
	skipIfNoTmuxBinaryUI(t)
	h, storage := newHeadlessHomeForTest(t, fmt.Sprintf("_test_web_launched_tool_%d", time.Now().UnixNano()))
	id, err := NewWebMutator(h).CreateSession("web-launched", "shell", t.TempDir(), "", "", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range instances {
		if inst.ID != id {
			continue
		}
		t.Cleanup(func() { _ = inst.Kill() })
		if inst.LaunchedTool != "shell" {
			t.Errorf("a web-created session holds LaunchedTool = %q, want \"shell\": its first save did not record its launch", inst.LaunchedTool)
		}
		return
	}
	t.Fatalf("created session %s missing from storage", id)
}
