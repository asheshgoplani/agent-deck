package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A misspelled argument is refused, not ignored: `toool` must not launch
// the default agent, and `mesage` must not count as no message.
func TestCallRejectsUnknownArguments(t *testing.T) {
	svc, fx := newTestService(t, DefaultLimits)
	rec, err := svc.Authenticate(issue(t, svc, "bot", AllScopes...))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ tool, args string }{
		{ToolLaunchSession, `{"path":"/tmp","title":"t","toool":"codex"}`},
		{ToolSendMessage, `{"session":"s1","message":"hi","sesion":"s2"}`},
		{ToolListSessions, `{"grup":"g"}`},
		{ToolWhoami, `{"x":1}`},
	} {
		before := len(fx.calls)
		r := svc.Call(context.Background(), rec, c.tool, json.RawMessage(c.args), TransportStdio)
		if r.Err == nil || r.Err.Code != CodeInvalid || !strings.Contains(r.Err.Message, "unknown argument") {
			t.Errorf("%s %s: %+v, want %s for the unknown argument", c.tool, c.args, r, CodeInvalid)
		}
		if len(fx.calls) != before {
			t.Errorf("%s %s: ran a command", c.tool, c.args)
		}
	}
	// Known arguments still work.
	if r := svc.Call(context.Background(), rec, ToolLaunchSession, json.RawMessage(`{"path":"/tmp","title":"t","tool":"codex"}`), TransportStdio); r.Err != nil {
		t.Fatalf("valid launch: %+v", r.Err)
	}
}

// The JSON API refuses a body that is not a JSON object (null included)
// and a body with keys the route does not take.
func TestAPIBodyMustBeKnownObject(t *testing.T) {
	svc, fx := newTestService(t, DefaultLimits)
	tok := issue(t, svc, "bot", AllScopes...)
	h := NewHTTPHandler(svc)
	for _, c := range []struct{ path, body string }{
		{"/api/assistant/v1/sessions/s1/approve", `null`},
		{"/api/assistant/v1/sessions/s1/send", `null`},
		{"/api/assistant/v1/sessions", `null`},
		{"/api/assistant/v1/sessions", `{"path":"/tmp","title":"t","toool":"codex"}`},
		{"/api/assistant/v1/sessions/s1/approve", `{"choise":"always"}`},
	} {
		req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("POST %s %s = %d %s, want 400", c.path, c.body, rr.Code, rr.Body)
		}
	}
	if len(fx.calls) != 0 {
		t.Errorf("refused bodies ran %d command(s)", len(fx.calls))
	}
	// An empty body is still an empty object (approve defaults to once).
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/v1/sessions/s1/approve", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("empty approve body = %d %s, want 200", rr.Code, rr.Body)
	}
}
