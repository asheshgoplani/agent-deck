package watcher

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const webhookTestSecret = "0123456789abcdef0123456789abcdef"

// newTestWebhook runs Setup with settings and wires an event channel, without
// listening: requests are served straight through the adapter's handler.
func newTestWebhook(t *testing.T, settings map[string]string) (*WebhookAdapter, chan Event) {
	t.Helper()
	a := &WebhookAdapter{}
	if err := a.Setup(context.Background(), AdapterConfig{Type: "webhook", Name: "auth-test", Settings: settings}); err != nil {
		t.Fatalf("Setup(%v): %v", settings, err)
	}
	ch := make(chan Event, 4)
	a.mu.Lock()
	a.eventsCh = ch
	a.mu.Unlock()
	return a, ch
}

// webhookRequest builds a POST to /webhook as sent to host, from a remote
// address the tests can tell apart from any sender header.
func webhookRequest(host, body string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://"+host+"/webhook", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:40000"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func serveWebhook(a *WebhookAdapter, req *http.Request) int {
	rr := httptest.NewRecorder()
	a.server.Handler.ServeHTTP(rr, req)
	return rr.Code
}

func signWebhookBody(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// signedHeaders returns the headers of a request signed the way senders are
// told to: HMAC-SHA256 over timestamp, sender and body, one per line.
func signedHeaders(secret string, ts time.Time, sender, body string) map[string]string {
	stamp := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp + "\n" + sender + "\n" + body))
	h := map[string]string{
		"X-Webhook-Timestamp":     stamp,
		"X-Webhook-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil)),
	}
	if sender != "" {
		h["X-Webhook-Sender"] = sender
	}
	return h
}

func serveWebhookBody(a *WebhookAdapter, req *http.Request) (int, string) {
	rr := httptest.NewRecorder()
	a.server.Handler.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

func expectNoEvent(t *testing.T, ch chan Event) {
	t.Helper()
	select {
	case evt := <-ch:
		t.Fatalf("a rejected request still produced an event: sender=%q body=%q", evt.Sender, evt.Body)
	default:
	}
}

func expectEvent(t *testing.T, ch chan Event) Event {
	t.Helper()
	select {
	case evt := <-ch:
		return evt
	default:
		t.Fatal("an accepted request produced no event")
		return Event{}
	}
}

// With a secret configured, a request carrying neither a valid signature nor
// the token is refused and never becomes an event.
func TestWebhookAuth_UnsignedRejectedWhenSecretSet(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"port": "18460", "secret": webhookTestSecret})
	cases := map[string]map[string]string{
		"no auth headers": {},
		"bad signature":   {"X-Webhook-Signature-256": signWebhookBody("wrong-secret", "hello")},
		"bad token":       {"X-Webhook-Token": "wrong-secret"},
		"bare hex":        {"X-Webhook-Signature-256": strings.TrimPrefix(signWebhookBody(webhookTestSecret, "hello"), "sha256=")},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			if code := serveWebhook(a, webhookRequest("127.0.0.1:18460", "hello", headers)); code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", code)
			}
			expectNoEvent(t, ch)
		})
	}
}

// A request signed over its timestamp, sender and body is accepted, and the
// signed sender header is honoured for routing. Without a sender header the
// sender is signed as "" and the event falls back to the remote address.
func TestWebhookAuth_SignedRequestAccepted(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"bind": "0.0.0.0", "port": "18460", "secret": webhookTestSecret})
	body := `{"text":"deploy done"}`
	code := serveWebhook(a, webhookRequest("203.0.113.7:18460", body, signedHeaders(webhookTestSecret, time.Now(), "ci-bot", body)))
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	if evt := expectEvent(t, ch); evt.Sender != "ci-bot" || evt.Body != body {
		t.Fatalf("event = sender %q body %q, want ci-bot / %q", evt.Sender, evt.Body, body)
	}

	code = serveWebhook(a, webhookRequest("203.0.113.7:18460", body, signedHeaders(webhookTestSecret, time.Now(), "", body)))
	if code != http.StatusAccepted {
		t.Fatalf("no-sender status = %d, want 202", code)
	}
	if evt := expectEvent(t, ch); evt.Sender != "192.0.2.10:40000" {
		t.Fatalf("no-sender event sender = %q, want the remote address", evt.Sender)
	}
}

