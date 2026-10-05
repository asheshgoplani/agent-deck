package session

// Tests for SSE status on OpenCode 2.x through the shared background service
// (issue #2511, item 3). The event shapes are recorded from a live 2.0.20
// service (`GET /api/event`, `GET /api/session/active`).

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	sharedTestPassword = "test-service-password"
	sesBound           = "ses_ef69e5361ffe6z2VXZPEe5SNjC"
	sesOther           = "ses_efe0f548dffeM3VeHMyLk2EjBF"
)

// sharedServiceEvent renders one 2.x event exactly as the service frames it.
func sharedServiceEvent(eventType, sessionID string) string {
	return fmt.Sprintf(`{"id":"evt_test","created":1791160634685,"type":%q,"data":{"sessionID":%q},"durable":{"aggregateID":%q,"seq":1,"version":1}}`,
		eventType, sessionID, sessionID)
}

// sharedServiceServer mimics the 2.x background service: basic auth on every
// /api route, a session.active snapshot, and one /api/event stream per client
// fed from events (each subscriber gets every event, as on the real service).
func sharedServiceServer(t *testing.T, snapshot string) (*httptest.Server, func(string)) {
	t.Helper()
	type sub struct{ ch chan string }
	subs := make(chan sub, 16)
	var clients []sub
	broadcast := make(chan string, 64)
	go func() {
		for {
			select {
			case s := <-subs:
				clients = append(clients, s)
			case ev, ok := <-broadcast:
				if !ok {
					return
				}
				for _, c := range clients {
					c.ch <- ev
				}
			}
		}
	}()
	authorized := func(r *http.Request) bool {
		user, pass, ok := r.BasicAuth()
		return ok && user == "opencode" && pass == sharedTestPassword
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session/active", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, snapshot)
	})
	mux.HandleFunc("/api/event", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"evt_c\",\"type\":\"server.connected\",\"data\":{}}\n\n: heartbeat\n\n")
		flusher.Flush()
		s := sub{ch: make(chan string, 64)}
		subs <- s
		for {
			select {
			case ev := <-s.ch:
				fmt.Fprintf(w, "data: %s\n\n", ev)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	return srv, func(ev string) { broadcast <- ev }
}

// useOpenCodeStub makes script the session's [opencode] command by absolute
// path (darwin's spawn-path prelude would otherwise put /opt/homebrew/bin
// ahead of PATH) and leaves PATH holding only the stub's directory.
func useOpenCodeStub(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write opencode stub: %v", err)
	}
	isolateOpenCodeConfig(t, path)
	t.Setenv("PATH", dir)
}

func sharedTarget(instanceID, sessionID, url string) SSETarget {
	return SSETarget{
		InstanceID: instanceID,
		sessionID:  sessionID,
		service: func() (openCodeService, error) {
			return openCodeService{URL: url, Password: sharedTestPassword}, nil
		},
	}
}

// waitConnected gives both streams time to subscribe before events flow.
func waitConnected() { time.Sleep(150 * time.Millisecond) }

