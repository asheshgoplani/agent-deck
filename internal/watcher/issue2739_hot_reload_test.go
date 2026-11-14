package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// #2739: the owner's engine applies started, stopped and edited watchers and
// clients.json routes while it runs, without restarting its process, and
// leaves the adapters of unchanged watchers alone.

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

func writeWebhookSource(t *testing.T, name, port string) {
	t.Helper()
	dir, err := session.WatcherNameDir(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Written whole (temporary file and rename), as an editor saves it.
	if err := writeFileAtomic(filepath.Join(dir, "watcher.toml"), []byte("[source]\nport = \""+port+"\"\n")); err != nil {
		t.Fatal(err)
	}
}

func writeClients(t *testing.T, clients map[string]ClientEntry) {
	t.Helper()
	dir, err := session.WatcherDir()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(clients)
	if err := writeFileAtomic(filepath.Join(dir, "clients.json"), b); err != nil {
		t.Fatal(err)
	}
}

func listeningOn(port string) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within 5s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// postFrom posts one event from sender to the webhook on port through client.
func postFrom(t *testing.T, client *http.Client, port, sender, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+port+"/webhook", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Webhook-Sender", sender)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post %q to :%s: %v", body, port, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook answered %d", resp.StatusCode)
	}
}

func TestEngineHost_StopsAWatcherStoppedWhileItRuns_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-stop")
	h, got := env.host(t)
	h.Start()
	env.post(t, "before stop")
	waitDelivered(t, got, "before stop")

	if err := env.db.UpdateWatcherStatus("w-hook", "stopped"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the stopped webhook watcher gives its port back", func() bool { return !env.listening() })
	if h.Engine() == nil || !h.IsOwner() {
		t.Fatal("the host gave its engine up; it should keep running it for watchers started later")
	}
}

// A watcher started while the engine runs gets its adapter, and the adapter
// of the watcher that did not change keeps its connections: it was not
// restarted.
func TestEngineHost_StartsAWatcherStartedWhileItRuns_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-start")
	saveTestWatcher(t, env.db, "w-two", "two", "webhook")
	if err := env.db.UpdateWatcherStatus("w-two", "stopped"); err != nil {
		t.Fatal(err)
	}
	port2 := freeLoopbackPort(t)
	writeWebhookSource(t, "two", port2)

	h, got := env.host(t)
	h.Start()
	keepAlive := &http.Client{Timeout: 2 * time.Second}
	defer keepAlive.CloseIdleConnections()
	// Start returns before the webhook's Listen goroutine binds its port, and
	// postFrom makes one attempt: wait for the port first.
	eventually(t, "the webhook watcher listens", env.listening)
	postFrom(t, keepAlive, env.port, "alice@example.com", "first")
	waitDelivered(t, got, "first")
	if listeningOn(port2) {
		t.Fatal("the stopped watcher listens before it was started")
	}

	if err := env.db.UpdateWatcherStatus("w-two", "running"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the started watcher listens", func() bool { return listeningOn(port2) })
	postFrom(t, &http.Client{Timeout: 2 * time.Second}, port2, "alice@example.com", "to two")
	waitDelivered(t, got, "to two")

	var reused bool
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+env.port+"/webhook", strings.NewReader("second"))
	req.Header.Set("X-Webhook-Sender", "alice@example.com")
	resp, err := keepAlive.Do(req)
	if err != nil {
		t.Fatalf("the unchanged watcher stopped accepting: %v", err)
	}
	_ = resp.Body.Close()
	waitDelivered(t, got, "second")
	if !reused {
		t.Error("the unchanged watcher's connection was closed: its adapter was restarted")
	}
}

func TestEngineHost_RestartsAWatcherWhoseSourceChanged_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-source")
	h, got := env.host(t)
	h.Start()
	env.post(t, "old port")
	waitDelivered(t, got, "old port")

	newPort := freeLoopbackPort(t)
	writeWebhookSource(t, "hook", newPort)
	eventually(t, "the edited watcher listens on its new port", func() bool { return listeningOn(newPort) })
	eventually(t, "the edited watcher leaves its old port", func() bool { return !env.listening() })
	postFrom(t, &http.Client{Timeout: 2 * time.Second}, newPort, "alice@example.com", "new port")
	waitDelivered(t, got, "new port")
}

