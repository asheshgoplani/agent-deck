package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func runQueueIfBusyHelper(t *testing.T, args ...string) (string, error) {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionSendQueueIfBusyHelper$")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_QUEUE_IF_BUSY_HELPER=1",
		"AGENT_DECK_QUEUE_IF_BUSY_ARGS="+string(encoded),
	)
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

func TestSessionSendQueueIfBusyFlagRegistered(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "missing", "hello", "--queue-if-busy", "--json")
	if err == nil {
		t.Fatal("helper unexpectedly succeeded for a missing target")
	}
	got := out
	if strings.Contains(got, "flag provided but not defined") {
		t.Fatalf("--queue-if-busy was not registered:\n%s", got)
	}
	if !strings.Contains(got, ErrCodeNotFound) {
		t.Fatalf("registered flag should reach target resolution and return %s; output:\n%s", ErrCodeNotFound, got)
	}
}

func TestSessionSendQueueIfBusyFlagConflicts(t *testing.T) {
	for _, conflict := range []string{"--no-wait", "--wait", "--stream", "--draft", "--defer-if-busy"} {
		t.Run(conflict, func(t *testing.T) {
			out, err := runQueueIfBusyHelper(t, "missing", "hello", "--queue-if-busy", conflict, "--json")
			if err == nil {
				t.Fatalf("conflict %s unexpectedly succeeded", conflict)
			}
			if !strings.Contains(out, ErrCodeInvalidOperation) || !strings.Contains(out, "--queue-if-busy") {
				t.Fatalf("conflict %s should return a queue-specific %s error; output:\n%s", conflict, ErrCodeInvalidOperation, out)
			}
		})
	}
}

func TestSessionSendQueueIfBusyBusyQueuesResolvedMessageFile(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "__busy__", "--message-file", "__message_file__", "--queue-if-busy", "--timeout", "1ms", "--json")
	if err != nil {
		t.Fatalf("busy queue helper failed: %v\n%s", err, out)
	}
	var receipt struct {
		Success    bool   `json:"success"`
		Queued     bool   `json:"queued"`
		SessionID  string `json:"session_id"`
		QueueDepth int    `json:"queue_depth"`
	}
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&receipt); err != nil {
		t.Fatalf("decode busy receipt: %v; output:\n%s", err, out)
	}
	if !receipt.Success || !receipt.Queued || receipt.SessionID == "" || receipt.QueueDepth != 1 {
		t.Fatalf("invalid busy receipt: %+v", receipt)
	}
	if !strings.Contains(out, "QUEUE_MESSAGE_OK") {
		t.Fatalf("resolved message was not persisted; output:\n%s", out)
	}
}

func TestSessionSendQueueIfBusyRejectsInvalidTargets(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"__stopped__", "is stopped"},
		{"__archived__", "is archived"},
		{"__non_hook__", "does not support hook-driven queueing"},
		{"__nonexistent__", "is not running"},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			out, err := runQueueIfBusyHelper(t, tc.target, "hello", "--queue-if-busy", "--json")
			if err == nil {
				t.Fatalf("target %s unexpectedly succeeded: %s", tc.target, out)
			}
			if !strings.Contains(out, ErrCodeInvalidOperation) || !strings.Contains(out, tc.want) {
				t.Fatalf("target %s should return %s with %q; output:\n%s", tc.target, ErrCodeInvalidOperation, tc.want, out)
			}
		})
	}
}

func TestSessionSendQueueIfBusyQueueFullCode(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "__full__", "overflow", "--queue-if-busy", "--json")
	if err == nil {
		t.Fatalf("full queue unexpectedly succeeded: %s", out)
	}
	if !strings.Contains(out, `"code": "`+ErrCodeQueueFull+`"`) {
		t.Fatalf("full queue should return %s; output:\n%s", ErrCodeQueueFull, out)
	}
}

func TestSessionSendQueueIfBusyIdleUsesSenderWithoutQueue(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "__idle__", "idle delivery", "--queue-if-busy", "--timeout", "6s", "--json")
	if err != nil {
		t.Fatalf("idle queue-if-busy send failed: %v\n%s", err, out)
	}
	if strings.Contains(out, `"queued": true`) || !strings.Contains(out, `"delivery": "typed"`) || !strings.Contains(out, "IDLE_SEND_OK") {
		t.Fatalf("idle target must use sender and leave queue empty; output:\n%s", out)
	}
}