// The sender header picks the routed conductor, so it is covered by the
// signature: a captured request with the sender swapped is refused.
func TestWebhookAuth_SenderSwapRejected(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"bind": "0.0.0.0", "port": "18460", "secret": webhookTestSecret})
	for name, mutate := range map[string]func(h map[string]string){
		"swapped":  func(h map[string]string) { h["X-Webhook-Sender"] = "boss@example.com" },
		"added":    func(h map[string]string) { h["X-Webhook-Sender"] = "boss@example.com" },
		"stripped": func(h map[string]string) { delete(h, "X-Webhook-Sender") },
	} {
		t.Run(name, func(t *testing.T) {
			body := "run this, " + name
			sender := "ci-bot"
			if name == "added" {
				sender = ""
			}
			h := signedHeaders(webhookTestSecret, time.Now(), sender, body)
			orig := map[string]string{}
			for k, v := range h {
				orig[k] = v
			}
			mutate(h)
			if code := serveWebhook(a, webhookRequest("203.0.113.7:18460", body, h)); code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", code)
			}
			expectNoEvent(t, ch)
			// The request as signed is still good.
			if code := serveWebhook(a, webhookRequest("203.0.113.7:18460", body, orig)); code != http.StatusAccepted {
				t.Fatalf("unchanged request: status = %d, want 202", code)
			}
			expectEvent(t, ch)
		})
	}
}

// A signature over the body alone is not enough, with or without a
// timestamp header.
func TestWebhookAuth_BodyOnlySignatureRejected(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"bind": "0.0.0.0", "port": "18460", "secret": webhookTestSecret})
	body := "hello"
	for name, h := range map[string]map[string]string{
		"no timestamp":   {"X-Webhook-Signature-256": signWebhookBody(webhookTestSecret, body), "X-Webhook-Sender": "ci-bot"},
		"with timestamp": {"X-Webhook-Signature-256": signWebhookBody(webhookTestSecret, body), "X-Webhook-Timestamp": strconv.FormatInt(time.Now().Unix(), 10)},
	} {
		t.Run(name, func(t *testing.T) {
			if code := serveWebhook(a, webhookRequest("203.0.113.7:18460", body, h)); code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", code)
			}
			expectNoEvent(t, ch)
		})
	}
}

// A signed request needs a timestamp within 5 minutes of now, either way.
func TestWebhookAuth_TimestampWindow(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"bind": "0.0.0.0", "port": "18460", "secret": webhookTestSecret})
	body := "hello"
	missing := signedHeaders(webhookTestSecret, time.Now(), "ci-bot", body)
	delete(missing, "X-Webhook-Timestamp")
	malformed := signedHeaders(webhookTestSecret, time.Now(), "ci-bot", body)
	malformed["X-Webhook-Timestamp"] = "yesterday"
	for name, h := range map[string]map[string]string{
		"missing":   missing,
		"malformed": malformed,
		"stale":     signedHeaders(webhookTestSecret, time.Now().Add(-6*time.Minute), "ci-bot", body),
		"future":    signedHeaders(webhookTestSecret, time.Now().Add(6*time.Minute), "ci-bot", body),
	} {
		t.Run(name, func(t *testing.T) {
			code, msg := serveWebhookBody(a, webhookRequest("203.0.113.7:18460", body, h))
			if code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", code)
			}
			if !strings.Contains(msg, "X-Webhook-Timestamp") {
				t.Errorf("response %q does not name X-Webhook-Timestamp", msg)
			}
			expectNoEvent(t, ch)
		})
	}
	h := signedHeaders(webhookTestSecret, time.Now().Add(-4*time.Minute), "ci-bot", body)
	if code := serveWebhook(a, webhookRequest("203.0.113.7:18460", body, h)); code != http.StatusAccepted {
		t.Fatalf("4-minute-old request: status = %d, want 202", code)
	}
	expectEvent(t, ch)
}