func TestEngineHost_ReloadsChangedRoutes_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-routes")
	h, got := env.host(t)
	h.Start()
	env.post(t, "alice")
	waitDelivered(t, got, "alice")

	writeClients(t, map[string]ClientEntry{
		"alice@example.com": {Conductor: "demo", Name: "Alice"},
		"bob@example.com":   {Conductor: "demo", Name: "Bob"},
	})
	eventually(t, "the new route reaches the router", func() bool {
		eng := h.Engine()
		return eng != nil && eng.cfg.Router.Match("bob@example.com") != nil
	})
	postFrom(t, &http.Client{Timeout: 2 * time.Second}, env.port, "bob@example.com", "from bob")
	evt := waitDelivered(t, got, "from bob")
	if evt.RoutedTo != "demo" {
		t.Errorf("bob's event routed to %q, want demo", evt.RoutedTo)
	}
}

// `watcher reload` writes a request; the owner answers it with what it did,
// and retries an adapter that is not running even though its settings did
// not change.
func TestEngineHost_AnswersAReloadRequest_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-request")
	// Something else holds the webhook's port: its Listen fails at start.
	squatter, err := net.Listen("tcp", "127.0.0.1:"+env.port)
	if err != nil {
		t.Fatal(err)
	}
	h, got := env.host(t)
	h.Start()
	eventually(t, "the adapter's Listen ends", func() bool {
		eng := h.Engine()
		if eng == nil {
			return false
		}
		a := eng.Adapters()
		return len(a) == 1 && !a[0].Running
	})
	_ = squatter.Close()

	token, err := RequestReload(env.profile)
	if err != nil {
		t.Fatal(err)
	}
	var rep *ReloadReport
	eventually(t, "the owner answers the reload", func() bool {
		rep, _ = ReadReloadReport(env.profile)
		return rep != nil && rep.Request == token
	})
	if rep.PID != os.Getpid() || len(rep.Restarted) != 1 || rep.Restarted[0] != "hook" || len(rep.Failed) != 0 {
		t.Fatalf("report = %+v, want hook restarted by this process", rep)
	}
	env.post(t, "after reload")
	waitDelivered(t, got, "after reload")

	// The same request is answered once; a new one again.
	token2, err := RequestReload(env.profile)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the owner answers the second reload", func() bool {
		rep, _ = ReadReloadReport(env.profile)
		return rep != nil && rep.Request == token2
	})
	if rep.Unchanged != 1 || rep.Changed() {
		t.Fatalf("second report = %+v, want nothing changed and hook unchanged", rep)
	}
}

func TestEngineOwnerPID_ProbesWithoutTakingTheLock_2739(t *testing.T) {
	const profile = "hotreload-probe"
	if _, running, err := EngineOwnerPID(profile); err != nil || running {
		t.Fatalf("EngineOwnerPID before any owner = %v, %v; want not running", running, err)
	}
	path, err := EngineLockPath(profile)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireEngineOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, running, err := EngineOwnerPID(profile)
	if err != nil || !running || pid != os.Getpid() {
		t.Fatalf("EngineOwnerPID with an owner = %d, %v, %v; want this pid running", pid, running, err)
	}
	_ = owner.Close()
	if _, running, _ := EngineOwnerPID(profile); running {
		t.Fatal("EngineOwnerPID reports an owner after it released the lock")
	}
	// The probe left the lock free for the next owner.
	next, err := AcquireEngineOwner(path)
	if err != nil {
		t.Fatalf("the lock is not free after the probe: %v", err)
	}
	_ = next.Close()
}

// flushOnStopAdapter is a webhook-like adapter: what it accepted is still in
// its channel when it is stopped, and it hands that on as Listen returns.
type flushOnStopAdapter struct {
	n         int
	teardowns atomic.Int32
}

func (a *flushOnStopAdapter) Setup(context.Context, AdapterConfig) error { return nil }
func (a *flushOnStopAdapter) Listen(ctx context.Context, events chan<- Event) error {
	<-ctx.Done()
	base := time.Now()
	for i := 0; i < a.n; i++ {
		events <- Event{Source: "mock", Sender: "s@example.com", Subject: fmt.Sprintf("accepted %d", i), Timestamp: base.Add(time.Duration(i))}
	}
	return nil
}
func (a *flushOnStopAdapter) Teardown() error    { a.teardowns.Add(1); return nil }
func (a *flushOnStopAdapter) HealthCheck() error { return nil }

