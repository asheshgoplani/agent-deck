package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rc feedback 2026-09-23 (1.16.17-rc1): a Codex 0.155 session working for
// 30+ minutes flipped running -> waiting every 1 to 4 minutes, and each flip
// sent the parent an inbox event. Every flip matched, to the second, an
// agent-turn-complete notify from a SUBAGENT thread (thread_source=subagent,
// agent_path /root/perf_module, /root/binary_resolver, /root/coverage) that
// the main turn had spawned. The notify writer recorded it as this pane's
// hook status ("waiting"), and the daemon's hook transition candidate and the
// cold-load fast path took it as the turn-finished edge while the pane still
// showed "• Working (32m 32s • esc to interrupt)".

// writeCodexHookRecord writes the hook status file the rc notify writer left
// for a completing subagent: its own thread id and a converged generation.
func writeCodexHookRecord(t *testing.T, instanceID, status, sessionID, event string) {
	t.Helper()
	hooks := GetHooksDir()
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	gen := sessionID + ":" + uniqueSID(t)
	rec := map[string]any{
		"status": status, "session_id": sessionID, "event": event, "ts": time.Now().Unix(),
		"codex_started_generation": gen, "codex_completed_generation": gen,
		"codex_started_session_id": sessionID, "codex_completed_session_id": sessionID,
	}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(hooks, instanceID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedCodexMainAndSubagent gives codexHome a user thread and a subagent
// thread spawned from it, shaped like the real Codex 0.155.1 rollouts.
func seedCodexMainAndSubagent(t *testing.T, codexHome string) (mainSID, subSID string) {
	t.Helper()
	mainSID, subSID = uniqueSID(t), uniqueSID(t)
	seedCodex155Rollout(t, codexHome, mainSID, uniqueSID(t))
	seedCodexRolloutWithMeta(t, codexHome, subSID, "subagent", mainSID, true)
	return mainSID, subSID
}

// The real frame from the defect (pane-101456: Working + background terminal +
// queued follow-up inputs) and a subagent's fresh turn-complete on disk. A
// fresh CLI process must stay running; a turn-complete from the pane's own
// thread still reads waiting, so the gate is limited to foreign threads.
func TestCodexSubagentTurnEnd_RealWorkingFrameStaysRunning(t *testing.T) {
	frame := piCorpusFrame(t, "codex-local-macapp-v4-working-queued-inputs_af58e2b8")
	for _, c := range []struct {
		name     string
		fromMain bool
		want     Status
	}{
		{"subagent-completion", false, StatusRunning},
		{"own-thread-completion", true, StatusWaiting},
	} {
		t.Run(c.name, func(t *testing.T) {
			inst, cleanup := startPaneInstance(t, "codex", "subagent-"+c.name, frame)
			defer cleanup()
			mainSID, subSID := seedCodexMainAndSubagent(t, os.Getenv("CODEX_HOME"))
			inst.CodexSessionID = mainSID
			hookSID := subSID
			if c.fromMain {
				hookSID = mainSID
			}
			writeCodexHookRecord(t, inst.ID, "waiting", hookSID, "agent-turn-complete")

			storage, err := NewStorageWithProfile("_test-codex-subagent-" + c.name)
			if err != nil {
				t.Fatalf("storage: %v", err)
			}
			defer storage.Close()
			fresh := persistAndReload(t, storage, inst, StatusRunning)
			if status, _ := cliPass(t, fresh); status != c.want {
				t.Fatalf("fresh process = %q, want %q (hook from %s thread, pane shows • Working)", status, c.want, c.name)
			}
			if fresh.CodexSessionID != mainSID {
				t.Fatalf("binding = %q, want the main thread %q", fresh.CodexSessionID, mainSID)
			}
		})
	}
}

// The notify daemon path that sent the spurious inbox events: the row says
// running (the TUI is alive and its pane read is right), and the hook file
// holds a subagent's turn-complete. No transition may reach the parent. The
// own-thread case proves the harness does deliver a real completion.
func TestCodexSubagentTurnEnd_DaemonSendsNoTransition(t *testing.T) {
	for n, c := range []struct {
		name     string
		fromMain bool
		want     int
	}{
		{"subagent-completion", false, 0},
		{"own-thread-completion", true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			profile := fmt.Sprintf("_test_codex_subagent_feed_%d", n)
			d, storage := bootstrapDaemonProfile(t, profile)
			codexHome := filepath.Join(os.Getenv("HOME"), ".codex")
			t.Setenv("CODEX_HOME", codexHome)
			ResetInboxFingerprintCacheForTest()
			t.Cleanup(ResetInboxFingerprintCacheForTest)
			mainSID, subSID := seedCodexMainAndSubagent(t, codexHome)

			parentID := fmt.Sprintf("codex-parent-%d", n)
			childID := fmt.Sprintf("codex-child-%d", n)
			now := time.Now()
			child := &Instance{ID: childID, Title: "macapp-v4", ProjectPath: "/tmp/" + childID, GroupPath: DefaultGroupPath,
				ParentSessionID: parentID, Tool: "codex", Status: StatusRunning, CreatedAt: now, CodexSessionID: mainSID}
			parent := &Instance{ID: parentID, Title: "conductor", ProjectPath: "/tmp/" + parentID, GroupPath: DefaultGroupPath,
				Tool: "claude", Status: StatusRunning, CreatedAt: now}
			if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
				t.Fatal(err)
			}
			db := storage.GetDB()
			if err := db.RegisterInstance(false); err != nil {
				t.Fatal(err)
			}
			if err := db.WriteStatus(childID, "running", "codex"); err != nil {
				t.Fatal(err)
			}
			if err := db.WriteStatus(parentID, "running", "claude"); err != nil {
				t.Fatal(err)
			}
			hookSID := subSID
			if c.fromMain {
				hookSID = mainSID
			}
			writeCodexHookRecord(t, childID, "waiting", hookSID, "agent-turn-complete")

			d.syncProfile(profile)
			events, err := DrainInboxForParent(parentID)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != c.want {
				t.Fatalf("daemon delivered %d events, want %d: %+v", len(events), c.want, events)
			}
		})
	}
}
