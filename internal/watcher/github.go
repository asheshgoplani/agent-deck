package watcher

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// githubLog is the package-level logger for the GitHub adapter. Used to
// surface webhook-payload parse failures that pre-v1.9 silently swallowed
// (`_ = json.Unmarshal(...)` × 4). See arch-review S2.
var githubLog = logging.ForComponent(logging.CompWatcher)

// GitHubAdapter implements WatcherAdapter by running an HTTP server that accepts
// POST requests on /github and verifies X-Hub-Signature-256 HMAC-SHA256 signatures
// before normalizing GitHub webhook events to Event structs. The server binds to
// 127.0.0.1 by default (configurable via Settings["bind"]) to avoid accidental
// public exposure.
type GitHubAdapter struct {
	server   *http.Server
	addr     string
	secret   string
	listener net.Listener
	eventsCh chan<- Event
	mu       sync.RWMutex // protects addr and eventsCh
}

// Setup initializes the adapter with the HTTP server configuration but does NOT start
// the server. The server is started in Listen.
//
// Settings:
//   - "secret": required webhook secret for HMAC-SHA256 verification
//   - "port": TCP port to listen on (default "18461")
//   - "bind": Bind address (default "127.0.0.1" per T-14-04)
func (a *GitHubAdapter) Setup(_ context.Context, config AdapterConfig) error {
	a.secret = config.Settings["secret"]
	if a.secret == "" {
		return errors.New("github adapter requires a webhook secret")
	}

	port := config.Settings["port"]
	if port == "" {
		port = "18461"
	}

	bind := config.Settings["bind"]
	if bind == "" {
		bind = "127.0.0.1"
	}

	a.addr = bind + ":" + port

	mux := http.NewServeMux()
	mux.HandleFunc("POST /github", a.handleWebhook)
	mux.HandleFunc("/github", func(w http.ResponseWriter, r *http.Request) {
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
func (a *GitHubAdapter) Listen(ctx context.Context, events chan<- Event) error {
	a.mu.Lock()
	a.eventsCh = events
	a.mu.Unlock()

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
		<-errCh
		return nil
	case srvErr := <-errCh:
		if errors.Is(srvErr, http.ErrServerClosed) {
			return nil
		}
		return srvErr
	}
}

// handleWebhook processes an incoming POST request on /github. It verifies the
// HMAC-SHA256 signature, responds 202 Accepted before processing, normalizes the
// GitHub event, and sends it to the channel.
func (a *GitHubAdapter) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Limit body to 10MB per T-14-03, T-14-08
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

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

	// Verify HMAC-SHA256 signature (T-14-02)
	signature := r.Header.Get("X-Hub-Signature-256")
	if signature == "" {
		http.Error(w, "missing signature", http.StatusUnauthorized)
		return
	}

	if !verifyGitHubSignature(a.secret, signature, body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Respond 202 Accepted BEFORE processing the event
	w.WriteHeader(http.StatusAccepted)

	// Normalize the GitHub event. Pre-v1.9 normalize* functions silently
	// dropped json.Unmarshal errors; v1.9 propagates them so the webhook
	// pipeline can log+drop instead of forwarding a stub Event.
	eventType := r.Header.Get("X-GitHub-Event")
	evt, err := normalizeGitHubEvent(eventType, body)
	if err != nil {
		// Drop the event rather than emit a zero-valued one. Emitting a
		// zero `Sender`/`Subject` would feed dedup + routing on garbage
		// values and silently misroute. The 202 was already sent so the
		// HTTP-protocol contract is preserved.
		githubLog.Warn("github_payload_unmarshal_failed",
			"event_type", eventType,
			"err", err.Error(),
			"body_bytes", len(body),
		)
		return
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

// verifyGitHubSignature verifies a GitHub webhook HMAC-SHA256 signature using
// constant-time comparison (per D-12, T-14-02).
func verifyGitHubSignature(secret, signature string, body []byte) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}

	expected, err := hex.DecodeString(signature[7:])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), expected)
}

// normalizeGitHubEvent converts a GitHub webhook payload into a normalized Event.
// Supported event types: issues, pull_request, push. Unknown types produce a
// generic event per D-14.
//
// Returns an error when the payload fails to unmarshal. Before the fix, the
// four normalizers all swallowed the unmarshal error and proceeded to emit an
// Event with zero `Sender`/`Subject`/`Ref`, which the downstream router used
// for dedup + routing — silently misroute or drop. See critical-hunt audit #1.
func normalizeGitHubEvent(eventType string, body []byte) (Event, error) {
	switch eventType {
	case "issues":
		return normalizeIssuesEvent(body)
	case "pull_request":
		return normalizePREvent(body)
	case "push":
		return normalizePushEvent(body)
	default:
		return normalizeUnknownEvent(eventType, body)
	}
}

