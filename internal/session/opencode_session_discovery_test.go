package session

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestUpdateOpenCodeSession_ManagedPortUsesHTTPNestedTimes(t *testing.T) {
	projectPath := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("directory"); got != projectPath {
			t.Errorf("directory query = %q, want %q", got, projectPath)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[
			{"id":"ses_OLD","directory":%q,"time":{"created":1000,"updated":2000}},
			{"id":"ses_NEW","directory":%q,"time":{"created":3000,"updated":4000}},
			{"id":"ses_OTHER","directory":"/another/project","time":{"created":5000,"updated":6000}}
		]`, projectPath, projectPath)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// The pre-fix implementation invokes the CLI even with a managed port.
	// Keep that RED path hermetic: it must never reach the installed OpenCode.
	fakeBin := t.TempDir()
	fakeOpenCode := filepath.Join(fakeBin, "opencode")
	if err := os.WriteFile(fakeOpenCode, []byte("#!/bin/sh\nprintf '[]'\n"), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	inst := &Instance{
		Tool:         "opencode",
		ProjectPath:  projectPath,
		OpenCodePort: server.Listener.Addr().(*net.TCPAddr).Port,
	}
	inst.UpdateOpenCodeSession()

	if got := inst.OpenCodeSessionID; got != "ses_NEW" {
		t.Fatalf("OpenCodeSessionID = %q, want %q", got, "ses_NEW")
	}
}

func TestQueryOpenCodeSession_ManagedPortRetriesHTTPAfterFailure(t *testing.T) {
	projectPath := t.TempDir()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "warming up", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"id":"ses_READY","directory":%q,"time":{"created":1000,"updated":2000}}]`, projectPath)
	}))
	t.Cleanup(server.Close)

	marker := filepath.Join(t.TempDir(), "cli-invoked")
	fakeBin := t.TempDir()
	fakeOpenCode := filepath.Join(fakeBin, "opencode")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\nprintf '[]'\n", marker)
	if err := os.WriteFile(fakeOpenCode, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	inst := &Instance{
		Tool:         "opencode",
		ProjectPath:  projectPath,
		OpenCodePort: server.Listener.Addr().(*net.TCPAddr).Port,
	}
	if got := inst.queryOpenCodeSession(); got != "" {
		t.Fatalf("first query session ID = %q after HTTP failure, want empty", got)
	}
	if got := inst.queryOpenCodeSession(); got != "ses_READY" {
		t.Fatalf("retry session ID = %q, want ses_READY", got)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("HTTP request count = %d, want 2", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("managed-port retry invoked CLI; marker stat error = %v", err)
	}
}

func TestQueryOpenCodeSession_NoManagedPortRateLimitsCLIFallback(t *testing.T) {
	projectPath := t.TempDir()
	payload, err := json.Marshal([]openCodeSessionMetadata{{
		ID:        "ses_COMPAT",
		Directory: projectPath,
		Created:   1000,
		Updated:   2000,
	}})
	if err != nil {
		t.Fatalf("marshal fake CLI response: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "cli-invocations")
	fakeBin := t.TempDir()
	fakeOpenCode := filepath.Join(fakeBin, "opencode")
	script := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\nprintf '%%s\\n' %q\n", marker, string(payload))
	if err := os.WriteFile(fakeOpenCode, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath}
	for attempt := 0; attempt < 2; attempt++ {
		if got := inst.queryOpenCodeSession(); got != "ses_COMPAT" {
			t.Fatalf("attempt %d session ID = %q, want ses_COMPAT", attempt+1, got)
		}
	}

	invocations, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read CLI invocation marker: %v", err)
	}
	if got := len(invocations); got != 1 {
		t.Fatalf("CLI invocation count = %d, want 1 within scan interval", got)
	}
}

func TestQueryOpenCodeSession_NoManagedPortCoalescesConcurrentCLIFallback(t *testing.T) {
	projectPath := t.TempDir()
	payload, err := json.Marshal([]openCodeSessionMetadata{{
		ID:        "ses_COALESCED",
		Directory: projectPath,
		Created:   1000,
		Updated:   2000,
	}})
	if err != nil {
		t.Fatalf("marshal fake CLI response: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "cli-invocations")
	fakeBin := t.TempDir()
	fakeOpenCode := filepath.Join(fakeBin, "opencode")
	script := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\n/bin/sleep 1\nprintf '%%s\\n' %q\n", marker, string(payload))
	if err := os.WriteFile(fakeOpenCode, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath}
	const callers = 8
	start := make(chan struct{})
	results := make(chan string, callers)
	for range callers {
		go func() {
			<-start
			results <- inst.queryOpenCodeSession()
		}()
	}
	close(start)

	for range callers {
		if got := <-results; got != "ses_COALESCED" {
			t.Fatalf("session ID = %q, want ses_COALESCED", got)
		}
	}

	invocations, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read CLI invocation marker: %v", err)
	}
	if got := len(invocations); got != 1 {
		t.Fatalf("concurrent CLI invocation count = %d, want 1", got)
	}
}