// A signed request is accepted once: the same request sent again is refused
// with 409 and produces no second event.
func TestWebhookAuth_ReplayRejected(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"bind": "0.0.0.0", "port": "18460", "secret": webhookTestSecret})
	body := "deploy"
	h := signedHeaders(webhookTestSecret, time.Now(), "ci-bot", body)
	if code := serveWebhook(a, webhookRequest("203.0.113.7:18460", body, h)); code != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202", code)
	}
	expectEvent(t, ch)
	for i := 0; i < 3; i++ {
		code, msg := serveWebhookBody(a, webhookRequest("203.0.113.7:18460", body, h))
		if code != http.StatusConflict {
			t.Fatalf("replay %d: status = %d, want 409", i, code)
		}
		if !strings.Contains(msg, "already accepted") {
			t.Errorf("replay response %q does not say it was already accepted", msg)
		}
		expectNoEvent(t, ch)
	}
}

// The set of accepted signatures is bounded: once full it refuses new signed
// requests rather than forget one, and entries past the window are dropped.
func TestWebhookAuth_ReplaySetBounded(t *testing.T) {
	old := webhookReplayLimit
	webhookReplayLimit = 2
	t.Cleanup(func() { webhookReplayLimit = old })
	a, ch := newTestWebhook(t, map[string]string{"bind": "0.0.0.0", "port": "18460", "secret": webhookTestSecret})
	now := time.Now()
	a.now = func() time.Time { return now }
	send := func(body string) int {
		return serveWebhook(a, webhookRequest("203.0.113.7:18460", body, signedHeaders(webhookTestSecret, now, "ci-bot", body)))
	}
	for _, body := range []string{"one", "two"} {
		if code := send(body); code != http.StatusAccepted {
			t.Fatalf("%s: status = %d, want 202", body, code)
		}
		expectEvent(t, ch)
	}
	if code := send("three"); code != http.StatusServiceUnavailable {
		t.Fatalf("over the limit: status = %d, want 503", code)
	}
	expectNoEvent(t, ch)
	if n := a.replaySize(); n != 2 {
		t.Fatalf("replay set holds %d, want 2", n)
	}

	now = now.Add(11 * time.Minute)
	if code := send("four"); code != http.StatusAccepted {
		t.Fatalf("after the window: status = %d, want 202", code)
	}
	expectEvent(t, ch)
	if n := a.replaySize(); n != 1 {
		t.Fatalf("replay set holds %d after pruning, want 1", n)
	}
}

// The token is the secret itself, sent in clear: it is refused on a wide
// bind with a message saying to sign instead.
func TestWebhookAuth_TokenRefusedOnWideBind(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"bind": "0.0.0.0", "port": "18460", "secret": webhookTestSecret})
	code, msg := serveWebhookBody(a, webhookRequest("203.0.113.7:18460", "hello", map[string]string{
		"X-Webhook-Token":  webhookTestSecret,
		"X-Webhook-Sender": "ci-bot",
	}))
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
	if want := "X-Webhook-Token is only accepted on a loopback bind"; !strings.Contains(msg, want) {
		t.Errorf("response %q lacks %q", msg, want)
	}
	expectNoEvent(t, ch)
}

