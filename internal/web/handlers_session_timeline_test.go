package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// timelineFixture is a web server whose only session writes a native
// transcript of the given harness, with recall on or off.
type timelineFixture struct {
	srv  *Server
	inst *session.Instance
	path string
}

func newTimelineFixture(t *testing.T, harness string, recall bool) *timelineFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	if recall {
		cfgPath, err := session.GetUserConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, []byte("[recall]\nenabled = true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	inst := session.NewInstanceWithTool("timeline", t.TempDir(), harness)
	inst.ID = "timeline-session"
	var path string
	switch harness {
	case "claude":
		inst.ClaudeSessionID = "conversation-1"
		path = session.ClaudeTranscriptPathForInstance(inst)
	case "codex":
		inst.CodexSessionID = "thread-timeline"
		path = filepath.Join(home, ".codex", "sessions", "2026", "11", "15", "rollout-2026-11-15T00-00-00-thread-timeline.jsonl")
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	service := NewSessionDataService("")
	service.openStorage = func(string) (storageLoader, error) {
		return &fakeStorage{instances: []*session.Instance{inst}}, nil
	}
	srv.menuData = service
	return &timelineFixture{srv: srv, inst: inst, path: path}
}

// appendTurns writes n user/assistant pairs, numbered from first.
func (f *timelineFixture) appendTurns(t *testing.T, first, n int) {
	t.Helper()
	var b strings.Builder
	for i := first; i < first+n; i++ {
		for _, role := range []string{"user", "assistant"} {
			id, text := fmt.Sprintf("%s-%d", role, i), fmt.Sprintf("%s turn %d", role, i)
			if f.inst.Tool == "claude" {
				fmt.Fprintf(&b, "{\"type\":%q,\"uuid\":%q,\"sessionId\":\"conversation-1\",\"message\":{\"role\":%q,\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n", role, id, role, text)
			} else {
				fmt.Fprintf(&b, "{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":%q,\"role\":%q,\"content\":[{\"type\":\"input_text\",\"text\":%q}]}}\n", id, role, text)
			}
		}
	}
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(b.String()); err != nil {
		t.Fatal(err)
	}
}

func (f *timelineFixture) get(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	target := "/api/sessions/" + f.inst.ID + "/timeline"
	if query != "" {
		target += "?" + query
	}
	f.srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, target, nil))
	return rr
}

func decodeTimeline(t *testing.T, rr *httptest.ResponseRecorder) query.RowsTimeline {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var tl query.RowsTimeline
	if err := json.Unmarshal(rr.Body.Bytes(), &tl); err != nil {
		t.Fatalf("decode: %v: %s", err, rr.Body.String())
	}
	return tl
}

func rowTexts(rows []query.Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Body)
	}
	return out
}

// The route answers what `recall timeline <id> --json` prints for the
// same session: the rows v2 object from query.ReadRows, paged with the
// CLI's tail, since, before and limit.
func TestSessionTimeline_MatchesRecallTimeline(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			f := newTimelineFixture(t, harness, true)
			f.appendTurns(t, 0, 10)

			tail := decodeTimeline(t, f.get(t, "tail=4"))
			want, err := query.ReadRows(context.Background(), query.RowsSource{Harness: harness, Path: f.path}, query.RowsOptions{Tail: 4})
			if err != nil {
				t.Fatal(err)
			}
			if got, exp := rowTexts(tail.Turns), rowTexts(want.Turns); strings.Join(got, "|") != strings.Join(exp, "|") || len(got) != 4 {
				t.Fatalf("tail rows = %q, want %q", got, exp)
			}
			if tail.Schema != query.RowsSchema || tail.Source != "native" || tail.Session.ID != f.inst.ID || tail.Session.Harness != harness || tail.Session.Path != f.path {
				t.Fatalf("envelope = %+v", tail)
			}
			if tail.ThroughCursor == "" || tail.BeforeCursor == "" {
				t.Fatalf("cursors missing: through=%q before=%q", tail.ThroughCursor, tail.BeforeCursor)
			}

			older := decodeTimeline(t, f.get(t, "limit=4&before="+url.QueryEscape(tail.BeforeCursor)))
			if got := rowTexts(older.Turns); len(got) != 4 || got[3] != "assistant turn 7" {
				t.Fatalf("older page = %q", got)
			}

			f.appendTurns(t, 10, 1)
			newer := decodeTimeline(t, f.get(t, "since="+url.QueryEscape(tail.ThroughCursor)))
			if got := rowTexts(newer.Turns); strings.Join(got, "|") != "user turn 10|assistant turn 10" {
				t.Fatalf("since rows = %q", got)
			}

			empty := decodeTimeline(t, f.get(t, "since="+url.QueryEscape(newer.ThroughCursor)))
			if empty.Turns == nil || len(empty.Turns) != 0 {
				t.Fatalf("caught-up poll turns = %#v, want []", empty.Turns)
			}
		})
	}
}

