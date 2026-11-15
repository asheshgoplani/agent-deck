package web

import (
	"context"
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

func TestSessionTimeline_RecallOff(t *testing.T) {
	f := newTimelineFixture(t, "claude", false)
	f.appendTurns(t, 0, 1)
	rr := f.get(t, "")
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "RECALL_DISABLED") {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

// A cursor carries its transcript path. One issued for another file must
// not read that file through this session's route.
func TestSessionTimeline_RejectsCursorForAnotherFile(t *testing.T) {
	other := newTimelineFixture(t, "claude", true)
	other.appendTurns(t, 0, 2)
	foreign := decodeTimeline(t, other.get(t, "")).ThroughCursor

	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 2)
	rr := f.get(t, "since="+url.QueryEscape(foreign))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "RESYNC_REQUIRED") {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	rr = f.get(t, "since=not-a-cursor")
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "RESYNC_REQUIRED") {
		t.Fatalf("bad cursor: status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestSessionTimeline_Errors(t *testing.T) {
	t.Run("unknown session", func(t *testing.T) {
		f := newTimelineFixture(t, "claude", true)
		rr := httptest.NewRecorder()
		f.srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, "/api/sessions/nope/timeline", nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
	})
	t.Run("tool without direct rows", func(t *testing.T) {
		f := newTimelineFixture(t, "shell", true)
		if rr := f.get(t, ""); rr.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
	})
	t.Run("no transcript yet", func(t *testing.T) {
		f := newTimelineFixture(t, "claude", true)
		if err := os.Remove(f.path); err != nil {
			t.Fatal(err)
		}
		if rr := f.get(t, ""); rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
	})
	t.Run("before without limit", func(t *testing.T) {
		f := newTimelineFixture(t, "claude", true)
		if rr := f.get(t, "before=x"); rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
	})
	t.Run("non-integer tail", func(t *testing.T) {
		f := newTimelineFixture(t, "claude", true)
		if rr := f.get(t, "tail=ten"); rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
	})
	t.Run("post", func(t *testing.T) {
		f := newTimelineFixture(t, "claude", true)
		rr := httptest.NewRecorder()
		req := newLocalRequest(http.MethodPost, "/api/sessions/"+f.inst.ID+"/timeline", nil)
		req.Header.Set("Origin", "http://localhost")
		f.srv.Handler().ServeHTTP(rr, req)
		if rr.Code == http.StatusOK {
			t.Fatalf("POST answered 200: %s", rr.Body.String())
		}
	})
}

// With a token configured, the route needs it like every other /api route.
func TestSessionTimeline_RequiresToken(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 1)
	f.srv.cfg.Token = "secret"
	if rr := f.get(t, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}
