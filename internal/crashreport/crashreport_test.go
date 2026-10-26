package crashreport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// fakeDSN is an obviously fake placeholder; example.invalid never resolves.
const fakeDSN = "https://public@example.invalid/1"

// recordingTransport keeps every event instead of sending it.
type recordingTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (r *recordingTransport) Configure(sentry.ClientOptions)        {}
func (r *recordingTransport) Flush(time.Duration) bool              { return true }
func (r *recordingTransport) FlushWithContext(context.Context) bool { return true }
func (r *recordingTransport) Close()                                {}
func (r *recordingTransport) SendEvent(ev *sentry.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}
func (r *recordingTransport) all() []*sentry.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*sentry.Event(nil), r.events...)
}

// Fake secrets are assembled at run time so no token-shaped literal sits in
// the source (secret scanners, the GitHub sanitize gate).
func fake(prefix string, n int) string { return prefix + strings.Repeat("aB3", n)[:n] }

const (
	testHome     = "/Users/alicew"
	sessionTitle = "fix payroll export for team x"
	promptText   = "please refactor the billing module and keep the customer list secret"
	sessionOut   = "Compiling billing v0.3.1 (/srv/clients/demo/billing)"
	envValue     = "SUPERSECRET_ENV_VALUE_42"
	email        = "alice.w@pilot-company.example"

	// Unquoted plain words: what %s / %v put into error text. No pattern
	// can tell them from the code's own words.
	groupName    = "Payroll Migration"
	plainTitle   = "Quarterly Forecast Rework"
	plainPrompt  = "summarise the merger memo for the board"
	plainCommand = "deploy staging tonight"
)

// sentryEnv are the SDK's own environment variables, set in the tests to
// prove they are ignored (no DSN, release, environment or tags from them).
var sentryEnv = map[string]string{
	"SENTRY_DSN":         "https://envkey@sentry-env.example.invalid/9",
	"SENTRY_RELEASE":     "env-release-leak",
	"SENTRY_ENVIRONMENT": "env-environment-leak",
	"SENTRY_TAGS_leak":   "env-tag-leak",
}

func setSentryEnv(t *testing.T) {
	t.Helper()
	for k, v := range sentryEnv {
		t.Setenv(k, v)
	}
}

// sentryServer is a local Sentry stand-in: it keeps every request body the
// production HTTP transport POSTs.
type sentryServer struct {
	mu     sync.Mutex
	bodies [][]byte
	header []http.Header
}

func newSentryServer(t *testing.T) (*sentryServer, string) {
	t.Helper()
	ss := &sentryServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ss.mu.Lock()
		ss.bodies = append(ss.bodies, body)
		ss.header = append(ss.header, r.Header.Clone())
		ss.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	// http is accepted for 127.0.0.1 only (managed.ValidateDSN).
	return ss, "http://public@" + srv.Listener.Addr().String() + "/1"
}

func (ss *sentryServer) wait(t *testing.T, n int) ([][]byte, []http.Header) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ss.mu.Lock()
		got := len(ss.bodies)
		ss.mu.Unlock()
		if got >= n || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return append([][]byte(nil), ss.bodies...), append([]http.Header(nil), ss.header...)
}

// envelopeEvent returns the event item of a Sentry envelope body.
func envelopeEvent(t *testing.T, body []byte) map[string]any {
	t.Helper()
	lines := bytes.Split(body, []byte("\n"))
	for i := 1; i+1 < len(lines); i++ {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(lines[i], &item) == nil && item.Type == "event" {
			var ev map[string]any
			if err := json.Unmarshal(lines[i+1], &ev); err != nil {
				t.Fatalf("event item: %v\n%s", err, lines[i+1])
			}
			return ev
		}
	}
	t.Fatalf("no event item in envelope:\n%s", body)
	return nil
}

func startForTest(t *testing.T, org string) *recordingTransport {
	t.Helper()
	tr := &recordingTransport{}
	ok, err := Init(Config{DSN: fakeDSN, Release: "1.9.0", OrgID: org, Home: testHome, Transport: tr})
	if err != nil || !ok {
		t.Fatalf("Init = %v, %v", ok, err)
	}
	t.Cleanup(Close)
	return tr
}

