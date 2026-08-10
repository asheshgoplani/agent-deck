package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	inflight := RuntimeQueuePathFor("gone") + ".inflight"
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
