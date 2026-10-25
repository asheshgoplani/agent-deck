package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// spyQueuedSpawn records each queued spawn's message and marks the instance
// started, as a real Start would, without spawning an agent.
func spyQueuedSpawn(t *testing.T) *[]string {
	t.Helper()
	var got []string
	t.Cleanup(SetQueuedSpawnForTest(func(i *Instance, message string) error {
		got = append(got, message)
		i.Status = StatusStarting
		return nil
	}))
	return &got
}

func claudeTrusted(t *testing.T, claudeJSON, dir string) bool {
	t.Helper()
	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		return false
	}
	var cfg struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse %s: %v", claudeJSON, err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Projects[real].HasTrustDialogAccepted
}

// A claude session that started from the queue sat on the "trust this
// folder" prompt: only the direct launch seeded trust. It must be seeded
// before the spawn.
func TestQueuedStart_SeedsClaudeTrustBeforeSpawn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	projectDir := t.TempDir()
	inst := NewInstanceWithGroup("worker", projectDir, "workers")
	inst.Tool = "claude"
	inst.MarkQueued("brief")
	storage := newTestStorage(t)
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}

	trustedAtSpawn := false
	t.Cleanup(SetQueuedSpawnForTest(func(i *Instance, _ string) error {
		trustedAtSpawn = claudeTrusted(t, filepath.Join(home, ".claude.json"), projectDir)
		return nil
	}))
	if _, err := storage.StartQueued(inst); err != nil {
		t.Fatal(err)
	}
	if !trustedAtSpawn {
		t.Fatal("folder trust was not seeded before the queued spawn")
	}
}

// sinkQueuedMessages records what reaches the send queue.
func sinkQueuedMessages(t *testing.T) *[]string {
	t.Helper()
	var got []string
	t.Cleanup(SetQueuedMessageSink(func(_, _ string, _ *Instance, message string) error {
		got = append(got, message)
		return nil
	}, 0))
	return &got
}

// The -m message of a queued launch must survive the store (the drain runs in
// another process) and go to the send queue once, never typed by the spawn
// itself: a second start of the reloaded row, as after a restart or a second
// drain, sends nothing.
func TestQueuedStart_DeliversMessageOnceAcrossReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	got := spyQueuedSpawn(t)
	sent := sinkQueuedMessages(t)

	inst := NewInstanceWithGroup("worker", t.TempDir(), "workers")
	inst.Tool = "shell"
	inst.MarkQueued("do the task")
	save := func(list []*Instance) []*Instance {
		t.Helper()
		if err := storage.SaveWithGroups(list, NewGroupTreeWithGroups(list, nil)); err != nil {
			t.Fatal(err)
		}
		loaded, _, err := storage.LoadWithGroups()
		if err != nil || len(loaded) != 1 {
			t.Fatalf("load: %v %+v", err, loaded)
		}
		return loaded
	}

	loaded := save([]*Instance{inst})
	first, err := storage.StartQueued(loaded[0])
	if err != nil {
		t.Fatal(err)
	}
	if !first.Started || len(*sent) != 1 || (*sent)[0] != "do the task" {
		t.Fatalf("first queued start = %+v, queued %q; want started with the message queued", first, *sent)
	}
	if len(*got) != 1 || (*got)[0] != "" {
		t.Fatalf("spawn messages = %q, want one spawn that types nothing", *got)
	}

	reloaded := save(loaded)
	if reloaded[0].QueuedMessage != "" {
		t.Fatalf("delivered message still stored: %q", reloaded[0].QueuedMessage)
	}
	second, err := storage.StartQueued(reloaded[0])
	if err != nil {
		t.Fatal(err)
	}
	if second.Started || len(*sent) != 1 || len(*got) != 1 {
		t.Fatalf("second queued start = %+v after %d spawns, queued %q; want nothing", second, len(*got), *sent)
	}
}

