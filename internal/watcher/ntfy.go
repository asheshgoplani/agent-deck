package watcher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// NtfyAdapter implements WatcherAdapter by subscribing to an ntfy topic via
// NDJSON streaming (one JSON object per line). It auto-reconnects with exponential
// backoff on disconnection and resumes from the last received message ID.
type NtfyAdapter struct {
	server string       // ntfy server URL (e.g., "https://ntfy.sh")
	topic  string       // topic name
	client *http.Client // HTTP client for requests

	lastID string     // last received message ID for reconnect resumption
	mu     sync.Mutex // protects lastID

	// initialBackoff and maxBackoff are configurable for testing.
	// Production defaults: 2s initial, 30s max.
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// ntfyMessage represents a single NDJSON line from the ntfy /topic/json endpoint.
type ntfyMessage struct {
	ID      string   `json:"id"`
	Time    int64    `json:"time"`
	Event   string   `json:"event"` // "open", "keepalive", "message"
	Topic   string   `json:"topic"`
	Message string   `json:"message"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
}

// Setup initializes the adapter with the ntfy server URL and topic. The topic is
// required; the server defaults to "https://ntfy.sh".
//
// Settings:
//   - "topic": required ntfy topic name
//   - "server": ntfy server URL (default "https://ntfy.sh")
func (a *NtfyAdapter) Setup(_ context.Context, config AdapterConfig) error {
	a.topic = config.Settings["topic"]
	if a.topic == "" {
		return errors.New("ntfy adapter requires Settings[\"topic\"]")
	}

	a.server = config.Settings["server"]
	if a.server == "" {
		a.server = "https://ntfy.sh"
	}
	a.server = strings.TrimRight(a.server, "/")

	// Streaming client: no timeout on body reads (context handles cancellation).
	a.client = &http.Client{Timeout: 0}

	// Set defaults for backoff if not already set (tests may override before Listen).
	if a.initialBackoff == 0 {
		a.initialBackoff = 2 * time.Second
	}
	if a.maxBackoff == 0 {
		a.maxBackoff = 30 * time.Second
	}

	return nil
}

// Listen connects to the ntfy NDJSON stream and emits normalized Events on the
// provided channel. On disconnection, it reconnects with exponential backoff
// (initial 2s, 2x factor, 30s cap) per D-08. Listen only returns when the context
// is cancelled.
func (a *NtfyAdapter) Listen(ctx context.Context, events chan<- Event) error {
	backoff := a.initialBackoff

	for {
		err := a.streamOnce(ctx, events)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Connection dropped or error; wait with backoff before reconnecting
		_ = err // logged by engine's runAdapter
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > a.maxBackoff {
			backoff = a.maxBackoff
		}
	}
}

// streamOnce opens a single NDJSON streaming connection and reads events until
// the connection closes or the context is cancelled. It resumes from the last
// received message ID on reconnection.
func (a *NtfyAdapter) streamOnce(ctx context.Context, events chan<- Event) error {
	a.mu.Lock()
	lastID := a.lastID
	a.mu.Unlock()

	streamURL := a.server + "/" + a.topic + "/json"
	if lastID != "" {
		streamURL += "?since=" + lastID
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy server returned status %d", resp.StatusCode)
	}

	host := hostFromURL(a.server)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var msg ntfyMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue // malformed JSON line, skip per T-14-07
		}
		if msg.Event != "message" {
			continue // skip "open" and "keepalive" events
		}

		// Track last ID for reconnect resumption
		a.mu.Lock()
		a.lastID = msg.ID
		a.mu.Unlock()

		// Normalize to Event
		subject := msg.Title
		if subject == "" {
			subject = firstLine(msg.Message, 200)
		}
		if subject == "" {
			subject = "ntfy notification"
		}

		evt := Event{
			Source:     "ntfy",
			Sender:     fmt.Sprintf("ntfy:%s@%s", a.topic, host),
			Subject:    subject,
			Body:       msg.Message,
			Timestamp:  time.Unix(msg.Time, 0),
			RawPayload: json.RawMessage(scanner.Bytes()),
		}

		// Non-blocking send (drop event if channel full)
		select {
		case events <- evt:
		default:
		}
	}

	return scanner.Err()
}

// Teardown is a no-op. The streaming HTTP connection is closed by context
// cancellation in Listen.
func (a *NtfyAdapter) Teardown() error {
	return nil
}

// HealthCheck verifies the ntfy server is reachable by sending an HTTP HEAD request
// with a 5-second timeout (per D-10). It reports an error instead of dereferencing
// a nil client when Setup never completed (#1886).
func (a *NtfyAdapter) HealthCheck() error {
	if a.client == nil {
		return errors.New("ntfy adapter is not set up")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, a.server, nil)
	if err != nil {
		return err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy health check returned status %d", resp.StatusCode)
	}

	return nil
}

// hostFromURL extracts the hostname from a URL string (e.g., "ntfy.sh" from
// "https://ntfy.sh"). Falls back to the raw URL if parsing fails.
func hostFromURL(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return serverURL
	}
	return u.Host
}