// TestOpenCodeSSEWatcher_SharedServiceFiltersBoundSession: two decks bound to
// two sessions on the one shared stream. Each instance's status moves only on
// its own session's execution events.
func TestOpenCodeSSEWatcher_SharedServiceFiltersBoundSession(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{}}`)

	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{
		sharedTarget("inst-bound", sesBound, srv.URL),
		sharedTarget("inst-other", sesOther, srv.URL),
	})
	waitConnected()

	send(sharedServiceEvent("session.execution.started", sesOther))
	waitForStatus(t, w, "inst-other", "running")
	if s := w.GetStatus("inst-bound"); s != nil {
		t.Fatalf("another session's events must not set the bound instance's status, got %+v", s)
	}

	send(sharedServiceEvent("session.execution.started", sesBound))
	waitForStatus(t, w, "inst-bound", "running")

	send(sharedServiceEvent("session.execution.succeeded", sesOther))
	waitForStatus(t, w, "inst-other", "waiting")
	time.Sleep(50 * time.Millisecond)
	if s := w.GetStatus("inst-bound"); s == nil || s.Status != "running" {
		t.Fatalf("bound instance must stay running while only the other session finished, got %+v", s)
	}

	// A provider retry keeps the turn going.
	send(sharedServiceEvent("session.retry.scheduled", sesBound))
	time.Sleep(50 * time.Millisecond)
	waitForStatus(t, w, "inst-bound", "running")

	send(sharedServiceEvent("session.execution.succeeded", sesBound))
	waitForStatus(t, w, "inst-bound", "waiting")
}

// TestOpenCodeSSEWatcher_SharedServiceTurnEndings: failed and interrupted end
// a turn the same way succeeded does.
func TestOpenCodeSSEWatcher_SharedServiceTurnEndings(t *testing.T) {
	for _, ending := range []string{"session.execution.failed", "session.execution.interrupted"} {
		t.Run(ending, func(t *testing.T) {
			srv, send := sharedServiceServer(t, `{"data":{}}`)
			w := NewOpenCodeSSEWatcher(nil)
			defer w.Stop()
			w.Sync([]SSETarget{sharedTarget("inst-1", sesBound, srv.URL)})
			waitConnected()

			send(sharedServiceEvent("session.execution.started", sesBound))
			waitForStatus(t, w, "inst-1", "running")
			send(sharedServiceEvent(ending, sesBound))
			waitForStatus(t, w, "inst-1", "waiting")
		})
	}
}

// TestOpenCodeSSEWatcher_SharedServiceSnapshotSeedsBoundOnly: connecting
// mid-turn seeds running from session.active, but only for the bound session.
func TestOpenCodeSSEWatcher_SharedServiceSnapshotSeedsBoundOnly(t *testing.T) {
	srv, _ := sharedServiceServer(t, `{"data":{"`+sesOther+`":{"type":"running"}}}`)
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{
		sharedTarget("inst-bound", sesBound, srv.URL),
		sharedTarget("inst-other", sesOther, srv.URL),
	})
	waitForStatus(t, w, "inst-other", "running")
	if s := w.GetStatus("inst-bound"); s != nil {
		t.Fatalf("snapshot of another session must not seed the bound instance, got %+v", s)
	}
}

// TestOpenCodeSSEWatcher_SharedServiceFeedsInstanceFastPath: the derived
// status reaches the instance through the same feed the 1.x stream uses.
func TestOpenCodeSSEWatcher_SharedServiceFeedsInstanceFastPath(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{}}`)
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	inst := &Instance{ID: "inst-1", Tool: "opencode"}
	w.Sync([]SSETarget{sharedTarget(inst.ID, sesBound, srv.URL)})
	waitConnected()

	send(sharedServiceEvent("session.execution.started", sesOther))
	send(sharedServiceEvent("session.execution.started", sesBound))
	waitForStatus(t, w, inst.ID, "running")
	ss := w.GetStatus(inst.ID)
	inst.UpdateOpenCodeSSEStatus(ss.Status, ss.UpdatedAt)
	if inst.sseStatus != "running" {
		t.Fatalf("sseStatus = %q, want running", inst.sseStatus)
	}
}

