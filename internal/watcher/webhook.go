package watcher

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

var webhookLog = logging.ForComponent(logging.CompWatcher)

// WebhookAdapter implements WatcherAdapter by running an HTTP server that accepts
// POST requests on /webhook and normalizes them to Event structs. The server binds
// to 127.0.0.1 by default (configurable via Settings["bind"]) to avoid accidental
// public exposure.
//
// With Settings["secret"] every request must be signed: X-Webhook-Timestamp
// (unix seconds, within webhookSignatureWindow of now) and
// X-Webhook-Signature-256, an HMAC-SHA256 of timestamp, X-Webhook-Sender and
// body, each signature accepted once. On a loopback bind X-Webhook-Token (the
// secret itself) is accepted instead. Requests from browser pages (any Origin
// header) are refused, and on a loopback bind so is any Host that is not a
// loopback name, which stops DNS rebinding.
type WebhookAdapter struct {
	server   *http.Server
	addr     string
	secret   string
	loopback bool         // bound to a loopback address only
	listener net.Listener // used when port=0 to capture the actual bound address
	eventsCh chan<- Event
	mu       sync.RWMutex // protects addr and eventsCh

	now    func() time.Time     // time.Now unless a test sets it
	seenMu sync.Mutex           // protects seen
	seen   map[string]time.Time // accepted signatures, to when they expire
}

// webhookSignatureWindow is how far a signed request's timestamp may be from
// now, either way. An accepted signature is remembered until its timestamp
// leaves the window, so a replay is refused.
const webhookSignatureWindow = 5 * time.Minute

// webhookReplayLimit caps how many accepted signatures are remembered. When
// it is reached, new signed requests get 503 until older ones expire: a
// forgotten signature could be replayed.
var webhookReplayLimit = 10000

// WebhookMinSecretLen is the shortest secret a webhook watcher accepts.
const WebhookMinSecretLen = 16

// CheckWebhookSecret reports why secret is too weak to sign with: it is
// blank, or shorter than WebhookMinSecretLen bytes once trimmed. The error
// completes a sentence that names where the secret came from.
func CheckWebhookSecret(secret string) error {
	secret = strings.TrimSpace(secret)
	switch {
	case secret == "":
		return fmt.Errorf("is blank; use at least %d bytes, e.g. from: openssl rand -hex 32", WebhookMinSecretLen)
	case len(secret) < WebhookMinSecretLen:
		return fmt.Errorf("is shorter than %d bytes; use at least %d bytes, e.g. from: openssl rand -hex 32", WebhookMinSecretLen, WebhookMinSecretLen)
	}
	// watcher.toml stores the secret as a quoted string and the printed
	// signing examples read it back verbatim, so a quote, backslash or
	// non-printable byte would make the file unparseable or every request 401.
	for i := 0; i < len(secret); i++ {
		if c := secret[i]; c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
			return errors.New(`contains a space, quote, backslash or non-printable character; use printable ASCII without " or \, e.g. from: openssl rand -hex 32`)
		}
	}
	return nil
}

// Setup initializes the adapter with the HTTP server configuration but does NOT start
// the server. The server is started in Listen.
//
// Settings:
//   - "port": TCP port to listen on (default "18460")
//   - "bind": Bind address (default "127.0.0.1" per T-14-04)
//   - "secret": shared secret senders sign with; required unless bind is loopback
func (a *WebhookAdapter) Setup(_ context.Context, config AdapterConfig) error {
	port := config.Settings["port"]
	if port == "" {
		port = "18460"
	}

	bind := config.Settings["bind"]
	if bind == "" {
		bind = "127.0.0.1"
	}

	raw, hasSecret := config.Settings["secret"]
	a.secret = strings.TrimSpace(raw)
	a.loopback = isLoopbackBind(bind)
	if hasSecret && raw != "" {
		if err := CheckWebhookSecret(raw); err != nil {
			return fmt.Errorf("webhook watcher %q: secret under [source] in %s %v", config.Name, webhookTomlPath(config.Name), err)
		}
	}
	if a.secret == "" {
		toml := webhookTomlPath(config.Name)
		if !a.loopback {
			return fmt.Errorf("webhook watcher %q: refusing to listen on %s without a secret; "+
				"add secret = \"<random hex, e.g. from: openssl rand -hex 32>\" under [source] in %s (chmod 600) "+
				"and sign requests with it, or set bind = \"127.0.0.1\" there", config.Name, bind, toml)
		}
		webhookLog.Warn("webhook_unsigned",
			"watcher", config.Name,
			"warning", fmt.Sprintf("WARNING: webhook watcher %q accepts unsigned requests from any local process; "+
				"to require a signature add secret = \"<random hex, e.g. from: openssl rand -hex 32>\" under [source] in %s (chmod 600)",
				config.Name, toml))
	}

	a.addr = bind + ":" + port

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", a.handlePost)
	// Catch all other methods on /webhook with 405
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	})

	a.server = &http.Server{
		Addr:              a.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	return nil
}

