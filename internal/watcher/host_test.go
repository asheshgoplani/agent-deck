package watcher

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// hostTestEnv is one profile's state for EngineHost tests: a database with a
// running webhook watcher on a free loopback port, and a clients.json that
// routes its sender to a conductor, so no event goes to triage.
type hostTestEnv struct {
	db      *statedb.StateDB
	profile string
	port    string
}

func newHostTestEnv(t *testing.T, profile string) *hostTestEnv {
	t.Helper()
	db := newTestDB(t)
	saveTestWatcher(t, db, "w-hook", "hook", "webhook")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	dir, err := session.WatcherNameDir("hook")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "watcher.toml"), []byte("[source]\nport = \""+port+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clients, _ := json.Marshal(map[string]ClientEntry{"alice@example.com": {Conductor: "demo", Name: "Alice"}})
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "clients.json"), clients, 0o600); err != nil {
		t.Fatal(err)
	}
	return &hostTestEnv{db: db, profile: profile, port: port}
}

// host returns a host for the env's profile whose routed events land on the
// returned channel. Cleanup stops it.
func (e *hostTestEnv) host(t *testing.T) (*EngineHost, <-chan Event) {
	t.Helper()
	got := make(chan Event, 16)
	h := NewEngineHost(HostConfig{
		Profile:       e.profile,
		DB:            e.db,
		DeliverEvent:  func(evt Event) { got <- evt },
		RetryInterval: 20 * time.Millisecond,
	})
	t.Cleanup(func() { h.Stop(nil) })
	return h, got
}

// post sends one event to the webhook watcher, retrying while nothing listens.
func (e *hostTestEnv) post(t *testing.T, body string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+e.port+"/webhook", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Webhook-Sender", "alice@example.com")
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("webhook answered %d", resp.StatusCode)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook watcher never accepted the event: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *hostTestEnv) listening() bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+e.port, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func waitDelivered(t *testing.T, got <-chan Event, body string) Event {
	t.Helper()
	select {
	case evt := <-got:
		if evt.Body != body {
			t.Fatalf("delivered %q, want %q", evt.Body, body)
		}
		return evt
	case <-time.After(5 * time.Second):
		t.Fatalf("event %q was not delivered", body)
	}
	return Event{}
}

// TestEngineHost_OneEnginePerProfileAndTakeover covers #2530 with two hosts
// of one profile, as two long-lived processes would run them: only the owner
// runs an engine and binds the webhook port, and once it stops the other
// takes over, binds the port and delivers.
func TestEngineHost_OneEnginePerProfileAndTakeover(t *testing.T) {
	env := newHostTestEnv(t, "hosttest")
	first, firstGot := env.host(t)
	second, secondGot := env.host(t)

	first.Start()
	if !first.IsOwner() || first.Engine() == nil {
		t.Fatal("the first host did not take the engine")
	}
	second.Start()
	if second.IsOwner() || second.Engine() != nil {
		t.Fatal("a second host started an engine while the first owns it")
	}

	env.post(t, "before-takeover")
	if evt := waitDelivered(t, firstGot, "before-takeover"); evt.RoutedTo != "demo" {
		t.Fatalf("routed to %q, want demo", evt.RoutedTo)
	}
	if n := countWatcherEvents(t, env.db, "w-hook"); n != 1 {
		t.Fatalf("watcher_events holds %d rows, want 1", n)
	}

	first.Stop(func() <-chan struct{} {
		// Still holding the lock, so the standby cannot have bound it.
		if env.listening() {
			t.Error("the webhook port still listens after its owner's engine stopped")
		}
		return nil
	})
	deadline := time.Now().Add(5 * time.Second)
	for second.Engine() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the standby host did not take over after the owner stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !second.IsOwner() {
		t.Fatal("the host that took over does not report itself as owner")
	}

	env.post(t, "after-takeover")
	waitDelivered(t, secondGot, "after-takeover")
	if n := countWatcherEvents(t, env.db, "w-hook"); n != 2 {
		t.Fatalf("watcher_events holds %d rows, want 2", n)
	}
	select {
	case evt := <-firstGot:
		t.Fatalf("the stopped host delivered %q", evt.Body)
	default:
	}
}

