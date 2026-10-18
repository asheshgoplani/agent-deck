package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A5 (1.16.30): a leftover run_in_background shell no longer pins a finished
// Claude session at running. The frames are the two owner reports (see
// internal/tmux/background_work_a5_test.go): a completed turn, "1 shell still
// running" on the done line, an unsent composer, "· 1 shell · ← for agents"
// on the footer.
const (
	a5PaneGhostSuggestion = "⏺ Done. The dev server is still up on :5173 if you want to look.\n" +
		"\n" +
		"✻ Crunched for 45s · done 10:20 PM · 1 shell still running\n" +
		"\n" +
		"────────────────────────────────────────────────────────────\n" +
		"❯ \x1b[2mrun the e2e suite against the dev server\x1b[22m\n" +
		"────────────────────────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on · 1 shell · ← for agents\n"

	a5PaneTypedDraft = "⏺ Done. The dev server is still up on :5173 if you want to look.\n" +
		"\n" +
		"✻ Crunched for 45s · done 10:20 PM · 1 shell still running\n" +
		"\n" +
		"────────────────────────────────────────────────────────────\n" +
		"❯ also check the mobile layout before we\n" +
		"────────────────────────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on · 1 shell · ← for agents\n"
)

var a5Panes = []struct{ name, frame string }{
	{"ghost suggestion", a5PaneGhostSuggestion},
	{"typed draft", a5PaneTypedDraft},
}

// a5BashLaunch is a main-chain run_in_background Bash tool_use and its launch
// receipt, both stamped at.
func a5BashLaunch(at time.Time) []string {
	stamp := at.UTC().Format(time.RFC3339Nano)
	use, _ := json.Marshal(map[string]any{
		"type": "assistant", "isSidechain": false, "timestamp": stamp,
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{
			"type": "tool_use", "id": "tu-dev", "name": "Bash",
			"input": map[string]any{"command": "npm run dev", "description": "Start the dev server", "run_in_background": true},
		}}},
	})
	receipt, _ := json.Marshal(map[string]any{
		"type": "user", "isSidechain": false, "timestamp": stamp,
		"toolUseResult": map[string]any{"backgroundTaskId": "bdev01"},
		"message": map[string]any{"role": "user", "content": []map[string]any{{
			"type": "tool_result", "tool_use_id": "tu-dev", "content": "Command running in background with ID: bdev01.",
		}}},
	})
	return []string{string(use), string(receipt)}
}

// The merge rule with an injected clock: the shell counter holds the session
// running only while the newest matching launch is inside the hold, and a pane
// sighting of the counter never extends it.
func TestA5_ShellCounterHoldIsBoundedByTheLaunch(t *testing.T) {
	now := time.Now()
	pane := tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: "1 shell", Source: "pane"}
	launchedAt := now.Add(-20 * time.Second)
	fresh := scanTranscriptBackground(a5BashLaunch(launchedAt))
	stale := scanTranscriptBackground(a5BashLaunch(now.Add(-2 * time.Hour)))

	if got, _ := mergeBackgroundWork(pane, transcriptBackgroundScan{}, false, time.Time{}, now); got.Running() {
		t.Fatalf("pane only = %+v, want nothing running", got)
	}
	if got, _ := mergeBackgroundWork(pane, stale, true, time.Time{}, now); got.Running() {
		t.Fatalf("launch older than the hold = %+v, want nothing running", got)
	}
	if got, _ := mergeBackgroundWork(pane, stale, true, now, now); got.Running() {
		t.Fatalf("a pane sighting refreshed the hold: %+v", got)
	}
	got, _ := mergeBackgroundWork(pane, fresh, true, time.Time{}, now)
	if !got.Running() || got.Kind != tmux.BackgroundKindBash || got.Task != "Start the dev server" {
		t.Fatalf("fresh launch = %+v, want bash \"Start the dev server\" running", got)
	}
	// The light stayed green for a whole hold, the pane showing the counter
	// on every poll; once the hold passes the session falls back to the prompt.
	later := launchedAt.Add(backgroundTranscriptHold + time.Second)
	if got, _ := mergeBackgroundWork(pane, fresh, true, later.Add(-time.Second), later); got.Running() {
		t.Fatalf("past the hold = %+v, want nothing running", got)
	}
	// The same bound holds when the counter is briefly not drawn.
	if got, _ := mergeBackgroundWork(tmux.BackgroundWork{}, fresh, true, later.Add(-time.Second), later); got.Running() {
		t.Fatalf("transcript only, past the hold = %+v, want nothing running", got)
	}
}