// Listen starts the HTTP server and blocks until the context is cancelled. On context
// cancellation, the server is gracefully shut down with a 5-second timeout.
func (a *WebhookAdapter) Listen(ctx context.Context, events chan<- Event) error {
	a.mu.Lock()
	a.eventsCh = events
	a.mu.Unlock()

	// If port is "0", use a listener to get the actual bound address.
	a.mu.RLock()
	listenAddr := a.addr
	a.mu.RUnlock()

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	a.listener = ln

	a.mu.Lock()
	a.addr = ln.Addr().String()
	a.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.server.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.server.Shutdown(shutCtx)
		// Drain the server error
		<-errCh
		return nil
	case srvErr := <-errCh:
		if errors.Is(srvErr, http.ErrServerClosed) {
			return nil
		}
		return srvErr
	}
}

// handlePost processes an incoming POST request on /webhook. It responds 202 Accepted
// before sending the event to the channel (per D-05: response before processing).
func (a *WebhookAdapter) handlePost(w http.ResponseWriter, r *http.Request) {
	// Server-to-server senders never send Origin; a browser page always does.
	if r.Header.Get("Origin") != "" {
		webhookLog.Warn("webhook_refused", "reason", "origin header", "remote", r.RemoteAddr)
		http.Error(w, "Forbidden: browser requests are not accepted", http.StatusForbidden)
		return
	}
	// Without a secret, a loopback watcher only takes requests addressed to
	// loopback, which stops DNS rebinding. A signed request may come through a
	// tunnel or proxy that keeps the public Host, so the signature decides.
	if a.loopback && a.secret == "" && !isLoopbackHost(r.Host) {
		webhookLog.Warn("webhook_refused", "reason", "non-loopback host", "host", r.Host, "remote", r.RemoteAddr)
		http.Error(w, "Forbidden: Host must be localhost, 127.0.0.1 or [::1]", http.StatusForbidden)
		return
	}

	// Limit body to 1MB per T-14-01
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	if len(r.Header.Values("X-Webhook-Sender")) > 1 {
		http.Error(w, "Bad Request: more than one X-Webhook-Sender", http.StatusBadRequest)
		return
	}
	authenticated := false
	if a.secret != "" {
		if code, msg := a.authenticate(r, body); code != 0 {
			webhookLog.Warn("webhook_refused", "reason", msg, "remote", r.RemoteAddr)
			http.Error(w, msg, code)
			return
		}
		authenticated = true
	}

	// Respond 202 Accepted BEFORE processing the event (D-05)
	w.WriteHeader(http.StatusAccepted)

	// Normalize to Event. The sender header picks the conductor the event is
	// routed to, so it is only trusted when the signature covers it, from the
	// token (loopback only), or from a local process when no secret is
	// configured.
	sender := ""
	if authenticated || (a.secret == "" && a.loopback) {
		sender = r.Header.Get("X-Webhook-Sender")
	}
	if sender == "" {
		sender = r.RemoteAddr
	}

	subject := r.Header.Get("X-Webhook-Subject")
	if subject == "" {
		subject = firstLine(string(body), 200)
	}

	var rawPayload json.RawMessage
	if json.Valid(body) {
		rawPayload = json.RawMessage(body)
	}

	evt := Event{
		Source:     "webhook",
		Sender:     sender,
		Subject:    subject,
		Body:       string(body),
		Timestamp:  time.Now(),
		RawPayload: rawPayload,
	}

	// Non-blocking send (drop event if channel full)
	a.mu.RLock()
	ch := a.eventsCh
	a.mu.RUnlock()

	if ch != nil {
		select {
		case ch <- evt:
		default:
		}
	}
}

