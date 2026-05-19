package watcher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// WebhookAdapter implements WatcherAdapter by running an HTTP server that accepts
// POST requests on /webhook and normalizes them to Event structs. The server binds
// to 127.0.0.1 by default (configurable via Settings["bind"]) to avoid accidental
// public exposure.
type WebhookAdapter struct {
	server   *http.Server
	addr     string
	listener net.Listener // used when port=0 to capture the actual bound address
	eventsCh chan<- Event
	mu       sync.RWMutex // protects addr and eventsCh
}

// Setup initializes the adapter with the HTTP server configuration but does NOT start
// the server. The server is started in Listen.
//
// Settings:
//   - "port": TCP port to listen on (default "18460")
//   - "bind": Bind address (default "127.0.0.1" per T-14-04)
func (a *WebhookAdapter) Setup(_ context.Context, config AdapterConfig) error {
	port := config.Settings["port"]
	if port == "" {
		port = "18460"
	}

	bind := config.Settings["bind"]
	if bind == "" {
		bind = "127.0.0.1"
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

	// Respond 202 Accepted BEFORE processing the event (D-05)
	w.WriteHeader(http.StatusAccepted)

	// Normalize to Event
	sender := r.Header.Get("X-Webhook-Sender")
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

	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// firstLine returns the first line of s, truncated to maxLen bytes.
// When the cap lands inside a multi-byte UTF-8 sequence, the partial
// codepoint is trimmed off so the result is always valid UTF-8. This
// prevents downstream consumers (SQLite readers, JSON encoders, logs)
// from choking on bytes stored to watcher_events.subject — a single
// poisoned row can wedge an entire watcher pipeline.
func firstLine(s string, maxLen int) string {
	line := s
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		line = s[:idx]
	}
	if len(line) > maxLen {
		line = line[:maxLen]
		// UTF-8 sequences are at most 4 bytes, so at most 3 trailing
		// bytes are stripped here. ValidString runs in O(len(line))
		// but only on suffix bytes after the first valid read; in
		// practice this is a handful of iterations.
		for len(line) > 0 && !utf8.ValidString(line) {
			line = line[:len(line)-1]
		}
	}
	return line
}