// Pane only (no transcript), tmux path, a fresh process that loaded the row
// as running (what the daemon left): waiting with the idle substate.
func TestA5_LeftoverShellPaneOnlyIsWaiting(t *testing.T) {
	for _, p := range a5Panes {
		t.Run(p.name, func(t *testing.T) {
			inst, cleanup := startPaneInstance(t, "claude", "a5-pane-only", p.frame)
			defer cleanup()
			storage, err := NewStorageWithProfile("_test-a5-pane-only")
			if err != nil {
				t.Fatalf("storage: %v", err)
			}
			defer storage.Close()
			fresh := persistAndReload(t, storage, inst, StatusRunning)
			status, sub := cliPass(t, fresh)
			if status != StatusWaiting || sub != SubstateIdleAtEmptyPrompt {
				t.Fatalf("leftover shell at the prompt = %q/%q, want waiting/%q", status, sub, SubstateIdleAtEmptyPrompt)
			}
			if work := fresh.BackgroundWork(); work.Running() {
				t.Fatalf("background work = %+v, want nothing running", work)
			}
		})
	}
}

// With a transcript, on the Stop hook path and the poll path: a launch older
// than the hold is waiting; a fresh launch is running background-work, and
// the same launch past the hold is waiting again.
func TestA5_LeftoverShellTranscriptHold(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, p := range a5Panes {
		for _, hook := range []bool{true, false} {
			name := p.name + map[bool]string{true: "/stop-hook", false: "/poll"}[hook]
			t.Run(name, func(t *testing.T) {
				inst, cleanup := startHookLagInstance(t, "a5-transcript", p.frame)
				defer cleanup()
				inst.ClaudeSessionID = "sess-610"
				pass := func(launchedAt time.Time) (Status, Substate) {
					t.Helper()
					turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
					writeInstanceTranscript(t, inst, a5BashLaunch(launchedAt))
					if hook {
						writeHookLagStopFile(t, inst.ID)
					} else {
						// A hook past its freshness window: the tmux poll path decides.
						writeHookFile(t, inst.ID, "waiting", int(hookFastPathWindow/time.Second)+60)
					}
					return cliPass(t, inst)
				}

				if status, sub := pass(time.Now().Add(-2 * time.Hour)); status != StatusWaiting || sub != SubstateIdleAtEmptyPrompt {
					t.Fatalf("launch older than the hold = %q/%q, want waiting/%q", status, sub, SubstateIdleAtEmptyPrompt)
				}
				if status, sub := pass(time.Now().Add(-20 * time.Second)); status != StatusRunning || sub != SubstateBackgroundWork {
					t.Fatalf("fresh launch = %q/%q, want running/%q", status, sub, SubstateBackgroundWork)
				}
				if work := inst.BackgroundWork(); work.Kind != tmux.BackgroundKindBash {
					t.Fatalf("background work = %+v, want bash", work)
				}
				expired := time.Now().Add(-backgroundTranscriptHold - 5*time.Second)
				status, sub := pass(expired)
				if !hook && status == StatusRunning {
					// The poll path holds a pane-derived flip away from running
					// for one confirming sample (debounceFlipFromRunning).
					status, sub = pass(expired)
				}
				if status != StatusWaiting || sub != SubstateIdleAtEmptyPrompt {
					t.Fatalf("same launch past the hold = %q/%q, want waiting/%q", status, sub, SubstateIdleAtEmptyPrompt)
				}
			})
		}
	}
}

// Remote rows: `remote sessions --json` and the TUI read each remote's own
// `agent-deck list --json` verbatim (SSHRunner.FetchSessions, no
// re-detection on the controller), so the A5 frame on an updated remote
// reaches the controller as the waiting row it is.
func TestA5_RemoteRowForLeftoverShellIsWaiting(t *testing.T) {
	payload := []byte(`[{"id":"a5","title":"dev-server","path":"/tmp/x","group":"work","tool":"claude",` +
		`"status":"waiting","substate":"idle-at-empty-prompt","created_at":"2026-10-10T22:20:00Z"}]`)
	runner, _ := stubbedRunner(payload)
	sessions, _, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Status != "waiting" || sessions[0].Substate != string(SubstateIdleAtEmptyPrompt) {
		t.Fatalf("remote rows = %+v, want one waiting / idle-at-empty-prompt row", sessions)
	}
}