// wantAPIError fails unless rr is an API error with this status and code.
func wantAPIError(t *testing.T, rr *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if rr.Code != status || body.Error.Code != code {
		t.Fatalf("got %d %q, want %d %q: %s", rr.Code, body.Error.Code, status, code, rr.Body.String())
	}
}

func TestSessionTimeline_RecallOff(t *testing.T) {
	f := newTimelineFixture(t, "claude", false)
	f.appendTurns(t, 0, 1)
	wantAPIError(t, f.get(t, ""), http.StatusServiceUnavailable, "RECALL_DISABLED")
}

// A cursor carries its transcript path. One issued for another file, or
// naming any other path, must not read that file through this session's
// route.
func TestSessionTimeline_RejectsCursorForAnotherFile(t *testing.T) {
	other := newTimelineFixture(t, "claude", true)
	other.appendTurns(t, 0, 2)
	foreign := decodeTimeline(t, other.get(t, "")).ThroughCursor

	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 2)
	wantAPIError(t, f.get(t, "since="+url.QueryEscape(foreign)), http.StatusConflict, "RESYNC_REQUIRED")

	passwd := base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"p":"/etc/passwd","o":1,"a":"x"}`))
	for _, q := range []string{"since=" + passwd, "limit=2&before=" + passwd} {
		rr := f.get(t, q)
		wantAPIError(t, rr, http.StatusConflict, "RESYNC_REQUIRED")
		if !strings.Contains(rr.Body.String(), "source_moved") {
			t.Fatalf("%s: want source_moved: %s", q, rr.Body.String())
		}
	}
	wantAPIError(t, f.get(t, "since=not-a-cursor"), http.StatusConflict, "RESYNC_REQUIRED")
	wantAPIError(t, f.get(t, "before=garbage&limit=2"), http.StatusConflict, "RESYNC_REQUIRED")
}

func TestSessionTimeline_Errors(t *testing.T) {
	for _, tc := range []struct {
		name, tool, path, query string
		noTranscript            bool
		status                  int
		code, message           string
	}{
		{name: "unknown session", tool: "claude", path: "/api/sessions/nope/timeline", status: http.StatusNotFound, code: "NOT_FOUND", message: "session not found"},
		{name: "tool without direct rows", tool: "shell", status: http.StatusNotImplemented, code: "NOT_IMPLEMENTED"},
		{name: "no transcript yet", tool: "claude", noTranscript: true, status: http.StatusNotFound, code: "NOT_FOUND", message: "no native transcript"},
		{name: "unknown agent", tool: "claude", query: "agent=nosuchagent", status: http.StatusNotFound, code: "NOT_FOUND", message: "transcript not found"},
		{name: "agent too long", tool: "claude", query: "agent=" + strings.Repeat("a", 5000), status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "agent on codex", tool: "codex", query: "agent=abc", status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "agent with a path", tool: "claude", query: "agent=..%2F..%2Fx", status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "before without limit", tool: "claude", query: "before=x", status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "non-integer tail", tool: "claude", query: "tail=ten", status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "negative tail", tool: "claude", query: "tail=-1", status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "negative limit", tool: "claude", query: "limit=-1", status: http.StatusBadRequest, code: "INVALID_REQUEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTimelineFixture(t, tc.tool, true)
			if f.path != "" {
				f.appendTurns(t, 0, 1)
			}
			if tc.noTranscript {
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
			}
			target := tc.path
			if target == "" {
				target = "/api/sessions/" + f.inst.ID + "/timeline"
			}
			if tc.query != "" {
				target += "?" + tc.query
			}
			rr := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, target, nil))
			wantAPIError(t, rr, tc.status, tc.code)
			if !strings.Contains(rr.Body.String(), tc.message) {
				t.Fatalf("body %s does not say %q", rr.Body.String(), tc.message)
			}
		})
	}
	t.Run("post", func(t *testing.T) {
		f := newTimelineFixture(t, "claude", true)
		rr := httptest.NewRecorder()
		req := newLocalRequest(http.MethodPost, "/api/sessions/"+f.inst.ID+"/timeline", nil)
		req.Header.Set("Origin", "http://localhost")
		f.srv.Handler().ServeHTTP(rr, req)
		wantAPIError(t, rr, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
	})
}

// With a token configured, the route needs it like every other /api route.
func TestSessionTimeline_RequiresToken(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 1)
	f.srv.cfg.Token = "secret"
	for _, tc := range []struct {
		name, auth string
		status     int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "Bearer nope", http.StatusUnauthorized},
		{"right", "Bearer secret", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := newLocalRequest(http.MethodGet, "/api/sessions/"+f.inst.ID+"/timeline?tail=1", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			f.srv.Handler().ServeHTTP(rr, req)
			if tc.status == http.StatusOK {
				if tl := decodeTimeline(t, rr); tl.Schema != query.RowsSchema || len(tl.Turns) != 1 {
					t.Fatalf("rows = %+v", tl)
				}
				return
			}
			wantAPIError(t, rr, tc.status, "UNAUTHORIZED")
		})
	}
}
