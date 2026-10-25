package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// seedQueued stores a group "serial" (cap 1) with one queued session "next"
// and returns the profile.
func seedQueued(t *testing.T, message string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	quiesceBackgroundWriters(t)
	profile := fmt.Sprintf("_core_drain_claim_%d", time.Now().UnixNano())
	next := session.NewInstanceWithGroup("next", t.TempDir(), "serial")
	next.MarkQueued(message)
	next.SetTmuxSessionForTest(nil)
	seedStore(t, profile, []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}, next)
	return profile
}

func openStorage(t *testing.T, profile string) *session.Storage {
	t.Helper()
	s, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Two stops in different processes can drain the same queued session. Only
// one may start it; the other backs off quietly instead of spawning it again
// and writing error over a live session.
func TestDrainGroupQueueTwoDrainersStartOnce(t *testing.T) {
	profile := seedQueued(t, "")
	groups := []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}
	first, second := loadStore(t, profile), loadStore(t, profile)
	a, b := openStorage(t, profile), openStorage(t, profile)
	defer a.Close()
	defer b.Close()

	spawns := 0
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		spawns++
		i.Status = session.StatusStarting
		return nil
	}))
	var failed []Event
	ctx := WithObserver(context.Background(), func(ev Event) {
		if ev.Kind == EventQueueDrainFailed {
			failed = append(failed, ev)
		}
	})
	if got := drainGroupQueue(ctx, a, "serial", first, groups); got == nil {
		t.Fatal("first drainer started nothing")
	}
	if got := drainGroupQueue(ctx, b, "serial", second, groups); got != nil {
		t.Errorf("second drainer also started %s", got.Title)
	}
	if spawns != 1 || len(failed) != 0 {
		t.Errorf("spawns = %d, drain failures = %+v; want one spawn and no failure", spawns, failed)
	}
	if n := findByTitle(loadStore(t, profile), "next"); n == nil || n.Status != session.StatusStarting {
		t.Errorf("stored status = %v, want starting", n)
	}
}

// A spawn that succeeded is a started session even when its final status
// cannot be saved: the drain reports it, with a warning, not as a failure.
func TestDrainGroupQueueSaveFailureAfterSpawnIsNotADrainFailure(t *testing.T) {
	profile := seedQueued(t, "")
	groups := []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}
	instances := loadStore(t, profile)
	storage := openStorage(t, profile)

	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		_ = storage.Close() // the save after the spawn fails
		i.Status = session.StatusStarting
		return nil
	}))
	var failed []Event
	ctx := WithObserver(context.Background(), func(ev Event) {
		if ev.Kind == EventQueueDrainFailed {
			failed = append(failed, ev)
		}
	})
	if got := drainGroupQueue(ctx, storage, "serial", instances, groups); got == nil || len(failed) != 0 {
		t.Fatalf("drained %v with failures %+v; want next reported as started", got, failed)
	}
}
