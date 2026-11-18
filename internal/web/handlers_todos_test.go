package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/todos"
)

type todoAPIStep struct {
	method, path, body string
	auth               string // bearer token to send; "" = none
	wantCode           int
	wantStatus         string // JSON "status" of a todos.Result; "" = not checked
	wantBody           string // substring
	wantPub            string // action published; "" = none
}

func runTodoAPISteps(t *testing.T, cfg Config, steps []todoAPIStep) {
	t.Helper()
	store := todos.NewStore(t.TempDir())
	var pubs []todos.Change
	var pubProfiles []string
	oldOpen, oldPub := todoStoreOpen, todoPublish
	todoStoreOpen = func() (*todos.Store, error) { return store, nil }
	todoPublish = func(c todos.Change, profile string) {
		pubs = append(pubs, c)
		pubProfiles = append(pubProfiles, profile)
	}
	t.Cleanup(func() { todoStoreOpen, todoPublish = oldOpen, oldPub })

	cfg.ListenAddr = "127.0.0.1:0"
	srv := NewServer(cfg)
	for i, st := range steps {
		before := len(pubs)
		var body *strings.Reader
		if st.body != "" {
			body = strings.NewReader(st.body)
		} else {
			body = strings.NewReader("")
		}
		req := newLocalRequest(st.method, st.path, body)
		req.Header.Set("Content-Type", "application/json")
		// Same-origin, as the web app and a gateway that sets Origin send it;
		// with a token set, the CSRF layer refuses a write without one.
		req.Header.Set("Origin", "http://localhost")
		if st.auth != "" {
			req.Header.Set("Authorization", "Bearer "+st.auth)
		}
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != st.wantCode {
			t.Fatalf("step %d %s %s: code %d, want %d: %s", i, st.method, st.path, rr.Code, st.wantCode, rr.Body.String())
		}
		if st.wantStatus != "" {
			var res todos.Result
			if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil || res.Status != st.wantStatus {
				t.Fatalf("step %d: status %q (err %v), want %q: %s", i, res.Status, err, st.wantStatus, rr.Body.String())
			}
		}
		if st.wantBody != "" && !strings.Contains(rr.Body.String(), st.wantBody) {
			t.Fatalf("step %d: body lacks %q: %s", i, st.wantBody, rr.Body.String())
		}
		got := pubs[before:]
		switch {
		case st.wantPub == "" && len(got) != 0:
			t.Fatalf("step %d published %+v, want nothing", i, got)
		case st.wantPub != "" && (len(got) != 1 || got[0].Action != st.wantPub):
			t.Fatalf("step %d published %+v, want one %q", i, got, st.wantPub)
		case st.wantPub != "" && pubProfiles[len(pubProfiles)-1] != cfg.Profile:
			t.Fatalf("step %d published to profile %q, want %q", i, pubProfiles[len(pubProfiles)-1], cfg.Profile)
		}
	}
}