// authenticate checks r against the secret and returns 0 when it passes, or
// the status and message to refuse it with. A signed request needs a fresh
// timestamp and a signature not accepted before; it is then remembered. The
// token, the secret itself sent in clear, is only taken on a loopback bind.
func (a *WebhookAdapter) authenticate(r *http.Request, body []byte) (int, string) {
	if sig := r.Header.Get("X-Webhook-Signature-256"); sig != "" {
		stamp := r.Header.Get("X-Webhook-Timestamp")
		ts, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || len(r.Header.Values("X-Webhook-Timestamp")) > 1 {
			return http.StatusUnauthorized, "missing or invalid X-Webhook-Timestamp (unix seconds); it is signed with the sender and body"
		}
		now := a.clock()
		at := time.Unix(ts, 0)
		if at.Before(now.Add(-webhookSignatureWindow)) || at.After(now.Add(webhookSignatureWindow)) {
			return http.StatusUnauthorized, "X-Webhook-Timestamp is more than 5 minutes from the server's clock"
		}
		if !verifyWebhookSignature(a.secret, sig, stamp, r.Header.Get("X-Webhook-Sender"), body) {
			return http.StatusUnauthorized, "invalid X-Webhook-Signature-256: expected sha256=<hex HMAC-SHA256 of timestamp, sender and body, joined by newlines>"
		}
		return a.remember(strings.ToLower(sig), at.Add(webhookSignatureWindow), now)
	}
	if token := r.Header.Get("X-Webhook-Token"); token != "" {
		if !a.loopback {
			return http.StatusUnauthorized, "X-Webhook-Token is only accepted on a loopback bind, as it sends the secret in clear; " +
				"sign the request with X-Webhook-Timestamp and X-Webhook-Signature-256"
		}
		// Hash both sides so the comparison does not leak the secret's length.
		got, want := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(a.secret))
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			return 0, ""
		}
	}
	return http.StatusUnauthorized, "missing or invalid X-Webhook-Signature-256 / X-Webhook-Token"
}

// remember records an accepted signature until expires, refusing one already
// recorded. Expired entries are dropped first; when the set is still full the
// request is refused, so no live signature is ever forgotten.
func (a *WebhookAdapter) remember(sig string, expires, now time.Time) (int, string) {
	a.seenMu.Lock()
	defer a.seenMu.Unlock()
	for k, exp := range a.seen {
		if now.After(exp) {
			delete(a.seen, k)
		}
	}
	if _, ok := a.seen[sig]; ok {
		return http.StatusConflict, "replayed request: this signature was already accepted"
	}
	if len(a.seen) >= webhookReplayLimit {
		return http.StatusServiceUnavailable, "too many signed requests in the last 5 minutes; try again later"
	}
	if a.seen == nil {
		a.seen = map[string]time.Time{}
	}
	a.seen[sig] = expires
	return 0, ""
}

// replaySize reports how many accepted signatures are remembered.
func (a *WebhookAdapter) replaySize() int {
	a.seenMu.Lock()
	defer a.seenMu.Unlock()
	return len(a.seen)
}

func (a *WebhookAdapter) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// verifyWebhookSignature checks signature, "sha256=<hex>", against the
// HMAC-SHA256 of stamp + "\n" + sender + "\n" + body, in constant time.
func verifyWebhookSignature(secret, signature, stamp, sender string, body []byte) bool {
	hexSig, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	expected, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp + "\n" + sender + "\n"))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), expected)
}

// isLoopbackBind reports whether bind only listens on this machine:
// localhost, an address in 127.0.0.0/8, or ::1.
func isLoopbackBind(bind string) bool {
	bind = strings.TrimSuffix(strings.TrimPrefix(bind, "["), "]")
	if strings.EqualFold(bind, "localhost") {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackHost reports whether a request's Host header (with or without a
// port) names this machine.
func isLoopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host != "" && isLoopbackBind(host)
}

// webhookTomlPath names the watcher.toml a watcher's settings live in, for
// messages that tell the user what to edit.
func webhookTomlPath(name string) string {
	dir, err := session.WatcherNameDir(name)
	if err != nil || name == "" {
		return "the watcher's watcher.toml"
	}
	return filepath.Join(dir, "watcher.toml")
}

// Teardown is a no-op because the server is shut down in Listen via context cancellation
// (per D-04).
func (a *WebhookAdapter) Teardown() error {
	return nil
}

// HealthCheck verifies the HTTP listener is accepting TCP connections (per D-06).
func (a *WebhookAdapter) HealthCheck() error {
	a.mu.RLock()
	addr := a.addr
	a.mu.RUnlock()
	if addr == "" {
		return fmt.Errorf("webhook: adapter is not initialized")
	}

	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// firstLine returns the first line of s, truncated to maxLen bytes.
// Invalid UTF-8 in the input becomes U+FFFD, and when the cap lands
// inside a multi-byte sequence the partial codepoint is trimmed off, so
// the result is always valid UTF-8. This prevents downstream consumers
// (SQLite readers, JSON encoders, logs) from choking on bytes stored to
// watcher_events.subject — a single poisoned row can wedge an entire
// watcher pipeline.
func firstLine(s string, maxLen int) string {
	line := s
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		line = s[:idx]
	}
	line = strings.ToValidUTF8(line, "\uFFFD")
	if len(line) > maxLen {
		// line is valid UTF-8 here, so backing up to a rune start
		// strips at most 3 bytes of a split codepoint.
		cut := maxLen
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut]
	}
	return line
}
