package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func isolateRuntimeQueue(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
}

func TestRuntimeQueueStore(t *testing.T) {
	isolateRuntimeQueue(t)

	path := RuntimeQueuePathFor(" ../a/b ")
	if filepath.Dir(path) != RuntimeQueueDir() || filepath.Base(path) != "__a_b.jsonl" {
		t.Fatalf("sanitized path = %q", path)
	}
	if RuntimeQueueHasPending("missing") {
		t.Fatal("missing queue reported pending")
	}
	if got, err := PeekRuntimeQueue("missing"); err != nil || len(got) != 0 {
		t.Fatalf("peek missing = %#v, %v", got, err)
	}

	for i, msg := range []string{"first\ncomplete", "second"} {
		depth, err := EnqueueRuntimeMessage("worker", msg)
		if err != nil || depth != i+1 {
			t.Fatalf("enqueue %d = depth %d, %v", i, depth, err)
		}
	}
	if !RuntimeQueueHasPending("worker") {
		t.Fatal("populated queue not pending")
	}
	beforePeek, err := os.ReadFile(RuntimeQueuePathFor("worker"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := PeekRuntimeQueue("worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Message != "first\ncomplete" || got[1].Message != "second" {
		t.Fatalf("FIFO messages = %#v", got)
	}
	for _, item := range got {
		if item.ID == "" || item.QueuedAt.IsZero() || item.QueuedAt.Location() != time.UTC || item.Source != "session-send" {
			t.Fatalf("invalid metadata: %#v", item)
		}
	}
	if got[0].ID == got[1].ID {
		t.Fatalf("IDs are not unique: %q", got[0].ID)
	}
	afterPeek, err := os.ReadFile(RuntimeQueuePathFor("worker"))
	if err != nil {
		t.Fatal(err)
	}
	peekedAgain, err := PeekRuntimeQueue("worker")
	if err != nil {
		t.Fatal(err)
	}
	if string(afterPeek) != string(beforePeek) || len(peekedAgain) != len(got) || peekedAgain[0].ID != got[0].ID || peekedAgain[1].ID != got[1].ID {
		t.Fatal("peek mutated the durable FIFO")
	}
	t.Run("failure atomic", testRuntimeQueueStoreFailureAtomic)
}

func testRuntimeQueueStoreFailureAtomic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(t *testing.T)
	}{
		{
			name: "write failure",
			inject: func(t *testing.T) {
				previous := runtimeQueuePersist
				runtimeQueuePersist = func(string, []byte, os.FileMode) error {
					return errors.New("injected write failure")
				}
				t.Cleanup(func() { runtimeQueuePersist = previous })
			},
		},
		{
			name: "sync failure",
			inject: func(t *testing.T) {
				restore := SetFsyncHookForTest(func(*os.File) error {
					return errors.New("injected sync failure")
				})
				t.Cleanup(restore)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateRuntimeQueue(t)
			if _, err := EnqueueRuntimeMessage("atomic", "existing"); err != nil {
				t.Fatal(err)
			}
			path := RuntimeQueuePathFor("atomic")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tc.inject(t)
			if _, err := EnqueueRuntimeMessage("atomic", "rejected"); err == nil {
				t.Fatal("expected injected persistence failure")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("failed enqueue mutated durable queue")
			}
		})
	}
}

