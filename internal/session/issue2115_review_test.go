package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func review2115Binary(t *testing.T) string {
	t.Helper()
	if completionExecutableForTests == "" {
		t.Fatal("TestMain did not provision matching agent-deck helper")
	}
	return completionExecutableForTests
}

// This writer runs after the initial hook read, at the actual later interval.
func TestReview2115LatePositiveWriter(t *testing.T) {
	binary := review2115Binary(t)
	for _, initial := range []string{"seed", "done"} {
		t.Run(initial, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			review2115Activate(t)
			id := "review2115-late"
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
					t.Fatalf("writer: %v %s", err, out)
				}
			}
			if initial == "done" {
				write("agent-turn-start", "A", "")
				write("agent-turn-complete", "A", "===AGENTDECK_DONE=== status=ok summary=first")
			}
			observer := &Instance{ID: id, Tool: "codex", CodexSessionID: "thread", Status: StatusWaiting, LastStartedAt: time.Now().Add(-time.Minute), tmuxSession: &tmux.Session{Name: "missing"}, paneDeadExitStatusForTest: func() (int, bool) { return 0, false }, serverHasSessionsForTest: func() bool { return true }}
			observer.terminatedCommitBarrierForTest = func() {
				write("agent-turn-start", "B", "")
				write("agent-turn-complete", "B", "===AGENTDECK_DONE=== status=ok summary=second")
				h := readHookStatusFile(id)
				if h == nil || h.DoneStatus != "ok" {
					t.Fatalf("writer did not publish proof: %+v", h)
				}
			}
			observer.mu.Lock()
			observer.applyTerminatedPaneStatus()
			got := observer.Status
			observer.mu.Unlock()
			if got != StatusStopped {
				t.Fatalf("late current-launch DONE classified %s, want stopped", got)
			}
		})
	}
}

func TestReview2115SameSecondCompletion(t *testing.T) {
	now := time.Now().Truncate(time.Second).Add(900 * time.Millisecond)
	started := now.Add(-500 * time.Millisecond)
	proof := &HookStatus{CompletionLaunchAt: started, TimestampKnown: true, HookGeneration: "G", SessionID: "thread", Sequence: 2, DoneStatus: "ok", Status: "waiting", DoneAt: now.Add(-100 * time.Millisecond), UpdatedAt: now.Truncate(time.Second)}
	if !validTerminatedCompletion(proof, "claude", "G", "thread", started, now) {
		t.Fatal("valid same-second assistant completion rejected by truncated status timestamp")
	}
}

func TestReview2115LegacyDeadSpawnOwnerRemainsUnsupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	path, err := instanceSpawnLockPath("review2115-dead")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprint(child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	release, ok := tryTerminatedStatusCommit("review2115-dead")
	if ok {
		release()
		t.Fatal("unversioned legacy owner unexpectedly reclaimed")
	}
}

func TestReview2115LiveOldSpawnOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	review2115Activate(t)
	path, err := instanceSpawnLockPath("review2115-live")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if release, ok := tryTerminatedStatusCommit("review2115-live"); ok {
		release()
		t.Fatal("live owner reclaimed by age")
	}
	if _, err := os.Stat(filepath.Clean(path)); err != nil {
		t.Fatal(err)
	}
}