// A secret is trimmed at load and must hold at least 16 bytes; a blank or
// short one fails Setup with a message naming the file.
func TestWebhookAuth_SecretStrength(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for secret, why := range map[string]string{
		"   \t":                  "is blank",
		"short-secret":           "is shorter than 16 bytes",
		" 0123456789abcde ":      "is shorter than 16 bytes",
		`0123456789"abcdef`:      "contains a space, quote, backslash",
		`0123456789\abcdef`:      "contains a space, quote, backslash",
		"0123456789 abcdef":      "contains a space, quote, backslash",
		"0123456789\x01abcdef":   "contains a space, quote, backslash",
		"0123456789\u00e9abcdef": "contains a space, quote, backslash",
	} {
		a := &WebhookAdapter{}
		err := a.Setup(context.Background(), AdapterConfig{Type: "webhook", Name: "weak", Settings: map[string]string{"port": "18460", "secret": secret}})
		if err == nil {
			t.Fatalf("Setup with secret of %d bytes succeeded", len(secret))
		}
		for _, w := range []string{`webhook watcher "weak": secret under [source] in `, "watcher.toml " + why, "e.g. from: openssl rand -hex 32"} {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("secret of %d bytes: error %q lacks %q", len(secret), err, w)
			}
		}
	}

	a, ch := newTestWebhook(t, map[string]string{"port": "18460", "secret": "  " + webhookTestSecret + "\n"})
	if code := serveWebhook(a, webhookRequest("127.0.0.1:18460", "hello", map[string]string{"X-Webhook-Token": webhookTestSecret})); code != http.StatusAccepted {
		t.Fatalf("token for the trimmed secret: status = %d, want 202", code)
	}
	expectEvent(t, ch)
}

// On a loopback bind the shared-secret token header is accepted as an
// alternative to signing, with its sender header.
func TestWebhookAuth_ValidTokenAccepted(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"port": "18460", "secret": webhookTestSecret})
	code := serveWebhook(a, webhookRequest("localhost:18460", "hello", map[string]string{
		"X-Webhook-Token":  webhookTestSecret,
		"X-Webhook-Sender": "ci-bot",
	}))
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	if evt := expectEvent(t, ch); evt.Sender != "ci-bot" {
		t.Fatalf("sender = %q, want ci-bot", evt.Sender)
	}
}

// Any request carrying an Origin header comes from a browser page and is
// refused, with or without a secret, even when otherwise authenticated.
func TestWebhookAuth_OriginRejected(t *testing.T) {
	t.Run("no secret", func(t *testing.T) {
		a, ch := newTestWebhook(t, map[string]string{"port": "18460"})
		code := serveWebhook(a, webhookRequest("127.0.0.1:18460", "hello", map[string]string{
			"Origin":           "https://evil.example",
			"Content-Type":     "text/plain",
			"X-Webhook-Sender": "alice@example.com",
		}))
		if code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", code)
		}
		expectNoEvent(t, ch)
	})
	t.Run("secret and valid token", func(t *testing.T) {
		a, ch := newTestWebhook(t, map[string]string{"port": "18460", "secret": webhookTestSecret})
		code := serveWebhook(a, webhookRequest("127.0.0.1:18460", "hello", map[string]string{
			"Origin":          "null",
			"X-Webhook-Token": webhookTestSecret,
		}))
		if code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", code)
		}
		expectNoEvent(t, ch)
	})
}

// On a loopback bind, a Host that is not a loopback name is a DNS-rebinding
// attempt and is refused; loopback names with any port are accepted.
func TestWebhookAuth_BadHostRejectedOnLoopback(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"port": "18460"})
	for _, host := range []string{"evil.example:18460", "evil.example", "192.168.1.5:18460", "localhost.evil.example:18460"} {
		t.Run("reject "+host, func(t *testing.T) {
			if code := serveWebhook(a, webhookRequest(host, "hello", nil)); code != http.StatusForbidden {
				t.Fatalf("Host %q: status = %d, want 403", host, code)
			}
			expectNoEvent(t, ch)
		})
	}
	for _, host := range []string{"127.0.0.1:18460", "127.0.0.1", "localhost:9", "LOCALHOST", "[::1]:18460", "[::1]"} {
		t.Run("accept "+host, func(t *testing.T) {
			if code := serveWebhook(a, webhookRequest(host, "hello", nil)); code != http.StatusAccepted {
				t.Fatalf("Host %q: status = %d, want 202", host, code)
			}
			expectEvent(t, ch)
		})
	}
}

