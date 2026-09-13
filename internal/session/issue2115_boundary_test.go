package session

import (
	"al.essio.dev/pkg/shellescape"
	"encoding/json"
	"fmt"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReview2115BoundaryHelperControls(t *testing.T) {
	binary := review2115Binary(t)
	for _, scenario := range []string{"valid", "missing", "reseed", "duplicate", "writer-held", "missing-sequence", "null-sequence", "nonzero-sequence", "null-launch"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			review2115Activate(t)
			p := &Instance{ID: "boundary-control", Tool: "codex"}
			if err := p.seedCompletionLaunch(); err != nil {
				t.Fatal(err)
			}
			gen := p.hookLaunchGeneration
			if scenario == "missing-sequence" || scenario == "null-sequence" || scenario == "nonzero-sequence" || scenario == "null-launch" {
				control := map[string]any{"generation": gen, "next_sequence": 0}
				switch scenario {
				case "missing-sequence":
					delete(control, "next_sequence")
				case "null-sequence":
					control["next_sequence"] = nil
				case "nonzero-sequence":
					control["next_sequence"] = 1
				case "null-launch":
					control["launch_at"] = nil
				}
				data, _ := json.Marshal(control)
				if err := os.WriteFile(filepath.Join(GetHooksDir(), p.ID+".generation.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}

			if scenario == "missing" {
				gen = "missing"
			}
			if scenario == "reseed" {
				if err := p.seedCompletionLaunch(); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "duplicate" {
				if err := RecordCompletionLaunch(p.ID, gen); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "writer-held" {
				locks, ok := tryCompletionWriterLocks(p.ID)
				if !ok {
					t.Fatal("lock")
				}
				defer locks.release()
			}
			cmd := exec.Command(binary, "completion-launch", p.ID, gen)
			out, err := cmd.CombinedOutput()
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("helper outcome %v %s", err, out)
			}
		})
	}
}

func TestReview2115ImmediateCompletionColdObserver(t *testing.T) {
	binary := review2115Binary(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	// Begin early in the second so immediate producer events share its precision.
	for time.Now().Nanosecond() > 300000000 {
		time.Sleep(10 * time.Millisecond)
	}
	p := &Instance{ID: "boundary-immediate", Tool: "codex"}
	if err := p.seedCompletionLaunch(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(binary, "completion-launch", p.ID, p.hookLaunchGeneration).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for _, event := range []string{"agent-turn-start", "agent-turn-complete"} {
		data, _ := json.Marshal(map[string]string{"type": event, "thread-id": "thread", "turn-id": "A", "last-assistant-message": "===AGENTDECK_DONE=== status=ok summary=done"})
		cmd := exec.Command(binary, "codex-notify", string(data))
		cmd.Env = append(os.Environ(), "AGENTDECK_INSTANCE_ID="+p.ID, "AGENTDECK_HOOK_GENERATION="+p.hookLaunchGeneration)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	// The persisted post-spawn stamp can be later than completion.
	postSpawn := time.Now()
	hook := readHookStatusFile(p.ID)
	if hook == nil || hook.UpdatedAt.Unix() != postSpawn.Unix() {
		t.Fatal("control failed to exercise same-second completion")
	}
	observer := &Instance{ID: p.ID, Tool: "codex", CodexSessionID: "thread", Status: StatusWaiting, LastStartedAt: postSpawn, tmuxSession: &tmux.Session{Name: "missing"}, paneDeadExitStatusForTest: func() (int, bool) { return 0, false }, serverHasSessionsForTest: func() bool { return true }}
	observer.mu.Lock()
	observer.applyTerminatedPaneStatus()
	got := observer.Status
	observer.mu.Unlock()
	if got != StatusStopped {
		t.Fatalf("cold observer immediate completion: %s", got)
	}
}

func TestReview2115RespawnRejectsOldSentinel(t *testing.T) {
	binary := review2115Binary(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	t.Setenv("PATH", filepath.Dir(binary)+":"+os.Getenv("PATH"))
	socket := fmt.Sprintf("r215-%d", time.Now().UnixNano())
	tm := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	tm("new-session", "-d", "-s", "keeper", "sleep 60")
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-session", "-t", "keeper").Run()
		_ = exec.Command("tmux", "-L", socket, "kill-session", "-t", "worker").Run()
	})
	tm("new-session", "-d", "-s", "worker", "sleep 60")
	p := &Instance{ID: "boundary-respawn", Tool: "claude", completionExecutableForTest: binary}
	if err := p.seedCompletionLaunch(); err != nil {
		t.Fatal(err)
	}
	// A sentinel written after early invalidation but before replacement prelude
	// belongs to the old process, even though it is newer than the seed.
	dir := filepath.Join(home, ".claude", "projects", "test")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "thread.jsonl")
	record, _ := json.Marshal(map[string]any{"timestamp": time.Now().Format(time.RFC3339Nano), "type": "assistant", "message": map[string]string{"role": "assistant", "content": "===AGENTDECK_DONE=== status=ok summary=old"}})
	if err := os.WriteFile(transcript, append(record, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "thread", "transcript_path": transcript, "stop_hook_active": true, "cwd": home})
	done := filepath.Join(home, "published")
	command := "export AGENTDECK_INSTANCE_ID=" + p.ID + "; printf '%s' " + shellescape.Quote(string(payload)) + " | " + shellescape.Quote(binary) + " hook-handler; touch " + shellescape.Quote(done)
	tm("respawn-pane", "-k", "-t", "worker", p.bindCompletionLaunchCommand(command))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(done); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement failed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	observer := &Instance{ID: p.ID, Tool: "claude", ClaudeSessionID: "thread", Status: StatusWaiting, LastStartedAt: time.Now(), tmuxSession: &tmux.Session{Name: "worker", SocketName: socket}}
	// Wait for natural exit before asking the actual tmux disappearance probe.
	for exec.Command("tmux", "-L", socket, "has-session", "-t", "worker").Run() == nil {
		if time.Now().After(deadline) {
			t.Fatal("worker did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	observer.mu.Lock()
	observer.applyTerminatedPaneStatus()
	got := observer.Status
	observer.mu.Unlock()
	if got != StatusError {
		t.Fatalf("old sentinel accepted after real respawn: %s", got)
	}
	locks, ok := tryCompletionWriterLocks(p.ID)
	if !ok {
		t.Fatal("lock")
	}
	proof := readSequencedCompletionLocked(p.ID)
	locks.release()
	if proof == nil || proof.DoneStatus != "ok" || !proof.DoneAt.Before(proof.CompletionLaunchAt) {
		t.Fatalf("negative control lacked stale proof: %+v", proof)
	}
}