func TestRuntimeQueueCapacity(t *testing.T) {
	t.Run("count boundary and rejected append is unchanged", func(t *testing.T) {
		isolateRuntimeQueue(t)
		for i := 1; i <= MaxRuntimeQueueMessages; i++ {
			depth, err := EnqueueRuntimeMessage("count", "x")
			if err != nil || depth != i {
				t.Fatalf("enqueue %d = depth %d, %v", i, depth, err)
			}
		}
		before, err := os.ReadFile(RuntimeQueuePathFor("count"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := EnqueueRuntimeMessage("count", "overflow"); !errors.Is(err, ErrRuntimeQueueFull) {
			t.Fatalf("count overflow error = %v", err)
		}
		after, _ := os.ReadFile(RuntimeQueuePathFor("count"))
		if string(after) != string(before) {
			t.Fatal("rejected count append mutated queue")
		}
	})

	t.Run("byte limit independently rejects without mutation", func(t *testing.T) {
		isolateRuntimeQueue(t)
		previousID, previousNow := runtimeQueueNewID, runtimeQueueNow
		runtimeQueueNewID = func() string { return "next" }
		runtimeQueueNow = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
		t.Cleanup(func() {
			runtimeQueueNewID, runtimeQueueNow = previousID, previousNow
		})
		path := RuntimeQueuePathFor("bytes")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		next, err := jsonMarshalRuntimeMessage(RuntimeQueuedMessage{
			ID: "next", Message: "accepted", QueuedAt: runtimeQueueNow().UTC(), Source: "session-send",
		})
		if err != nil {
			t.Fatal(err)
		}
		seedRecord := RuntimeQueuedMessage{
			ID: "seed", QueuedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Source: "session-send",
		}
		base, err := jsonMarshalRuntimeMessage(seedRecord)
		if err != nil {
			t.Fatal(err)
		}
		seedRecord.Message = strings.Repeat("x", MaxRuntimeQueueBytes-len(next)-len(base))
		seed, err := jsonMarshalRuntimeMessage(seedRecord)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, seed, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := EnqueueRuntimeMessage("bytes", "accepted"); err != nil {
			t.Fatalf("exact-limit enqueue: %v", err)
		}
		atLimit, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(atLimit) != MaxRuntimeQueueBytes {
			t.Fatalf("queue size = %d, want exact limit %d", len(atLimit), MaxRuntimeQueueBytes)
		}
		if _, err := EnqueueRuntimeMessage("bytes", "x"); !errors.Is(err, ErrRuntimeQueueFull) {
			t.Fatalf("byte overflow error = %v", err)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(atLimit) {
			t.Fatal("rejected byte append mutated queue")
		}
	})
}

func TestRuntimeQueueRestart(t *testing.T) {
	if os.Getenv("AGENT_DECK_RUNTIME_QUEUE_RESTART_HELPER") == "1" {
		t.Setenv("XDG_DATA_HOME", os.Getenv("AGENT_DECK_RUNTIME_QUEUE_RESTART_DATA_HOME"))
		if _, err := EnqueueRuntimeMessage("restart", "persist me"); err != nil {
			t.Fatal(err)
		}
		return
	}
	isolateRuntimeQueue(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeQueueRestart$")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_RUNTIME_QUEUE_RESTART_HELPER=1",
		"AGENT_DECK_RUNTIME_QUEUE_RESTART_DATA_HOME="+os.Getenv("XDG_DATA_HOME"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("enqueue helper: %v\n%s", err, output)
	}
	got, err := PeekRuntimeQueue("restart")
	if err != nil || len(got) != 1 || got[0].Message != "persist me" {
		t.Fatalf("peek after restart simulation = %#v, %v", got, err)
	}
}

func TestRuntimeQueueDiscard(t *testing.T) {
	isolateRuntimeQueue(t)
	if err := DiscardRuntimeQueue("missing"); err != nil {
		t.Fatalf("discard missing: %v", err)
	}
	if _, err := EnqueueRuntimeMessage("gone", "active"); err != nil {
		t.Fatal(err)
	}
	inflight := runtimeQueueInflightPathFor("gone")
	if err := os.MkdirAll(filepath.Dir(inflight), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inflight, []byte("staged"), 0o644); err != nil {
		t.Fatal(err)
	}
	directorySyncs := 0
	restore := SetFsyncHookForTest(func(*os.File) error {
		directorySyncs++
		return nil
	})
	defer restore()
	if err := DiscardRuntimeQueue("gone"); err != nil {
		t.Fatal(err)
	}
	if directorySyncs != 1 {
		t.Fatalf("discard directory syncs = %d, want 1", directorySyncs)
	}
	if RuntimeQueueHasPending("gone") {
		t.Fatal("discarded queue still pending")
	}
	for _, path := range []string{RuntimeQueuePathFor("gone"), inflight} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("discard left %s: %v", path, err)
		}
	}
}

func TestRuntimeQueueMalformed(t *testing.T) {
	isolateRuntimeQueue(t)
	path := RuntimeQueuePathFor("bad")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		"malformed":         []byte("not-json\n"),
		"empty ID":          []byte(`{"id":"","message":"orphan","queued_at":"2026-01-01T00:00:00Z","source":"session-send"}` + "\n"),
		"zero timestamp":    []byte(`{"id":"seed","message":"orphan","queued_at":"0001-01-01T00:00:00Z","source":"session-send"}` + "\n"),
		"unexpected source": []byte(`{"id":"seed","message":"orphan","queued_at":"2026-01-01T00:00:00Z","source":"other"}` + "\n"),
		"missing newline":   []byte(`{"id":"seed","message":"ok","queued_at":"2026-01-01T00:00:00Z","source":"session-send"}`),
		"overlong":          []byte(strings.Repeat("x", MaxRuntimeQueueBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := PeekRuntimeQueue("bad"); err == nil {
				t.Fatal("expected malformed queue error")
			}
			before, _ := os.ReadFile(path)
			if _, err := EnqueueRuntimeMessage("bad", "new"); err == nil {
				t.Fatal("expected enqueue to reject malformed queue")
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatal("malformed queue was mutated")
			}
		})
	}
}

func TestRuntimeQueueStageLeavesActiveAndFreezesFIFO(t *testing.T) {
	isolateRuntimeQueue(t)
	for _, msg := range []string{"first", "second"} {
		if _, err := EnqueueRuntimeMessage("stage", msg); err != nil {
			t.Fatal(err)
		}
	}
	active := RuntimeQueuePathFor("stage")
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := StageRuntimeQueue("stage")
	if err != nil {
		t.Fatal(err)
	}
	if batch.Token == "" || len(batch.Messages) != 2 || batch.Messages[0].Message != "first" || batch.Messages[1].Message != "second" {
		t.Fatalf("staged batch = %#v", batch)
	}
	after, _ := os.ReadFile(active)
	if string(after) != string(before) {
		t.Fatal("stage changed active queue")
	}
	if _, err := EnqueueRuntimeMessage("stage", "later"); err != nil {
		t.Fatal(err)
	}
	again, err := StageRuntimeQueue("stage")
	if err != nil {
		t.Fatal(err)
	}
	if again.Token != batch.Token || len(again.Messages) != 2 || again.Messages[0].ID != batch.Messages[0].ID || again.Messages[1].ID != batch.Messages[1].ID {
		t.Fatalf("restaged batch = %#v, want frozen %#v", again, batch)
	}
}

func TestRuntimeQueueCrashRedeliversSameBatch(t *testing.T) {
	if os.Getenv("AGENT_DECK_RUNTIME_STAGE_HELPER") == "1" {
		t.Setenv("XDG_DATA_HOME", os.Getenv("AGENT_DECK_RUNTIME_STAGE_DATA_HOME"))
		batch, err := StageRuntimeQueue("crash")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("AGENT_DECK_RUNTIME_STAGE_DATA_HOME"), "batch-token"), []byte(batch.Token), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	isolateRuntimeQueue(t)
	if _, err := EnqueueRuntimeMessage("crash", "survive"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeQueueCrashRedeliversSameBatch$")
	cmd.Env = append(os.Environ(), "AGENT_DECK_RUNTIME_STAGE_HELPER=1", "AGENT_DECK_RUNTIME_STAGE_DATA_HOME="+os.Getenv("XDG_DATA_HOME"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stage helper: %v\n%s", err, output)
	}
	wantToken, err := os.ReadFile(filepath.Join(os.Getenv("XDG_DATA_HOME"), "batch-token"))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := StageRuntimeQueue("crash")
	if err != nil {
		t.Fatal(err)
	}
	if batch.Token != string(wantToken) || len(batch.Messages) != 1 || batch.Messages[0].Message != "survive" {
		t.Fatalf("recovered batch = %#v, token %q", batch, wantToken)
	}
}

func TestRuntimeQueueAcknowledgeValidatesAndRemovesOnlyPrefix(t *testing.T) {
	isolateRuntimeQueue(t)
	for _, msg := range []string{"one", "two"} {
		if _, err := EnqueueRuntimeMessage("ack", msg); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := StageRuntimeQueue("ack")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueRuntimeMessage("ack", "three"); err != nil {
		t.Fatal(err)
	}
	active, wal := RuntimeQueuePathFor("ack"), runtimeQueueInflightPathFor("ack")
	beforeActive, _ := os.ReadFile(active)
	beforeWAL, _ := os.ReadFile(wal)
	if err := AcknowledgeRuntimeQueue("ack", "wrong"); err == nil {
		t.Fatal("mismatched token succeeded")
	}
	afterActive, _ := os.ReadFile(active)
	afterWAL, _ := os.ReadFile(wal)
	if string(afterActive) != string(beforeActive) || string(afterWAL) != string(beforeWAL) {
		t.Fatal("failed acknowledgment changed durable files")
	}
	if err := AcknowledgeRuntimeQueue("ack", batch.Token); err != nil {
		t.Fatal(err)
	}
	remaining, err := PeekRuntimeQueue("ack")
	if err != nil || len(remaining) != 1 || remaining[0].Message != "three" {
		t.Fatalf("remaining = %#v, %v", remaining, err)
	}
	if _, err := os.Stat(wal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WAL remains: %v", err)
	}
	if err := AcknowledgeRuntimeQueue("ack", batch.Token); err != nil {
		t.Fatalf("repeated ack: %v", err)
	}
	if err := AcknowledgeRuntimeQueue("ack", "unknown"); err == nil {
		t.Fatal("unknown completed token succeeded")
	}
}

func TestRuntimeQueueAcknowledgeRejectsChangedPrefix(t *testing.T) {
	isolateRuntimeQueue(t)
	if _, err := EnqueueRuntimeMessage("prefix", "original"); err != nil {
		t.Fatal(err)
	}
	batch, err := StageRuntimeQueue("prefix")
	if err != nil {
		t.Fatal(err)
	}
	path := RuntimeQueuePathFor("prefix")
	records, _, err := readRuntimeQueueLocked(path)
	if err != nil {
		t.Fatal(err)
	}
	records[0].ID = "replacement"
	line, _ := jsonMarshalRuntimeMessage(records[0])
	if err := os.WriteFile(path, line, 0o644); err != nil {
		t.Fatal(err)
	}
	wal := runtimeQueueInflightPathFor("prefix")
	beforeWAL, _ := os.ReadFile(wal)
	if err := AcknowledgeRuntimeQueue("prefix", batch.Token); err == nil {
		t.Fatal("changed prefix acknowledged")
	}
	afterWAL, _ := os.ReadFile(wal)
	if string(afterWAL) != string(beforeWAL) {
		t.Fatal("prefix failure changed WAL")
	}
}

func TestRuntimeQueueStageMalformedWALIsRetained(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content func(messageID string) []byte
		wantErr string
	}{
		{name: "invalid JSON", content: func(string) []byte { return []byte("not-json\n") }, wantErr: "invalid character"},
		{name: "empty token", content: func(id string) []byte { return []byte(fmt.Sprintf("{\"token\":\"\",\"message_ids\":[%q]}\n", id)) }, wantErr: "invalid token or message IDs"},
		{name: "missing IDs", content: func(string) []byte { return []byte("{\"token\":\"batch\"}\n") }, wantErr: "invalid token or message IDs"},
		{name: "empty IDs", content: func(string) []byte { return []byte("{\"token\":\"batch\",\"message_ids\":[]}\n") }, wantErr: "invalid token or message IDs"},
		{name: "empty member ID", content: func(string) []byte { return []byte("{\"token\":\"batch\",\"message_ids\":[\"\"]}\n") }, wantErr: "empty message ID"},
		{name: "unknown field", content: func(id string) []byte {
			return []byte(fmt.Sprintf("{\"token\":\"batch\",\"message_ids\":[%q],\"extra\":true}\n", id))
		}, wantErr: "unknown field"},
		{name: "unterminated JSON", content: func(id string) []byte { return []byte(fmt.Sprintf("{\"token\":\"batch\",\"message_ids\":[%q]}", id)) }, wantErr: "unterminated JSON record"},
		{name: "trailing record", content: func(id string) []byte {
			return []byte(fmt.Sprintf("{\"token\":\"batch\",\"message_ids\":[%q]}\n{}\n", id))
		}, wantErr: "multiple JSON records"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateRuntimeQueue(t)
			if _, err := EnqueueRuntimeMessage("bad-wal", "queued"); err != nil {
				t.Fatal(err)
			}
			queued, err := PeekRuntimeQueue("bad-wal")
			if err != nil || len(queued) != 1 {
				t.Fatalf("queued = %#v, %v", queued, err)
			}
			want := tc.content(queued[0].ID)
			wal := runtimeQueueInflightPathFor("bad-wal")
			if err := os.MkdirAll(filepath.Dir(wal), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(wal, want, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := StageRuntimeQueue("bad-wal"); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("malformed WAL error = %v, want containing %q", err, tc.wantErr)
			}
			got, _ := os.ReadFile(wal)
			if string(got) != string(want) {
				t.Fatal("malformed WAL was changed")
			}
		})
	}
}

func TestRuntimeQueueAcknowledgePersistenceFailureLeavesQueueAndWAL(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failPath func(id string) string
	}{
		{name: "completion marker", failPath: runtimeQueueCompletionPathFor},
		{name: "active queue rewrite", failPath: RuntimeQueuePathFor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateRuntimeQueue(t)
			const id = "ack-fail"
			if _, err := EnqueueRuntimeMessage(id, "queued"); err != nil {
				t.Fatal(err)
			}
			batch, err := StageRuntimeQueue(id)
			if err != nil {
				t.Fatal(err)
			}
			active, wal := RuntimeQueuePathFor(id), runtimeQueueInflightPathFor(id)
			beforeActive, _ := os.ReadFile(active)
			beforeWAL, _ := os.ReadFile(wal)
			previous := runtimeQueuePersist
			runtimeQueuePersist = func(path string, data []byte, perm os.FileMode) error {
				if path == tc.failPath(id) {
					return errors.New("injected persistence failure")
				}
				return previous(path, data, perm)
			}
			t.Cleanup(func() { runtimeQueuePersist = previous })
			if err := AcknowledgeRuntimeQueue(id, batch.Token); err == nil {
				t.Fatal("acknowledgment persistence failure succeeded")
			}
			afterActive, _ := os.ReadFile(active)
			afterWAL, _ := os.ReadFile(wal)
			if string(afterActive) != string(beforeActive) || string(afterWAL) != string(beforeWAL) {
				t.Fatal("failed acknowledgment changed active queue or WAL")
			}
		})
	}
}

func TestRuntimeQueueConcurrentOperationsUseIndependentLock(t *testing.T) {
	isolateRuntimeQueue(t)
	const id = "concurrent"
	if _, err := EnqueueRuntimeMessage(id, "staged"); err != nil {
		t.Fatal(err)
	}
	batch, err := StageRuntimeQueue(id)
	if err != nil {
		t.Fatal(err)
	}

	enteredPersist := make(chan struct{})
	releasePersist := make(chan struct{})
	previous := runtimeQueuePersist
	var once sync.Once
	runtimeQueuePersist = func(path string, data []byte, perm os.FileMode) error {
		if path == RuntimeQueuePathFor(id) && strings.Contains(string(data), "overlap") {
			once.Do(func() { close(enteredPersist) })
			<-releasePersist
		}
		return previous(path, data, perm)
	}
	t.Cleanup(func() { runtimeQueuePersist = previous })

	enqueueErr := make(chan error, 1)
	go func() {
		_, err := EnqueueRuntimeMessage(id, "overlap")
		enqueueErr <- err
	}()
	<-enteredPersist
	if runtimeQueueMu.TryLock() {
		runtimeQueueMu.Unlock()
		close(releasePersist)
		t.Fatal("enqueue persistence did not hold the independent runtime lock")
	}

	stageEntered := make(chan struct{})
	startStage := make(chan struct{})
	stageErr := make(chan error, 1)
	ackErr := make(chan error, 1)
	go func() {
		close(stageEntered)
		<-startStage
		_, err := StageRuntimeQueue(id)
		stageErr <- err
	}()
	<-stageEntered
	close(startStage)
	select {
	case err := <-stageErr:
		close(releasePersist)
		<-enqueueErr
		t.Fatalf("stage returned before enqueue released runtime lock: %v", err)
	case <-time.After(100 * time.Millisecond):
		// Stage reached its call boundary but remains blocked by runtimeQueueMu.
	}
	go func() { ackErr <- AcknowledgeRuntimeQueue(id, batch.Token) }()
	close(releasePersist)
	for name, result := range map[string]<-chan error{"enqueue": enqueueErr, "stage": stageErr, "acknowledge": ackErr} {
		if err := <-result; err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	remaining, err := PeekRuntimeQueue(id)
	if err != nil || len(remaining) != 1 || remaining[0].Message != "overlap" {
		t.Fatalf("remaining queue = %#v, %v", remaining, err)
	}
}

func TestRuntimeQueueAcknowledgeRecoversAfterActiveRewriteBeforeWALRemoval(t *testing.T) {
	if os.Getenv("AGENT_DECK_RUNTIME_FINALIZE_RECOVERY_HELPER") == "1" {
		t.Setenv("XDG_DATA_HOME", os.Getenv("AGENT_DECK_RUNTIME_FINALIZE_RECOVERY_DATA_HOME"))
		batch, err := StageRuntimeQueue("finalize-crash")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("XDG_DATA_HOME"), "recovered-token"), []byte(batch.Token), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, tc := range []struct {
		name    string
		recover func(t *testing.T, oldToken string) RuntimeQueueBatch
	}{
		{
			name: "acknowledgment retry finishes removal",
			recover: func(t *testing.T, oldToken string) RuntimeQueueBatch {
				t.Helper()
				if err := AcknowledgeRuntimeQueue("finalize-crash", oldToken); err != nil {
					t.Fatalf("retry acknowledgment: %v", err)
				}
				return RuntimeQueueBatch{}
			},
		},
		{
			name: "restart staging finishes removal and stages remainder",
			recover: func(t *testing.T, oldToken string) RuntimeQueueBatch {
				t.Helper()
				cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeQueueAcknowledgeRecoversAfterActiveRewriteBeforeWALRemoval$")
				cmd.Env = append(os.Environ(),
					"AGENT_DECK_RUNTIME_FINALIZE_RECOVERY_HELPER=1",
					"AGENT_DECK_RUNTIME_FINALIZE_RECOVERY_DATA_HOME="+os.Getenv("XDG_DATA_HOME"),
				)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("stage recovery helper: %v\n%s", err, output)
				}
				token, err := os.ReadFile(filepath.Join(os.Getenv("XDG_DATA_HOME"), "recovered-token"))
				if err != nil {
					t.Fatal(err)
				}
				batch, err := StageRuntimeQueue("finalize-crash")
				if err != nil {
					t.Fatal(err)
				}
				if batch.Token != string(token) || batch.Token == oldToken || len(batch.Messages) != 1 || batch.Messages[0].Message != "later" {
					t.Fatalf("remainder batch = %#v", batch)
				}
				return batch
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateRuntimeQueue(t)
			for _, msg := range []string{"one", "two"} {
				if _, err := EnqueueRuntimeMessage("finalize-crash", msg); err != nil {
					t.Fatal(err)
				}
			}
			staged, err := StageRuntimeQueue("finalize-crash")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := EnqueueRuntimeMessage("finalize-crash", "later"); err != nil {
				t.Fatal(err)
			}

			previousRemove := runtimeQueueRemove
			runtimeQueueRemove = func(path string) error {
				if path == runtimeQueueInflightPathFor("finalize-crash") {
					return errors.New("injected WAL removal failure")
				}
				return previousRemove(path)
			}
			if err := AcknowledgeRuntimeQueue("finalize-crash", staged.Token); err == nil {
				t.Fatal("acknowledgment succeeded despite WAL removal failure")
			}
			runtimeQueueRemove = previousRemove
			t.Cleanup(func() { runtimeQueueRemove = previousRemove })
			if _, err := os.Stat(runtimeQueueInflightPathFor("finalize-crash")); err != nil {
				t.Fatalf("failed removal did not retain WAL: %v", err)
			}

			recovered := tc.recover(t, staged.Token)
			if _, err := os.Stat(runtimeQueueInflightPathFor("finalize-crash")); !errors.Is(err, os.ErrNotExist) && recovered.Token == "" {
				t.Fatalf("completed WAL remains after recovery: %v", err)
			}
			left, err := PeekRuntimeQueue("finalize-crash")
			if err != nil || len(left) != 1 || left[0].Message != "later" {
				t.Fatalf("queue after recovery = %#v, %v", left, err)
			}
		})
	}
}

func TestRuntimeQueueFormat(t *testing.T) {
	tests := []struct {
		name string
		msgs []RuntimeQueuedMessage
		want string
	}{
		{name: "empty", want: ""},
		{name: "single", msgs: []RuntimeQueuedMessage{{ID: "secret", Message: "hello", Source: "metadata"}}, want: "## Queued runtime messages\n\n1. hello"},
		{name: "multiple multiline", msgs: []RuntimeQueuedMessage{{Message: "first\ncontinued"}, {Message: "second"}}, want: "## Queued runtime messages\n\n1. first\n   continued\n2. second"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatRuntimeMessagesForInjection(tc.msgs); got != tc.want {
				t.Fatalf("format = %q, want %q", got, tc.want)
			}
		})
	}
}
