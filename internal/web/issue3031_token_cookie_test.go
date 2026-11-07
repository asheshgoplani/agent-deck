package web

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Issue #3031: the web server sets the agentdeck_token cookie on a tokened
// visit; an installed PWA (start_url "/") then relies on that cookie alone.
// These tests drive the full handler chain (allowHosts -> tokenCookie ->
// csrfProtect -> mux), not authorize() directly.

const issue3031Token = "0123456789abcdef0123456789abcdef"

// 30 days, the value of tokenCookieMaxAge in auth.go (kept literal so this
// file also compiles against the unfixed code for the fail-first run).
const issue3031WantMaxAge = 30 * 24 * 60 * 60

func newIssue3031Server(t *testing.T, token string) (*httptest.Server, *http.Client) {
	t.Helper()
	s := NewServer(Config{ListenAddr: "127.0.0.1:8420", Token: token})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return ts, &http.Client{Jar: jar, Timeout: 3 * time.Second}
}

func issue3031TokenCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == "agentdeck_token" {
			return c
		}
	}
	return nil
}

// A cookie-only client must reach the JSON API, the SSE stream and the
// WebSocket bridge exactly as a bearer client does.
func TestIssue3031_CookieOnlyAuthorizesAPIEventsAndWS(t *testing.T) {
	ts, client := newIssue3031Server(t, issue3031Token)

	resp, err := client.Get(ts.URL + "/?token=" + issue3031Token)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if issue3031TokenCookie(t, resp) == nil {
		t.Fatalf("tokened visit set no agentdeck_token cookie: %q", resp.Header.Values("Set-Cookie"))
	}

	for _, path := range []string{"/api/sessions", "/api/menu", "/events/menu"} {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("cookie-only GET %s = %d, want 200", path, resp.StatusCode)
		}
	}

	// Without the cookie the same routes stay closed.
	for _, path := range []string{"/api/sessions", "/events/menu"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s = %d, want 401", path, resp.StatusCode)
		}
	}

	// WebSocket handshake: the cookie must pass auth. The session does not
	// exist, so a passing handshake fails later with something other than 401.
	u, _ := url.Parse(ts.URL)
	wsURL := "ws://" + u.Host + "/ws/session/does-not-exist"
	hdr := http.Header{}
	hdr.Set("Origin", ts.URL)
	for _, c := range client.Jar.Cookies(u) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	conn, wsResp, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if conn != nil {
		_ = conn.Close()
	}
	if wsResp != nil && wsResp.StatusCode == http.StatusUnauthorized {
		t.Errorf("cookie-only WebSocket handshake got 401 (err %v)", err)
	}
	conn, wsResp, _ = websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {ts.URL}})
	if conn != nil {
		_ = conn.Close()
	}
	if wsResp == nil || wsResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous WebSocket handshake not rejected with 401: %+v", wsResp)
	}
}

// The cookie must outlive the browser session: an installed app is closed
// and relaunched, and a session cookie is gone by then.
func TestIssue3031_CookieHasMaxAge(t *testing.T) {
	ts, client := newIssue3031Server(t, issue3031Token)
	resp, err := client.Get(ts.URL + "/?token=" + issue3031Token)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	c := issue3031TokenCookie(t, resp)
	if c == nil {
		t.Fatal("no agentdeck_token cookie")
	}
	if c.MaxAge != issue3031WantMaxAge {
		t.Errorf("cookie Max-Age = %d, want %d", c.MaxAge, issue3031WantMaxAge)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie attributes weakened: %+v", c)
	}
}

