package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeRemoteRunner answers one negotiated read the way SSHRunner.RunReadIO
// does: stdout from the remote command, and an error when it exits non-zero.
type fakeRemoteRunner struct {
	args   []string
	stdout string
	err    error
}

func (f *fakeRemoteRunner) RunReadIO(_ context.Context, _ io.Reader, stdout, _ io.Writer, unsupported []byte, _ bool, args ...string) error {
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

func newRemoteTimelineServer(t *testing.T, runner *fakeRemoteRunner, online bool) *Server {
	t.Helper()
	srv := NewServer(Config{RemoteFleet: &fakeRemoteFleetLoader{snapshot: session.RemoteFleetSnapshot{
		Remotes: []session.RemoteFleetRemote{{Name: "mac", Online: online, Sessions: []session.RemoteSessionInfo{{ID: "remote-1", Tool: "claude"}, {ID: "codex-1", Tool: "codex"}}}},
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
		{name: "no transcript on the remote", path: "/api/remotes/mac/sessions/remote-1/timeline", stdout: `{"success":false,"error":"session remote has no native transcript on this machine yet","code":"NOT_FOUND"}`, runErr: true, status: http.StatusNotFound, code: "NOT_FOUND"},
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
