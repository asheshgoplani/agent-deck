package ui

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil/logassert"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// captureStopLogs sends the UI and watcher host logs to a capture until the
// test ends. Call it before anything that registers a cleanup which logs.
func captureStopLogs(t *testing.T) *logassert.Capture {
	t.Helper()
	logs := logassert.NewCapture()
	prevUI, prevWatcher := uiLog, watcherHostLog
	uiLog, watcherHostLog = slog.New(logs), slog.New(logs)
	t.Cleanup(func() { uiLog, watcherHostLog = prevUI, prevWatcher })
	return logs
}

// TestIssue2530_SecondTUIWaitsAndTakesOverTheEngine: two TUIs of one profile
// (allow_multiple). The first owns the watcher engine. The second runs none,
// so the webhook port is bound once and the conductor gets each event once,
// and its panel feed stays quiet; its panel re-reads statedb. When the first
// quits, the second takes over: it delivers, and its panel feed gets the
// events on the channels it has listened on since Init.
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
	if !env.waitForPane(wantBefore, 5*time.Second) {
		t.Fatal("the owner did not deliver the event")
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
	if !env.waitForPane(wantAfter, 5*time.Second) {
		t.Fatal("the TUI that took over did not deliver the event")
	}
	select {
	case evt, ok := <-second.watcherPanelEvents:
		if !ok || !strings.Contains(evt.Body, after) {
			t.Fatalf("panel feed after the takeover = %q (open %v), want the new event", evt.Body, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the panel feed the second TUI listened on since Init got nothing after the takeover")
	}
	if n := env.paneCount(wantBefore); n != 1 {
		t.Fatalf("event before the takeover delivered %d times, want once", n)
	}
	if n := env.paneCount(wantAfter); n != 1 {
		t.Fatalf("event after the takeover delivered %d times, want once", n)
	}
}

// TestIssue2530_QuitKeepsTheEngineLockWhileADeliveryIsInFlight: a quit waits
// a bounded time for the conductor delivery in flight, which can take longer
// (the verify loop runs up to about 10 s). The owner lock must outlive that
// delivery, or a standby TUI could take over and type into the same pane next
// to it (review on #2638).
func TestIssue2530_QuitKeepsTheEngineLockWhileADeliveryIsInFlight(t *testing.T) {
	logs := captureStopLogs(t)
	port := issue2524FreePort(t)
	env := newIssue2524Env(t, "hook-2530-inflight", map[string]string{"bind": "127.0.0.1", "port": port}, "", "")
	home := env.home
	send, started, release := blockingSend(t, &sentLog{})
	home.conductorDeliveriesOnce.Do(func() { home.conductorDeliveries = newConductorQueue(send) })
	if home.startWatcherEngine() == nil {
		t.Fatal("the TUI did not start the watcher host")
	}
	t.Cleanup(home.StopWatcherEngine)
	lockPath, err := watcher.EngineLockPath(home.profile)
	if err != nil {
		t.Fatal(err)
	}

	postWebhook(t, port, "alice@example.com", "in-flight")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the routed event never reached the conductor delivery")
	}
	home.StopWatcherEngineAndDeliveries(50 * time.Millisecond) // gives up on the hung delivery

	_, err = watcher.AcquireEngineOwner(lockPath)
	var running *watcher.AlreadyRunningError
	if !errors.As(err, &running) {
		t.Fatalf("lock after quit with a delivery in flight: %v, want it still held", err)
	}
	logs.AssertContains(t, "conductor_delivery_stop_timeout")
	logs.AssertContains(t, "watcher_engine_release_deferred")

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

// TestIssue2530_ASignalExitWithNothingInFlightLogsNoTimeoutOrDeferral: an
// owner that exits on a signal (no wait) while no conductor delivery is being
// sent releases the engine lock before Stop returns, and its log reports a
// normal exit: no stop timeout, no deferred release. Both used to be written
// on every such exit, the zero wait and the release check racing the runners
// winding down (#2638).
func TestIssue2530_ASignalExitWithNothingInFlightLogsNoTimeoutOrDeferral(t *testing.T) {
	logs := captureStopLogs(t)
	port := issue2524FreePort(t)
	env := newIssue2524Env(t, "hook-2530-signal-exit", map[string]string{"bind": "127.0.0.1", "port": port}, "", "")
	home := env.home
	sent := &sentLog{}
	home.conductorDeliveriesOnce.Do(func() { home.conductorDeliveries = newConductorQueue(sent.add) })
	if home.startWatcherEngine() == nil {
		t.Fatal("the TUI did not start the watcher host")
	}
	t.Cleanup(home.StopWatcherEngine)
	lockPath, err := watcher.EngineLockPath(home.profile)
	if err != nil {
		t.Fatal(err)
	}

	postWebhook(t, port, "alice@example.com", "delivered")
	deadline := time.Now().Add(5 * time.Second)
	for len(sent.texts()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the routed event never reached the conductor delivery")
		}
		time.Sleep(10 * time.Millisecond)
	}
	queueIdle(t, home.conductorDeliveries)

	home.StopWatcherEngine()

	owner, err := watcher.AcquireEngineOwner(lockPath)
	if err != nil {
		t.Fatalf("lock after a signal exit with nothing in flight: %v, want it released", err)
	}
	_ = owner.Close()
	logs.AssertContains(t, "watcher_engine_released")
	logs.AssertNotContains(t, "conductor_delivery_stop_timeout")
	logs.AssertNotContains(t, "watcher_engine_release_deferred")
}

// TestIssue2530_ADeliveryArrivingAfterQuitStartsNothing: the queue is created
// on first use. If no delivery had created it by the time of a quit, a relay
// callback still running past the engine's stop (Stop gives the relay 5 s)
// must not create it afterwards and start delivering once the owner lock is
// released (review on #2638).
func TestIssue2530_ADeliveryArrivingAfterQuitStartsNothing(t *testing.T) {
	home := NewHome()
	home.StopWatcherEngineAndDeliveries(50 * time.Millisecond)
	home.dispatchWatcherEvent(watcher.Event{Source: "slack", Sender: "alice", Body: "late", RoutedTo: "demo"})
	q := home.deliveryQueue()
	q.mu.Lock()
	started := q.started
	q.mu.Unlock()
	if started != 0 {
		t.Fatalf("a delivery dispatched after quit started %d runner(s), want none", started)
	}
}