func TestTodoAPITable(t *testing.T) {
	cases := []struct {
		name  string
		cfg   Config
		steps []todoAPIStep
	}{
		{name: "add, list, done", cfg: Config{Profile: "team-alex", WebMutations: true}, steps: []todoAPIStep{
			{method: "GET", path: "/api/todos", wantCode: 200, wantBody: `"todos":[]`},
			{method: "POST", path: "/api/todos", body: `{"text":"call the bank"}`, wantCode: 201, wantStatus: "added", wantBody: `"source":"phone"`, wantPub: "add"},
			{method: "POST", path: "/api/todos", body: `{"text":"buy milk","source":"mac","session_id":"s-9"}`, wantCode: 201, wantBody: `"session_id":"s-9"`, wantPub: "add"},
			{method: "GET", path: "/api/todos", wantCode: 200, wantBody: `"open":2`},
			{method: "POST", path: "/api/todos/1/done?source=mac", wantCode: 200, wantStatus: "done", wantBody: `"open":1`, wantPub: "done"},
			{method: "POST", path: "/api/todos/1/done", wantCode: 200, wantStatus: "already_done"},
			{method: "GET", path: "/api/todos", wantCode: 200, wantBody: `"done":1`},
			{method: "GET", path: "/api/todos?all=1", wantCode: 200, wantBody: `"text":"call the bank"`},
		}},
		{name: "undo and remove reverse a wrong tap", cfg: Config{Profile: "team-alex", WebMutations: true}, steps: []todoAPIStep{
			{method: "POST", path: "/api/todos", body: `{"text":"call the bank"}`, wantCode: 201, wantPub: "add"},
			{method: "POST", path: "/api/todos/1/done", wantCode: 200, wantStatus: "done", wantPub: "done"},
			{method: "POST", path: "/api/todos/1/undo?source=mac", wantCode: 200, wantStatus: "reopened", wantBody: `"done_at":null`, wantPub: "undo"},
			{method: "POST", path: "/api/todos/1/undo", wantCode: 200, wantStatus: "already_open"},
			{method: "POST", path: "/api/todos/1/remove", wantCode: 200, wantStatus: "removed", wantBody: `"open":0`, wantPub: "remove"},
			{method: "POST", path: "/api/todos/1/remove", wantCode: 404},
			{method: "POST", path: "/api/todos/1/undo", wantCode: 404},
			{method: "POST", path: "/api/todos/1/frobnicate", wantCode: 404},
			{method: "GET", path: "/api/todos/1/undo", wantCode: 405},
		}},
		{name: "read-only refuses undo and remove", cfg: Config{ReadOnly: true}, steps: []todoAPIStep{
			{method: "POST", path: "/api/todos/1/undo", wantCode: 403, wantBody: "READ_ONLY"},
			{method: "POST", path: "/api/todos/1/remove", wantCode: 403, wantBody: "READ_ONLY"},
		}},
		{name: "bad requests", cfg: Config{WebMutations: true}, steps: []todoAPIStep{
			{method: "POST", path: "/api/todos", body: `{"text":"  "}`, wantCode: 400},
			{method: "POST", path: "/api/todos", body: `{"text":"x","source":"email"}`, wantCode: 400},
			{method: "POST", path: "/api/todos", body: `not json`, wantCode: 400},
			{method: "POST", path: "/api/todos/abc/done", wantCode: 400},
			{method: "POST", path: "/api/todos/7/done", wantCode: 404},
			{method: "POST", path: "/api/todos/7/done?source=fax", wantCode: 400},
			{method: "DELETE", path: "/api/todos", wantCode: 405},
			{method: "GET", path: "/api/todos/1/done", wantCode: 405},
		}},
		{name: "read-only refuses writes, still reads", cfg: Config{ReadOnly: true, WebMutations: false}, steps: []todoAPIStep{
			{method: "POST", path: "/api/todos", body: `{"text":"x"}`, wantCode: 403, wantBody: "READ_ONLY"},
			{method: "POST", path: "/api/todos/1/done", wantCode: 403, wantBody: "READ_ONLY"},
			{method: "GET", path: "/api/todos", wantCode: 200},
		}},
		{name: "mutations off refuses writes", cfg: Config{WebMutations: false}, steps: []todoAPIStep{
			{method: "POST", path: "/api/todos", body: `{"text":"x"}`, wantCode: 403, wantBody: "MUTATIONS_DISABLED"},
		}},
		{name: "token required", cfg: Config{Token: "secret-token-1", WebMutations: true}, steps: []todoAPIStep{
			{method: "GET", path: "/api/todos", wantCode: 401},
			{method: "POST", path: "/api/todos", body: `{"text":"x"}`, wantCode: 401},
			{method: "POST", path: "/api/todos/1/done", wantCode: 401},
			{method: "GET", path: "/api/todos?token=secret-token-1", wantCode: 401},
			{method: "POST", path: "/api/todos", body: `{"text":"x"}`, auth: "secret-token-1", wantCode: 201, wantPub: "add"},
			{method: "GET", path: "/api/todos", auth: "secret-token-1", wantCode: 200, wantBody: `"open":1`},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runTodoAPISteps(t, tc.cfg, tc.steps) })
	}
}