// The row is written again after the spawn: a failed queued start is stored as
// error, not left as starting, while the message stays consumed.
func TestStartQueued_PersistsFailedSpawn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	t.Cleanup(SetQueuedSpawnForTest(func(*Instance, string) error { return errors.New("spawn failed") }))

	inst := NewInstanceWithGroup("worker", t.TempDir(), "workers")
	inst.Tool = "shell"
	inst.MarkQueued("brief")
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.StartQueued(inst); err == nil {
		t.Fatal("StartQueued returned nil for a failed spawn")
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load: %v %+v", err, loaded)
	}
	if loaded[0].Status != StatusError || loaded[0].QueuedMessage != "" {
		t.Fatalf("stored status %q, queued message %q; want error and cleared", loaded[0].Status, loaded[0].QueuedMessage)
	}
}

// A start of a queued session that fails (here before the spawn) hands
// nothing off: the message stays queued with the row, for the start or drain
// that does succeed to deliver once. A hand-off before the spawn queued it
// once per failed attempt.
func TestStart_FailedStartKeepsQueuedMessage(t *testing.T) {
	for name, start := range map[string]func(*Instance) error{
		"Start":            (*Instance).Start,
		"StartWithMessage": func(i *Instance) error { return i.StartWithMessage("hello") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sent := sinkQueuedMessages(t)
			inst := NewInstanceWithGroup("worker", t.TempDir(), "workers")
			inst.Tool = "shell"
			inst.MarkQueued("secret brief")
			if err := newTestStorage(t).SaveWithGroups([]*Instance{inst}, nil); err != nil {
				t.Fatal(err)
			}
			inst.SetTmuxSessionForTest(nil) // the start fails: no spawn
			if err := start(inst); err == nil {
				t.Fatal("start without a tmux session succeeded")
			}
			if inst.QueuedMessage != "secret brief" || len(*sent) != 0 {
				t.Fatalf("after a failed %s: kept %q, queued %q; want kept, nothing queued", name, inst.QueuedMessage, *sent)
			}
		})
	}
}

// The instance keeps its queued message unless the take actually removed it
// from the row: otherwise the next full save deletes it from the row
// (queued_message is a typed key, so its absence wins) and it is lost.
func TestTakeOffQueueKeepsMessageWhenNothingWasTaken(t *testing.T) {
	t.Run("no store", func(t *testing.T) {
		sent := sinkQueuedMessages(t)
		inst := NewInstanceWithGroup("worker", t.TempDir(), "workers")
		inst.MarkQueued("brief")
		inst.takeOffQueueAfterStart()
		if inst.QueuedMessage != "brief" || len(*sent) != 0 {
			t.Fatalf("kept %q, queued %q; want kept, nothing queued", inst.QueuedMessage, *sent)
		}
	})
	t.Run("row gone", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		sent := sinkQueuedMessages(t)
		storage := newTestStorage(t)
		inst := NewInstanceWithGroup("worker", t.TempDir(), "workers")
		inst.MarkQueued("brief")
		if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
			t.Fatal(err)
		}
		if err := storage.GetDB().DeleteInstance(inst.ID); err != nil {
			t.Fatal(err)
		}
		inst.takeOffQueueAfterStart()
		if inst.QueuedMessage != "brief" || len(*sent) != 0 {
			t.Fatalf("kept %q, queued %q; want kept, nothing queued", inst.QueuedMessage, *sent)
		}
	})
}

// Recording a take in the storage baseline is copy-on-write: a save in flight
// holds the current baseline rows and must not see them change.
func TestNoteTakenOffQueueDoesNotEditTheBaselineInPlace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	inst := NewInstanceWithGroup("worker", t.TempDir(), "workers")
	inst.MarkQueued("brief")
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	held := inst.storageSnapshot.original // as a save in flight holds it
	inst.noteTakenOffQueue(string(StatusStarting))
	if held.Status != string(StatusQueued) || ReadQueuedMessageFromToolData(held.ToolData) != "brief" {
		t.Fatalf("baseline row edited in place: status %q, message %q", held.Status, ReadQueuedMessageFromToolData(held.ToolData))
	}
	if got := inst.storageSnapshot.original; got.Status != string(StatusStarting) || ReadQueuedMessageFromToolData(got.ToolData) != "" {
		t.Fatalf("new baseline = status %q, message %q; want starting, none", got.Status, ReadQueuedMessageFromToolData(got.ToolData))
	}
}
