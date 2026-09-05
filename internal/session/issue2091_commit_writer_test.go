package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// The barrier is after the formerly final authority read, before i.mu is
// reacquired. A real hook subprocess finishes publishing N+1 while the watcher
// remains delayed. Cached DONE N must not authorize the later status commit.
func TestIssue2091RealWriterBeforeStatusCommit(t *testing.T) {
	binary := review2115Binary(t)
	for _, event := range []string{"agent-turn-start", "turn-failed"} {
		t.Run(event, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			review2115Activate(t)
			id := "issue2091-commit-writer"
			producer := &Instance{ID: id, Tool: "codex"}
			if err := producer.seedCompletionLaunch(); err != nil {
				t.Fatal(err)
			}
			if err := RecordCompletionLaunch(producer.ID, producer.hookLaunchGeneration); err != nil {
				t.Fatal(err)
			}
			write := func(event, turn, text string) {
				t.Helper()
				payload, _ := json.Marshal(map[string]string{"type": event, "thread-id": "thread", "turn-id": turn, "last-assistant-message": text})
				cmd := exec.Command(binary, "codex-notify", string(payload))
				cmd.Env = append(os.Environ(), "AGENTDECK_INSTANCE_ID="+id, "AGENTDECK_HOOK_GENERATION="+producer.hookLaunchGeneration)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("actual hook: %v: %s", err, out)
				}
			}
			write("agent-turn-complete", "A", "===AGENTDECK_DONE=== status=ok summary=done")
			before := readHookStatusFile(id)
			if before == nil || before.DoneStatus != "ok" {
				t.Fatalf("missing initial proof: %+v", before)
			}
			observer := &Instance{ID: id, Tool: "codex", CodexSessionID: "thread", Status: StatusWaiting, LastStartedAt: time.Now().Add(-time.Minute), tmuxSession: &tmux.Session{Name: "missing"}, paneDeadExitStatusForTest: func() (int, bool) { return 0, false }, serverHasSessionsForTest: func() bool { return true }}
			observer.terminatedCommitBarrierForTest = func() {
				turn := "A"
				if event == "agent-turn-start" {
					turn = "B"
				}
				write(event, turn, "")
				after := readHookStatusFile(id)
				if after == nil || after.Sequence <= before.Sequence || after.DoneStatus != "" {
					t.Fatalf("contradiction not published: %+v", after)
				}
			}
			observer.mu.Lock()
			observer.applyTerminatedPaneStatus()
			got := observer.Status
			observer.mu.Unlock()
			if got != StatusError {
				t.Fatalf("completed real writer before commit was ignored: %s", got)
			}
		})
	}
}

func issue2091ProofObserver(t *testing.T, sandbox bool) (*Instance, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	id := "issue2091-lock-proof"
	producer := &Instance{ID: id, Tool: "claude"}
	if sandbox {
		producer.Sandbox = &SandboxConfig{Enabled: true}
	}
	if err := producer.seedCompletionLaunch(); err != nil {
		t.Fatal(err)
	}
	scope := hermesHookScope(id, sandbox)
	now := time.Now().Truncate(time.Second)
	status, _ := json.Marshal(map[string]any{"status": "waiting", "event": "Stop", "session_id": "thread", "ts": now.Unix(), "done_at": now.Format(time.RFC3339Nano), "done_status": "ok", "hook_generation": producer.hookLaunchGeneration, "sequence": 2})
	control, _ := json.Marshal(map[string]any{"generation": producer.hookLaunchGeneration, "next_sequence": 2, "launch_at": now.Add(-10 * time.Second)})
	for name, data := range map[string][]byte{id + ".json": status, id + ".generation.json": control} {
		if err := os.WriteFile(filepath.Join(scope, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &Instance{ID: id, Tool: "claude", ClaudeSessionID: "thread", Status: StatusWaiting, LastStartedAt: now.Add(-time.Minute), tmuxSession: &tmux.Session{Name: "missing"}, paneDeadExitStatusForTest: func() (int, bool) { return 0, false }, serverHasSessionsForTest: func() bool { return true }}, scope
}

func TestIssue2091TornControlPublication(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		t.Run(fmt.Sprint(sandbox), func(t *testing.T) {
			i, scope := issue2091ProofObserver(t, sandbox)
			generation, _ := hookGenerationForInstance(i.ID)
			// Producer published its next control but failed to replace old DONE status.
			data, _ := json.Marshal(map[string]any{"generation": generation, "next_sequence": 3})
			if err := os.WriteFile(filepath.Join(scope, i.ID+".generation.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			i.mu.Lock()
			i.applyTerminatedPaneStatus()
			got := i.Status
			i.mu.Unlock()
			if got != StatusError {
				t.Fatalf("torn publication accepted old DONE: %s", got)
			}
		})
	}
}

func TestIssue2091HeldWriterLockDefersPromptly(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		t.Run(fmt.Sprint(sandbox), func(t *testing.T) {
			i, _ := issue2091ProofObserver(t, sandbox)
			scope := hermesHookScope(i.ID, sandbox)
			lock, err := os.OpenFile(filepath.Join(scope, i.ID+".lock"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			finished := make(chan Status, 1)
			go func() { i.mu.Lock(); i.applyTerminatedPaneStatus(); got := i.Status; i.mu.Unlock(); finished <- got }()
			select {
			case got := <-finished:
				if got != StatusWaiting {
					t.Fatalf("held writer did not defer classification: %s", got)
				}
			case <-time.After(time.Second):
				t.Fatal("status blocked on hook writer")
			}
		})
	}
}

func TestIssue2091FinalProofScopeControls(t *testing.T) {
	for _, scenario := range []string{"valid", "missing-sequence", "boolean-sequence", "duplicate-authority", "symlink-control", "wrong-status-scope"} {
		t.Run(scenario, func(t *testing.T) {
			i, scope := issue2091ProofObserver(t, false)
			control := filepath.Join(scope, i.ID+".generation.json")
			generation, _ := hookGenerationForInstance(i.ID)
			switch scenario {
			case "missing-sequence":
				data, _ := json.Marshal(map[string]any{"generation": generation})
				if err := os.WriteFile(control, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "boolean-sequence":
				data, _ := json.Marshal(map[string]any{"generation": generation, "next_sequence": false})
				if err := os.WriteFile(control, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "duplicate-authority", "wrong-status-scope":
				other := hermesHookScope(i.ID, true)
				if err := os.MkdirAll(other, 0700); err != nil {
					t.Fatal(err)
				}
				name := i.ID + ".generation.json"
				if scenario == "wrong-status-scope" {
					name = i.ID + ".json"
				}
				data, err := os.ReadFile(filepath.Join(scope, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(other, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-control":
				if err := os.Rename(control, control+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(control+".saved", control); err != nil {
					t.Fatal(err)
				}
			}
			i.mu.Lock()
			i.applyTerminatedPaneStatus()
			got := i.Status
			i.mu.Unlock()
			want := StatusError
			if scenario == "valid" {
				want = StatusStopped
			}
			if got != want {
				t.Fatalf("%s: got %s, want %s", scenario, got, want)
			}
		})
	}
}
