package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestIssue2091CompletionEvidenceControls(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	base := HookStatus{CompletionLaunchAt: now.Add(-10 * time.Second), Status: "waiting", Event: "Stop", SessionID: "thread", HookGeneration: "launch", Sequence: 2, TimestampKnown: true, UpdatedAt: now.Add(-time.Second), DoneAt: now.Add(-time.Second), DoneStatus: "ok"}
	for _, tc := range []struct {
		name   string
		change func(*HookStatus)
		want   bool
	}{
		{"current explicit done", func(*HookStatus) {}, true},
		{"prior launch transcript", func(h *HookStatus) { h.DoneAt = now.Add(-time.Hour) }, false},
		{"missing transcript timestamp", func(h *HookStatus) { h.DoneAt = time.Time{} }, false},
		{"future transcript timestamp", func(h *HookStatus) { h.DoneAt = now.Add(time.Second) }, false},
		{"ordinary stop", func(h *HookStatus) { h.DoneStatus = "" }, false},
		{"waiting alone", func(h *HookStatus) { h.Event = ""; h.DoneStatus = "" }, false},
		{"wrong launch", func(h *HookStatus) { h.HookGeneration = "prior" }, false},
		{"wrong conversation", func(h *HookStatus) { h.SessionID = "other" }, false},
		{"unknown timestamp", func(h *HookStatus) { h.TimestampKnown = false }, false},
		{"future", func(h *HookStatus) { h.UpdatedAt = now.Add(time.Second) }, false},
		{"expired", func(h *HookStatus) { h.UpdatedAt = now.Add(-time.Minute) }, false},
		{"failed done", func(h *HookStatus) { h.DoneStatus = "fail" }, false},
		{"new running", func(h *HookStatus) { h.Status = "running" }, false},
		{"unsequenced", func(h *HookStatus) { h.Sequence = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := base
			tc.change(&h)
			if got := validTerminatedCompletion(&h, "claude", "launch", "thread", now.Add(-10*time.Second), now); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIssue2091DurableColdCompletionAndContradictions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	now := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		name          string
		exit          int
		known, server bool
		memory        Status
		newer         bool
		want          Status
	}{
		{"reload valid", 0, false, true, StatusWaiting, false, StatusStopped},
		{"explicit failure", 3, true, true, StatusWaiting, false, StatusError},
		{"explicit success", 0, true, false, StatusWaiting, false, StatusStopped},
		{"server vanished", 0, false, false, StatusWaiting, false, StatusError},
		{"running contradicts", 0, false, true, StatusRunning, false, StatusError},
		{"newer hook contradicts", 0, false, true, StatusWaiting, true, StatusError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "issue2091-" + strings.ReplaceAll(tc.name, " ", "-")
			producer := &Instance{ID: id, Tool: "codex"}
			if err := producer.seedCompletionLaunch(); err != nil {
				t.Fatal(err)
			}
			gen := producer.hookLaunchGeneration
			control, _ := json.Marshal(map[string]any{"generation": gen, "next_sequence": 2, "launch_at": now.Add(-10 * time.Second)})
			if err := os.WriteFile(filepath.Join(GetHooksDir(), id+".generation.json"), control, 0600); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(producer.bindCompletionLaunchCommand("codex"), gen) {
				t.Fatal("launch token missing from real command")
			}
			data, _ := json.Marshal(map[string]any{"status": "waiting", "event": "agent-turn-complete", "session_id": "thread", "ts": now.Unix(), "hook_generation": gen, "sequence": 2, "done_status": "ok", "codex_started_generation": "thread:turn", "codex_completed_generation": "thread:turn", "codex_started_session_id": "thread", "codex_completed_session_id": "thread"})
			if err := os.WriteFile(filepath.Join(GetHooksDir(), id+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			// A separate observer has no in-memory launch token or hook state.
			observer := &Instance{ID: id, Tool: "codex", CodexSessionID: "thread", Status: tc.memory, LastStartedAt: now.Add(-10 * time.Second), tmuxSession: &tmux.Session{Name: "missing"}, paneDeadExitStatusForTest: func() (int, bool) { return tc.exit, tc.known }, serverHasSessionsForTest: func() bool { return tc.server }}
			if tc.newer {
				observer.hookLastUpdate = now.Add(time.Second)
			}
			observer.mu.Lock()
			observer.applyTerminatedPaneStatus()
			got := observer.Status
			observer.mu.Unlock()
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestIssue2091PiHasNoSupportedPositiveProducer(t *testing.T) {
	inst := &Instance{ID: "pi-unbound", Tool: "pi"}
	if completionHookTool(inst.Tool) {
		t.Fatal("Pi must remain fail-closed pending supported producer")
	}
	if err := inst.seedCompletionLaunch(); err != nil {
		t.Fatal(err)
	}
	if got := inst.bindCompletionLaunchCommand("pi"); got != "pi" {
		t.Fatalf("unsupported token injection: %s", got)
	}
}

func TestIssue2091ConsumedCodexCompletionIsNotProof(t *testing.T) {
	now := time.Now()
	h := &HookStatus{CompletionLaunchAt: now.Add(-time.Minute), Status: "waiting", SessionID: "thread", HookGeneration: "launch", Sequence: 1, TimestampKnown: true, UpdatedAt: now, DoneStatus: "ok", CodexStartedGeneration: "thread:turn", CodexCompletedGeneration: "thread:turn", CodexStartedSessionID: "thread", CodexCompletedSessionID: "thread"}
	if !validTerminatedCompletion(h, "codex", "launch", "thread", now.Add(-time.Minute), now) {
		t.Fatal("valid control rejected")
	}
	h.codexCompletionConsumed = true
	if validTerminatedCompletion(h, "codex", "launch", "thread", now.Add(-time.Minute), now) {
		t.Fatal("consumed completion accepted")
	}
}

func TestIssue2091DurableContradictionDuringServerProbe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	now := time.Now().Truncate(time.Second)
	i := &Instance{ID: "issue2091-durable-race", Tool: "claude", ClaudeSessionID: "thread", Status: StatusWaiting, LastStartedAt: now.Add(-time.Minute), tmuxSession: &tmux.Session{Name: "missing"}, paneDeadExitStatusForTest: func() (int, bool) { return 0, false }}
	if err := i.seedCompletionLaunch(); err != nil {
		t.Fatal(err)
	}
	i.Status = StatusWaiting
	i.hookLastUpdate = time.Time{}
	record := map[string]any{"status": "waiting", "session_id": "thread", "ts": now.Unix(), "hook_generation": i.hookLaunchGeneration, "sequence": 1, "done_status": "ok", "done_at": now.Format(time.RFC3339Nano)}
	write := func() {
		b, _ := json.Marshal(record)
		if err := os.WriteFile(filepath.Join(GetHooksDir(), i.ID+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	control, _ := json.Marshal(map[string]any{"generation": i.hookLaunchGeneration, "next_sequence": 1, "launch_at": now.Add(-10 * time.Second)})
	if err := os.WriteFile(filepath.Join(GetHooksDir(), i.ID+".generation.json"), control, 0600); err != nil {
		t.Fatal(err)
	}
	i.serverHasSessionsForTest = func() bool {
		record["status"] = "running"
		record["sequence"] = 2
		delete(record, "done_status")
		write()
		return true
	}
	i.mu.Lock()
	i.applyTerminatedPaneStatus()
	got := i.Status
	i.mu.Unlock()
	if got != StatusError {
		t.Fatalf("new durable running ignored: %s", got)
	}
}

func TestIssue2091SpawnOwnerVetoesTerminationCommit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	i := &Instance{ID: "issue2091-owned", Tool: "claude", Status: StatusWaiting}
	release, err := acquireInstanceSpawnLock(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	i.mu.Lock()
	i.applyTerminatedPaneStatus()
	got := i.Status
	i.mu.Unlock()
	if got != StatusWaiting {
		t.Fatalf("active launch owner overwritten: %s", got)
	}
}

func TestIssue2091LaunchPublicationVetoesOldProbe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	entered, releaseProbe, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	i := &Instance{ID: "issue2091-publication", Tool: "claude", Status: StatusRunning, tmuxSession: &tmux.Session{Name: "missing"}, paneDeadExitStatusForTest: func() (int, bool) { close(entered); <-releaseProbe; return 7, true }}
	locks, err := acquireHermesHookLocks(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			locks.release()
		}
	}()
	go func() { i.mu.Lock(); i.applyTerminatedPaneStatus(); i.mu.Unlock(); close(finished) }()
	<-entered
	seeded := make(chan error, 1)
	epoch := i.spawnGen.Load()
	go func() { seeded <- i.seedCompletionLaunch() }()
	deadline := time.Now().Add(time.Second)
	for i.spawnGen.Load() == epoch {
		if time.Now().After(deadline) {
			t.Fatal("launch epoch not advanced before blocked publication")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseProbe)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("old probe blocked on publication")
	}
	if got := i.GetStatusThreadSafe(); got != StatusRunning {
		t.Fatalf("old probe committed while seed was blocked: %s", got)
	}
	locks.release()
	released = true
	if err := <-seeded; err != nil {
		t.Fatal(err)
	}
}
