package session

import (
	"fmt"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMonitorWatchingIdle(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	pane, err := os.ReadFile(filepath.Join("testdata", "background_work", "idle-monitor.txt"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := os.ReadFile(filepath.Join("testdata", "background_work", "idle-monitor.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, hook := range []bool{true, false} {
		t.Run(map[bool]string{true: "stop-hook", false: "poll"}[hook], func(t *testing.T) {
			inst, cleanup := startPaneInstance(t, "claude", "monitor-watching", string(pane))
			inst.Command = "claude"
			inst.tmuxSession.Command = "claude"
			defer cleanup()
			inst.ClaudeSessionID = "sess-610"
			writeInstanceTranscript(t, inst, restamp(strings.Split(strings.TrimSpace(string(tx)), "\n"), time.Now().Add(-20*time.Second)))
			if hook {
				if err := os.MkdirAll(GetHooksDir(), 0755); err != nil {
					t.Fatal(err)
				}
				writeHookLagStopFile(t, inst.ID)
			}
			status, sub := cliPass(t, inst)
			t.Logf("status=%s substate=%s detail=%s work=%+v", status, sub, inst.SubstateDetail(), inst.BackgroundWorkJSON())
			if status != StatusWaiting || string(sub) != "watching" {
				t.Fatalf("idle Monitor = %s/%s, want waiting/watching", status, sub)
			}
			inst.tmuxSession.Acknowledge()
			{
				status, sub = cliPass(t, inst)
				if status != StatusIdle || string(sub) != "watching" {
					t.Fatalf("acknowledged Monitor = %s/%s", status, sub)
				}
			}
		})
	}
}

func TestMonitorWatchingMixedTranscript(t *testing.T) {
	now := time.Now()
	pane := tmux.BackgroundWork{Kind: tmux.BackgroundKindMonitor, Task: "1 monitor", Source: "pane"}
	sc := transcriptBackgroundScan{Pending: []transcriptBackgroundTask{{ID: "finite", Kind: tmux.BackgroundKindWorkflow, Name: "finite workflow", At: now}}}
	got, _ := mergeBackgroundWork(pane, sc, true, time.Time{}, now)
	if got.Kind != tmux.BackgroundKindWorkflow {
		t.Fatalf("monitor footer hid active transcript workflow: %+v", got)
	}
}

func TestMonitorWatchingUntilEvidence(t *testing.T) {
	cases := []struct {
		command string
		passive bool
	}{
		{"until [ -f /tmp/fixture-ready ]; do sleep 2; done", true},
		{"until [ -e ready ]; do sleep 0.5; done", true},
		{"until make; do sleep 1; done", false},
		{"until [ -f ready ]; do build; sleep 1; done", false},
		{"until [ -f $(touch bad) ]; do sleep 1; done", false},
		{"until [ -f ready ]; do sleep 1; done; deploy", false},
		{"sleep 50", false},
	}
	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			lines := []string{fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"wait-use","name":"Bash","input":{"command":%q,"description":"until wait"}}]}}`, c.command), `{"type":"user","toolUseResult":{"backgroundTaskId":"wait-task"},"message":{"content":[{"type":"tool_result","tool_use_id":"wait-use"}]}}`}
			sc := scanTranscriptBackground(lines)
			for idx := range sc.Pending {
				sc.Pending[idx].At = time.Now()
			}
			work, _ := sc.inFlight()
			if work.Watching() != c.passive {
				t.Fatalf("command classified %+v, passive=%v", work, c.passive)
			}
			for _, count := range []int{1, 2} {
				pane := tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: fmt.Sprintf("%d shells", count), Source: "pane"}
				got, _ := mergeBackgroundWork(pane, sc, true, time.Time{}, time.Now())
				if got.Watching() != (c.passive && count == 1) {
					t.Fatalf("%d shells merged to %+v", count, got)
				}
			}
			missing := scanTranscriptBackground(lines[1:])
			unknown, _ := missing.inFlight()
			if unknown.Watching() {
				t.Fatal("missing tool_use classified passive")
			}
		})
	}
}

func TestMonitorWatchingFiniteAndBusyPrecedence(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	pane, err := os.ReadFile(filepath.Join("testdata", "background_work", "idle-monitor.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"workflow", "agent", "bash", "spinner"} {
		t.Run(kind, func(t *testing.T) {
			turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
			frame := string(pane)
			if kind == "spinner" {
				frame = strings.Replace(frame, "✻ Baked for 13s · 1 monitor still running", "✳ Working… (2s · ↓ 50 tokens)", 1)
			}
			inst, cleanup := startHookLagInstance(t, "monitor-mixed", frame)
			defer cleanup()
			inst.ClaudeSessionID = "sess-610"
			lines := loadProbeTranscript(t)
			switch kind {
			case "workflow":
				lines = lines[:10]
			case "agent":
				lines = lines[:28]
			case "bash":
				lines = lines[:34]
			case "spinner":
				lines = nil
			}
			writeInstanceTranscript(t, inst, restamp(lines, time.Now().Add(-20*time.Second)))
			writeHookLagStopFile(t, inst.ID)
			status, sub := cliPass(t, inst)
			if status != StatusRunning {
				t.Fatalf("%s with monitor became %s/%s", kind, status, sub)
			}
			if kind != "spinner" && sub != SubstateBackgroundWork {
				t.Fatalf("%s: sub=%s", kind, sub)
			}
			if kind == "spinner" && sub != SubstateRunning {
				t.Fatalf("spinner sub=%s", sub)
			}
		})
	}
}

func TestMonitorWatchingUntilCountAndAge(t *testing.T) {
	now := time.Now()
	for _, age := range []time.Duration{time.Second, 10 * time.Minute} {
		sc := transcriptBackgroundScan{Pending: []transcriptBackgroundTask{{ID: "wait", Kind: tmux.BackgroundKindWatcher, Name: "wait for marker", At: now.Add(-age)}}}
		pane := tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: "1 shell, 2 monitors", Source: "pane"}
		got, _ := mergeBackgroundWork(pane, sc, true, time.Time{}, now)
		if age > backgroundTranscriptHold {
			if got.Watching() {
				t.Fatalf("stale receipt hid unknown shell: %+v", got)
			}
		} else if !got.Watching() || got.Count != 3 {
			t.Fatalf("watcher count = %+v, want 3", got)
		}
	}
}

func TestMonitorWatchingUntilPollAcknowledged(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	pane, err := os.ReadFile(filepath.Join("testdata", "background_work", "idle-monitor.txt"))
	if err != nil {
		t.Fatal(err)
	}
	frame := strings.ReplaceAll(string(pane), "1 monitor", "1 shell")
	inst, cleanup := startPaneInstance(t, "claude", "until-poll", frame)
	defer cleanup()
	inst.Command = "claude"
	inst.tmuxSession.Command = "claude"
	inst.ClaudeSessionID = "sess-610"
	lines := []string{`{"type":"assistant","timestamp":"2026-10-07T08:00:00Z","message":{"content":[{"type":"tool_use","id":"wait","name":"Bash","input":{"command":"until [ -f /tmp/ready ]; do sleep 1; done"}}]}}`, `{"type":"user","timestamp":"2026-10-07T08:00:00Z","toolUseResult":{"backgroundTaskId":"wait-task"},"message":{"content":[{"type":"tool_result","tool_use_id":"wait"}]}}`}
	writeInstanceTranscript(t, inst, restamp(lines, time.Now()))
	if status, sub := cliPass(t, inst); status != StatusWaiting || sub != SubstateWatching {
		t.Fatalf("until poll=%s/%s", status, sub)
	}
	inst.tmuxSession.Acknowledge()
	for n := 0; n < 2; n++ {
		time.Sleep(600 * time.Millisecond)
		if status, sub := cliPass(t, inst); status != StatusIdle || sub != SubstateWatching {
			t.Fatalf("acknowledged until poll=%s/%s", status, sub)
		}
	}
}