// TestOpenCodeSSEWatcher_SharedServiceRetriesDiscovery: a service that is not
// up yet is looked up again on the reconnect backoff, never on the TUI tick.
func TestOpenCodeSSEWatcher_SharedServiceRetriesDiscovery(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{}}`)
	calls := make(chan struct{}, 8)
	target := SSETarget{
		InstanceID: "inst-1",
		sessionID:  sesBound,
		service: func() (openCodeService, error) {
			calls <- struct{}{}
			if len(calls) == 1 {
				return openCodeService{}, errors.New("service not running")
			}
			return openCodeService{URL: srv.URL, Password: sharedTestPassword}, nil
		},
	}
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{target})
	deadline := time.Now().Add(5 * time.Second)
	for len(calls) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(calls) < 2 {
		t.Fatalf("discovery not retried after a failure, calls = %d", len(calls))
	}
	waitConnected()
	send(sharedServiceEvent("session.execution.started", sesBound))
	waitForStatus(t, w, "inst-1", "running")
}

// TestOpenCodeSSEWatcher_SharedServiceRebindReconnects: a new bound session id
// for the same instance replaces the connection and drops the old status.
func TestOpenCodeSSEWatcher_SharedServiceRebindReconnects(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{}}`)
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{sharedTarget("inst-1", sesOther, srv.URL)})
	waitConnected()
	send(sharedServiceEvent("session.execution.started", sesOther))
	waitForStatus(t, w, "inst-1", "running")

	w.Sync([]SSETarget{sharedTarget("inst-1", sesBound, srv.URL)})
	if s := w.GetStatus("inst-1"); s != nil {
		t.Fatalf("rebinding must drop the old session's status, got %+v", s)
	}
	waitConnected()
	send(sharedServiceEvent("session.execution.succeeded", sesOther))
	send(sharedServiceEvent("session.execution.started", sesBound))
	waitForStatus(t, w, "inst-1", "running")
}

// TestOpenCodeSSETarget_V1Unchanged: on 1.x the target is the per-TUI --port
// server, exactly as before; no shared-service fields are set.
func TestOpenCodeSSETarget_V1Unchanged(t *testing.T) {
	pinOpenCodeMajorVersion(t, 1, true)
	inst := &Instance{ID: "inst-1", Tool: "opencode", OpenCodeSessionID: sesBound}
	cmd := inst.buildOpenCodeCommand("opencode")
	want := fmt.Sprintf("opencode -s %s --port %d", sesBound, inst.GetOpenCodePort())
	if !strings.HasSuffix(cmd, want) {
		t.Fatalf("1.x launch changed: %q, want suffix %q", cmd, want)
	}

	target, ok := inst.OpenCodeSSETarget()
	if !ok {
		t.Fatal("1.x instance with a port must have an SSE target")
	}
	if target.InstanceID != "inst-1" || target.Port != inst.GetOpenCodePort() || target.sessionID != "" || target.service != nil {
		t.Fatalf("1.x target must be port-only, got %+v", target)
	}

	// 1.x without a port (custom command, restored session) keeps tmux.
	noPort := &Instance{ID: "inst-2", Tool: "opencode", OpenCodeSessionID: sesBound}
	if target, ok := noPort.OpenCodeSSETarget(); ok {
		t.Fatalf("1.x without a port must keep the tmux fallback, got %+v", target)
	}
}

// TestOpenCodeSSETarget_V1FakeBinaryNeverAsksService: with a real 1.x binary
// on PATH the shared service is never queried.
func TestOpenCodeSSETarget_V1FakeBinaryNeverAsksService(t *testing.T) {
	useInstalledOpenCodeProbe(t)
	calls := filepath.Join(t.TempDir(), "calls")
	useOpenCodeStub(t, "#!/bin/sh\necho \"$*\" >> '"+calls+"'\necho '1.14.3'\n")

	inst := &Instance{ID: "inst-1", Tool: "opencode", OpenCodeSessionID: sesBound}
	if target, ok := inst.OpenCodeSSETarget(); ok {
		t.Fatalf("1.x without a port must not get a shared target, got %+v", target)
	}
	data, _ := os.ReadFile(calls)
	if strings.Contains(string(data), "service") {
		t.Fatalf("1.x must never run `opencode service`, calls:\n%s", data)
	}
}