// Removing one adapter from a running engine is not a stop: the events it had
// already accepted reach the writer and are stored. The other adapter keeps
// running, and Stop does not tear the removed adapter down a second time.
func TestEngine_RemoveAdapterKeepsAcceptedEvents_2739(t *testing.T) {
	engine, db := newTestEngine(t, nil)
	saveTestWatcher(t, db, "w1", "flusher", "mock")
	saveTestWatcher(t, db, "w2", "other", "mock")
	other := &MockAdapter{}
	engine.RegisterAdapter("w2", other, AdapterConfig{Type: "mock", Name: "other"}, 60)
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop()

	flusher := &flushOnStopAdapter{n: 20}
	if err := engine.AddAdapter("w1", flusher, AdapterConfig{Type: "mock", Name: "flusher"}, 60); err != nil {
		t.Fatalf("AddAdapter: %v", err)
	}
	if err := engine.AddAdapter("w1", &flushOnStopAdapter{}, AdapterConfig{Type: "mock", Name: "flusher"}, 60); err == nil {
		t.Fatal("AddAdapter accepted a second adapter for the same watcher")
	}
	if !engine.RemoveAdapter("w1") {
		t.Fatal("RemoveAdapter found no adapter")
	}
	if engine.RemoveAdapter("w1") {
		t.Fatal("RemoveAdapter removed the adapter twice")
	}
	eventually(t, "all 20 accepted events are stored", func() bool {
		rows, err := db.LoadWatcherEvents("w1", 100)
		return err == nil && len(rows) == 20
	})
	if n := flusher.teardowns.Load(); n != 1 {
		t.Errorf("Teardown ran %d times on removal, want 1", n)
	}
	if a := engine.Adapters(); len(a) != 1 || a[0].WatcherID != "w2" || !a[0].Running {
		t.Errorf("Adapters() = %+v, want only w2, running", a)
	}
	if other.teardownCalled {
		t.Error("removing w1 tore down w2")
	}

	engine.Stop()
	if n := flusher.teardowns.Load(); n != 1 {
		t.Errorf("Teardown ran %d times after Stop, want still 1", n)
	}
	if err := engine.AddAdapter("w3", &MockAdapter{}, AdapterConfig{Type: "mock", Name: "late"}, 60); err != ErrEngineStopped {
		t.Errorf("AddAdapter after Stop = %v, want ErrEngineStopped", err)
	}
}

// reload requests a reload of env's engine and returns the answer.
func (e *hostTestEnv) reload(t *testing.T) *ReloadReport {
	t.Helper()
	token, err := RequestReload(e.profile)
	if err != nil {
		t.Fatal(err)
	}
	var rep *ReloadReport
	eventually(t, "the owner answers the reload", func() bool {
		rep, _ = ReadReloadReport(e.profile)
		return rep != nil && rep.Request == token
	})
	return rep
}

func writeRawWatcherFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := writeFileAtomic(path, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

// A clients.json that does not parse keeps the routes, and every reload
// reports it until it is fixed, not only the first pass after the edit.
func TestEngineHost_BrokenClientsJSONKeepsRoutesAndIsReported_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-badclients")
	h, got := env.host(t)
	h.Start()
	dir, err := session.WatcherDir()
	if err != nil {
		t.Fatal(err)
	}
	writeRawWatcherFile(t, filepath.Join(dir, "clients.json"), "{not json")
	time.Sleep(100 * time.Millisecond) // several automatic passes see it first

	for i := 0; i < 2; i++ {
		if rep := env.reload(t); rep.RoutesError == "" {
			t.Fatalf("reload %d: report %+v does not report the broken clients.json", i, rep)
		}
	}
	env.post(t, "still routed")
	if evt := waitDelivered(t, got, "still routed"); evt.RoutedTo != "demo" {
		t.Fatalf("event routed to %q after a broken clients.json, want demo (routes kept)", evt.RoutedTo)
	}

	// An empty file is one being written: nothing changes, nothing is reported.
	writeRawWatcherFile(t, filepath.Join(dir, "clients.json"), "")
	if rep := env.reload(t); rep.RoutesReloaded {
		t.Fatalf("an empty clients.json reloaded the routes: %+v", rep)
	}
	env.post(t, "after empty")
	if evt := waitDelivered(t, got, "after empty"); evt.RoutedTo != "demo" {
		t.Fatalf("event routed to %q after an empty clients.json, want demo", evt.RoutedTo)
	}
}