var hashedValue = regexp.MustCompile(`^msg:[0-9a-f]{12}$`)

// TestScrubbedEventCarriesNoPrivateData: errors and panics whose messages,
// error chain and breadcrumbs carry a home path, quoted and unquoted session
// text (a group name, a title, a prompt sentence, a command), an API-key-like
// token, an email and an env value reach a local Sentry stand-in through the
// production HTTP transport (no injected transport) with none of them in the
// bytes on the wire, and with SENTRY_* env vars ignored. What is sent: the
// exception type, a 12-hex message hash, stack frames, release and org.
func TestScrubbedEventCarriesNoPrivateData(t *testing.T) {
	setSentryEnv(t)
	ss, dsn := newSentryServer(t)
	ok, err := Init(Config{DSN: dsn, Release: "1.9.0", OrgID: "pilot-7f3k2", Home: testHome})
	if err != nil || !ok {
		t.Fatalf("Init = %v, %v", ok, err)
	}
	t.Cleanup(Close)
	apiKey := "s" + "k-ant-api03-" + fake("", 40)
	ghToken := fake("gh"+"p_", 36)

	h := current()
	h.AddBreadcrumb(&sentry.Breadcrumb{Category: "session.send", Message: "prompt: " + promptText + " " + plainPrompt,
		Data: map[string]any{"path": testHome + "/work/app", "env": "API_KEY=" + envValue, "out": sessionOut, "group": groupName}}, nil)
	h.AddBreadcrumb(&sentry.Breadcrumb{Category: "Session Title: " + sessionTitle, Message: email}, nil)
	h.AddBreadcrumb(&sentry.Breadcrumb{Category: groupName, Type: plainTitle, Message: plainCommand}, nil)
	h.ConfigureScope(func(s *sentry.Scope) {
		s.SetUser(sentry.User{Email: email, Username: "alicew", IPAddress: "203.0.113.7"})
		s.SetTag("title", sessionTitle)
		s.SetTag("group", groupName)
		s.SetContext("session", sentry.Context{"output": sessionOut, "title": plainTitle, "prompt": plainPrompt})
	})

	// An error chain: unquoted names at every level, quoted ones too.
	inner := fmt.Errorf("group snapshot conflict: %s", groupName)
	mid := fmt.Errorf("session %s: run %s: %w", plainTitle, plainCommand, inner)
	err = fmt.Errorf("send keys to %q: prompt %q: %s: open %s/.claude/projects/x.jsonl for %s with key %s: %w: %w",
		sessionTitle, promptText, plainPrompt, testHome, email, apiKey, mid, io.ErrUnexpectedEOF)
	CaptureError(err)
	Flush()

	func() { // a string panic value
		defer func() { _ = recover() }() // Recover re-panics; stop it here
		defer Recover()
		panic(fmt.Sprintf("tmux pane %s died for %s in %s\n%s\ntoken=%s mail %s",
			testHome+"/repo/agent-deck", plainTitle, groupName, sessionOut, ghToken, email))
	}()
	func() { // an error panic value
		defer func() { _ = recover() }()
		defer Recover()
		panic(fmt.Errorf("restore %s failed: %s", groupName, plainPrompt))
	}()

	bodies, headers := ss.wait(t, 3)
	if len(bodies) != 3 {
		t.Fatalf("server got %d requests, want 3 (error + 2 panics)", len(bodies))
	}
	forbidden := []string{testHome, "alicew", sessionTitle, promptText, "payroll", "migration", "billing", sessionOut, "/srv/clients",
		"quarterly", "forecast", "rework", "merger", "memo", "board", "staging", "tonight", "summarise",
		apiKey, "api03", ghToken, email, "pilot-company.example", envValue, "203.0.113.7", ".claude/projects", "x.jsonl",
		"sentry-env.example.invalid", "envkey", "env-release-leak", "env-environment-leak", "env-tag-leak"}
	for i, body := range bodies {
		wire := strings.ToLower(string(body))
		for k, vs := range headers[i] {
			wire += "\n" + strings.ToLower(k+": "+strings.Join(vs, ","))
		}
		for _, f := range forbidden {
			if strings.Contains(wire, strings.ToLower(f)) {
				t.Errorf("request %d leaks %q:\n%s", i, f, body)
			}
		}
		ev := envelopeEvent(t, body)
		if ev["release"] != "agent-deck@1.9.0" || ev["environment"] != "production" {
			t.Errorf("request %d release/environment = %v/%v", i, ev["release"], ev["environment"])
		}
		tags, _ := ev["tags"].(map[string]any)
		if tags["org"] != "pilot-7f3k2" || len(tags) != 3 {
			t.Errorf("request %d tags = %v, want os, arch, org", i, tags)
		}
		for _, k := range []string{"server_name", "user", "contexts", "request", "extra", "modules"} {
			if v, ok := ev[k]; ok && v != nil && fmt.Sprint(v) != "map[]" && fmt.Sprint(v) != "" {
				t.Errorf("request %d kept %s: %v", i, k, v)
			}
		}
		if m, _ := ev["message"].(string); m != "" && !hashedValue.MatchString(m) {
			t.Errorf("request %d message %q is not a hash", i, m)
		}
		exc, _ := ev["exception"].([]any)
		for _, x := range exc {
			xm := x.(map[string]any)
			if v, _ := xm["value"].(string); !hashedValue.MatchString(v) {
				t.Errorf("request %d exception value %q is not a hash", i, v)
			}
		}
		if bcs, _ := ev["breadcrumbs"].([]any); len(bcs) != 3 {
			t.Errorf("request %d has %d breadcrumbs, want 3", i, len(bcs))
		}
		if !strings.Contains(string(body), `"lineno"`) || !strings.Contains(string(body), "crashreport_test.go") {
			t.Errorf("request %d lost its stack trace:\n%s", i, body)
		}
	}
	// The error chain keeps its types and one hash per level.
	ev := envelopeEvent(t, bodies[0])
	exc, _ := ev["exception"].([]any)
	if len(exc) < 3 {
		t.Fatalf("error chain has %d exceptions, want >= 3:\n%s", len(exc), bodies[0])
	}
	if typ := exc[len(exc)-1].(map[string]any)["type"]; typ != "*fmt.wrapErrors" {
		t.Errorf("outer exception type = %v", typ)
	}
}