func TestSessionSendQueueIfBusyDefaultSendUnchanged(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "__idle__", "default delivery", "--no-wait", "--timeout", "6s", "--json")
	if err != nil {
		t.Fatalf("default send failed: %v\n%s", err, out)
	}
	if strings.Contains(out, `"queued": true`) || !strings.Contains(out, `"delivery": "typed"`) || !strings.Contains(out, "IDLE_SEND_OK") {
		t.Fatalf("default send behavior changed; output:\n%s", out)
	}
}

func TestSessionSendQueueIfBusyHelper(t *testing.T) {
	if os.Getenv("AGENT_DECK_QUEUE_IF_BUSY_HELPER") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("AGENT_DECK_QUEUE_IF_BUSY_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	profile := "queue-if-busy-helper"
	if os.Getenv("AGENT_DECK_QUEUE_HANDLER") == "1" {
		handleSessionSend(profile, args)
		return
	}
	if len(args) > 0 && strings.HasPrefix(args[0], "__") && args[0] != "__message_file__" {
		mode := args[0]
		project := t.TempDir()
		inst := session.NewInstance("queue-target", project)
		fakeTUI := fakeClaudeWithDraft
		if mode == "__idle__" {
			fakeTUI = `bash -c '
				printf "\033[2J\033[H\033[999B\r› "
				while :; do
					pane="$(tmux capture-pane -p -t "$TMUX_PANE")"
					case "$pane" in
						*"idle delivery"*) printf "\nGOT: idle delivery\n› "; break ;;
						*"default delivery"*) printf "\nGOT: default delivery\n› "; break ;;
					esac
					sleep 0.05
				done
				sleep 15
			'`
		}
		if mode == "__busy__" || mode == "__full__" || mode == "__idle__" {
			if err := inst.GetTmuxSession().Start(fakeTUI); err != nil {
				t.Fatalf("start target: %v", err)
			}
			if err := inst.GetTmuxSession().Resize(80, 24); err != nil {
				t.Fatalf("resize target: %v", err)
			}
			t.Cleanup(func() { _ = inst.Kill() })
		}
		inst.Tool = "claude"
		if mode == "__idle__" {
			inst.Tool = "cursor"
		}
		inst.Status = session.StatusRunning
		inst.ClaudeSessionID = "queue-busy-session"
		if mode == "__stopped__" {
			inst.Status = session.StatusStopped
		}
		if mode == "__archived__" {
			inst.ArchivedAt = time.Now().UTC()
		}
		if mode == "__non_hook__" {
			inst.Tool = "shell"
		}
		storage, err := session.NewStorageWithProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Save([]*session.Instance{inst}); err != nil {
			t.Fatal(err)
		}
		_ = storage.Close()
		hookStatus := "running"
		hookEvent := "UserPromptSubmit"
		if mode == "__idle__" {
			hookStatus = "waiting"
			hookEvent = "Stop"
		}
		seedDeferHookFile(t, inst.ID, hookEvent, inst.ClaudeSessionID, hookStatus)
		if mode == "__full__" {
			for i := 0; i < session.MaxRuntimeQueueMessages; i++ {
				if _, err := session.EnqueueRuntimeMessage(inst.ID, "seed"); err != nil {
					t.Fatal(err)
				}
			}
		}
		messagePath := filepath.Join(project, "message.txt")
		if err := os.WriteFile(messagePath, []byte("resolved from file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for i := range args {
			if args[i] == mode {
				args[i] = inst.Title
			}
			if args[i] == "__message_file__" {
				args[i] = messagePath
			}
		}
		encodedArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestSessionSendQueueIfBusyHelper$")
		cmd.Env = append(os.Environ(),
			"AGENT_DECK_QUEUE_HANDLER=1",
			"AGENT_DECK_TASK6_HELPER_PROCESS=1",
			"AGENT_DECK_QUEUE_IF_BUSY_ARGS="+string(encodedArgs),
		)
		commandOutput, commandErr := cmd.CombinedOutput()
		_, _ = os.Stdout.Write(commandOutput)
		if commandErr != nil && mode != "__idle__" {
			t.Fatalf("session send command failed: %v", commandErr)
		}
		if mode == "__idle__" {
			if session.RuntimeQueueHasPending(inst.ID) {
				t.Fatal("idle send unexpectedly queued")
			}
			println("IDLE_SEND_OK")
			return
		}
		if mode != "__busy__" {
			return
		}
		batch, err := session.StageRuntimeQueue(inst.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Messages) != 1 || batch.Messages[0].Message != "resolved from file" {
			t.Fatalf("queued messages = %#v", batch.Messages)
		}
		println("QUEUE_MESSAGE_OK")
		return
	}
	handleSessionSend(profile, args)
}
