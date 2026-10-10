package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// TestIssue2530_SecondTUIWaitsAndTakesOverTheEngine: two TUIs of one profile
// (allow_multiple). The first owns the watcher engine. The second runs none,
// so the webhook port is bound once and each event is queued for the
// conductor once, and its panel feed stays quiet; its panel re-reads statedb.
// When the first quits, the second takes over: it queues, and its panel feed
// gets the events on the channels it has listened on since Init.
func TestIssue2530_SecondTUIWaitsAndTakesOverTheEngine(t *testing.T) {
	port := issue2524FreePort(t)
	env := newIssue2524Env(t, "hook-2530", map[string]string{"bind": "127.0.0.1", "port": port}, "", "")

	first := env.home
	if first.startWatcherEngine() == nil {
		t.Fatal("the first TUI did not start the watcher host")
	}
	t.Cleanup(first.StopWatcherEngine)
	if host := first.watcherHost.Load(); !host.IsOwner() || host.Engine() == nil {
		t.Fatal("the first TUI does not own the engine")
	}

	second := NewHome()
	second.spawnSendWorker = env.spawned.spawn
	first.instancesMu.RLock()
	instances := append([]*session.Instance(nil), first.instances...)
	first.instancesMu.RUnlock()
	second.instancesMu.Lock()
	second.instances = instances
	second.instancesMu.Unlock()
	if second.startWatcherEngine() == nil {
		t.Fatal("the second TUI does not listen for its watcher panel feed")
	}
	t.Cleanup(second.StopWatcherEngine)
	standby := second.watcherHost.Load()
	if standby.IsOwner() || standby.Engine() != nil {
		t.Fatal("the second TUI started an engine while the first owns it")
	}

	before := fmt.Sprintf("issue-2530-before-%d", time.Now().UnixNano())
	postWebhook(t, port, "alice@example.com", before)
	wantBefore := "[webhook] alice@example.com: " + before
	if !env.waitQueued(t, wantBefore, 5*time.Second) {
		t.Fatal("the owner did not queue the event for the conductor")
	}
	select {
	case evt := <-second.watcherPanelEvents:
		t.Fatalf("the standby TUI's panel feed got %q while another TUI owns the engine", evt.Body)
	case <-time.After(300 * time.Millisecond):
	}

	// The first TUI quits.
	first.StopWatcherEngineAndDeliveries(5 * time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for standby.Engine() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the second TUI did not take the engine over after the first quit")
		}
		time.Sleep(50 * time.Millisecond)
	}

	after := fmt.Sprintf("issue-2530-after-%d", time.Now().UnixNano())
	postWebhook(t, port, "alice@example.com", after)
	wantAfter := "[webhook] alice@example.com: " + after
	if !env.waitQueued(t, wantAfter, 5*time.Second) {
		t.Fatal("the TUI that took over did not queue the event for the conductor")
	}
	select {
	case evt, ok := <-second.watcherPanelEvents:
		if !ok || !strings.Contains(evt.Body, after) {
			t.Fatalf("panel feed after the takeover = %q (open %v), want the new event", evt.Body, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the panel feed the second TUI listened on since Init got nothing after the takeover")
	}
	if n := len(env.queued(t, wantBefore)); n != 1 {
		t.Fatalf("event before the takeover queued %d times, want once", n)
	}
	if n := len(env.queued(t, wantAfter)); n != 1 {
		t.Fatalf("event after the takeover queued %d times, want once", n)
	}
}

// TestIssue2530_QuitKeepsTheEngineLockWhileADeliveryIsInFlight: a quit waits
// a bounded time for the conductor delivery in flight (a health alert since
// #2537), which can take longer (the verify loop runs up to about 10 s). The
// owner lock must outlive that delivery, or a standby TUI could take over and
// type into the same pane next to it (review on #2638).
func TestIssue2530_QuitKeepsTheEngineLockWhileADeliveryIsInFlight(t *testing.T) {
	port := issue2524FreePort(t)
	env := newIssue2524Env(t, "hook-2530-inflight", map[string]string{"bind": "127.0.0.1", "port": port}, "", "")
	home := env.home
	send, started, release := blockingSend(t, &sentLog{})
	home.conductorDeliveriesOnce.Do(func() { home.conductorDeliveries = newConductorQueue(send) })
	if home.startWatcherEngine() == nil {
		t.Fatal("the TUI did not start the watcher host")
	}
	t.Cleanup(home.StopWatcherEngine)
	if host := home.watcherHost.Load(); !host.IsOwner() {
		t.Fatal("the TUI does not own the engine")
	}
	lockPath, err := watcher.EngineLockPath(home.profile)
	if err != nil {
		t.Fatal(err)
	}

	home.deliveryQueue().enqueue(conductorDelivery{Conductor: "demo", Text: "alert", Alert: "hook-2530-inflight", QueuedAt: time.Now()})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the alert never reached the conductor delivery")
	}
	home.StopWatcherEngineAndDeliveries(50 * time.Millisecond) // gives up on the hung delivery

	_, err = watcher.AcquireEngineOwner(lockPath)
	var running *watcher.AlreadyRunningError
	if !errors.As(err, &running) {
		t.Fatalf("lock after quit with a delivery in flight: %v, want it still held", err)
	}

	release()
	deadline := time.Now().Add(5 * time.Second)
	for {
		owner, err := watcher.AcquireEngineOwner(lockPath)
		if err == nil {
			_ = owner.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock still held after the delivery returned: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestIssue2530_ADeliveryArrivingAfterQuitStartsNothing: the queue is created
// on first use. If no alert had created it by the time of a quit, a relay
// callback still running past the engine's stop (Stop gives the relay 5 s)
// must not create it afterwards and start delivering once the owner lock is
// released (review on #2638).
func TestIssue2530_ADeliveryArrivingAfterQuitStartsNothing(t *testing.T) {
	home := NewHome()
	home.StopWatcherEngineAndDeliveries(50 * time.Millisecond)
	home.deliveryQueue().enqueue(alert("demo", "w1", "late"))
	q := home.deliveryQueue()
	q.mu.Lock()
	started := q.started
	q.mu.Unlock()
	if started != 0 {
		t.Fatalf("a delivery queued after quit started %d runner(s), want none", started)
	}
}