// TestNoDSNNothingInitialized: without a DSN, Init creates no client, the
// global sentry hub stays empty, and every entry point is a no-op.
func TestNoDSNNothingInitialized(t *testing.T) {
	setSentryEnv(t) // SENTRY_DSN must not turn it on
	ok, err := Init(Config{Release: "1.9.0", OrgID: "pilot-7f3k2"})
	if ok || err != nil {
		t.Fatalf("Init without DSN = %v, %v; want false, nil", ok, err)
	}
	if Enabled() || current() != nil {
		t.Fatal("a client exists without a DSN")
	}
	if sentry.CurrentHub().Client() != nil {
		t.Fatal("the global sentry hub got a client")
	}
	CaptureError(errors.New("boom"))
	Breadcrumb("session.start")
	Flush()
	func() {
		defer func() {
			if r := recover(); r != "still panics" {
				t.Errorf("Recover changed the panic: %v", r)
			}
		}()
		defer Recover()
		panic("still panics")
	}()
}

// TestInitRefusesBadDSN: a DSN with a secret part or plain http starts nothing.
func TestInitRefusesBadDSN(t *testing.T) {
	for _, dsn := range []string{"https://public:secret@example.invalid/1", "http://public@example.invalid/1", "not a url"} {
		tr := &recordingTransport{}
		ok, err := Init(Config{DSN: dsn, Transport: tr})
		if ok || err == nil || Enabled() {
			t.Errorf("Init(%q) = %v, %v; want refused", dsn, ok, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret@") {
			t.Errorf("error quotes the DSN: %v", err)
		}
	}
}

// TestNoOrgNoOrgTag: without an org id the event carries no org tag.
func TestNoOrgNoOrgTag(t *testing.T) {
	tr := startForTest(t, "")
	CaptureError(errors.New("boom"))
	evs := tr.all()
	if len(evs) != 1 {
		t.Fatalf("got %d events", len(evs))
	}
	if _, ok := evs[0].Tags["org"]; ok {
		t.Fatalf("org tag present without an org id: %v", evs[0].Tags)
	}
}

// TestSDKCollectsNothingAutomatically: the client is configured with the
// SDK's DataCollection options so it collects no user info, cookies, HTTP
// headers, HTTP bodies or query parameters on its own. scrubEvent drops
// these too; this is the first line, set through the non-deprecated option
// (SendDefaultPII is deprecated and ignored once DataCollection is set).
func TestSDKCollectsNothingAutomatically(t *testing.T) {
	startForTest(t, "")
	h := current()
	if h == nil || h.Client() == nil {
		t.Fatal("no client after Init")
	}
	dc := h.Client().GetDataCollection()
	if !dc.UserInfo.IsSet || dc.UserInfo.Value {
		t.Errorf("UserInfo = %+v, want set to false", dc.UserInfo)
	}
	if len(dc.HTTPBodies) != 0 {
		t.Errorf("HTTPBodies = %v, want none", dc.HTTPBodies)
	}
	off := map[string]*sentry.KeyValueCollectionBehavior{
		"Cookies":     dc.Cookies,
		"QueryParams": dc.QueryParams,
	}
	if dc.HTTPHeaders != nil {
		off["HTTPHeaders.Request"] = dc.HTTPHeaders.Request
		off["HTTPHeaders.Response"] = dc.HTTPHeaders.Response
	} else {
		t.Error("HTTPHeaders unset")
	}
	for name, b := range off {
		if b == nil || b.Mode != sentry.CollectionOff {
			t.Errorf("%s = %+v, want CollectionOff", name, b)
		}
	}
}

// TestMessageHash: 12 hex characters, stable, the same for messages that
// differ only in digits, home or token, different for different words, and
// never any of the words.
func TestMessageHash(t *testing.T) {
	a := MessageHash("group snapshot conflict: "+groupName+" on port 5432", testHome)
	if !hashedValue.MatchString(a) {
		t.Fatalf("MessageHash = %q, want msg: and 12 hex", a)
	}
	if b := MessageHash("group snapshot conflict: "+groupName+" on port 6543", testHome); b != a {
		t.Errorf("digits split the group: %q vs %q", a, b)
	}
	if b := MessageHash("group snapshot conflict: Other Group on port 5432", testHome); b == a {
		t.Errorf("different words, same hash %q", a)
	}
	p1 := MessageHash("open "+testHome+"/a/b.json: denied", testHome)
	p2 := MessageHash("open /Users/bob/c/d.json: denied", "/Users/bob")
	if p1 != p2 {
		t.Errorf("home paths split the group: %q vs %q", p1, p2)
	}
	if MessageHash("", testHome) != "" {
		t.Error("empty message must stay empty")
	}
	if strings.Contains(strings.ToLower(a), "payroll") {
		t.Errorf("hash carries words: %q", a)
	}
}

func TestScrubText(t *testing.T) {
	cases := []struct{ in, want string }{
		{`open "/tmp/a b": no such file`, "open <text>: no such file"},
		{"read /var/lib/x/state.json: permission denied", "read <path>: permission denied"},
		{"stat " + testHome + "/proj/main.go failed", "stat <path> failed"},
		{"line one\nline two with secret", "line one"},
		{"write to " + email, "write to <email>"},
		{strings.Repeat("x", 300), strings.Repeat("x", 199) + "…"},
	}
	for _, tc := range cases {
		if got := ScrubText(tc.in, testHome); got != tc.want {
			t.Errorf("ScrubText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFrameFile(t *testing.T) {
	cases := []struct {
		f    sentry.Frame
		want string
	}{
		{sentry.Frame{AbsPath: "/Users/alicew/src/github.com/asheshgoplani/agent-deck/internal/session/x.go"}, "internal/session/x.go"},
		{sentry.Frame{Filename: "/usr/local/go/src/runtime/panic.go", Module: "runtime"}, "panic.go"},
		{sentry.Frame{Filename: "/home/bob/go/pkg/mod/example.com/lib@v1/a.go", Module: "example.com/lib"}, "a.go"},
	}
	for _, tc := range cases {
		if got := frameFile(tc.f); got != tc.want {
			t.Errorf("frameFile(%+v) = %q, want %q", tc.f, got, tc.want)
		}
	}
}