// A watcher.toml that does not parse, or is empty, leaves the adapter running
// with its settings instead of restarting it with none.
func TestEngineHost_UnreadableSourceKeepsTheAdapter_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-badsource")
	h, got := env.host(t)
	h.Start()
	env.post(t, "before")
	waitDelivered(t, got, "before")
	dir, err := session.WatcherNameDir("hook")
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"[source\nport = ", ""} {
		writeRawWatcherFile(t, filepath.Join(dir, "watcher.toml"), content)
		time.Sleep(100 * time.Millisecond)
		rep := env.reload(t)
		if len(rep.Failed) != 1 || !strings.HasPrefix(rep.Failed[0], "hook: ") || len(rep.Restarted) != 0 {
			t.Fatalf("watcher.toml %q: report %+v, want hook failed and not restarted", content, rep)
		}
		env.post(t, "still on "+env.port)
		waitDelivered(t, got, "still on "+env.port)
	}
}

// A webhook whose new port is busy fails in Listen, after Setup: the reload
// reports it failed, not restarted.
func TestEngineHost_ReportsAWebhookThatCannotBind_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-busy")
	h, _ := env.host(t)
	h.Start()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	writeWebhookSource(t, "hook", strconv.Itoa(busy.Addr().(*net.TCPAddr).Port))

	rep := env.reload(t)
	if len(rep.Failed) != 1 || !strings.HasPrefix(rep.Failed[0], "hook: ") || len(rep.Restarted) != 0 {
		t.Fatalf("report %+v, want hook failed (its port is busy)", rep)
	}
}

// Two webhooks that swap ports in one edit both end up listening after one
// automatic pass: every adapter is removed before any is added.
func TestEngineHost_SwapsPortsBetweenTwoWebhooks_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-swap")
	saveTestWatcher(t, env.db, "w-two", "two", "webhook")
	port2 := freeLoopbackPort(t)
	writeWebhookSource(t, "two", port2)
	// No ticks: the test runs the one pass itself.
	h := NewEngineHost(HostConfig{
		Profile:         env.profile,
		DB:              env.db,
		SpawnSendWorker: env.spawned.spawn,
		RetryInterval:   time.Hour,
	})
	t.Cleanup(func() { h.Stop(nil) })
	h.Start()
	eventually(t, "both listen", func() bool { return env.listening() && listeningOn(port2) })

	writeWebhookSource(t, "hook", port2)
	writeWebhookSource(t, "two", env.port)
	h.reconcile()
	a := h.Engine().Adapters()
	if len(a) != 2 || !a[0].Running || !a[1].Running {
		t.Fatalf("adapters after the swap = %+v, want both running", a)
	}
	if !env.listening() || !listeningOn(port2) {
		t.Fatal("a swapped port is not listening")
	}
}

// The health loop runs next to adapters being added and removed (-race).
func TestEngine_HealthLoopWithAddAndRemove_2739(t *testing.T) {
	db := newTestDB(t)
	engine := NewEngine(EngineConfig{
		DB:                  db,
		Router:              NewRouter(nil),
		MaxEventsPerWatcher: 500,
		HealthCheckInterval: time.Millisecond,
		TriageSpawner:       &fakeSpawner{},
		TriageDir:           t.TempDir(),
		ClientsPath:         filepath.Join(t.TempDir(), "clients.json"),
	})
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop()
	go func() {
		for range engine.HealthCh() {
		}
	}()
	for i := 0; i < 50; i++ {
		id := "w" + strconv.Itoa(i%3)
		engine.RemoveAdapter(id)
		if err := engine.AddAdapter(id, &MockAdapter{}, AdapterConfig{Type: "mock", Name: id}, 60); err != nil {
			t.Fatal(err)
		}
		_ = engine.Adapters()
		time.Sleep(time.Millisecond)
	}
}

// A new owner does not replay a reload request an earlier owner already
// answered: the request file stays after the answer.
func TestEngineHost_TakeoverDoesNotReplayAnAnsweredReload_2739(t *testing.T) {
	env := newHostTestEnv(t, "hotreload-replay")
	first, _ := env.host(t)
	first.Start()
	answered := env.reload(t)
	first.Stop(nil)

	second, _ := env.host(t)
	second.Start()
	if !second.IsOwner() {
		t.Fatal("the second host did not take the engine over")
	}
	time.Sleep(200 * time.Millisecond) // ten passes of the new owner
	rep, err := ReadReloadReport(env.profile)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.StartedAt.Equal(answered.StartedAt) {
		t.Fatalf("the new owner answered the old request again: %+v, was %+v", rep, answered)
	}
}
