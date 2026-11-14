package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeExec records calls and answers like the real CLI would.
type fakeExec struct {
	mu    sync.Mutex
	calls []fakeCall
}

type fakeCall struct {
	profile string
	args    []string
	stdin   string
}

func (f *fakeExec) Run(_ context.Context, profile string, args []string, stdin []byte) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{profile, append([]string(nil), args...), string(stdin)})
	f.mu.Unlock()
	switch {
	case len(args) >= 1 && args[0] == "list":
		return []byte(`[{"id":"s1","title":"demo","tool":"shell","status":"idle","group":"g","path":"/tmp/p","tmux_session":"x"},
			{"id":"s2","title":"other","tool":"claude","status":"running","group":"h","path":"/tmp/q"}]`), nil
	case len(args) >= 2 && args[0] == "session" && args[1] == "show":
		return []byte(`{"id":"s1","status":"idle"}`), nil
	case len(args) >= 2 && args[0] == "session" && args[1] == "output":
		return []byte("last answer\n"), nil
	case len(args) >= 2 && args[0] == "session" && args[1] == "send":
		return []byte(`{"success":true}`), nil
	}
	return []byte(`{"success":true}`), nil
}

func (f *fakeExec) last() fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeCall{}
	}
	return f.calls[len(f.calls)-1]
}

func newTestService(t *testing.T, limits Limits) (*Service, *fakeExec) {
	t.Helper()
	fx := &fakeExec{}
	svc := NewServiceWithLimits(NewStore(t.TempDir()), fx, "test", limits)
	return svc, fx
}

func issue(t *testing.T, svc *Service, name string, scopes ...Scope) string {
	t.Helper()
	tok, _, err := svc.Store.Create(name, "mcp", scopes, "_test")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestStoreHashesAndPermissions(t *testing.T) {
	st := NewStore(t.TempDir())
	tok, rec, err := st.Create("hermes", "hermes", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) < 50 {
		t.Fatalf("token shape: %q", tok)
	}
	if HasScope(rec.Scopes, ScopeSend) || !HasScope(rec.Scopes, ScopeRead) {
		t.Fatalf("default scopes = %v, want read only", rec.Scopes)
	}
	data, _ := os.ReadFile(st.Path())
	if strings.Contains(string(data), tok) {
		t.Fatal("raw token stored in assistants.json")
	}
	for _, p := range []string{st.Path(), st.TokenFile("hermes")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", p, info.Mode().Perm())
		}
	}
	got, err := ReadTokenFile(st.TokenFile("hermes"))
	if err != nil || got != tok {
		t.Fatalf("token file = %q, %v", got, err)
	}
	if _, err := st.Authenticate("adk_nope"); err == nil {
		t.Fatal("bogus token accepted")
	}
	if err := ValidateName("Bad Name"); err == nil {
		t.Fatal("bad name accepted")
	}
}

func TestScopeDenied(t *testing.T) {
	svc, fx := newTestService(t, DefaultLimits)
	tok := issue(t, svc, "bot")
	rec, err := svc.Authenticate(tok)
	if err != nil {
		t.Fatal(err)
	}
	res := svc.Call(context.Background(), rec, ToolSendMessage, json.RawMessage(`{"session":"s1","message":"secret body"}`), TransportAPI)
	if res.Err == nil || res.Err.Code != CodeScopeMissing {
		t.Fatalf("send with read scope: %+v", res)
	}
	if !strings.Contains(res.Err.Hint, "--scopes read,send") {
		t.Fatalf("hint lacks the raise command: %q", res.Err.Hint)
	}
	if len(fx.calls) != 0 {
		t.Fatalf("denied call ran a command: %+v", fx.calls)
	}
	for _, tool := range []string{ToolApprove, ToolLaunchSession} {
		if r := svc.Call(context.Background(), rec, tool, json.RawMessage(`{"session":"s1","path":"/tmp","title":"x"}`), TransportAPI); r.Err == nil || r.Err.Code != CodeScopeMissing {
			t.Fatalf("%s with read scope: %+v", tool, r)
		}
	}
}

