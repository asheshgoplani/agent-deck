package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2436: on attach-return, refreshAttachedSessionStatus wiped the hook
// status (instance, watcher cache and hook file) for EVERY tool. The wipe exists
// for Claude/Codex, which can exit via /q without writing a fresh "dead" hook
// (#854). Tools whose hooks only fire at turn boundaries (pi, and likewise
// gemini, cursor, hermes) lost their status on every attach/detach, so the dot
// fell back to pane heuristics until the next turn event.
//
// The test drives the real attach-return handler (statusUpdateMsg with
// attachedSessionID, then the deferred reconcile Cmd it returns) and checks all
// three places the hook status lives.
func TestIssue2436_AttachReturnHookWipeGatedToClaudeCodex(t *testing.T) {
	for _, tc := range []struct {
		tool      string
		wantClear bool
	}{
		{tool: "claude", wantClear: true},
		{tool: "codex", wantClear: true},
		{tool: "pi", wantClear: false},
		{tool: "gemini", wantClear: false},
		{tool: "cursor", wantClear: false},
		{tool: "hermes", wantClear: false},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("AGENTDECK_PROFILE", "issue2436-test")

			hooksDir := session.GetHooksDir()
			if err := os.MkdirAll(hooksDir, 0o755); err != nil {
				t.Fatalf("mkdir hooks dir: %v", err)
			}

			instanceID := "issue2436-" + tc.tool
			now := time.Now()
			data, err := json.Marshal(map[string]any{
				"status": "waiting",
				"event":  "turn_end",
				"ts":     now.Unix(),
			})
			if err != nil {
				t.Fatalf("marshal hook status: %v", err)
			}
			hookPath := filepath.Join(hooksDir, instanceID+".json")
			if err := os.WriteFile(hookPath, data, 0o644); err != nil {
				t.Fatalf("write hook status file: %v", err)
			}

			// Start the watcher only after the file exists so it is picked up by
			// the initial load and no write event is left in flight.
			watcher, err := session.NewStatusFileWatcher(nil)
			if err != nil {
				t.Fatalf("NewStatusFileWatcher: %v", err)
			}
			go watcher.Start()
			t.Cleanup(watcher.Stop)
			deadline := time.Now().Add(5 * time.Second)
			for watcher.GetHookStatus(instanceID) == nil {
				if time.Now().After(deadline) {
					t.Fatal("timed out waiting for watcher to load the pre-seeded hook status file")
				}
				time.Sleep(10 * time.Millisecond)
			}

			h := newAttachReturnTestHome()
			h.hookWatcher = watcher
			inst := session.NewInstanceWithGroupAndTool(tc.tool, "/tmp/"+tc.tool, "work", tc.tool)
			inst.ID = instanceID
			inst.CreatedAt = now.Add(-2 * time.Second)
			inst.Status = session.StatusWaiting
			setAttachReturnTestInstances(h, []*session.Instance{inst})
			inst.UpdateHookStatus(watcher.GetHookStatus(instanceID))
			if got, _ := inst.GetHookStatus(); got != "waiting" {
				t.Fatalf("precondition: instance hook status = %q, want %q", got, "waiting")
			}

			_, cmd := h.Update(statusUpdateMsg{attachedSessionID: inst.ID})
			if !yieldsMsg(cmd, "ui.attachReturnSyncedMsg") {
				t.Fatal("attach-return Update returned no deferred reconcile Cmd")
			}

			gotStatus, _ := inst.GetHookStatus()
			watcherStatus := watcher.GetHookStatus(instanceID)
			_, statErr := os.Stat(hookPath)

			if tc.wantClear {
				if gotStatus != "" {
					t.Errorf("instance hook status = %q, want cleared on attach-return", gotStatus)
				}
				if watcherStatus != nil {
					t.Errorf("watcher hook status = %q, want cleared on attach-return", watcherStatus.Status)
				}
				if !os.IsNotExist(statErr) {
					t.Errorf("hook file still present after attach-return (stat err %v), want removed", statErr)
				}
				return
			}

			if gotStatus != "waiting" {
				t.Errorf("instance hook status = %q, want %q kept across attach/detach (#2436)", gotStatus, "waiting")
			}
			if watcherStatus == nil || watcherStatus.Status != "waiting" {
				t.Errorf("watcher hook status = %+v, want %q kept across attach/detach (#2436)", watcherStatus, "waiting")
			}
			if statErr != nil {
				t.Errorf("hook file removed by attach-return (#2436): %v", statErr)
			}
		})
	}
}
