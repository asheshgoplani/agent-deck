package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		"AGENT_DECK_QUEUE_PROFILE=queue-if-busy-helper-"+session.GenerateID(),
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

func TestSessionSendQueueIfBusyStartingQueues(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "__starting__", "starting message", "--queue-if-busy", "--timeout", "1ms", "--json")
	if err != nil {
		t.Fatalf("starting queue helper failed: %v\n%s", err, out)
	}
	receipt := decodeQueuedReceipt(t, out)
	if !receipt.Success || !receipt.Queued || receipt.SessionID == "" || receipt.QueueDepth != 1 {
		t.Fatalf("invalid starting receipt: %+v", receipt)
	}
	if !strings.Contains(out, "QUEUE_MESSAGE_OK") {
		t.Fatalf("starting send was not persisted without invoking sender; output:\n%s", out)
	}
}

func TestSessionSendQueueIfBusyPersistenceFailureCode(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "__persist_error__", "will fail", "--queue-if-busy", "--json")
	if err == nil {
		t.Fatalf("forced persistence failure unexpectedly succeeded: %s", out)
	}
	if !strings.Contains(out, `"code": "`+ErrCodeDeliveryFailed+`"`) || !strings.Contains(out, "forced persistence failure") {
		t.Fatalf("persistence failure should return %s; output:\n%s", ErrCodeDeliveryFailed, out)
	}
}

func TestSessionSendQueueIfBusyRenameKeepsOriginalID(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "__rename__", "rename message", "--queue-if-busy", "--timeout", "1ms", "--json")
	if err != nil {
		t.Fatalf("rename queue helper failed: %v\n%s", err, out)
	}
	receipt := decodeQueuedReceipt(t, out)
	if !receipt.Success || !receipt.Queued || receipt.SessionID == "" || receipt.QueueDepth != 1 {
		t.Fatalf("invalid rename receipt: %+v", receipt)
	}
	if !strings.Contains(out, "RENAME_DESTINATION_OK") {
		t.Fatalf("renamed target was not queued by original immutable ID; output:\n%s", out)
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
	out, err := runQueueIfBusyHelper(t, "__idle__", "idle ping", "--queue-if-busy", "--timeout", "6s", "--json")
	if err != nil {
		t.Fatalf("idle queue-if-busy send failed: %v\n%s", err, out)
	}
	receipt := decodeSendReceipt(t, out)
	if !receipt.Success || receipt.Delivery != deliveryUnverified || receipt.Queued || !strings.Contains(out, "IDLE_SEND_OK") {
		t.Fatalf("idle target must use sender and leave queue empty; output:\n%s", out)
	}
}

func TestSessionSendQueueIfBusyBecomesIdleDuringLockedRevalidation(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "__busy_to_idle__", "became idle", "--queue-if-busy", "--timeout", "6s", "--json")
	if err != nil {
		t.Fatalf("busy-to-idle queue-if-busy send failed: %v\n%s", err, out)
	}
	receipt := decodeSendReceipt(t, out)
	if !receipt.Success || receipt.Delivery != deliveryUnverified || receipt.Queued || !strings.Contains(out, "IDLE_SEND_OK") {
		t.Fatalf("busy-to-idle target must use direct sender and leave queue empty; output:\n%s", out)
	}
}

func TestSessionSendQueueIfBusyDefaultSendUnchanged(t *testing.T) {
	out, err := runQueueIfBusyHelper(t, "__idle__", "plain ping", "--timeout", "6s", "--json")
	if err != nil {
		t.Fatalf("default send failed: %v\n%s", err, out)
	}
	receipt := decodeSendReceipt(t, out)
	if !receipt.Success || receipt.Delivery != deliveryUnverified || receipt.Queued || !strings.Contains(out, "IDLE_SEND_OK") {
		t.Fatalf("default send behavior changed; output:\n%s", out)
	}
}