func TestSendUsesStdinAndAuditHasNoBody(t *testing.T) {
	svc, fx := newTestService(t, DefaultLimits)
	tok := issue(t, svc, "bot", ScopeRead, ScopeSend)
	rec, _ := svc.Authenticate(tok)
	const body = "please run the tests TOPSECRETBODY"
	res := svc.Call(context.Background(), rec, ToolSendMessage, json.RawMessage(`{"session":"s1","message":"`+body+`"}`), TransportStdio)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	last := fx.last()
	if last.stdin != body {
		t.Fatalf("message not passed on stdin: %+v", last)
	}
	for _, a := range last.args {
		if strings.Contains(a, "TOPSECRET") {
			t.Fatalf("message leaked into argv: %v", last.args)
		}
	}
	if strings.Join(last.args, " ") != "session send --json --message-file - s1" || last.profile != "_test" {
		t.Fatalf("argv = %v profile %q", last.args, last.profile)
	}
	// a denied call too
	_ = svc.Call(context.Background(), rec, ToolApprove, json.RawMessage(`{"session":"s1"}`), TransportStdio)
	data, err := os.ReadFile(svc.Store.AuditPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "TOPSECRET") {
		t.Fatalf("audit log contains the message body:\n%s", data)
	}
	entries, err := ReadAudit(svc.Store.AuditPath(), "bot", 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("audit entries = %+v, %v", entries, err)
	}
	if !entries[0].Allowed || entries[0].Tool != ToolSendMessage || entries[0].Session != "s1" || entries[0].Transport != TransportStdio {
		t.Fatalf("first entry: %+v", entries[0])
	}
	if entries[1].Allowed || entries[1].Reason != CodeScopeMissing {
		t.Fatalf("second entry: %+v", entries[1])
	}
	info, _ := os.Stat(svc.Store.AuditPath())
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("audit mode %v", info.Mode().Perm())
	}
}

func TestRevokeTakesEffect(t *testing.T) {
	dir := t.TempDir()
	server := NewServiceWithLimits(NewStore(dir), &fakeExec{}, "test", DefaultLimits)
	cli := NewStore(dir) // a second process (the CLI) sharing the files
	tok, _, err := cli.Create("bot", "mcp", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Authenticate(tok); err != nil {
		t.Fatalf("fresh token refused: %v", err)
	}
	// Make sure the rewrite gets a different mtime even on coarse clocks.
	time.Sleep(20 * time.Millisecond)
	ok, err := cli.Revoke("bot")
	if err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if _, err := server.Authenticate(tok); err == nil {
		t.Fatal("revoked token still accepted by a running server")
	}
	if _, err := os.Stat(cli.TokenFile("bot")); !os.IsNotExist(err) {
		t.Fatalf("token file still there: %v", err)
	}
}

func TestRateLimit429(t *testing.T) {
	svc, _ := newTestService(t, Limits{ReadPerMinute: 3, WritePerMinute: 1})
	tok := issue(t, svc, "bot", ScopeRead, ScopeSend)
	h := NewHTTPHandler(svc)
	get := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/assistant/v1/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	for i := 0; i < 3; i++ {
		if c := get(); c != http.StatusOK {
			t.Fatalf("call %d: %d", i, c)
		}
	}
	if c := get(); c != http.StatusTooManyRequests {
		t.Fatalf("4th read call: %d, want 429", c)
	}
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/assistant/v1/sessions/s1/send", strings.NewReader(`{"message":"hi"}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	if c := send(); c != http.StatusOK {
		t.Fatalf("first send: %d", c)
	}
	if c := send(); c != http.StatusTooManyRequests {
		t.Fatalf("second send: %d, want 429", c)
	}
}

