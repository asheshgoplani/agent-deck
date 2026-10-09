package session

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// fixtureSpawnedAt predates every fixture session, so the 2.x adoption guard
// (created after spawn) admits them all.
const fixtureSpawnedAt int64 = 1

// configureFakeOpenCode points [opencode].command at the stub on PATH: 2.x
// discovery resolves the configured binary with the spawn-path dirs first, so
// a bare name could reach the host's real opencode.
func configureFakeOpenCode(t *testing.T) {
	t.Helper()
	stub, err := exec.LookPath("opencode")
	if err != nil {
		t.Fatalf("look up fake opencode: %v", err)
	}
	isolateOpenCodeConfig(t, stub)
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

func TestQueryOpenCodeSession_V2QueriesSharedServiceAndSkipsChildren(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	payload := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_ROOT","parentID":null,"location":{"directory":%q},"time":{"created":1000,"updated":2000}},`+
		`{"id":"ses_CHILD","parentID":"ses_ROOT","location":{"directory":%q},"time":{"created":3000,"updated":4000}},`+
		`{"id":"ses_OTHER","location":{"directory":"/another/project"},"time":{"created":5000,"updated":6000}}`+
		`],"cursor":{"previous":null,"next":null}}`, projectPath, projectPath)

	argv := filepath.Join(t.TempDir(), "argv")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nprintf '%%s\\n' %q\n", argv, payload)
	setFakeOpenCodePath(t, script, false)
	configureFakeOpenCode(t)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeStartedAt: fixtureSpawnedAt}
	if got := inst.queryOpenCodeSession(); got != "ses_ROOT" {
		t.Fatalf("session ID = %q, want ses_ROOT", got)
	}

	gotArgv, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("read argv marker: %v", err)
	}
	want := "api\nsession.list\n--param\ndirectory=" + projectPath + "\n--param\nlimit=" +
		strconv.Itoa(openCodeServiceSessionPageSize) + "\n"
	if string(gotArgv) != want {
		t.Fatalf("opencode argv =\n%s\nwant\n%s", gotArgv, want)
	}
}

func TestQueryOpenCodeSession_V1KeepsSessionListCommand(t *testing.T) {
	pinOpenCodeMajorVersion(t, 1, true)
	projectPath := t.TempDir()
	payload := fmt.Sprintf(`[{"id":"ses_V1","directory":%q,"created":1000,"updated":2000}]`, projectPath)

	argv := filepath.Join(t.TempDir(), "argv")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nprintf '%%s\\n' %q\n", argv, payload)
	setFakeOpenCodePath(t, script, false)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath}
	if got := inst.queryOpenCodeSession(); got != "ses_V1" {
		t.Fatalf("session ID = %q, want ses_V1", got)
	}

	gotArgv, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("read argv marker: %v", err)
	}
	if want := "session\nlist\n--format\njson\n"; string(gotArgv) != want {
		t.Fatalf("opencode argv =\n%s\nwant\n%s", gotArgv, want)
	}
}

// setFakeOpenCodeServicePages serves page1 on a cursor-less call and page2 when
// the argv carries cursor=c1, recording every argv under dir/argv.N. Builtins
// only: the fake PATH holds nothing but the stub.
func setFakeOpenCodeServicePages(t *testing.T, page1, page2 string) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\n"+
		"n=0; read -r n < %[1]q/count 2>/dev/null; n=$((n+1)); echo $n > %[1]q/count\n"+
		"printf '%%s\\n' \"$@\" > %[1]q/argv.$n\n"+
		"case \"$*\" in *cursor=c1*) printf '%%s\\n' %[3]q;; *) printf '%%s\\n' %[2]q;; esac\n",
		dir, page1, page2)
	setFakeOpenCodePath(t, script, false)
	configureFakeOpenCode(t)
	return dir
}

func fakeOpenCodeCallCount(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "count"))
	if err != nil {
		t.Fatalf("read call count: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse call count %q: %v", raw, err)
	}
	return n
}

func TestQueryOpenCodeSession_V2FollowsCursorPastChildOnlyPage(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	page1 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_CHILD1","parentID":"ses_ROOT","location":{"directory":%q},"time":{"created":5000,"updated":6000}},`+
		`{"id":"ses_CHILD2","parentID":"ses_ROOT","location":{"directory":%q},"time":{"created":3000,"updated":4000}}`+
		`],"cursor":{"previous":null,"next":"c1"}}`, projectPath, projectPath)
	page2 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_ROOT","parentID":null,"location":{"directory":%q},"time":{"created":1000,"updated":2000}}`+
		`],"cursor":{"previous":"p1","next":"c2"}}`, projectPath)
	dir := setFakeOpenCodeServicePages(t, page1, page2)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeStartedAt: fixtureSpawnedAt}
	if got := inst.queryOpenCodeSession(); got != "ses_ROOT" {
		t.Fatalf("session ID = %q, want ses_ROOT", got)
	}
	if got := fakeOpenCodeCallCount(t, dir); got != 2 {
		t.Fatalf("opencode call count = %d, want 2 (stop once a root is in hand)", got)
	}
	gotArgv, err := os.ReadFile(filepath.Join(dir, "argv.2"))
	if err != nil {
		t.Fatalf("read second argv: %v", err)
	}
	if !strings.Contains(string(gotArgv), "--param\ncursor=c1\n") {
		t.Fatalf("second call argv =\n%s\nwant a cursor=c1 param", gotArgv)
	}
}