// A loopback watcher with a secret is often reached through a tunnel or a
// proxy that keeps the public Host; the signature, not the Host, decides.
func TestWebhookAuth_SignedRequestAcceptedWithProxyHost(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"port": "18460", "secret": webhookTestSecret})
	code := serveWebhook(a, webhookRequest("hooks.example.com", "hello", map[string]string{
		"X-Webhook-Token": webhookTestSecret,
	}))
	if code != http.StatusAccepted {
		t.Fatalf("signed request via proxy Host: status = %d, want 202", code)
	}
	expectEvent(t, ch)
	if code := serveWebhook(a, webhookRequest("hooks.example.com", "hello", nil)); code != http.StatusUnauthorized {
		t.Fatalf("unsigned request via proxy Host: status = %d, want 401", code)
	}
	expectNoEvent(t, ch)
}

// On a wide bind the sender header can never pick the routed conductor
// without the secret: an unauthenticated request is refused outright, so its
// X-Webhook-Sender never reaches routing.
func TestWebhookAuth_SenderHeaderIgnoredWhenUnauthenticated(t *testing.T) {
	a, ch := newTestWebhook(t, map[string]string{"bind": "0.0.0.0", "port": "18460", "secret": webhookTestSecret})
	code := serveWebhook(a, webhookRequest("203.0.113.7:18460", "run this", map[string]string{
		"X-Webhook-Sender": "alice@example.com",
	}))
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
	expectNoEvent(t, ch)

	// The same request, signed, keeps its sender. A wide bind does not
	// check Host.
	code = serveWebhook(a, webhookRequest("203.0.113.7:18460", "run this",
		signedHeaders(webhookTestSecret, time.Now(), "alice@example.com", "run this")))
	if code != http.StatusAccepted {
		t.Fatalf("authenticated status = %d, want 202", code)
	}
	if evt := expectEvent(t, ch); evt.Sender != "alice@example.com" {
		t.Fatalf("sender = %q, want alice@example.com", evt.Sender)
	}
}

// Setup refuses to listen beyond loopback without a secret, with a message
// that says how to fix it; loopback binds, and wide binds with a secret, are
// accepted.
func TestWebhookAuth_SetupRefusesWideBindWithoutSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, bind := range []string{"0.0.0.0", "::", "192.168.1.5", "example.com"} {
		t.Run("refuse "+bind, func(t *testing.T) {
			a := &WebhookAdapter{}
			err := a.Setup(context.Background(), AdapterConfig{Type: "webhook", Name: "wide", Settings: map[string]string{"bind": bind, "port": "18460"}})
			if err == nil {
				t.Fatalf("Setup on bind %q without a secret succeeded", bind)
			}
			msg := err.Error()
			for _, want := range []string{
				`webhook watcher "wide": refusing to listen on ` + bind + ` without a secret`,
				`secret = "`,
				"openssl rand -hex 32",
				"watcher.toml",
				`bind = "127.0.0.1"`,
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q lacks %q", msg, want)
				}
			}
		})
	}
	for _, settings := range []map[string]string{
		{},
		{"bind": "127.0.0.1"},
		{"bind": "127.0.0.5"},
		{"bind": "localhost"},
		{"bind": "::1"},
		{"bind": "0.0.0.0", "secret": webhookTestSecret},
	} {
		a := &WebhookAdapter{}
		if err := a.Setup(context.Background(), AdapterConfig{Type: "webhook", Name: "ok", Settings: settings}); err != nil {
			t.Errorf("Setup(%v): %v", settings, err)
		}
	}
}