func TestHTTPAPIAuthAndRoutes(t *testing.T) {
	svc, fx := newTestService(t, DefaultLimits)
	tok := issue(t, svc, "bot")
	h := NewHTTPHandler(svc)
	do := func(method, path, auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	if rr := do("GET", "/api/assistant/v1/sessions", "", ""); rr.Code != 401 {
		t.Fatalf("no token: %d", rr.Code)
	}
	if rr := do("GET", "/api/assistant/v1/sessions", "Bearer owner-token", ""); rr.Code != 401 {
		t.Fatalf("non-assistant token: %d", rr.Code)
	}
	rr := do("GET", "/api/assistant/v1/sessions?status=idle", "Bearer "+tok, "")
	if rr.Code != 200 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			Count    int              `json:"count"`
			Sessions []map[string]any `json:"sessions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Result.Count != 1 {
		t.Fatalf("list body: %s (%v)", rr.Body, err)
	}
	if _, leaked := out.Result.Sessions[0]["tmux_session"]; leaked {
		t.Fatal("list_sessions did not trim fields")
	}
	if rr := do("GET", "/api/assistant/v1/sessions/s1/output", "Bearer "+tok, ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "last answer") {
		t.Fatalf("output: %d %s", rr.Code, rr.Body)
	}
	if strings.Join(fx.last().args, " ") != "session output s1" {
		t.Fatalf("output argv: %v", fx.last().args)
	}
	if rr := do("POST", "/api/assistant/v1/sessions/s1/send", "Bearer "+tok, `{"message":"x"}`); rr.Code != 403 {
		t.Fatalf("send without scope: %d", rr.Code)
	}
	if rr := do("GET", "/api/assistant/v1/sessions/-rf", "Bearer "+tok, ""); rr.Code != 400 {
		t.Fatalf("flag-shaped id: %d", rr.Code)
	}
	if rr := do("DELETE", "/api/assistant/v1/sessions/s1", "Bearer "+tok, ""); rr.Code != 404 {
		t.Fatalf("delete route must not exist: %d", rr.Code)
	}
	req := httptest.NewRequest("GET", "/api/assistant/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", "http://evil.example")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if rr2.Code != 403 {
		t.Fatalf("cross-origin: %d", rr2.Code)
	}
}

func TestLaunchValidation(t *testing.T) {
	svc, fx := newTestService(t, DefaultLimits)
	tok := issue(t, svc, "bot", ScopeLaunch)
	rec, _ := svc.Authenticate(tok)
	bad := []string{
		`{"path":"relative","title":"t"}`,
		`{"path":"/tmp","title":"t","tool":"bash -c evil"}`,
		`{"path":"/tmp","title":"-x"}`,
	}
	for _, b := range bad {
		if r := svc.Call(context.Background(), rec, ToolLaunchSession, json.RawMessage(b), TransportAPI); r.Err == nil || r.Err.Code != CodeInvalid {
			t.Fatalf("%s: %+v", b, r)
		}
	}
	r := svc.Call(context.Background(), rec, ToolLaunchSession, json.RawMessage(`{"path":"/tmp/p","title":"demo","tool":"codex","message":"hello"}`), TransportAPI)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if got := strings.Join(fx.last().args, " "); got != "launch --json --no-parent -t demo -c codex --message-file - /tmp/p" || fx.last().stdin != "hello" {
		t.Fatalf("launch argv %q stdin %q", got, fx.last().stdin)
	}
}

func TestReadOnlyServiceRefusesWrites(t *testing.T) {
	svc, _ := newTestService(t, DefaultLimits)
	svc.ReadOnly = true
	tok := issue(t, svc, "bot", AllScopes...)
	rec, _ := svc.Authenticate(tok)
	if r := svc.Call(context.Background(), rec, ToolSendMessage, json.RawMessage(`{"session":"s1","message":"x"}`), TransportAPI); r.Err == nil || r.Err.Code != CodeReadOnly {
		t.Fatalf("read-only send: %+v", r)
	}
}

// mcpRoundTrip checks initialize, tools/list and tools/call list_sessions.
func checkMCPReplies(t *testing.T, replies []map[string]any) {
	t.Helper()
	if len(replies) != 3 {
		t.Fatalf("got %d replies, want 3 (notification must not be answered): %v", len(replies), replies)
	}
	init := replies[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize did not echo the requested version: %v", init)
	}
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"whoami", "list_sessions", "session_status", "read_output", "send_message", "approve", "launch_session"} {
		if !names[want] {
			t.Fatalf("tools/list misses %s", want)
		}
	}
	for _, forbidden := range []string{"stop_session", "remove_session", "restart_session", "kill_session"} {
		if names[forbidden] {
			t.Fatalf("tools/list has %s", forbidden)
		}
	}
	call := replies[2]["result"].(map[string]any)
	if call["isError"] != false {
		t.Fatalf("tools/call failed: %v", call)
	}
	text := call["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"count": 2`) {
		t.Fatalf("tools/call text: %s", text)
	}
}