// safeUnmarshalGitHubPayload is the single helper that all four GitHub
// webhook normalizers route through to parse `body` into a payload
// struct. Pre-v1.9 each normalizer used `_ = json.Unmarshal(body, &p)`
// — four sister-function copies of the same swallow. Centralizing into
// this helper guarantees that any JSON-decode failure becomes an error
// rather than a stub Event with empty fields.
func safeUnmarshalGitHubPayload(eventType string, body []byte, target any) error {
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("github webhook %s: unmarshal failed: %w", eventType, err)
	}
	return nil
}

// GitHub payload structs for type-safe field extraction.

type ghIssuesPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	} `json:"issue"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type ghPRPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	} `json:"pull_request"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type ghPushPayload struct {
	Ref     string `json:"ref"`
	Commits []struct {
		Message string `json:"message"`
	} `json:"commits"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Pusher struct {
		Email string `json:"email"`
	} `json:"pusher"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type ghGenericPayload struct {
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func normalizeIssuesEvent(body []byte) (Event, error) {
	var p ghIssuesPayload
	if err := safeUnmarshalGitHubPayload("issues", body, &p); err != nil {
		return Event{}, err
	}

	return Event{
		Source:     "github",
		Sender:     p.Sender.Login + "@github.com",
		Subject:    fmt.Sprintf("[%s] #%d: %s", p.Action, p.Issue.Number, p.Issue.Title),
		Body:       p.Issue.Body,
		Timestamp:  time.Now(),
		RawPayload: json.RawMessage(body),
	}, nil
}

func normalizePREvent(body []byte) (Event, error) {
	var p ghPRPayload
	if err := safeUnmarshalGitHubPayload("pull_request", body, &p); err != nil {
		return Event{}, err
	}

	return Event{
		Source:     "github",
		Sender:     p.Sender.Login + "@github.com",
		Subject:    fmt.Sprintf("[PR %s] #%d: %s", p.Action, p.Number, p.PullRequest.Title),
		Body:       p.PullRequest.Body,
		Timestamp:  time.Now(),
		RawPayload: json.RawMessage(body),
	}, nil
}

func normalizePushEvent(body []byte) (Event, error) {
	var p ghPushPayload
	if err := safeUnmarshalGitHubPayload("push", body, &p); err != nil {
		return Event{}, err
	}

	shortRef := strings.TrimPrefix(p.Ref, "refs/heads/")

	commitBody := "no commits"
	if len(p.Commits) > 0 {
		commitBody = p.Commits[0].Message
	}

	// Prefer pusher email if it contains "@", otherwise fall back to sender login
	sender := p.Sender.Login + "@github.com"
	if p.Pusher.Email != "" && strings.Contains(p.Pusher.Email, "@") {
		sender = p.Pusher.Email
	}

	return Event{
		Source:     "github",
		Sender:     sender,
		Subject:    fmt.Sprintf("[push] %s: %d commit(s)", shortRef, len(p.Commits)),
		Body:       commitBody,
		Timestamp:  time.Now(),
		RawPayload: json.RawMessage(body),
	}, nil
}

func normalizeUnknownEvent(eventType string, body []byte) (Event, error) {
	var p ghGenericPayload
	if err := safeUnmarshalGitHubPayload(eventType, body, &p); err != nil {
		return Event{}, err
	}

	bodyStr := string(body)
	if len(bodyStr) > 1000 {
		bodyStr = bodyStr[:1000]
	}

	sender := p.Sender.Login + "@github.com"

	return Event{
		Source:     "github",
		Sender:     sender,
		Subject:    fmt.Sprintf("[%s] event from %s", eventType, p.Repository.FullName),
		Body:       bodyStr,
		Timestamp:  time.Now(),
		RawPayload: json.RawMessage(body),
	}, nil
}

// Teardown is a no-op because the server is shut down in Listen via context cancellation.
func (a *GitHubAdapter) Teardown() error {
	return nil
}

// HealthCheck verifies the HTTP listener is accepting TCP connections.
func (a *GitHubAdapter) HealthCheck() error {
	a.mu.RLock()
	addr := a.addr
	a.mu.RUnlock()

	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// Addr returns the current listen address (thread-safe). Useful for tests when
// port 0 is used to get a random available port.
func (a *GitHubAdapter) Addr() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.addr
}
