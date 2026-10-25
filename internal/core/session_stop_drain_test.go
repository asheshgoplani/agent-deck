package core

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A drained queued start waits for its agent to be ready before the stop
// returns, often tens of seconds. The stop must be persisted before that wait,
// the drained row must say starting (message cleared) before the spawn, and
// nothing written by another process during the wait may be overwritten by a
// stale full-list save afterwards.
func TestSessionStopDrainPersistsBeforeSpawnAndKeepsConcurrentWrites(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	quiesceBackgroundWriters(t)
	profile := fmt.Sprintf("_core_stop_drain_%d", time.Now().UnixNano())

	busy := session.NewInstanceWithGroup(fmt.Sprintf("busy-%d", time.Now().UnixNano()), t.TempDir(), "serial")
	busy.Command = ""
	if err := busy.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = busy.Kill() })
	for deadline := time.Now().Add(3 * time.Second); !busy.Exists() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if !busy.Exists() {
		t.Fatal("tmux session never appeared")
	}
	busy.Status = session.StatusRunning
	next := session.NewInstanceWithGroup("next", t.TempDir(), "serial")
	next.MarkQueued("brief")
	next.SetTmuxSessionForTest(nil)
	other := session.NewInstanceWithGroup("other", t.TempDir(), "else")
	other.Status = session.StatusIdle
	other.SetTmuxSessionForTest(nil)
	seedStore(t, profile, []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}, {Name: "else", Path: "else"}}, busy, next, other)

	peer, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	late := session.NewInstanceWithGroup("late", t.TempDir(), "else")
	late.SetTmuxSessionForTest(nil)

	var busyAtSpawn, nextAtSpawn string
	var messageAtSpawn string
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, message string) error {
		db := peer.GetDB()
		if row, err := db.LoadInstanceByID(busy.ID); err == nil && row != nil {
			busyAtSpawn = row.Status
		}
		if row, err := db.LoadInstanceByID(next.ID); err == nil && row != nil {
			nextAtSpawn = row.Status
			messageAtSpawn = session.ReadQueuedMessageFromToolData(row.ToolData)
		}
		// Another process works while the drained agent boots.
		if err := db.WriteStatus(other.ID, string(session.StatusWaiting), other.Tool); err != nil {
			t.Errorf("concurrent status write: %v", err)
		}
		if _, err := peer.UpdateTitleIfUnlocked(other.ID, "other-renamed"); err != nil {
			t.Errorf("concurrent rename: %v", err)
		}
		if err := peer.InsertSessionAndVerify(late, nil); err != nil {
			t.Errorf("concurrent insert: %v", err)
		}
		// ... restarts the session that was just stopped ...
		if err := db.WriteStatus(busy.ID, string(session.StatusRunning), busy.Tool); err != nil {
			t.Errorf("concurrent restart: %v", err)
		}
		// ... and edits the drained session's own settings, as
		// `session set next context-level primer` would.
		if loaded, _, err := peer.LoadWithGroups(); err != nil {
			t.Errorf("peer load: %v", err)
		} else if n := findByTitle(loaded, "next"); n != nil {
			n.ContextLevel = session.ContextLevelPrimer
			if err := peer.SaveWithGroups([]*session.Instance{n}, nil); err != nil {
				t.Errorf("concurrent context-level edit: %v", err)
			}
		}
		i.Status = session.StatusStarting
		return nil
	}))

	prev := spawnSendWorker
	spawnSendWorker = func(string, string) error { return nil }
	t.Cleanup(func() { spawnSendWorker = prev })

	out, res := Invoke[SessionStopOut](context.Background(), testRegistry(t, Deps{}), IDSessionStop, SessionStopIn{Profile: profile, Session: busy.Title})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.Drained != next.ID {
		t.Fatalf("drained %q, want next", out.Drained)
	}
	if busyAtSpawn != string(session.StatusStopped) {
		t.Errorf("stopped session's row during the spawn = %q, want stopped", busyAtSpawn)
	}
	if nextAtSpawn != string(session.StatusStarting) || messageAtSpawn != "" {
		t.Errorf("drained row during the spawn = %q with queued message %q, want starting and cleared", nextAtSpawn, messageAtSpawn)
	}

	got := loadStore(t, profile)
	if o := findByTitle(got, "other-renamed"); o == nil || o.Status != session.StatusWaiting {
		t.Errorf("concurrent edit of a sibling lost (status %q)", statusOf(o))
	}
	if b := findByTitle(got, busy.Title); b == nil || b.Status != session.StatusRunning {
		t.Errorf("restart of the stopped session during the drain was overwritten with %q", statusOf(b))
	}
	if findByTitle(got, "late") == nil {
		t.Error("session another process added during the drain was lost")
	}
	if n := findByTitle(got, "next"); n == nil || n.ContextLevel != session.ContextLevelPrimer {
		t.Error("concurrent context-level edit of the drained session lost")
	}
	if n := findByTitle(got, "next"); n == nil || n.Status != session.StatusStarting || n.QueuedMessage != "" {
		t.Errorf("drained row after stop = %q, want starting with no queued message", statusOf(n))
	}
}

func statusOf(inst *session.Instance) string {
	if inst == nil {
		return "<missing>"
	}
	return string(inst.Status)
}
