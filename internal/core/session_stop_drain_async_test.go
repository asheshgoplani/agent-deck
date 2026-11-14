package core

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// session.stop runs under the profile-wide mutation lock. A drained session's
// queued message must not be typed there, behind the agent's ready-wait (up
// to send.DefaultAgentReadyTimeout): the stop returns at once and the message
// goes to the send queue, once, for the target's worker to deliver.
func TestSessionStopDrainDoesNotWaitForTheQueuedAgent(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	quiesceBackgroundWriters(t)
	profile := fmt.Sprintf("_core_stop_drain_async_%d", time.Now().UnixNano())

	busy := session.NewInstanceWithGroup(fmt.Sprintf("busy-%d", time.Now().UnixNano()), t.TempDir(), "serial")
	busy.Command = ""
	if err := busy.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = busy.Kill() })
	for deadline := time.Now().Add(3 * time.Second); !busy.Exists() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	busy.Status = session.StatusRunning
	next := session.NewInstanceWithGroup("next", t.TempDir(), "serial")
	next.MarkQueued("brief")
	next.SetTmuxSessionForTest(nil)
	seedStore(t, profile, []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}, busy, next)

	// A spawn handed the message stands for StartWithMessage: it blocks until
	// the agent is ready.
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, message string) error {
		if message != "" {
			time.Sleep(5 * time.Second)
		}
		i.Status = session.StatusStarting
		return nil
	}))
	var workers []string
	prev := spawnSendWorker
	spawnSendWorker = func(_, id string) error { workers = append(workers, id); return nil }
	t.Cleanup(func() { spawnSendWorker = prev })

	began := time.Now()
	out, res := Invoke[SessionStopOut](context.Background(), testRegistry(t, Deps{}), IDSessionStop, SessionStopIn{Profile: profile, Session: busy.Title})
	took := time.Since(began)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.Drained != next.ID {
		t.Fatalf("drained %q, want next", out.Drained)
	}
	if took > 3*time.Second {
		t.Errorf("session.stop took %s: it waited for the drained agent under the mutation lock", took.Round(time.Millisecond))
	}

	storage := openStorage(t, profile)
	defer storage.Close()
	recs, err := sendqueue.List(sendqueue.Dir(filepath.Dir(storage.Path())), next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Message != "brief" || recs[0].State != sendqueue.StateQueued {
		t.Fatalf("queued sends for next = %+v, want one queued \"brief\"", recs)
	}
	if len(workers) != 1 || workers[0] != next.ID {
		t.Errorf("send workers started = %q, want one for next", workers)
	}
}