func TestOpenCodeSSETarget_V2SharedService(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)

	bound := &Instance{ID: "inst-1", Tool: "opencode", OpenCodeSessionID: sesBound}
	target, ok := bound.OpenCodeSSETarget()
	if !ok {
		t.Fatal("2.x instance with a bound session must use the shared service")
	}
	if target.InstanceID != "inst-1" || target.Port != 0 || target.sessionID != sesBound || target.service == nil {
		t.Fatalf("unexpected 2.x target %+v", target)
	}

	unbound := &Instance{ID: "inst-2", Tool: "opencode"}
	if target, ok := unbound.OpenCodeSSETarget(); ok {
		t.Fatalf("2.x without a bound session must keep the tmux fallback, got %+v", target)
	}

	other := &Instance{ID: "inst-3", Tool: "claude", OpenCodeSessionID: sesBound}
	if target, ok := other.OpenCodeSSETarget(); ok {
		t.Fatalf("non-OpenCode tools have no SSE target, got %+v", target)
	}
}

func TestOpenCodeSSETarget_V2RespectsDisableSSEStatus(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	t.Setenv("HOME", t.TempDir())
	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("[opencode]\ndisable_sse_status = true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	inst := &Instance{ID: "inst-1", Tool: "opencode", OpenCodeSessionID: sesBound}
	if target, ok := inst.OpenCodeSSETarget(); ok {
		t.Fatalf("disable_sse_status must also disable the shared stream, got %+v", target)
	}
}

// TestResolveOpenCodeService: the URL comes from `opencode service status`,
// the password from the service's own registration file, and only when that
// file describes the same service.
func TestResolveOpenCodeService(t *testing.T) {
	useInstalledOpenCodeProbe(t)
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	writeRegistration := func(url string) {
		t.Helper()
		dir := filepath.Join(stateHome, "opencode")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		body := fmt.Sprintf(`{"id":"svc","version":"2.0.20","url":%q,"pid":50713,"password":%q}`, url, sharedTestPassword)
		if err := os.WriteFile(filepath.Join(dir, "service.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("write registration: %v", err)
		}
	}
	stub := func(statusOut string) {
		t.Helper()
		script := "#!/bin/sh\nif [ \"$1\" = service ] && [ \"$2\" = status ]; then printf '%s\\n' '" + statusOut + "'; exit 0; fi\necho 'opencode v2.0.20'\n"
		useOpenCodeStub(t, script)
	}
	inst := &Instance{ID: "inst-1", Tool: "opencode"}

	stub("http://127.0.0.1:49374")
	writeRegistration("http://127.0.0.1:49374")
	svc, err := inst.resolveOpenCodeService()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if svc.URL != "http://127.0.0.1:49374" || svc.Password != sharedTestPassword {
		t.Fatalf("resolve = {URL:%q, password set:%v}", svc.URL, svc.Password != "")
	}

	// A registration for another service never lends its password.
	writeRegistration("http://127.0.0.1:50000")
	svc, err = inst.resolveOpenCodeService()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if svc.Password != "" {
		t.Fatal("password from a registration for a different URL must not be used")
	}

	// The password is only ever sent to this machine.
	stub("http://example.com:49374")
	if _, err := inst.resolveOpenCodeService(); err == nil {
		t.Fatal("a non-loopback service URL must be refused")
	}
	stub("no service running")
	if _, err := inst.resolveOpenCodeService(); err == nil {
		t.Fatal("output that is not a URL must be an error")
	}
}

// TestOpenCodeSSEWatcher_V1StreamIgnoresSharedEventTypes: the 1.x per-TUI
// stream keeps its own event vocabulary; 2.x names do not move it.
func TestOpenCodeSSEWatcher_V1StreamIgnoresSharedEventTypes(t *testing.T) {
	events := make(chan string, 4)
	_, port := sseTestServer(t, "{}", events)
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{{InstanceID: "inst-1", Port: port}})
	waitConnected()

	events <- sharedServiceEvent("session.execution.started", sesBound)
	time.Sleep(150 * time.Millisecond)
	if s := w.GetStatus("inst-1"); s != nil {
		t.Fatalf("1.x stream must not react to 2.x event names, got %+v", s)
	}
	events <- `{"type":"session.status","properties":{"sessionID":"ses_A","status":{"type":"busy"}}}`
	waitForStatus(t, w, "inst-1", "running")
}
