package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// readOnlyHeartbeat returns the one heartbeat under dir.
func readOnlyHeartbeat(t *testing.T, dir string) update.TUIHeartbeat {
	t.Helper()
	hbs, err := update.ListTUIHeartbeats(dir, func(int) bool { return true })
	if err != nil || len(hbs) != 1 {
		t.Fatalf("heartbeats = %v, %v", hbs, err)
	}
	return hbs[0]
}

// 2026-10-09 22:08: a TUI attached to a session at 22:08:37 and stayed
// attached all night. The Bubble Tea loop is parked in tea.Exec for the
// whole attach, so the heartbeat froze at its last tick and the fleet
// watch read "not ticking: attached or hung" for four hours, through a
// release: nothing could tell a parked deck from a hung one. The attach
// worker, which runs off the event loop, keeps the file fresh and says
// "attached", while the last tick stays where the loop left it.
func TestHeartbeat_KeepsReportingWhileAttached(t *testing.T) {
	dir := t.TempDir()
	h := newAutoRestartTestHome(t)
	h.heartbeatDir = dir

	t0 := time.Now().Add(-5 * time.Hour)
	h.maybeWriteHeartbeat(t0)

	h.isAttaching.Store(true)
	attachedAt := t0.Add(5 * time.Second)
	h.attachedHeartbeatTick(attachedAt)
	later := t0.Add(4 * time.Hour)
	h.attachedHeartbeatTick(later)

	hb := readOnlyHeartbeat(t, dir)
	if !hb.Attached || !hb.AttachedSince.Equal(attachedAt) {
		t.Fatalf("attached = %v since %v, want true since %v", hb.Attached, hb.AttachedSince, attachedAt)
	}
	if !hb.UpdatedAt.Equal(later) || !hb.LastTickAt.Equal(t0) {
		t.Fatalf("updated %v / last tick %v, want %v / %v", hb.UpdatedAt, hb.LastTickAt, later, t0)
	}
	if hb.InstalledVersion != "1.16.1" || hb.Version != Version {
		t.Fatalf("the attached heartbeat keeps the loop's view: %+v", hb)
	}

	reps := update.ReportTUIs([]update.TUIHeartbeat{hb}, "1.16.27", later.Add(time.Minute))
	r := reps[0]
	if r.Ticking || !r.Attached || r.AttachedSince == "" {
		t.Fatalf("report = %+v, want not ticking but attached", r)
	}
	line := update.DescribeTUIReport(r)
	if !strings.Contains(line, "attached to a session since") || strings.Contains(line, "hung") {
		t.Fatalf("line = %q", line)
	}

	// Detached: the worker stays quiet and the loop's next write clears it.
	h.isAttaching.Store(false)
	h.attachedHeartbeatTick(later.Add(time.Hour))
	if hb := readOnlyHeartbeat(t, dir); !hb.UpdatedAt.Equal(later) {
		t.Fatalf("the worker must not write while the loop runs: %+v", hb)
	}
	back := later.Add(2 * time.Minute)
	h.maybeWriteHeartbeat(back)
	hb = readOnlyHeartbeat(t, dir)
	if hb.Attached || !hb.AttachedSince.IsZero() || !hb.LastTickAt.Equal(back) {
		t.Fatalf("after detach = %+v", hb)
	}

	// A heartbeat that stops moving altogether still reads as not
	// ticking, and never as attached.
	stale := update.ReportTUIs([]update.TUIHeartbeat{hb}, "", back.Add(time.Hour))[0]
	if stale.Ticking || stale.Attached || !strings.Contains(update.DescribeTUIReport(stale), "not ticking") {
		t.Fatalf("stale report = %+v", stale)
	}
}

// An attached heartbeat that has itself gone stale (the worker stopped
// too) is not "attached" any more: the watch must see a dead deck.
func TestReportTUIs_StaleAttachedHeartbeatIsNotAttached(t *testing.T) {
	now := time.Now()
	hb := update.TUIHeartbeat{
		PID: 1, Version: "1.16.26", StartedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Hour), LastTickAt: now.Add(-2 * time.Hour),
		Attached: true, AttachedSince: now.Add(-2 * time.Hour),
	}
	r := update.ReportTUIs([]update.TUIHeartbeat{hb}, "", now)[0]
	if r.Attached || r.Ticking {
		t.Fatalf("report = %+v", r)
	}
}

// The worker runs on its own goroutine with its own clock: ticks arrive
// and the file moves while nothing drives the Bubble Tea loop.
func TestHeartbeat_AttachWorkerRunsOffTheEventLoop(t *testing.T) {
	dir := t.TempDir()
	h := newAutoRestartTestHome(t)
	h.heartbeatDir = dir
	t0 := time.Now().Add(-time.Hour)
	h.maybeWriteHeartbeat(t0)
	h.isAttaching.Store(true)

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runAttachedHeartbeat(ctx, ticks)
	}()
	ticks <- t0.Add(time.Minute)
	ticks <- t0.Add(10 * time.Minute)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker must stop with its context")
	}
	hb := readOnlyHeartbeat(t, dir)
	if !hb.Attached || !hb.UpdatedAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("heartbeat = %+v", hb)
	}
}