func TestSessionSendQueueIfBusyRejectsArchivePersistedAfterValidation(t *testing.T) {
	root := t.TempDir()
	metaPath := filepath.Join(root, "meta.json")
	readyPath := filepath.Join(root, "ready")
	releasePath := filepath.Join(root, "release")
	args, err := json.Marshal([]string{"__busy__", "must not survive archive", "--queue-if-busy", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	sender := exec.Command(os.Args[0], "-test.run=^TestSessionSendQueueIfBusyHelper$")
	profile := "queue-if-busy-helper-" + session.GenerateID()
	sender.Env = append(os.Environ(),
		"AGENT_DECK_QUEUE_IF_BUSY_HELPER=1",
		"AGENT_DECK_QUEUE_IF_BUSY_ARGS="+string(args),
		"AGENT_DECK_QUEUE_PROFILE="+profile,
		"AGENT_DECK_QUEUE_RACE_META="+metaPath,
		"AGENT_DECK_QUEUE_RACE_READY="+readyPath,
		"AGENT_DECK_QUEUE_RACE_RELEASE="+releasePath,
	)
	var senderOut strings.Builder
	sender.Stdout = &senderOut
	sender.Stderr = &senderOut
	if err := sender.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(releasePath, []byte("release"), 0o644)
		if sender.ProcessState == nil {
			_ = sender.Process.Kill()
			_ = sender.Wait()
		}
	}()
	waitForFile := func(path string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(path); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; sender output: %s", filepath.Base(path), senderOut.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitForFile(metaPath)
	waitForFile(readyPath)
	var meta struct {
		ID  string            `json:"id"`
		Env map[string]string `json:"env"`
	}
	data, err := os.ReadFile(metaPath)
	if err != nil || json.Unmarshal(data, &meta) != nil {
		t.Fatalf("read sender metadata: %v (%s)", err, data)
	}
	archive := exec.Command(channelsCLIBinary(t), "-p", profile, "session", "archive", meta.ID, "--json")
	archive.Env = os.Environ()
	for key, value := range meta.Env {
		archive.Env = append(archive.Env, key+"="+value)
	}
	if out, err := archive.CombinedOutput(); err != nil {
		t.Fatalf("archive during sender pause: %v\n%s", err, out)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sender.Wait(); err == nil {
		t.Fatalf("sender queued after archive persisted: %s", senderOut.String())
	}
	if !strings.Contains(senderOut.String(), "is archived") {
		t.Fatalf("sender did not reject archived target: %s", senderOut.String())
	}
}

type sendReceipt struct {
	Success  bool   `json:"success"`
	Delivery string `json:"delivery"`
	Queued   bool   `json:"queued"`
}

type queuedReceipt struct {
	Success    bool   `json:"success"`
	Queued     bool   `json:"queued"`
	SessionID  string `json:"session_id"`
	QueueDepth int    `json:"queue_depth"`
}

func decodeQueuedReceipt(t *testing.T, out string) queuedReceipt {
	t.Helper()
	var receipt queuedReceipt
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&receipt); err != nil {
		t.Fatalf("decode queued receipt: %v; output:\n%s", err, out)
	}
	return receipt
}

func decodeSendReceipt(t *testing.T, out string) sendReceipt {
	t.Helper()
	var receipt sendReceipt
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&receipt); err != nil {
		t.Fatalf("decode send receipt: %v; output:\n%s", err, out)
	}
	return receipt
}

func TestSessionSendQueueIfBusyHelper(t *testing.T) {
	if os.Getenv("AGENT_DECK_QUEUE_IF_BUSY_HELPER") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("AGENT_DECK_QUEUE_IF_BUSY_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	profile := os.Getenv("AGENT_DECK_QUEUE_PROFILE")
	if profile == "" {
		profile = "queue-if-busy-helper"
	}
	if os.Getenv("AGENT_DECK_QUEUE_HANDLER") == "1" {
		if os.Getenv("AGENT_DECK_QUEUE_ENQUEUE_ERROR") == "1" {
			originalEnqueue := sessionSendQueueTxEnqueue
			sessionSendQueueTxEnqueue = func(*session.RuntimeQueueTransaction, string) (int, error) {
				return 0, errors.New("forced persistence failure")
			}
			defer func() { sessionSendQueueTxEnqueue = originalEnqueue }()
		}
		if os.Getenv("AGENT_DECK_QUEUE_RENAME_BEFORE_REFRESH") == "1" {
			originalStatus := sessionSendQueueStatus
			sessionSendQueueStatus = func(profile, sessionID string) (*session.Instance, string, error) {
				storage, instances, _, err := loadSessionData(profile)
				if err != nil {
					return nil, "", err
				}
				defer storage.Close()
				inst, errMsg, _ := ResolveSession(sessionID, instances)
				if inst == nil {
					return nil, "", errors.New(errMsg)
				}
				inst.Title = "renamed-during-refresh"
				collision := session.NewInstance(sessionID, inst.ProjectPath)
				collision.Tool = "claude"
				collision.Status = session.StatusRunning
				collision.ClaudeSessionID = "queue-collision-session"
				if err := storage.Save(append(instances, collision)); err != nil {
					return nil, "", err
				}
				seedDeferHookFile(t, collision.ID, "UserPromptSubmit", collision.ClaudeSessionID, "running")
				return originalStatus(profile, sessionID)
			}
			defer func() { sessionSendQueueStatus = originalStatus }()
		}
		if ready := os.Getenv("AGENT_DECK_QUEUE_RACE_READY"); ready != "" {
			originalStatus := sessionSendQueueStatus
			calls := 0
			sessionSendQueueStatus = func(profile, sessionID string) (*session.Instance, string, error) {
				inst, status, err := originalStatus(profile, sessionID)
				calls++
				if calls == 1 && err == nil {
					// This test owns the state transition: validation observes a
					// busy target, then archive persists while the queue lock is held.
					if err := os.WriteFile(ready, []byte("ready"), 0o644); err != nil {
						return nil, "", err
					}
					deadline := time.Now().Add(60 * time.Second)
					for {
						if _, err := os.Stat(os.Getenv("AGENT_DECK_QUEUE_RACE_RELEASE")); err == nil {
							break
						}
						if time.Now().After(deadline) {
							return nil, "", errors.New("timed out waiting for archive")
						}
						time.Sleep(10 * time.Millisecond)
					}
					// The archive completed while this status read was paused. Do
					// not return the pre-archive instance snapshot to the sender.
					return originalStatus(profile, sessionID)
				}
				return inst, status, err
			}
			defer func() { sessionSendQueueStatus = originalStatus }()
		}
		if os.Getenv("AGENT_DECK_QUEUE_BUSY_TO_IDLE") == "1" {
			originalStatus := sessionSendQueueStatus
			calls := 0
			sessionSendQueueStatus = func(profile, sessionID string) (*session.Instance, string, error) {
				inst, _, err := originalStatus(profile, sessionID)
				calls++
				if calls == 1 {
					return inst, "running", err
				}
				return inst, "waiting", err
			}
			defer func() { sessionSendQueueStatus = originalStatus }()
		}
		handleSessionSend(profile, args)
		return
	}
	if len(args) > 0 && strings.HasPrefix(args[0], "__") && args[0] != "__message_file__" {
		mode := args[0]
		project := t.TempDir()
		inst := session.NewInstance("queue-target", project)
		fakeTUI := fakeClaudeWithDraft
		if mode == "__idle__" || mode == "__busy_to_idle__" {
			fakePath := filepath.Join(project, "fake-agent.sh")
			fakeScript := `#!/usr/bin/env bash
trap '' INT
stty raw -echo
printf '\033[2J\033[H\033[999B\r› '
head -c ` + strconv.Itoa(len(args[1])+1) + ` > "$0.input"
stty sane
line="$(tr -d '\r' < "$0.input")"
printf '\r\nThinking… ctrl+c to interrupt'
sleep 2
printf '\r\nGOT: %s\r\n› ' "$line"
sleep 60
`
			if err := os.WriteFile(fakePath, []byte(fakeScript), 0o755); err != nil {
				t.Fatalf("write fake agent: %v", err)
			}
		}
		if mode == "__busy__" || mode == "__full__" || mode == "__idle__" || mode == "__busy_to_idle__" || mode == "__starting__" || mode == "__persist_error__" || mode == "__rename__" {
			if mode == "__idle__" || mode == "__busy_to_idle__" {
				fakePath := filepath.Join(project, "fake-agent.sh")
				cmd := exec.Command("tmux", "new-session", "-d", "-s", inst.GetTmuxSession().Name,
					"-c", project, "-x", "80", "-y", "24", "bash", fakePath)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("start raw fake target: %v (%s)", err, output)
				}
			} else if err := inst.GetTmuxSession().Start(fakeTUI); err != nil {
				t.Fatalf("start target: %v", err)
			}
			if err := inst.GetTmuxSession().Resize(80, 24); err != nil {
				t.Fatalf("resize target: %v", err)
			}
			t.Cleanup(func() { _ = inst.Kill() })
		}
		inst.Tool = "claude"
		if mode == "__idle__" || mode == "__busy_to_idle__" {
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
		if metaPath := os.Getenv("AGENT_DECK_QUEUE_RACE_META"); metaPath != "" {
			meta := struct {
				ID  string            `json:"id"`
				Env map[string]string `json:"env"`
			}{ID: inst.ID, Env: map[string]string{
				"HOME": os.Getenv("HOME"), "XDG_CONFIG_HOME": os.Getenv("XDG_CONFIG_HOME"),
				"XDG_DATA_HOME": os.Getenv("XDG_DATA_HOME"), "XDG_CACHE_HOME": os.Getenv("XDG_CACHE_HOME"),
			}}
			data, _ := json.Marshal(meta)
			if err := os.WriteFile(metaPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		hookStatus := "running"
		hookEvent := "UserPromptSubmit"
		if mode == "__starting__" {
			hookStatus = "starting"
		}
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
		for _, key := range []string{"AGENT_DECK_QUEUE_RACE_READY", "AGENT_DECK_QUEUE_RACE_RELEASE"} {
			if value := os.Getenv(key); value != "" {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		if mode == "__persist_error__" {
			cmd.Env = append(cmd.Env, "AGENT_DECK_QUEUE_ENQUEUE_ERROR=1")
		}
		if mode == "__rename__" {
			cmd.Env = append(cmd.Env, "AGENT_DECK_QUEUE_RENAME_BEFORE_REFRESH=1")
		}
		if mode == "__busy_to_idle__" {
			cmd.Env = append(cmd.Env, "AGENT_DECK_QUEUE_BUSY_TO_IDLE=1")
		}
		commandOutput, commandErr := cmd.CombinedOutput()
		_, _ = os.Stdout.Write(commandOutput)
		if commandErr != nil {
			pane, _ := inst.GetTmuxSession().CapturePaneFresh()
			t.Fatalf("session send command failed: %v\npane:\n%s", commandErr, pane)
		}
		if mode == "__idle__" || mode == "__busy_to_idle__" {
			wantInput := args[1] + "\r"
			inputPath := filepath.Join(project, "fake-agent.sh.input")
			deadline := time.Now().Add(3 * time.Second)
			var input []byte
			for time.Now().Before(deadline) {
				input, _ = os.ReadFile(inputPath)
				if len(input) >= len(wantInput) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if string(input) != wantInput {
				t.Fatalf("sender bytes = %q, want exact body plus carriage return %q", input, wantInput)
			}
			if session.RuntimeQueueHasPending(inst.ID) {
				t.Fatal("idle send unexpectedly queued")
			}
			println("IDLE_SEND_OK")
			return
		}
		if mode != "__busy__" && mode != "__starting__" && mode != "__rename__" {
			return
		}
		batch, err := session.StageRuntimeQueue(inst.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantMessage := "resolved from file"
		if mode == "__starting__" {
			wantMessage = "starting message"
		}
		if mode == "__rename__" {
			wantMessage = "rename message"
		}
		if len(batch.Messages) != 1 || batch.Messages[0].Message != wantMessage {
			t.Fatalf("queued messages = %#v", batch.Messages)
		}
		if mode == "__rename__" {
			storage, instances, _, err := loadSessionData(profile)
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			var collision *session.Instance
			for _, candidate := range instances {
				if candidate.Title == inst.ID {
					collision = candidate
					break
				}
			}
			if collision == nil {
				t.Fatal("ID/title collision session not found")
			}
			if session.RuntimeQueueHasPending(collision.ID) {
				t.Fatalf("message redirected to title-collision session %s", collision.ID)
			}
			if receipt := decodeQueuedReceipt(t, string(commandOutput)); receipt.SessionID != inst.ID {
				t.Fatalf("queued destination ID = %q, want exact original ID %q", receipt.SessionID, inst.ID)
			}
			println("RENAME_DESTINATION_OK")
			return
		}
		println("QUEUE_MESSAGE_OK")
		return
	}
	handleSessionSend(profile, args)
}
