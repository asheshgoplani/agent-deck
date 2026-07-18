package session

// Tests for SSE-based OpenCode status tracking (issue #1614).

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sseTestServer serves a /session/status snapshot and an /event stream fed
// from a channel, mimicking OpenCode's TUI server with --port.
func sseTestServer(t *testing.T, snapshot string, events <-chan string) (*httptest.Server, int) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/session/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, snapshot)
	})
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer is not a flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
		flusher.Flush()
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", ev)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().(*net.TCPAddr)
	return srv, addr.Port
}

// waitForStatus polls the watcher until the instance's derived status matches
// want or the timeout expires.
func waitForStatus(t *testing.T, w *OpenCodeSSEWatcher, instanceID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := w.GetStatus(instanceID); s != nil && s.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := "<nil>"
	if s := w.GetStatus(instanceID); s != nil {
		got = s.Status
	}
	t.Fatalf("timed out waiting for status %q, got %q", want, got)
}

// TestOpenCodeSSEWatcher_StatusTransitions drives the full stream lifecycle:
// busy -> running, idle -> waiting, and subagent aggregation (an idle event
// for one session does not flip the instance to waiting while another session
// on the same server is still busy).
func TestOpenCodeSSEWatcher_StatusTransitions(t *testing.T) {
	events := make(chan string, 8)
	_, port := sseTestServer(t, "{}", events)

	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{{InstanceID: "inst-1", Port: port}})

	// Empty snapshot + connection banner only: no derived status yet, so the
	// tmux fallback stays authoritative until a real transition arrives.
	time.Sleep(100 * time.Millisecond)
	if s := w.GetStatus("inst-1"); s != nil {
		t.Fatalf("expected no status before first session event, got %+v", s)
	}

	events <- `{"type":"session.status","properties":{"sessionID":"ses_A","status":{"type":"busy"}}}`
	waitForStatus(t, w, "inst-1", "running")

	// A second (sub)session goes busy, then idle: instance must stay running
	// because ses_A is still busy.
	events <- `{"type":"session.status","properties":{"sessionID":"ses_B","status":{"type":"busy"}}}`
	events <- `{"type":"session.status","properties":{"sessionID":"ses_B","status":{"type":"idle"}}}`
	time.Sleep(100 * time.Millisecond)
	waitForStatus(t, w, "inst-1", "running")

	// retry keeps the session working (running), not waiting.
	events <- `{"type":"session.status","properties":{"sessionID":"ses_A","status":{"type":"retry","attempt":1,"message":"overloaded","next":123}}}`
	time.Sleep(50 * time.Millisecond)
	waitForStatus(t, w, "inst-1", "running")

	// Legacy session.idle event ends the turn -> waiting.
	events <- `{"type":"session.idle","properties":{"sessionID":"ses_A"}}`
	waitForStatus(t, w, "inst-1", "waiting")

	// Sync with an empty target set tears the connection down and drops state.
	w.Sync(nil)
	if s := w.GetStatus("inst-1"); s != nil {
		t.Fatalf("expected status cleared after Sync(nil), got %+v", s)
	}
}

// TestOpenCodeSSEWatcher_SnapshotSeedsBusy verifies that connecting mid-turn
// derives running from the /session/status snapshot without waiting for the
// next transition event.
func TestOpenCodeSSEWatcher_SnapshotSeedsBusy(t *testing.T) {
	events := make(chan string, 1)
	_, port := sseTestServer(t, `{"ses_A":{"type":"busy"}}`, events)

	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{{InstanceID: "inst-1", Port: port}})
	waitForStatus(t, w, "inst-1", "running")
}