func TestQueryOpenCodeSession_V2KeepsBoundSessionFoundOnLaterPage(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	page1 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_NEWER","parentID":null,"location":{"directory":%q},"time":{"created":5000,"updated":6000}}`+
		`],"cursor":{"previous":null,"next":"c1"}}`, projectPath)
	page2 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_BOUND","parentID":null,"location":{"directory":%q},"time":{"created":1000,"updated":2000}}`+
		`],"cursor":{"previous":"p1","next":null}}`, projectPath)
	dir := setFakeOpenCodeServicePages(t, page1, page2)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeSessionID: "ses_BOUND"}
	if got := inst.queryOpenCodeSession(); got != "ses_BOUND" {
		t.Fatalf("session ID = %q, want ses_BOUND", got)
	}
	if got := fakeOpenCodeCallCount(t, dir); got != 2 {
		t.Fatalf("opencode call count = %d, want 2 (page past the newer sibling)", got)
	}
}

func TestQueryOpenCodeSession_V2BoundInstanceSkipsUnboundCachedPage(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	page1 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_NEWER","parentID":null,"location":{"directory":%q},"time":{"created":5000,"updated":6000}}`+
		`],"cursor":{"previous":null,"next":"c1"}}`, projectPath)
	page2 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_BOUND","parentID":null,"location":{"directory":%q},"time":{"created":1000,"updated":2000}}`+
		`],"cursor":{"previous":"p1","next":null}}`, projectPath)
	setFakeOpenCodeServicePages(t, page1, page2)

	unbound := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeStartedAt: fixtureSpawnedAt}
	if got := unbound.queryOpenCodeSession(); got != "ses_NEWER" {
		t.Fatalf("unbound session ID = %q, want ses_NEWER", got)
	}
	bound := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeSessionID: "ses_BOUND"}
	if got := bound.queryOpenCodeSession(); got != "ses_BOUND" {
		t.Fatalf("bound session ID = %q, want ses_BOUND (unbound page-1 result reused)", got)
	}
}

func TestQueryOpenCodeSession_V2DropsResultWhenPageCapHitsBeforeBoundSession(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	page1 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_SIB1","parentID":null,"location":{"directory":%q},"time":{"created":5000,"updated":6000}}`+
		`],"cursor":{"previous":null,"next":"c1"}}`, projectPath)
	page2 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_SIB2","parentID":null,"location":{"directory":%q},"time":{"created":3000,"updated":4000}}`+
		`],"cursor":{"previous":"p1","next":"c1"}}`, projectPath)
	dir := setFakeOpenCodeServicePages(t, page1, page2)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeSessionID: "ses_BOUND"}
	if got := inst.queryOpenCodeSession(); got != "" {
		t.Fatalf("session ID = %q, want empty (bound session may sit past the page cap)", got)
	}
	if got := fakeOpenCodeCallCount(t, dir); got != openCodeServiceSessionMaxPages {
		t.Fatalf("opencode call count = %d, want %d", got, openCodeServiceSessionMaxPages)
	}
}

func TestQueryOpenCodeSession_V2RunsConfiguredBinary(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	page := func(id string) string {
		return fmt.Sprintf(`{"data":[`+
			`{"id":%q,"parentID":null,"location":{"directory":%q},"time":{"created":1000,"updated":2000}}`+
			`],"cursor":{"previous":null,"next":null}}`, id, projectPath)
	}
	commandDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", page("ses_CONFIGURED"))
	if err := os.WriteFile(filepath.Join(commandDir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatalf("write configured opencode: %v", err)
	}
	isolateOpenCodeConfig(t, "PATH="+commandDir+":$PATH opencode")
	setFakeOpenCodePath(t, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", page("ses_PROCESS_PATH")), false)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeStartedAt: fixtureSpawnedAt}
	if got := inst.queryOpenCodeSession(); got != "ses_CONFIGURED" {
		t.Fatalf("session ID = %q, want ses_CONFIGURED (discovery must run the configured binary)", got)
	}
}

func TestQueryOpenCodeSession_V2UnboundIgnoresSiblingUpdatedAfterSpawn(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	startedAt := time.Now().UnixMilli()
	// A sibling deck session keeps this conversation busy, so its updated time
	// is newer than our spawn even though it was created long before it.
	payload := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_SIBLING","location":{"directory":%q},"time":{"created":%d,"updated":%d}}`+
		`],"cursor":{"previous":null,"next":null}}`, projectPath, startedAt-3_600_000, startedAt+60_000)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", payload)
	setFakeOpenCodePath(t, script, false)
	configureFakeOpenCode(t)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeStartedAt: startedAt}
	if got := inst.queryOpenCodeSession(); got != "" {
		t.Fatalf("unbound 2.x instance adopted a sibling's conversation: %q", got)
	}
}

