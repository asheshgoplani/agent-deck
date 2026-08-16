package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHermesDaemonFeedAdmitsOnlyCurrentAfterAgent(t *testing.T) {
	for n, tc := range []struct {
		name, event, generation, control string
		want                             int
	}{
		{"post-llm-call", "post_llm_call", "", "", 1},
		{"tool-activity", "post_tool_call", "", "", 0},
		{"stale-generation", "post_llm_call", "old", "current", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := fmt.Sprintf("_test_hermes_feed_%d", n)
			d, storage := bootstrapDaemonProfile(t, profile)
			ResetInboxFingerprintCacheForTest()
			t.Cleanup(ResetInboxFingerprintCacheForTest)
			parentID := fmt.Sprintf("hermes-parent-%d", n)
			childID := fmt.Sprintf("hermes-child-%d", n)
			now := time.Now()
			child := &Instance{ID: childID, Title: "hermes-worker", ProjectPath: "/tmp/" + childID, GroupPath: DefaultGroupPath, ParentSessionID: parentID, Tool: "hermes", Status: StatusRunning, CreatedAt: now}
			parent := &Instance{ID: parentID, Title: "orchestrator", ProjectPath: "/tmp/" + parentID, GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: now}
			if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
				t.Fatal(err)
			}
			db := storage.GetDB()
			if err := db.RegisterInstance(false); err != nil {
				t.Fatal(err)
			}
			if err := db.WriteStatus(childID, "running", "hermes"); err != nil {
				t.Fatal(err)
			}
			if err := db.WriteStatus(parentID, "running", "claude"); err != nil {
				t.Fatal(err)
			}
			hooks := GetHooksDir()
			if err := os.MkdirAll(hooks, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.control != "" {
				b, _ := json.Marshal(hermesHookControl{Generation: tc.control})
				if err := os.WriteFile(filepath.Join(hooks, childID+".generation.json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			status := map[string]any{"status": "waiting", "event": tc.event, "ts": time.Now().Unix()}
			if tc.generation != "" {
				status["hook_generation"] = tc.generation
				status["sequence"] = 1
			}
			b, _ := json.Marshal(status)
			if err := os.WriteFile(filepath.Join(hooks, childID+".json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			d.syncProfile(profile)
			events, err := DrainInboxForParent(parentID)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != tc.want {
				t.Fatalf("daemon feed delivered %d events, want %d: %+v", len(events), tc.want, events)
			}
		})
	}
}