// TestUpdateOpenCodeSSEStatus_FastPathMapping verifies the instance-side
// consumption: fresh SSE statuses map onto Running/Waiting, stale ones are
// ignored so control falls through to tmux polling.
func TestUpdateOpenCodeSSEStatus_FastPathMapping(t *testing.T) {
	inst := &Instance{Tool: "opencode"}

	inst.UpdateOpenCodeSSEStatus("running", time.Now())
	if inst.sseStatus != "running" {
		t.Fatalf("sseStatus = %q, want running", inst.sseStatus)
	}
	if time.Since(inst.sseLastUpdate) >= opencodeSSEFreshnessWindow {
		t.Fatal("fresh update should be inside the freshness window")
	}

	inst.UpdateOpenCodeSSEStatus("waiting", time.Now())
	if inst.sseStatus != "waiting" {
		t.Fatalf("sseStatus = %q, want waiting", inst.sseStatus)
	}

	// Empty status is a no-op guard.
	inst.UpdateOpenCodeSSEStatus("", time.Now())
	if inst.sseStatus != "waiting" {
		t.Fatalf("empty status must not clobber, got %q", inst.sseStatus)
	}

	// A stale timestamp must fall outside the freshness window.
	stale := time.Now().Add(-2 * opencodeSSEFreshnessWindow)
	inst.UpdateOpenCodeSSEStatus("running", stale)
	if time.Since(inst.sseLastUpdate) < opencodeSSEFreshnessWindow {
		t.Fatal("stale update should be outside the freshness window")
	}
}

// TestBuildOpenCodeCommand_SSEPortFlag verifies --port injection on fresh and
// resume launches, absence on custom-command passthrough, and the
// [opencode].disable_sse_status escape hatch.
func TestBuildOpenCodeCommand_SSEPortFlag(t *testing.T) {
	fresh := &Instance{Tool: "opencode"}
	cmd := fresh.buildOpenCodeCommand("opencode")
	if !strings.Contains(cmd, " --port ") {
		t.Fatalf("fresh launch missing --port: %q", cmd)
	}
	if fresh.OpenCodePort <= 0 {
		t.Fatalf("OpenCodePort not recorded, got %d", fresh.OpenCodePort)
	}
	if !strings.Contains(cmd, fmt.Sprintf(" --port %d", fresh.OpenCodePort)) {
		t.Fatalf("command port and OpenCodePort disagree: %q vs %d", cmd, fresh.OpenCodePort)
	}

	resume := &Instance{Tool: "opencode", OpenCodeSessionID: "ses_ABC123"}
	cmd = resume.buildOpenCodeCommand("opencode")
	if !strings.Contains(cmd, "-s ses_ABC123") || !strings.Contains(cmd, " --port ") {
		t.Fatalf("resume launch missing -s or --port: %q", cmd)
	}

	// Custom commands pass through untouched (user controls their own flags),
	// and a stale port from a prior launch is cleared so the SSE watcher can
	// never attach to a freed port reused by an unrelated process.
	custom := &Instance{Tool: "opencode", OpenCodePort: 999}
	cmd = custom.buildOpenCodeCommand("opencode --model gpt-4")
	if strings.Contains(cmd, "--port") {
		t.Fatalf("custom command must not gain --port: %q", cmd)
	}
	if custom.OpenCodePort != 0 {
		t.Fatalf("custom command must clear stale port, got %d", custom.OpenCodePort)
	}
}

// TestBuildOpenCodeSSEPortFlag_Disabled verifies the config escape hatch.
// The package TestMain isolates HOME, so the effective config path is a
// sandboxed temp home — writing it never touches a real user config.
func TestBuildOpenCodeSSEPortFlag_Disabled(t *testing.T) {
	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if _, statErr := os.Stat(configPath); statErr == nil {
		t.Fatalf("sandboxed config %s already exists; refusing to clobber", configPath)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("[opencode]\ndisable_sse_status = true\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Remove(configPath)
		ClearUserConfigCache()
	})

	inst := &Instance{Tool: "opencode", OpenCodePort: 4242}
	cmd := inst.buildOpenCodeCommand("opencode")
	if strings.Contains(cmd, "--port") {
		t.Fatalf("disable_sse_status must suppress --port: %q", cmd)
	}
	if inst.OpenCodePort != 0 {
		t.Fatalf("disable_sse_status must clear stored port, got %d", inst.OpenCodePort)
	}
}