func TestQueryOpenCodeSession_V2UnboundAdoptsSessionCreatedAfterSpawn(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	startedAt := time.Now().UnixMilli()
	payload := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_SIBLING","location":{"directory":%q},"time":{"created":%d,"updated":%d}},`+
		`{"id":"ses_MINE","location":{"directory":%q},"time":{"created":%d,"updated":%d}}`+
		`],"cursor":{"previous":null,"next":null}}`, projectPath, startedAt-3_600_000, startedAt+120_000, projectPath, startedAt+1_000, startedAt+2_000)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", payload)
	setFakeOpenCodePath(t, script, false)
	configureFakeOpenCode(t)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeStartedAt: startedAt}
	if got := inst.queryOpenCodeSession(); got != "ses_MINE" {
		t.Fatalf("session ID = %q, want ses_MINE", got)
	}
}

func TestQueryOpenCodeSession_V2RestoredUnboundUsesLastStartedAt(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	lastStarted := time.Now().Add(-time.Minute)
	startedAt := lastStarted.UnixMilli()
	payload := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_SIBLING","location":{"directory":%q},"time":{"created":%d,"updated":%d}},`+
		`{"id":"ses_MINE","location":{"directory":%q},"time":{"created":%d,"updated":%d}}`+
		`],"cursor":{"previous":null,"next":null}}`, projectPath, startedAt-3_600_000, startedAt+120_000, projectPath, startedAt+1_000, startedAt+2_000)
	setFakeOpenCodePath(t, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", payload), false)
	configureFakeOpenCode(t)

	// OpenCodeStartedAt is not persisted, so a reloaded instance only has LastStartedAt.
	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, LastStartedAt: lastStarted}
	if got := inst.queryOpenCodeSession(); got != "ses_MINE" {
		t.Fatalf("session ID = %q, want ses_MINE", got)
	}
}

func TestQueryOpenCodeSession_V2UnboundWithoutStartTimeStaysUnbound(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	now := time.Now().UnixMilli()
	payload := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_SIBLING","location":{"directory":%q},"time":{"created":%d,"updated":%d}}`+
		`],"cursor":{"previous":null,"next":null}}`, projectPath, now-3_600_000, now)
	setFakeOpenCodePath(t, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", payload), false)
	configureFakeOpenCode(t)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath}
	if got := inst.queryOpenCodeSession(); got != "" {
		t.Fatalf("unbound 2.x instance with no start time adopted %q", got)
	}
}

func TestQueryOpenCodeSession_V2UnboundPagesPastRootsCreatedBeforeSpawn(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	startedAt := time.Now().UnixMilli()
	page1 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_SIBLING","parentID":null,"location":{"directory":%q},"time":{"created":%d,"updated":%d}}`+
		`],"cursor":{"previous":null,"next":"c1"}}`, projectPath, startedAt-3_600_000, startedAt+120_000)
	page2 := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_MINE","parentID":null,"location":{"directory":%q},"time":{"created":%d,"updated":%d}}`+
		`],"cursor":{"previous":"p1","next":null}}`, projectPath, startedAt+1_000, startedAt+2_000)
	dir := setFakeOpenCodeServicePages(t, page1, page2)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeStartedAt: startedAt}
	if got := inst.queryOpenCodeSession(); got != "ses_MINE" {
		t.Fatalf("session ID = %q, want ses_MINE", got)
	}
	if got := fakeOpenCodeCallCount(t, dir); got != 2 {
		t.Fatalf("opencode call count = %d, want 2 (page past roots created before spawn)", got)
	}
}

// On 2.x the shared service lists every deck session's conversation in the
// directory, so recent local activity is no evidence that a newer sibling
// belongs to this instance.
func TestFindBestOpenCodeSession_V2BoundKeepsSessionOverNewerSibling(t *testing.T) {
	const now int64 = 200000
	sessions := []openCodeSessionMetadata{
		{ID: "ses_ours", Directory: "/project", Created: 100000, Updated: 190000},
		{ID: "ses_other", Directory: "/project", Created: 100000, Updated: 200000},
	}
	if got := findBestOpenCodeSession(sessions, "/project", "ses_ours", 100000, now, true); got != "ses_ours" {
		t.Fatalf("bound 2.x instance adopted a sibling: got %q, want ses_ours", got)
	}
}

func TestQueryOpenCodeSession_V2BoundMissingSessionDoesNotAdoptSibling(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	page := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_SIBLING","parentID":null,"location":{"directory":%q},"time":{"created":5000,"updated":6000}}`+
		`],"cursor":{"previous":null,"next":null}}`, projectPath)
	setFakeOpenCodeServicePages(t, page, page)

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeSessionID: "ses_GONE"}
	if got := inst.queryOpenCodeSession(); got != "" {
		t.Fatalf("bound 2.x instance whose session is gone adopted a sibling: %q", got)
	}
}