const mcpScript = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_sessions","arguments":{}}}
`

func TestMCPStdioRoundTrip(t *testing.T) {
	svc, _ := newTestService(t, DefaultLimits)
	tok := issue(t, svc, "bot")
	var out bytes.Buffer
	h := NewMCPHandler(svc, TransportStdio)
	if err := h.ServeStdio(context.Background(), func() (string, error) { return tok, nil }, strings.NewReader(mcpScript), &out); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad reply %q: %v", sc.Text(), err)
		}
		replies = append(replies, m)
	}
	checkMCPReplies(t, replies)

	// Revoked mid-session: the next request is refused.
	if _, err := svc.Store.Revoke("bot"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	_ = h.ServeStdio(context.Background(), func() (string, error) { return tok, nil },
		strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`+"\n"), &out)
	if !strings.Contains(out.String(), `"code":-32001`) {
		t.Fatalf("revoked token not refused over stdio: %s", out.String())
	}
}

func TestMCPHTTPRoundTrip(t *testing.T) {
	svc, _ := newTestService(t, DefaultLimits)
	tok := issue(t, svc, "bot")
	srv := httptest.NewServer(NewHTTPHandler(svc))
	defer srv.Close()

	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(mcpScript), "\n") {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(line))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if resp.StatusCode == http.StatusAccepted {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode != 200 {
			t.Fatalf("status %d for %s", resp.StatusCode, line)
		}
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		replies = append(replies, m)
	}
	checkMCPReplies(t, replies)

	resp, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp: %d", resp.StatusCode)
	}
	resp, err = http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tokenless POST /mcp: %d", resp.StatusCode)
	}
}

func TestIssueRendersEveryKind(t *testing.T) {
	st := NewStore(t.TempDir())
	for _, id := range KindIDs() {
		c, err := Issue(st, ConnectOptions{Kind: id, Command: "/usr/local/bin/agent-deck", URL: "http://127.0.0.1:8420"})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if c.ConfigSnippet == "" || !strings.Contains(c.ContextPrompt, "ASK THE USER") || !strings.Contains(c.Block, c.ConfigSnippet[:10]) {
			t.Fatalf("%s: incomplete connection %+v", id, c)
		}
		if c.MCPHTTP.URL != "http://127.0.0.1:8420/mcp" || c.HTTPAPI.Base != "http://127.0.0.1:8420/api/assistant/v1" {
			t.Fatalf("%s: urls %+v %+v", id, c.MCPHTTP, c.HTTPAPI)
		}
	}
	if _, err := Issue(st, ConnectOptions{Kind: "nope"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	c, err := Issue(st, ConnectOptions{})
	if err != nil || c.Assistant != DefaultKind || c.Name != "assistant" {
		t.Fatalf("default issue: %+v %v", c, err)
	}
}