// Opening the app shell with a valid cookie (the installed-app launch) renews
// the cookie, so a device in regular use does not get logged out after
// Max-Age. A request with no or a wrong cookie gets nothing.
func TestIssue3031_AppShellRenewsValidCookie(t *testing.T) {
	ts, client := newIssue3031Server(t, issue3031Token)
	resp, err := client.Get(ts.URL + "/?token=" + issue3031Token)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	resp, err = client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	c := issue3031TokenCookie(t, resp)
	if c == nil || c.MaxAge != issue3031WantMaxAge {
		t.Fatalf("cookie-only app-shell load did not renew cookie: %q", resp.Header.Values("Set-Cookie"))
	}

	for name, cookie := range map[string]*http.Cookie{
		"none":  nil,
		"wrong": {Name: "agentdeck_token", Value: "wrong"},
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if issue3031TokenCookie(t, resp) != nil {
			t.Errorf("%s cookie: app shell issued a token cookie", name)
		}
	}

	// API responses never re-issue the cookie.
	resp, err = client.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if issue3031TokenCookie(t, resp) != nil {
		t.Error("API response re-issued the token cookie")
	}
}

// net/http drops bytes that are not legal in a cookie value ('"', '\\',
// non-ASCII). For such a token the server used to set a truncated cookie and
// then reject it with 401, the symptom #3031 reports. It must not set a
// cookie it would not accept.
func TestIssue3031_NoCookieThatWouldBeRejected(t *testing.T) {
	for _, token := range []string{`abc"def`, `abc\def`, "abcdéf"} {
		ts, client := newIssue3031Server(t, token)
		resp, err := client.Get(ts.URL + "/?token=" + url.QueryEscape(token))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		c := issue3031TokenCookie(t, resp)
		if c == nil {
			continue // no cookie is fine: bearer/query auth still works
		}
		resp, err = client.Get(ts.URL + "/api/sessions")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("token %q: server set cookie %q but then answered %d", token, c.Value, resp.StatusCode)
		}
	}
}

// With cookie auth a cross-origin page must not be able to drive mutations
// or open the terminal bridge, even when the browser would attach the cookie
// (same-site, different port).
func TestIssue3031_CookieDoesNotEnableCrossOrigin(t *testing.T) {
	ts, _ := newIssue3031Server(t, issue3031Token)
	cookie := &http.Cookie{Name: "agentdeck_token", Value: issue3031Token}

	for _, origin := range []string{"http://127.0.0.1:1", "https://evil.example"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{}`))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("cross-origin POST from %s with cookie = %d, want 403", origin, resp.StatusCode)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("CORS header returned for %s", origin)
		}

		u, _ := url.Parse(ts.URL)
		hdr := http.Header{"Origin": {origin}, "Cookie": {cookie.Name + "=" + cookie.Value}}
		conn, wsResp, _ := websocket.DefaultDialer.Dial("ws://"+u.Host+"/ws/session/x", hdr)
		if conn != nil {
			_ = conn.Close()
			t.Errorf("cross-origin WebSocket from %s with cookie was upgraded", origin)
		}
		_ = wsResp
	}
}

// Behind a TLS-terminating proxy (tailscale serve, Caddy) the request reaches
// the server as plain HTTP with X-Forwarded-Proto: https; the cookie must
// still carry Secure. Plain HTTP without the header must not (localhost).
func TestIssue3031_SecureBehindHTTPSProxy(t *testing.T) {
	ts, _ := newIssue3031Server(t, issue3031Token)
	for _, tc := range []struct {
		xfp        string
		wantSecure bool
	}{{"https", true}, {"HTTPS, http", true}, {"http", false}, {"", false}} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/?token="+issue3031Token, nil)
		if tc.xfp != "" {
			req.Header.Set("X-Forwarded-Proto", tc.xfp)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		c := issue3031TokenCookie(t, resp)
		if c == nil || c.Secure != tc.wantSecure {
			t.Errorf("X-Forwarded-Proto %q: cookie %+v, want Secure=%v", tc.xfp, c, tc.wantSecure)
		}
	}
}

// A same-site sibling origin can plant a bogus agentdeck_token with a longer
// Path that the browser sends first. The real cookie must still authorize.
func TestIssue3031_ShadowCookieDoesNotLockOut(t *testing.T) {
	ts, _ := newIssue3031Server(t, issue3031Token)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions", nil)
	req.Header.Set("Cookie", "agentdeck_token=bogus; agentdeck_token="+issue3031Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("bogus cookie ahead of the real one: %d, want 200", resp.StatusCode)
	}
}
