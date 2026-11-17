package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeRemoteRunner answers one negotiated read the way SSHRunner.RunReadIO
// does: stdout from the remote command, and an error when it exits non-zero.
type fakeRemoteRunner struct {
	args   []string
	stdout string
	err    error
	// started and hold, when set, park the read until hold closes or the
	// request's deadline passes.
	started chan struct{}
	hold    chan struct{}
}

func (f *fakeRemoteRunner) RunReadIO(ctx context.Context, _ io.Reader, stdout, _ io.Writer, unsupported []byte, _ bool, args ...string) error {
	if f.hold != nil {
		select {
		case f.started <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-f.hold:
			_, err := io.WriteString(stdout, f.stdout)
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.args = args
	if f.stdout == "unsupported" {
		_, _ = stdout.Write(unsupported)
		return errors.New("exit status 1")
	}
	if _, err := io.WriteString(stdout, f.stdout); err != nil {
		return err
	}
	return f.err
}

// setLocalRecall points HOME at a fresh config with [recall] enabled set.
func setLocalRecall(t *testing.T, on bool) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	cfgPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("[recall]\nenabled = %t\n", on)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newRemoteTimelineServer(t *testing.T, runner *fakeRemoteRunner, online bool) *Server {
	t.Helper()
	setLocalRecall(t, true)
	srv := NewServer(Config{RemoteFleet: &fakeRemoteFleetLoader{snapshot: session.RemoteFleetSnapshot{
		ObservedAt: time.Now(),
		Remotes:    []session.RemoteFleetRemote{{Name: "mac", Online: online, Sessions: []session.RemoteSessionInfo{{ID: "remote-1", Tool: "claude"}, {ID: "codex-1", Tool: "codex"}}}},
	}}})
	srv.remoteTimelineRunner = func(name string) (remoteTimelineRunner, error) {
		if name != "mac" {
			return nil, errRemoteNotConfigured
		}
		return runner, nil
	}
	return srv
}

func getRemoteTimeline(srv *Server, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, path, nil))
	return rr
}

