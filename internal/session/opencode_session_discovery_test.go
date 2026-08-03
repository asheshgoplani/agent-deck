package session

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func setFakeOpenCodePath(t *testing.T, script string, includeSystemPath bool) {
	t.Helper()
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	pathValue := fakeBin
	if includeSystemPath && os.Getenv("PATH") != "" {
		pathValue += string(os.PathListSeparator) + os.Getenv("PATH")
	}
	t.Setenv("PATH", pathValue)
}

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
			{"id":"ses_NEW","location":{"directory":%q},"time":{"created":3000,"updated":4000}},
			{"id":"ses_OTHER","directory":"/another/project","time":{"created":5000,"updated":6000}}
		]`, projectPath, projectPath)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	setFakeOpenCodePath(t, "#!/bin/sh\nprintf '[]'\n", false)

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
	script := fmt.Sprintf("#!/bin/sh\nprintf x > %q\nprintf '[]'\n", marker)
	setFakeOpenCodePath(t, script, false)

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

func TestQueryOpenCodeSession_ManagedPortRefusesRedirect(t *testing.T) {
	projectPath := t.TempDir()
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedRequests.Add(1)
		fmt.Fprintf(w, `[{"id":"ses_REDIRECTED","directory":%q,"time":{"created":1000,"updated":2000}}]`, projectPath)
	}))
	t.Cleanup(target.Close)

	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	t.Cleanup(redirect.Close)
	cliMarker := filepath.Join(t.TempDir(), "cli-invoked")
	setFakeOpenCodePath(t, fmt.Sprintf("#!/bin/sh\nprintf x > %q\nprintf '[]'\n", cliMarker), false)
	inst := &Instance{
		Tool:         "opencode",
		ProjectPath:  projectPath,
		OpenCodePort: redirect.Listener.Addr().(*net.TCPAddr).Port,
	}

	if got := inst.queryOpenCodeSession(); got != "" {
		t.Fatalf("redirected query session ID = %q, want empty", got)
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("redirect target request count = %d, want 0", got)
	}
	if _, err := os.Stat(cliMarker); !os.IsNotExist(err) {
		t.Fatalf("redirect invoked CLI; marker stat error = %v", err)
	}
}

func TestQueryOpenCodeSession_SnapshotsBindingWhileHTTPIsInFlight(t *testing.T) {
	projectPath := t.TempDir()
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseResponse
		fmt.Fprintf(w, `[
			{"id":"ses_OLD","directory":%q,"time":{"created":1000,"updated":2000}},
			{"id":"ses_NEW","directory":%q,"time":{"created":3000,"updated":4000}}
		]`, projectPath, projectPath)
	}))
	t.Cleanup(server.Close)

	inst := &Instance{
		Tool:              "opencode",
		ProjectPath:       projectPath,
		OpenCodePort:      server.Listener.Addr().(*net.TCPAddr).Port,
		OpenCodeSessionID: "ses_OLD",
	}
	result := make(chan string, 1)
	go func() { result <- inst.queryOpenCodeSession() }()

	select {
	case <-requestStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP query did not start")
	}
	inst.setOpenCodeSession("ses_NEW")
	close(releaseResponse)

	if got := <-result; got != "ses_OLD" {
		t.Fatalf("query result = %q, want snapshotted binding ses_OLD", got)
	}
}

func TestQueryOpenCodeSessionsHTTP_RejectsOversizedResponse(t *testing.T) {
	const oversizedPayload = (8 << 20) + 1024
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"id":%q,"directory":"/project","time":{"created":1000,"updated":2000}}]`, strings.Repeat("x", oversizedPayload))
	}))
	t.Cleanup(server.Close)

	inst := &Instance{Tool: "opencode", ProjectPath: "/project"}
	port := server.Listener.Addr().(*net.TCPAddr).Port
	if _, err := inst.queryOpenCodeSessionsHTTP(port, inst.ProjectPath); err == nil {
		t.Fatal("oversized OpenCode session response unexpectedly succeeded")
	}
}

func TestQueryOpenCodeSession_NoManagedPortRateLimitsCLIFallback(t *testing.T) {
	projectPath := t.TempDir()
	payload := fmt.Sprintf(`[{"id":"ses_COMPAT","directory":%q,"created":1000,"updated":2000}]`, projectPath)

	marker := filepath.Join(t.TempDir(), "cli-invocations")
	script := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\nprintf '%%s\\n' %q\n", marker, payload)
	setFakeOpenCodePath(t, script, false)

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
	payload := fmt.Sprintf(`[{"id":"ses_COALESCED","directory":%q,"created":1000,"updated":2000}]`, projectPath)

	marker := filepath.Join(t.TempDir(), "cli-invocations")
	script := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\nsleep 1\nprintf '%%s\\n' %q\n", marker, payload)
	setFakeOpenCodePath(t, script, true)

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