// TestEngineHost_StopReleasesTheLockLast: by the time drain runs, the engine
// has stopped and its port is closed, but the lock is still held, so a
// successor can neither bind a port this engine still holds nor deliver next
// to a delivery still finishing here.
func TestEngineHost_StopReleasesTheLockLast(t *testing.T) {
	env := newHostTestEnv(t, "hostorder")
	h, _ := env.host(t)
	h.Start()
	if h.Engine() == nil {
		t.Fatal("host did not start an engine")
	}
	lockPath, err := EngineLockPath(env.profile)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !env.listening() {
		if time.Now().After(deadline) {
			t.Fatal("the webhook port never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}

	drained := false
	h.Stop(func() <-chan struct{} {
		drained = true
		if env.listening() {
			t.Error("drain ran while the engine still held the webhook port")
		}
		_, err := AcquireEngineOwner(lockPath)
		var running *AlreadyRunningError
		if !errors.As(err, &running) {
			t.Errorf("lock during drain: %v, want it still held", err)
		}
		return nil
	})
	if !drained {
		t.Fatal("Stop did not run drain")
	}
	owner, err := AcquireEngineOwner(lockPath)
	if err != nil {
		t.Fatalf("lock after Stop: %v", err)
	}
	_ = owner.Close()
	for range h.PanelEvents() {
	}
	for range h.PanelHealth() {
	}
}

// TestEngineHost_StopKeepsTheLockWhileADeliveryIsInFlight: when drain
// reports a conductor delivery still being sent, Stop returns but the lock
// stays held until that delivery returns, so a standby cannot type into the
// same pane next to it (review on #2638).
func TestEngineHost_StopKeepsTheLockWhileADeliveryIsInFlight(t *testing.T) {
	env := newHostTestEnv(t, "hostinflight")
	lockPath, err := EngineLockPath(env.profile)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := env.host(t)
	h.Start()
	if !h.IsOwner() {
		t.Fatal("host did not take the engine")
	}
	delivering := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		h.Stop(func() <-chan struct{} { return delivering })
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop waited for the delivery in flight instead of returning")
	}
	_, err = AcquireEngineOwner(lockPath)
	var running *AlreadyRunningError
	if !errors.As(err, &running) || running.PID != os.Getpid() {
		t.Fatalf("lock while a delivery is in flight: %v, want it still held by pid %d", err, os.Getpid())
	}

	close(delivering)
	deadline := time.Now().Add(5 * time.Second)
	for {
		owner, err := AcquireEngineOwner(lockPath)
		if err == nil {
			_ = owner.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock still held after the delivery returned: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestEngineHost_StandbyStopsWithoutOwning: a host that never won stops
// promptly, closes its panel channels and leaves the owner's lock alone.
func TestEngineHost_StandbyStopsWithoutOwning(t *testing.T) {
	env := newHostTestEnv(t, "hoststandby")
	lockPath, err := EngineLockPath(env.profile)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireEngineOwner(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	h, _ := env.host(t)
	h.Start()
	time.Sleep(100 * time.Millisecond) // several retries
	if h.IsOwner() || h.Engine() != nil {
		t.Fatal("host took the engine while another owner holds the lock")
	}
	if env.listening() {
		t.Fatal("a standby host bound the webhook port")
	}
	stopped := make(chan struct{})
	go func() {
		h.Stop(nil)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop of a standby host did not return")
	}
	if _, ok := <-h.PanelEvents(); ok {
		t.Fatal("panel events not closed")
	}
	if _, ok := <-h.PanelHealth(); ok {
		t.Fatal("panel health not closed")
	}
	_, err = AcquireEngineOwner(lockPath)
	var running *AlreadyRunningError
	if !errors.As(err, &running) || running.PID != os.Getpid() {
		t.Fatalf("owner lock after the standby stopped: %v, want still held by pid %d", err, os.Getpid())
	}
}

// TestEngineHost_IdleWithoutARunningWatcher: a process with no running
// watcher does not hold the lock, so it cannot keep another process of the
// profile from running a watcher started later; once one runs, the next
// retry starts the engine (review on #2638).
func TestEngineHost_IdleWithoutARunningWatcher(t *testing.T) {
	env := newHostTestEnv(t, "hostidle")
	if err := env.db.UpdateWatcherStatus("w-hook", "stopped"); err != nil {
		t.Fatal(err)
	}
	lockPath, err := EngineLockPath(env.profile)
	if err != nil {
		t.Fatal(err)
	}
	h, got := env.host(t)
	h.Start()
	time.Sleep(100 * time.Millisecond) // several retries
	if h.IsOwner() || h.Engine() != nil {
		t.Fatal("a host with no running watcher took the engine")
	}
	owner, err := AcquireEngineOwner(lockPath)
	if err != nil {
		t.Fatalf("lock while the only host has nothing to run: %v, want it free", err)
	}
	_ = owner.Close()

	if err := env.db.UpdateWatcherStatus("w-hook", "running"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.Engine() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the host did not start the engine once a watcher was running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	env.post(t, "first-watcher")
	waitDelivered(t, got, "first-watcher")
}

// TestEngineHost_GivesTheLockBackWhenNoEngineStarts: when the engine does
// not start (its watchers were stopped after the check, or Start failed), the
// host releases the lock it took instead of keeping it with nothing to run,
// and starts the engine on a later retry.
func TestEngineHost_GivesTheLockBackWhenNoEngineStarts(t *testing.T) {
	env := newHostTestEnv(t, "hostnoengine")
	lockPath, err := EngineLockPath(env.profile)
	if err != nil {
		t.Fatal(err)
	}
	h, got := env.host(t)
	h.start = func(*statedb.StateDB, *slog.Logger) *Engine { return nil }
	h.Start()
	if h.IsOwner() || h.Engine() != nil {
		t.Fatal("host reports an engine although none started")
	}
	owner, err := AcquireEngineOwner(lockPath)
	if err != nil {
		t.Fatalf("lock after a failed engine start: %v, want it released", err)
	}
	_ = owner.Close()

	h.mu.Lock()
	h.start = startEngine
	h.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for h.Engine() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the host did not retry the engine start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	env.post(t, "after-retry")
	waitDelivered(t, got, "after-retry")
}

// TestEngineHost_StopIsIdempotent: quit and a signal can both stop the host.
func TestEngineHost_StopIsIdempotent(t *testing.T) {
	env := newHostTestEnv(t, "hostidem")
	h, _ := env.host(t)
	h.Start()
	drains := 0
	done := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			h.Stop(func() <-chan struct{} { drains++; return nil })
			done <- struct{}{}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Stop did not return")
		}
	}
	if drains != 1 {
		t.Fatalf("drain ran %d times, want 1", drains)
	}
	if h.IsOwner() || h.Engine() != nil {
		t.Fatal("host still reports an engine after Stop")
	}
}