func remoteTimelineAnswer(t *testing.T, id string) string {
	t.Helper()
	b, err := json.Marshal(query.RowsTimeline{
		Schema: query.RowsSchema, Source: "native",
		Session:       query.RowsSession{ID: id, Harness: "claude"},
		Turns:         []query.Row{{ID: "u1", Kind: "user", Body: "remote question"}, {ID: "a1", Kind: "assistant", Body: "remote answer"}},
		ThroughCursor: "dGhyb3VnaA", BeforeCursor: "YmVmb3Jl",
		Status: &query.LiveStatus{SessionID: id, Running: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// The route runs the remote's `recall timeline <id> --json` with the
// request's paging and answers its rows v2 object, minus the pane status.
func TestRemoteSessionTimeline_RunsRecallTimelineOnTheRemote(t *testing.T) {
	for _, tc := range []struct {
		query string
		args  string
	}{
		{"tail=60", "recall timeline --json --tail=60 remote-1"},
		{"since=dGhyb3VnaA", "recall timeline --json --since=dGhyb3VnaA remote-1"},
		{"before=YmVmb3Jl&limit=60", "recall timeline --json --limit=60 --before=YmVmb3Jl remote-1"},
		{"tail=5&agent=agent-1", "recall timeline --json --tail=5 --agent=agent-1 remote-1"},
		{"since=--json", "recall timeline --json --since=--json remote-1"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			runner := &fakeRemoteRunner{stdout: remoteTimelineAnswer(t, "remote-1")}
			rr := getRemoteTimeline(newRemoteTimelineServer(t, runner, true), "/api/remotes/mac/sessions/remote-1/timeline?"+tc.query)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			if got := strings.Join(runner.args, " "); got != tc.args {
				t.Fatalf("remote command = %q, want %q", got, tc.args)
			}
			var tl query.RowsTimeline
			if err := json.Unmarshal(rr.Body.Bytes(), &tl); err != nil {
				t.Fatal(err)
			}
			if tl.Schema != query.RowsSchema || tl.Session.ID != "remote-1" || len(tl.Turns) != 2 || tl.Turns[1].Body != "remote answer" || tl.ThroughCursor != "dGhyb3VnaA" {
				t.Fatalf("timeline = %+v", tl)
			}
			if tl.Status != nil || strings.Contains(rr.Body.String(), `"status"`) {
				t.Fatalf("pane status passed through: %s", rr.Body.String())
			}
			if rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", rr.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestRemoteSessionTimeline_Errors(t *testing.T) {
	for _, tc := range []struct {
		name, path, stdout string
		runErr             bool
		offline            bool
		status             int
		code               string
	}{
		{name: "remote not in the fleet", path: "/api/remotes/other/sessions/remote-1/timeline", status: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "session not on the remote", path: "/api/remotes/mac/sessions/remote-2/timeline", status: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "id that reads as an option", path: "/api/remotes/mac/sessions/-rf/timeline", status: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "unknown action", path: "/api/remotes/mac/sessions/remote-1/output", status: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "remote offline", path: "/api/remotes/mac/sessions/remote-1/timeline", offline: true, status: http.StatusServiceUnavailable, code: "REMOTE_OFFLINE"},
		{name: "cursor with a shell character", path: "/api/remotes/mac/sessions/remote-1/timeline?since=a%3Bb", status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "before without limit", path: "/api/remotes/mac/sessions/remote-1/timeline?before=YmVmb3Jl", status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "agent with a path", path: "/api/remotes/mac/sessions/remote-1/timeline?agent=..%2Fx", status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "agent on a Codex session", path: "/api/remotes/mac/sessions/codex-1/timeline?agent=agent-1", stdout: `{"success":false,"error":"recall rows: --agent is supported for Claude Code sidechains only","code":"INVALID_OPERATION"}`, runErr: true, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "remote without recall timeline", path: "/api/remotes/mac/sessions/remote-1/timeline", stdout: "unsupported", status: http.StatusNotImplemented, code: "NOT_IMPLEMENTED"},
		{name: "recall off on the remote", path: "/api/remotes/mac/sessions/remote-1/timeline", stdout: `{"success":false,"error":"recall is off: set [recall] enabled = true in config.toml (docs/recall.md); hints and 'session annotate' work without it","code":"INVALID_OPERATION"}`, runErr: true, status: http.StatusServiceUnavailable, code: "RECALL_DISABLED"},
		{name: "stale cursor", path: "/api/remotes/mac/sessions/remote-1/timeline?since=dGhyb3VnaA", stdout: `{"success":false,"error":"recall rows: resync required (source_moved)","code":"RESYNC_REQUIRED"}`, runErr: true, status: http.StatusConflict, code: "RESYNC_REQUIRED"},
		{name: "no transcript on the remote", path: "/api/remotes/mac/sessions/remote-1/timeline", stdout: `{"success":false,"error":"open /home/someone/.claude/projects/p/s.jsonl: no such file or directory","code":"NOT_FOUND"}`, runErr: true, status: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "ssh failure", path: "/api/remotes/mac/sessions/remote-1/timeline", runErr: true, status: http.StatusBadGateway, code: "INTERNAL_ERROR"},
		{name: "answer for another session", path: "/api/remotes/mac/sessions/remote-1/timeline", stdout: `{"schema":"agent-deck.recall.rows/v2","session":{"id":"remote-9"},"turns":[]}`, status: http.StatusBadGateway, code: "INTERNAL_ERROR"},
		{name: "answer that is not a timeline", path: "/api/remotes/mac/sessions/remote-1/timeline", stdout: "hello", status: http.StatusBadGateway, code: "INTERNAL_ERROR"},
		{name: "answer too large", path: "/api/remotes/mac/sessions/remote-1/timeline", stdout: strings.Repeat("x", remoteTimelineMaxBytes+1), status: http.StatusBadGateway, code: "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRemoteRunner{stdout: tc.stdout}
			if tc.runErr {
				runner.err = errors.New("exit status 1")
			}
			rr := getRemoteTimeline(newRemoteTimelineServer(t, runner, !tc.offline), tc.path)
			wantAPIError(t, rr, tc.status, tc.code)
			if strings.Contains(rr.Body.String(), "/home/") {
				t.Fatalf("a remote path reached the client: %s", rr.Body.String())
			}
		})
	}
}

func TestRemoteSessionTimeline_MethodAndToken(t *testing.T) {
	srv := newRemoteTimelineServer(t, &fakeRemoteRunner{}, true)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodPost, "/api/remotes/mac/sessions/remote-1/timeline", nil))
	wantAPIError(t, rr, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")

	srv = NewServer(Config{Token: "secret", RemoteFleet: &fakeRemoteFleetLoader{}})
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, "/api/remotes/mac/sessions/remote-1/timeline", nil))
	wantAPIError(t, rr, http.StatusUnauthorized, "UNAUTHORIZED")
}

// Each remote serves a few reads at once. The next one is refused with 429
// instead of opening another SSH connection, and a slot frees when a read ends.
func TestRemoteSessionTimeline_InFlightCap(t *testing.T) {
	runner := &fakeRemoteRunner{stdout: remoteTimelineAnswer(t, "remote-1"), started: make(chan struct{}), hold: make(chan struct{})}
	srv := newRemoteTimelineServer(t, runner, true)
	path := "/api/remotes/mac/sessions/remote-1/timeline"
	done := make(chan *httptest.ResponseRecorder, remoteTimelineInFlight)
	for i := 0; i < remoteTimelineInFlight; i++ {
		go func() { done <- getRemoteTimeline(srv, path) }()
		<-runner.started
	}
	rr := getRemoteTimeline(srv, path)
	wantAPIError(t, rr, http.StatusTooManyRequests, "RATE_LIMITED")
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	close(runner.hold)
	for i := 0; i < remoteTimelineInFlight; i++ {
		if rr := <-done; rr.Code != http.StatusOK {
			t.Fatalf("held read: %d %s", rr.Code, rr.Body.String())
		}
	}
	runner.hold, runner.started = nil, nil
	if rr := getRemoteTimeline(srv, path); rr.Code != http.StatusOK {
		t.Fatalf("after the reads ended: %d %s", rr.Code, rr.Body.String())
	}
}

// A read that runs past its deadline is a 504, so a client can tell slow
// from broken.
func TestRemoteSessionTimeline_Timeout(t *testing.T) {
	old := remoteTimelineTimeout
	remoteTimelineTimeout = 50 * time.Millisecond
	t.Cleanup(func() { remoteTimelineTimeout = old })
	runner := &fakeRemoteRunner{started: make(chan struct{}, 1), hold: make(chan struct{})}
	rr := getRemoteTimeline(newRemoteTimelineServer(t, runner, true), "/api/remotes/mac/sessions/remote-1/timeline")
	wantAPIError(t, rr, http.StatusGatewayTimeout, "INTERNAL_ERROR")
}

// Before the first fleet scan a remote session is not known yet, which is
// not the same as gone.
func TestRemoteSessionTimeline_BeforeFirstScan(t *testing.T) {
	setLocalRecall(t, true)
	srv := NewServer(Config{RemoteFleet: &fakeRemoteFleetLoader{}})
	srv.remoteTimelineRunner = func(string) (remoteTimelineRunner, error) { return &fakeRemoteRunner{}, nil }
	rr := getRemoteTimeline(srv, "/api/remotes/mac/sessions/remote-1/timeline")
	wantAPIError(t, rr, http.StatusServiceUnavailable, "REMOTE_OFFLINE")
}

// Recall off on this machine closes the route too, before any SSH: turning
// it off means no transcript content goes out through this server.
func TestRemoteSessionTimeline_LocalRecallOff(t *testing.T) {
	runner := &fakeRemoteRunner{stdout: "{}"}
	srv := newRemoteTimelineServer(t, runner, true)
	setLocalRecall(t, false)
	rr := getRemoteTimeline(srv, "/api/remotes/mac/sessions/remote-1/timeline")
	wantAPIError(t, rr, http.StatusServiceUnavailable, "RECALL_DISABLED")
	if runner.args != nil {
		t.Fatalf("the remote was asked: %v", runner.args)
	}
}

// A remote whose scan failed before it ever listed sessions is offline, so a
// saved chat on it answers 503, not 404.
func TestRemoteSessionTimeline_OfflineWithoutSessions(t *testing.T) {
	setLocalRecall(t, true)
	srv := NewServer(Config{RemoteFleet: &fakeRemoteFleetLoader{snapshot: session.RemoteFleetSnapshot{
		ObservedAt: time.Now(),
		Remotes:    []session.RemoteFleetRemote{{Name: "mac", Issue: "unavailable", Sessions: []session.RemoteSessionInfo{}}},
	}}})
	runner := &fakeRemoteRunner{}
	srv.remoteTimelineRunner = func(string) (remoteTimelineRunner, error) { return runner, nil }
	wantAPIError(t, getRemoteTimeline(srv, "/api/remotes/mac/sessions/remote-1/timeline"), http.StatusServiceUnavailable, "REMOTE_OFFLINE")
	if runner.args != nil {
		t.Fatalf("the remote was asked: %v", runner.args)
	}
}
